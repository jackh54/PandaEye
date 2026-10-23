package ml

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/config"
)

func goodMeta() map[string]string {
	names, _ := json.Marshal(FeatureNames())
	return map[string]string{
		MetaFeatureVersion: "1", MetaFeatureNames: string(names), MetaNFeatures: "94", MetaModelName: "gbm-v1",
		MetaThreshold: "0.92", MetaMinClientProtocol: "768", MetaTrainedOnSynthetic: "false",
	}
}

func lookupIn(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestValidateModelMeta(t *testing.T) {
	info, err := ValidateModelMeta(lookupIn(goodMeta()))
	if err != nil || info.CheckID() != "ml.gbm-v1" || info.Threshold != 0.92 || info.MinClientProtocol != 768 {
		t.Fatalf("%+v %v", info, err)
	}
	reordered := FeatureNames()
	reordered[0], reordered[1] = reordered[1], reordered[0]
	bad, _ := json.Marshal(reordered)
	for name, mod := range map[string]func(m map[string]string){
		"other version":       func(m map[string]string) { m[MetaFeatureVersion] = "2" },
		"reordered names":     func(m map[string]string) { m[MetaFeatureNames] = string(bad) },
		"names missing":       func(m map[string]string) { delete(m, MetaFeatureNames) },
		"synthetic":           func(m map[string]string) { m[MetaTrainedOnSynthetic] = "true" },
		"synthetic unknown":   func(m map[string]string) { delete(m, MetaTrainedOnSynthetic) },
		"bad threshold":       func(m map[string]string) { m[MetaThreshold] = "1.5" },
		"zero threshold":      func(m map[string]string) { m[MetaThreshold] = "0" },
		"negative threshold":  func(m map[string]string) { m[MetaThreshold] = "-0.1" },
		"non-tick model":      func(m map[string]string) { m[MetaMinClientProtocol] = "47" },
		"bad name":            func(m map[string]string) { m[MetaModelName] = "Bad Name!" },
		"wrong feature count": func(m map[string]string) { m[MetaNFeatures] = "93" },
	} {
		m := goodMeta()
		mod(m)
		if _, err := ValidateModelMeta(lookupIn(m)); !errors.Is(err, ErrModelRejected) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
}

func TestNewScorerDisabledByDefault(t *testing.T) {
	s, err := New(&app.Deps{Config: &config.Config{}})
	if err != nil || s.Enabled() {
		t.Fatalf("%v %v", s, err)
	}
	if res, err := s.Score([]*Window{{}}); err != nil || res != nil || s.Applies(&Window{ClientProtocol: 769}) {
		t.Fatal("the disabled scorer scores nothing")
	}
	if s, err := New(nil); err != nil || s.Enabled() {
		t.Fatal("nil deps")
	}
}

// writeGated writes model bytes to <dir>/model.onnx with a passing gate report (adjusted by mod) and
// returns the model path.
func writeGated(t *testing.T, model []byte, mod func(g map[string]any)) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model.onnx")
	sum := sha256.Sum256(model)
	g := map[string]any{"deployable": true, "synthetic": false, "feature_version": 1, "model_sha256": hex.EncodeToString(sum[:])}
	if mod != nil {
		mod(g)
	}
	b, _ := json.Marshal(g)
	if err := os.WriteFile(path, model, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(GatePath(path), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckGate(t *testing.T) {
	if GatePath("/srv/ml/model.onnx") != "/srv/ml/model.gate.json" {
		t.Fatal(GatePath("/srv/ml/model.onnx"))
	}
	model := []byte("stand-in model bytes")
	if err := CheckGate(writeGated(t, model, nil)); err != nil {
		t.Fatal(err)
	}
	if err := CheckGate(writeGated(t, model, func(g map[string]any) { g["model_sha256"] = strings.ToUpper(g["model_sha256"].(string)) })); err != nil {
		t.Fatalf("hex case: %v", err)
	}
	for name, mod := range map[string]func(g map[string]any){
		"not deployable":    func(g map[string]any) { g["deployable"] = false },
		"deployable absent": func(g map[string]any) { delete(g, "deployable") },
		"synthetic":         func(g map[string]any) { g["synthetic"] = true },
		"synthetic absent":  func(g map[string]any) { delete(g, "synthetic") },
		"feature version":   func(g map[string]any) { g["feature_version"] = 2 },
		"hash absent":       func(g map[string]any) { delete(g, "model_sha256") },
		"hash of another":   func(g map[string]any) { g["model_sha256"] = strings.Repeat("0", 64) },
	} {
		if err := CheckGate(writeGated(t, model, mod)); !errors.Is(err, ErrModelRejected) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	// A report without a hash (--sklearn, or from before the hash) says so instead of "another file".
	if err := CheckGate(writeGated(t, model, func(g map[string]any) { g["model_sha256"] = nil })); err == nil ||
		!strings.Contains(err.Error(), "no model_sha256") {
		t.Errorf("null hash: %v", err)
	}
	path := writeGated(t, model, nil)
	if err := os.WriteFile(path, append(model, '!'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckGate(path); !errors.Is(err, ErrModelRejected) {
		t.Errorf("model changed after its gate: %v", err)
	}
	if err := os.WriteFile(GatePath(path), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckGate(path); !errors.Is(err, ErrModelRejected) {
		t.Errorf("invalid report: %v", err)
	}
	if err := CheckGate(filepath.Join(t.TempDir(), "model.onnx")); !errors.Is(err, ErrModelRejected) {
		t.Errorf("no report: %v", err)
	}
}

// A model that cannot load disables ML in shadow (0 points) and fails the process with points.
func TestNewLoadFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model.onnx") // no model, no gate report (and no onnx support by default)
	var logs bytes.Buffer
	d := &app.Deps{Config: &config.Config{ML: config.ML{Model: path}}, Log: slog.New(slog.NewJSONHandler(&logs, nil))}
	s, err := New(d)
	if err != nil || s.Enabled() {
		t.Fatalf("shadow: %v %v", s, err)
	}
	if !strings.Contains(logs.String(), `"level":"ERROR"`) || !strings.Contains(logs.String(), "scoring is off") {
		t.Fatalf("load failure not logged: %s", logs.String())
	}
	if s, err := New(&app.Deps{Config: &config.Config{ML: config.ML{Model: path, Points: 5}}}); err == nil {
		t.Fatalf("points > 0: %v loaded", s)
	}
}
