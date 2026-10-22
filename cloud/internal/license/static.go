package license

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackh54/PandaEye/cloud/internal/config"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
)

// Static verifies keys against PE_STATIC_LICENSES. Keys are held by HMAC only.
type Static struct {
	mac  []byte
	keys map[string]License
	m    *verifyMetrics
}

// NewStatic returns a verifier for entries. pepper keys the in-memory lookup (a random key is used
// when it is empty).
func NewStatic(entries []config.StaticLicense, pepper []byte) *Static {
	s := &Static{mac: cacheKeyMAC(pepper), keys: make(map[string]License, len(entries))}
	for _, e := range entries {
		plan := e.Plan
		if plan == "" {
			plan = "free"
		}
		s.keys[string(identity.HMACSHA256(s.mac, e.Key.Bytes()))] = License{Valid: true, MaxServers: e.MaxServers, Plan: plan}
	}
	return s
}

// Verify implements Verifier. It never fails.
func (s *Static) Verify(_ context.Context, key string) (License, error) {
	if !ValidFormat(key) {
		s.m.observe("format")
		return License{}, nil
	}
	l, ok := s.keys[string(identity.HMACSHA256(s.mac, []byte(key)))]
	if !ok {
		s.m.observe("invalid")
		return License{}, nil
	}
	s.m.observe("valid")
	return l, nil
}

// FromConfig builds the verifier of PE_LICENSE_MODE.
func FromConfig(c config.License, pepper []byte, log *slog.Logger, m *metrics.Metrics) (Verifier, error) {
	vm := newVerifyMetrics(m, c.Mode)
	switch c.Mode {
	case config.LicenseStatic, "":
		s := NewStatic(c.Static, pepper)
		s.m = vm
		return s, nil
	case config.LicensePandascript:
		if c.URL == "" || c.Product == "" {
			return nil, errors.New("license: pandascript mode needs PE_LICENSE_URL and PE_LICENSE_PRODUCT")
		}
		p := NewPandascript(PandascriptOptions{URL: c.URL, Product: c.Product, Pepper: pepper, Log: log})
		p.m = vm
		if len(c.Static) > 0 {
			// PE_STATIC_LICENSES stay valid next to the licence worker (own servers, partners).
			s := NewStatic(c.Static, pepper)
			s.m = vm
			return &Chain{First: s, Then: p}, nil
		}
		return p, nil
	default:
		return nil, fmt.Errorf("license: unknown mode %q", c.Mode)
	}
}

// cacheKeyMAC returns the key of in-memory lookups: the pepper itself, so lookups are keyed by the
// key's identity.LicenseKeyHMAC, or a random key when no pepper is configured.
func cacheKeyMAC(pepper []byte) []byte {
	if len(pepper) > 0 {
		return pepper
	}
	return identity.RandomKey(32)
}

// Chain answers from First when it knows the key (a valid licence) and asks Then otherwise.
type Chain struct {
	First, Then Verifier
}

// Verify implements Verifier.
func (c *Chain) Verify(ctx context.Context, key string) (License, error) {
	if l, err := c.First.Verify(ctx, key); err == nil && l.Valid {
		return l, nil
	}
	return c.Then.Verify(ctx, key)
}
