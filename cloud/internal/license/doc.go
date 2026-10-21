// Package license verifies licence keys (ARCHITECTURE 5.9) and maps them to tenants.
//
// A Verifier answers whether a key is valid: static (PE_STATIC_LICENSES) or pandascript (the HTTP
// licence worker; cached by key HMAC, singleflight per key, a global outbound rate limit). Only a
// definitive answer makes a key invalid; an unreachable verifier is an error, never "invalid".
//
// A Resolver turns a key into its tenant (licenses.key_hmac -> tenant, created with a random id on
// first use), persists the last successful verification on the tenant row and falls back to it for
// up to 24 h while the verifier is unreachable.
//
// Raw licence keys are never stored or logged; logs carry Fingerprint(key) at most.
package license

import (
	"context"
	"errors"

	"github.com/jackh54/PandaEye/cloud/internal/app"
)

// License is the outcome of a verification.
type License struct {
	Valid      bool
	MaxServers int    // 0 = unlimited
	Plan       string // "" = the verifier does not report plans (the tenant keeps its plan)
}

// Verifier checks licence keys. Errors mean "unknown" (verifier unreachable), never "invalid".
type Verifier interface {
	Verify(ctx context.Context, key string) (License, error)
}

// ErrUnavailable is wrapped by Verify errors: the verifier could not give a definitive answer.
var ErrUnavailable = errors.New("license: verifier unavailable")

// New returns the process's verifier, shared by every service of the process (app.Deps.Shared).
func New(d *app.Deps) (Verifier, error) {
	v, err := d.Shared("license", func() (any, error) {
		return FromConfig(d.Config.License, d.Secrets.LicensePepper.Bytes(), d.Log.With("component", "license"), d.Metrics)
	})
	if err != nil {
		return nil, err
	}
	return v.(Verifier), nil
}
