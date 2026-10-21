//go:build !onnx

package ml

import "fmt"

// openModel fails in builds without the onnx tag: PE_ML_MODEL needs a `-tags onnx` build.
func openModel(path string, points float64) (Scorer, error) {
	return nil, fmt.Errorf("ml: PE_ML_MODEL is set (%s) but this build has no ONNX support (build with -tags onnx)", path)
}
