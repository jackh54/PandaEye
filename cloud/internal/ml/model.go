package ml

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Model metadata keys (ml/FEATURES.md section 8, ModelProto.metadata_props).
const (
	MetaFeatureVersion     = "pandaeye.feature_version"
	MetaFeatureNames       = "pandaeye.feature_names"
	MetaNFeatures          = "pandaeye.n_features"
	MetaModelName          = "pandaeye.model_name"
	MetaThreshold          = "pandaeye.threshold"
	MetaMinClientProtocol  = "pandaeye.min_client_protocol"
	MetaTrainedOnSynthetic = "pandaeye.trained_on_synthetic"
)

// ModelInfo is the validated contract of a loaded model.
type ModelInfo struct {
	Name              string  // signal check id is "ml." + Name
	Threshold         float64 // operating threshold on P(cheat)
	MinClientProtocol int32
}

var modelNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)

// ValidateModelMeta checks a model's metadata against the Go feature contract: feature version and
// ordered names must equal FeatureVersion and FeatureNames, the model must say it was not trained on
// synthetic data, and the name, threshold and minimum client protocol must be well formed. lookup
// returns a metadata value and whether it is present.
func ValidateModelMeta(lookup func(key string) (string, bool)) (ModelInfo, error) {
	get := func(k string) (string, error) {
		v, ok := lookup(k)
		if !ok {
			return "", fmt.Errorf("%w: metadata %s missing", ErrModelRejected, k)
		}
		return strings.TrimSpace(v), nil
	}
	var info ModelInfo
	v, err := get(MetaFeatureVersion)
	if err != nil {
		return info, err
	}
	if v != strconv.Itoa(FeatureVersion) {
		return info, fmt.Errorf("%w: feature version %q, this build computes %d", ErrModelRejected, v, FeatureVersion)
	}
	v, err = get(MetaFeatureNames)
	if err != nil {
		return info, err
	}
	var names []string
	if err := json.Unmarshal([]byte(v), &names); err != nil {
		return info, fmt.Errorf("%w: feature names are not a JSON array: %v", ErrModelRejected, err)
	}
	if !slices.Equal(names, FeatureNames()) {
		return info, fmt.Errorf("%w: feature names differ from feature version %d", ErrModelRejected, FeatureVersion)
	}
	if v, ok := lookup(MetaNFeatures); ok && strings.TrimSpace(v) != strconv.Itoa(NumFeatures) {
		return info, fmt.Errorf("%w: n_features %q, want %d", ErrModelRejected, v, NumFeatures)
	}
	v, err = get(MetaTrainedOnSynthetic)
	if err != nil {
		return info, err
	}
	if !strings.EqualFold(v, "false") {
		return info, fmt.Errorf("%w: trained_on_synthetic is %q (synthetic models are never deployed)", ErrModelRejected, v)
	}
	if info.Name, err = get(MetaModelName); err != nil {
		return info, err
	}
	if !modelNameRE.MatchString(info.Name) {
		return info, fmt.Errorf("%w: model name %q", ErrModelRejected, info.Name)
	}
	if v, err = get(MetaThreshold); err != nil {
		return info, err
	}
	if info.Threshold, err = strconv.ParseFloat(v, 64); err != nil || !(info.Threshold > 0 && info.Threshold <= 1) {
		return info, fmt.Errorf("%w: threshold %q", ErrModelRejected, v)
	}
	if v, err = get(MetaMinClientProtocol); err != nil {
		return info, err
	}
	p, err := strconv.ParseInt(v, 10, 32)
	if err != nil || p < 768 {
		return info, fmt.Errorf("%w: min_client_protocol %q (v1 models apply to tick clients only)", ErrModelRejected, v)
	}
	info.MinClientProtocol = int32(p)
	return info, nil
}

// CheckID returns the signal check id of a model.
func (m ModelInfo) CheckID() string { return "ml." + m.Name }
