package ml

import (
	"encoding/json"
	"errors"
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
