package license

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
)

// switchable is a verifier whose answers the test controls.
type switchable struct {
	mu   sync.Mutex
	lic  License
	err  error
	keys []string
}

func (s *switchable) set(l License, err error) { s.mu.Lock(); s.lic, s.err = l, err; s.mu.Unlock() }

func (s *switchable) Verify(_ context.Context, key string) (License, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, key)
	return s.lic, s.err
}

func TestResolver(t *testing.T) {
	ctx := context.Background()
	st := testkit.NewStore(t)
	bus := testkit.NewBus(t)
	var logs bytes.Buffer
	var logMu sync.Mutex
	log := app.NewLogger(lockedWriter{&logs, &logMu}, slog.LevelDebug)
	v := &switchable{}
	r, err := NewResolver(v, st, bus, identity.Secret(identity.RandomKey(32)), log)
	if err != nil {
		t.Fatal(err)
	}
	const key = "SECRET-LICENCE-KEY-123"

	v.set(License{Valid: true, MaxServers: 3, Plan: "pro"}, nil)
	a, err := r.Resolve(ctx, key)
	if err != nil || !a.Created || a.MaxServers != 3 || a.Fallback || !identity.ValidTenantID(a.Tenant.ID) {
		t.Fatalf("first use: %+v %v", a, err)
	}
	tid := a.Tenant.ID
	stored, err := st.GetTenant(ctx, tid)
	if err != nil || stored.MaxServers != 3 || stored.Plan != "pro" || stored.LicenseVerifiedAt.IsZero() ||
		stored.LicenseValidUntil.Sub(stored.LicenseVerifiedAt) != FallbackValidity {
		t.Fatalf("persisted verification: %+v %v", stored, err)
	}
	if s, _, err := bus.GetTenant(ctx, tid); err != nil || s.Plan != "pro" || s.MaxServers != 3 {
		t.Fatalf("PE_TENANTS: %+v %v", s, err)
	}

	a, err = r.Resolve(ctx, key)
	if err != nil || a.Created || a.Tenant.ID != tid {
		t.Fatalf("second use: %+v %v", a, err)
	}

	v.set(License{}, ErrUnavailable)
	a, err = r.Resolve(ctx, key)
	if err != nil || !a.Fallback || a.Tenant.ID != tid || a.MaxServers != 3 {
		t.Fatalf("fallback: %+v %v", a, err)
	}
	if _, err := r.Resolve(ctx, "never-seen-key"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unknown key while unavailable: %v", err)
	}
	if err := st.RecordLicenseCheck(ctx, tid, 3, time.Now().Add(-25*time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, key); !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInvalid) {
		t.Fatalf("expired persisted verification: %v", err)
	}

	v.set(License{}, nil)
	if _, err := r.Resolve(ctx, key); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid: %v", err)
	}
	if _, err := r.Resolve(ctx, "bad key"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed: %v", err)
	}

	v.set(License{Valid: true}, nil)
	if err := st.RevokeLicense(ctx, tid, identity.LicenseKeyHMAC(r.pepper, key), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, key); !errors.Is(err, ErrInvalid) {
		t.Fatalf("revoked: %v", err)
	}
	v.set(License{}, ErrUnavailable)
	if _, err := r.Resolve(ctx, key); !errors.Is(err, ErrInvalid) {
		t.Fatalf("revoked while unavailable: %v", err)
	}

	logMu.Lock()
	defer logMu.Unlock()
	if strings.Contains(logs.String(), key) {
		t.Fatalf("licence key in logs: %s", logs.String())
	}
}

type lockedWriter struct {
	b  *bytes.Buffer
	mu *sync.Mutex
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}
