//go:build onnx

package ml

import (
	"errors"
	"fmt"
	"os"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// OnnxRuntimeLibEnv names the environment variable with the path of the ONNX Runtime shared library
// (default: "onnxruntime.so" on the library search path).
const OnnxRuntimeLibEnv = "PE_ML_ONNXRUNTIME_LIB"

var ortEnv struct {
	once sync.Once
	err  error
}

func initRuntime() error {
	ortEnv.once.Do(func() {
		if lib := os.Getenv(OnnxRuntimeLibEnv); lib != "" {
			ort.SetSharedLibraryPath(lib)
		}
		ortEnv.err = ort.InitializeEnvironment()
	})
	return ortEnv.err
}

// onnxScorer runs a model exported by ml/ (input "features" [batch, N] float32, outputs "label" and
// "probabilities" [batch, 2]).
type onnxScorer struct {
	info   ModelInfo
	points float64

	mu      sync.Mutex
	session *ort.DynamicAdvancedSession
}

func openModel(path string, points float64) (Scorer, error) {
	if err := initRuntime(); err != nil {
		return nil, fmt.Errorf("ml: onnxruntime: %w", err)
	}
	meta, err := ort.GetModelMetadata(path)
	if err != nil {
		return nil, fmt.Errorf("ml: model %s: %w", path, err)
	}
	info, err := ValidateModelMeta(func(k string) (string, bool) {
		v, ok, err := meta.LookupCustomMetadataMap(k)
		return v, ok && err == nil
	})
	_ = meta.Destroy()
	if err != nil {
		return nil, fmt.Errorf("ml: model %s: %w", path, err)
	}
	sess, err := ort.NewDynamicAdvancedSession(path, []string{"features"}, []string{"label", "probabilities"}, nil)
	if err != nil {
		return nil, fmt.Errorf("ml: model %s: %w", path, err)
	}
	return &onnxScorer{info: info, points: max(0, points), session: sess}, nil
}

func (s *onnxScorer) Enabled() bool   { return true }
func (s *onnxScorer) CheckID() string { return s.info.CheckID() }
func (s *onnxScorer) Points() float64 { return s.points }

func (s *onnxScorer) Applies(w *Window) bool {
	return w.FeatureVersion == FeatureVersion && w.ClientProtocol >= s.info.MinClientProtocol
}

func (s *onnxScorer) Score(ws []*Window) ([]Score, error) {
	if len(ws) == 0 {
		return nil, nil
	}
	data := make([]float32, 0, len(ws)*NumFeatures)
	for _, w := range ws {
		data = append(data, w.Features[:]...)
	}
	in, err := ort.NewTensor(ort.NewShape(int64(len(ws)), NumFeatures), data)
	if err != nil {
		return nil, err
	}
	defer in.Destroy()
	outs := []ort.Value{nil, nil}
	s.mu.Lock()
	if s.session == nil {
		s.mu.Unlock()
		return nil, errors.New("ml: scorer closed")
	}
	err = s.session.Run([]ort.Value{in}, outs)
	s.mu.Unlock()
	defer func() {
		for _, o := range outs {
			if o != nil {
				o.Destroy()
			}
		}
	}()
	if err != nil {
		return nil, err
	}
	probs, ok := outs[1].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("ml: probabilities output is %T, want a float32 tensor", outs[1])
	}
	p := probs.GetData()
	if len(p) != 2*len(ws) {
		return nil, fmt.Errorf("ml: probabilities have %d values for %d windows", len(p), len(ws))
	}
	res := make([]Score, len(ws))
	for i := range ws {
		pc := float64(p[2*i+1])
		res[i] = Score{PCheat: pc, Flagged: pc >= s.info.Threshold}
	}
	return res, nil
}

func (s *onnxScorer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil {
		return nil
	}
	err := s.session.Destroy()
	s.session = nil
	return err
}
