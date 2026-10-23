//go:build onnx

package ml

import (
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/config"
	"google.golang.org/protobuf/encoding/protowire"
)

// pbuf appends protobuf fields (the ONNX ModelProto is encoded by hand, field numbers from onnx.proto).
type pbuf []byte

func (b pbuf) str(n protowire.Number, s string) pbuf {
	return protowire.AppendString(protowire.AppendTag(b, n, protowire.BytesType), s)
}

func (b pbuf) msg(n protowire.Number, m pbuf) pbuf {
	return protowire.AppendBytes(protowire.AppendTag(b, n, protowire.BytesType), m)
}

func (b pbuf) num(n protowire.Number, v uint64) pbuf {
	return protowire.AppendVarint(protowire.AppendTag(b, n, protowire.VarintType), v)
}

// tinyModel is an ONNX model with the scorer's I/O contract whose P(cheat) is the window's feature 1:
// probabilities = features[:, 0:2], label = argmax(probabilities).
func tinyModel(meta map[string]string) []byte {
	tensor := func(name string, dims []uint64, elem uint64) pbuf { // ValueInfoProto
		var shape pbuf
		for _, d := range dims {
			if d == 0 {
				shape = shape.msg(1, pbuf{}.str(2, "batch"))
			} else {
				shape = shape.msg(1, pbuf{}.num(1, d))
			}
		}
		return pbuf{}.str(1, name).msg(2, pbuf{}.msg(1, pbuf{}.num(1, elem).msg(2, shape)))
	}
	int64s := func(name string, v uint64) pbuf { // TensorProto, INT64 [1]
		return pbuf{}.num(1, 1).num(2, 7).num(7, v).str(8, name)
	}
	g := pbuf{}.
		msg(1, pbuf{}.str(1, "features").str(1, "starts").str(1, "ends").str(1, "axes").str(2, "probabilities").str(4, "Slice")).
		msg(1, pbuf{}.str(1, "probabilities").str(2, "label").str(4, "ArgMax").
			msg(5, pbuf{}.str(1, "axis").num(3, 1).num(20, 2)).msg(5, pbuf{}.str(1, "keepdims").num(3, 0).num(20, 2))).
		str(2, "tiny").
		msg(5, int64s("starts", 0)).msg(5, int64s("ends", 2)).msg(5, int64s("axes", 1)).
		msg(11, tensor("features", []uint64{0, NumFeatures}, 1)).
		msg(12, tensor("label", []uint64{0}, 7)).
		msg(12, tensor("probabilities", []uint64{0, 2}, 1))
	m := pbuf{}.num(1, 8).msg(8, pbuf{}.num(2, 13)).msg(7, g)
	for k, v := range meta {
		m = m.msg(14, pbuf{}.str(1, k).str(2, v))
	}
	return m
}

// Runs only with PE_TEST_ONNXRUNTIME_LIB set to an ONNX Runtime shared library (go test -tags onnx).
func TestONNXScorer(t *testing.T) {
	lib := os.Getenv("PE_TEST_ONNXRUNTIME_LIB")
	if lib == "" {
		t.Skip("PE_TEST_ONNXRUNTIME_LIB is not set")
	}
	t.Setenv(OnnxRuntimeLibEnv, lib)
	path := writeGated(t, tinyModel(goodMeta()), nil)
	sc, err := openModel(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	if !sc.Enabled() || sc.CheckID() != "ml.gbm-v1" || sc.Points() != 0 {
		t.Fatalf("%s %v", sc.CheckID(), sc.Points())
	}
	hi, lo := &Window{FeatureVersion: FeatureVersion, ClientProtocol: 768}, &Window{FeatureVersion: FeatureVersion, ClientProtocol: 770}
	hi.Features[1], lo.Features[1] = 0.95, 0.2
	if !sc.Applies(hi) || sc.Applies(&Window{FeatureVersion: FeatureVersion, ClientProtocol: 767}) {
		t.Fatal("applies to tick clients only")
	}
	res, err := sc.Score([]*Window{hi, lo})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || float32(res[0].PCheat) != 0.95 || !res[0].Flagged || float32(res[1].PCheat) != 0.2 || res[1].Flagged {
		t.Fatalf("%+v", res)
	}

	synthetic := goodMeta()
	synthetic[MetaTrainedOnSynthetic] = "true"
	if _, err := openModel(writeGated(t, tinyModel(synthetic), nil), 0); !errors.Is(err, ErrModelRejected) {
		t.Errorf("synthetic model: %v", err)
	}
	if _, err := openModel(writeGated(t, tinyModel(goodMeta()), func(g map[string]any) { g["deployable"] = false }), 0); !errors.Is(err, ErrModelRejected) {
		t.Errorf("model without a deployable gate: %v", err)
	}

	// Through New: a gated model loads in shadow; a model the contract refuses after ONNX Runtime opened
	// it (threshold 0) leaves ML off in shadow and fails with points.
	if s, err := New(&app.Deps{Config: &config.Config{ML: config.ML{Model: path}}}); err != nil || !s.Enabled() {
		t.Errorf("gated model in shadow: %v %v", s, err)
	} else {
		_ = s.Close()
	}
	zero := goodMeta()
	zero[MetaThreshold] = "0"
	path0 := writeGated(t, tinyModel(zero), nil)
	if s, err := New(&app.Deps{Config: &config.Config{ML: config.ML{Model: path0}}, Log: slog.New(slog.DiscardHandler)}); err != nil || s.Enabled() {
		t.Errorf("threshold 0 in shadow: %v %v", s, err)
	}
	if _, err := New(&app.Deps{Config: &config.Config{ML: config.ML{Model: path0, Points: 5}}}); !errors.Is(err, ErrModelRejected) {
		t.Errorf("threshold 0 with points: %v", err)
	}

	o, err := sessionOptions()
	if err != nil {
		t.Fatal(err)
	}
	defer o.Destroy()
	for _, k := range []string{"session.intra_op.allow_spinning", "session.inter_op.allow_spinning"} {
		if v, err := o.GetSessionConfigEntry(k); err != nil || v != "0" {
			t.Errorf("%s = %q (%v)", k, v, err)
		}
	}
}
