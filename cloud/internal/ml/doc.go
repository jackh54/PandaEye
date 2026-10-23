// Package ml builds per-player feature windows (ml/FEATURES.md, feature version 1) from the worker's
// derived state and scores them with an optional model (ARCHITECTURE 5.10).
//
//   - features.go: the 94-feature vector of one session per 30 s window (Session).
//   - The worker writes finalised windows to telemetry (telemetry.FeatureSink, table
//     feature_windows) and, when a model is loaded, scores them.
//   - Scorer scores windows. Default builds have only the disabled scorer; `-tags onnx` builds an
//     ONNX Runtime implementation (github.com/yalue/onnxruntime_go, shared library at runtime) that
//     refuses models whose feature version or names differ from FeatureNames, that were trained on
//     synthetic data, or that lack a deployable gate report for exactly this file (CheckGate).
package ml

import (
	"errors"
	"strings"

	"github.com/jackh54/PandaEye/cloud/internal/app"
)

// Window is one finalised feature window of a session (ml/FEATURES.md sections 4-5).
type Window struct {
	TenantID       string
	Player         [16]byte
	ServerID       string
	SessionID      string
	WindowStartUs  int64 // normalised Unix microseconds, multiple of WindowUs
	WindowEndUs    int64 // WindowStartUs + WindowUs
	ClientProtocol int32 // PlayerJoin.protocol_version (-1 unknown)
	TickClient     bool
	HotLoaded      bool
	FeatureVersion uint16
	Features       [NumFeatures]float32 // table order, defaults and clipping applied
	WorkerUnixUs   int64                // when the worker finalised the window
}

// Score is a model's verdict on one window.
type Score struct {
	PCheat  float64 // P(cheat) = probabilities[:, 1]
	Flagged bool    // PCheat >= the model's threshold
}

// Scorer scores feature windows with a model. A disabled scorer scores nothing.
type Scorer interface {
	// Enabled reports whether a model is loaded.
	Enabled() bool
	// CheckID is the signal check id of the model: "ml.<model_name>".
	CheckID() string
	// Points are the raw points of a flagged window: PE_ML_POINTS, 0 by default (shadow).
	Points() float64
	// Applies reports whether the model may score w (client protocol >= the model's minimum).
	Applies(w *Window) bool
	// Score scores windows that Applies accepted.
	Score(ws []*Window) ([]Score, error)
	// Close releases the model.
	Close() error
}

// ErrModelRejected is returned for models that must not be loaded (feature contract mismatch,
// trained on synthetic data, missing metadata, no deployable gate report for the file).
var ErrModelRejected = errors.New("ml: model rejected")

// New returns the scorer configured by PE_ML_MODEL: disabled when unset; in builds without the onnx
// tag a configured model cannot load. A model that cannot load (no onnx support, no ONNX Runtime,
// rejected metadata or gate report) is an error when PE_ML_POINTS > 0; in shadow (0 points) it is
// logged and scoring stays off, so the rest of the process keeps running. The scorer is shared by
// every service of the process.
func New(d *app.Deps) (Scorer, error) {
	if d == nil || d.Config == nil || strings.TrimSpace(d.Config.ML.Model) == "" {
		return Disabled{}, nil
	}
	s, err := d.Shared("ml.scorer", func() (any, error) {
		sc, err := openModel(d.Config.ML.Model, d.Config.ML.Points)
		if err != nil {
			if d.Config.ML.Points > 0 {
				return nil, err
			}
			if d.Log != nil {
				d.Log.Error("ml: model not loaded, scoring is off (PE_ML_POINTS is 0)", "model", d.Config.ML.Model, "err", err)
			}
			return Disabled{}, nil
		}
		d.OnClose(sc.Close)
		return sc, nil
	})
	if err != nil {
		return nil, err
	}
	return s.(Scorer), nil
}

// Disabled is the scorer of default builds and of processes without PE_ML_MODEL.
type Disabled struct{}

func (Disabled) Enabled() bool                    { return false }
func (Disabled) CheckID() string                  { return "" }
func (Disabled) Points() float64                  { return 0 }
func (Disabled) Applies(*Window) bool             { return false }
func (Disabled) Score([]*Window) ([]Score, error) { return nil, nil }
func (Disabled) Close() error                     { return nil }

// ModelMeta is the metadata a model must carry (ml/FEATURES.md section 8).
type ModelMeta struct {
	FeatureVersion     string // pandaeye.feature_version
	FeatureNames       string // pandaeye.feature_names (JSON array)
	NFeatures          string // pandaeye.n_features
	ModelName          string // pandaeye.model_name
	Threshold          string // pandaeye.threshold
	MinClientProtocol  string // pandaeye.min_client_protocol
	TrainedOnSynthetic string // pandaeye.trained_on_synthetic
}
