// Package ml builds per-player feature windows and scores them with an optional model (ARCHITECTURE
// 5.10); the ONNX implementation is behind the `onnx` build tag.
//
// This is the foundation placeholder: New is final and returns a disabled scorer.
package ml

import "github.com/jackh54/PandaEye/cloud/internal/app"

// Scorer scores feature windows.
type Scorer interface {
	Enabled() bool
}

// New returns the scorer configured by PE_ML_MODEL (disabled in default builds).
func New(d *app.Deps) (Scorer, error) { return disabled{}, nil }

type disabled struct{}

func (disabled) Enabled() bool { return false }
