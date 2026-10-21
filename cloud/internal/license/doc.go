// Package license verifies licence keys (ARCHITECTURE 5.9): static (PE_STATIC_LICENSES) and
// pandascript (HTTP, cached, singleflight, global rate limit).
//
// This is the foundation placeholder: the Verifier contract and New are final; Verify reports
// ErrNotImplemented until the package is implemented.
package license

import (
	"context"
	"errors"

	"github.com/jackh54/PandaEye/cloud/internal/app"
)

// ErrNotImplemented is returned by Verify until verification exists.
var ErrNotImplemented = errors.New("license: not implemented")

// License is the outcome of a verification.
type License struct {
	Valid      bool
	MaxServers int // 0 = unlimited
	Plan       string
}

// Verifier checks licence keys. Errors mean "unknown" (verifier unreachable), never "invalid".
type Verifier interface {
	Verify(ctx context.Context, key string) (License, error)
}

// New returns the process's verifier, shared by every service of the process (app.Deps.Shared).
func New(d *app.Deps) (Verifier, error) {
	v, err := d.Shared("license", func() (any, error) { return placeholder{}, nil })
	if err != nil {
		return nil, err
	}
	return v.(Verifier), nil
}

type placeholder struct{}

func (placeholder) Verify(context.Context, string) (License, error) {
	return License{}, ErrNotImplemented
}
