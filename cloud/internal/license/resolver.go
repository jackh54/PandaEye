package license

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
)

// Persisted verification (ARCHITECTURE 5.3): license_valid_until = verified_at + FallbackValidity.
const (
	FallbackValidity = 24 * time.Hour
	// RecordEvery bounds how often an unchanged successful verification is written to the tenant row.
	RecordEvery = 5 * time.Minute
)

// ErrInvalid is returned by Resolve for keys that are invalid, of another product or revoked.
var ErrInvalid = errors.New("license: invalid licence")

// Admission is a verified licence key mapped to its tenant.
type Admission struct {
	Tenant     *store.Tenant
	MaxServers int  // 0 = unlimited
	Created    bool // the tenant was created by this call (first use of the key)
	Fallback   bool // the verifier was unreachable: the verification persisted on the tenant row was used
}

// Resolver maps licence keys to tenants (licenses.key_hmac = HMAC(PE_LICENSE_PEPPER, key)).
type Resolver struct {
	v      Verifier
	st     store.Store
	bus    *stream.Bus // may be nil: tenant settings are then not published to PE_TENANTS
	pepper []byte
	log    *slog.Logger
	now    func() time.Time

	mu    sync.Mutex
	cache map[string]store.Tenant // last tenant read per key HMAC (stale-OK on database errors)
}

// NewResolver returns a resolver. pepper is PE_LICENSE_PEPPER and must not be empty.
func NewResolver(v Verifier, st store.Store, bus *stream.Bus, pepper identity.Secret, log *slog.Logger) (*Resolver, error) {
	if pepper.IsZero() {
		return nil, errors.New("license: resolver needs PE_LICENSE_PEPPER")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Resolver{v: v, st: st, bus: bus, pepper: pepper.Bytes(), log: log, now: time.Now, cache: map[string]store.Tenant{}}, nil
}

// NewSharedResolver returns the process's resolver around the shared verifier (app.Deps.Shared).
func NewSharedResolver(d *app.Deps) (*Resolver, error) {
	r, err := d.Shared("license.resolver", func() (any, error) {
		v, err := New(d)
		if err != nil {
			return nil, err
		}
		return NewResolver(v, d.Store, d.Bus, d.Secrets.LicensePepper, d.Log.With("component", "license"))
	})
	if err != nil {
		return nil, err
	}
	return r.(*Resolver), nil
}

// Resolve verifies key and returns its tenant, creating the tenant on first use. Errors: ErrInvalid
// (answer CODE_UNAUTHENTICATED), or one wrapping ErrUnavailable when neither the verifier nor a
// persisted verification can decide (answer CODE_INTERNAL with a retry delay).
func (r *Resolver) Resolve(ctx context.Context, key string) (*Admission, error) {
	if !ValidFormat(key) {
		return nil, ErrInvalid
	}
	h := identity.LicenseKeyHMAC(r.pepper, key)
	lic, verr := r.v.Verify(ctx, key)
	if verr != nil {
		return r.fallback(ctx, h, verr)
	}
	if !lic.Valid {
		return nil, ErrInvalid
	}
	t, created, err := r.tenant(ctx, h)
	if err != nil {
		return nil, err
	}
	changed := created
	now := r.now().UTC()
	if t.MaxServers != lic.MaxServers || now.Sub(t.LicenseVerifiedAt) >= RecordEvery {
		if err := r.st.RecordLicenseCheck(ctx, t.ID, lic.MaxServers, now, now.Add(FallbackValidity)); err != nil {
			r.log.Warn("recording the licence verification failed", "tenant", t.ID, "err", err)
		} else {
			changed = changed || t.MaxServers != lic.MaxServers
			t.MaxServers, t.LicenseVerifiedAt, t.LicenseValidUntil = lic.MaxServers, now, now.Add(FallbackValidity)
		}
	}
	if lic.Plan != "" && lic.Plan != t.Plan {
		plan := lic.Plan
		if nt, err := r.st.UpdateTenant(ctx, t.ID, store.TenantPatch{Plan: &plan}, now); err != nil {
			r.log.Warn("updating the tenant plan from its licence failed", "tenant", t.ID, "err", err)
		} else {
			t.Plan, t.UpdatedAt, changed = nt.Plan, nt.UpdatedAt, true
		}
	}
	r.remember(h, t)
	if changed && r.bus != nil {
		if err := app.PublishTenant(ctx, r.st, r.bus, t.ID); err != nil {
			r.log.Warn("publishing tenant settings failed", "tenant", t.ID, "err", err)
		}
	}
	return &Admission{Tenant: t, MaxServers: lic.MaxServers, Created: created}, nil
}

// tenant finds or creates the tenant of a key HMAC. Database errors fall back to the last tenant
// read for the key.
func (r *Resolver) tenant(ctx context.Context, h []byte) (*store.Tenant, bool, error) {
	l, err := r.st.LookupLicense(ctx, h)
	if err == nil {
		if !l.RevokedAt.IsZero() {
			return nil, false, ErrInvalid
		}
		t, err := r.st.GetTenant(ctx, l.TenantID)
		if err != nil {
			return r.cachedTenant(h, err)
		}
		return t, false, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return r.cachedTenant(h, err)
	}
	for attempt := 0; ; attempt++ {
		now := r.now().UTC()
		id := identity.NewTenantID()
		t, l, created, err := r.st.EnsureLicenseTenant(ctx, h, store.NewTenant(id, "", strings.ReplaceAll(id, "_", "-"), now), now)
		switch {
		case err == nil && !l.RevokedAt.IsZero():
			return nil, false, ErrInvalid
		case err == nil:
			if created {
				r.log.Info("tenant created for a new licence", "tenant", t.ID, "license_fp", hex.EncodeToString(h[:4]))
			}
			return t, created, nil
		case errors.Is(err, store.ErrConflict) && attempt < 3:
			continue // the random id or slug collided
		default:
			return r.cachedTenant(h, err)
		}
	}
}

func (r *Resolver) cachedTenant(h []byte, cause error) (*store.Tenant, bool, error) {
	r.mu.Lock()
	t, ok := r.cache[string(h)]
	r.mu.Unlock()
	if !ok {
		return nil, false, fmt.Errorf("%w: tenant lookup: %w", ErrUnavailable, cause)
	}
	r.log.Warn("tenant lookup failed, using the cached tenant", "tenant", t.ID, "err", cause)
	return &t, false, nil
}

func (r *Resolver) remember(h []byte, t *store.Tenant) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.cache) >= 100_000 {
		clear(r.cache)
	}
	r.cache[string(h)] = *t
}

// fallback admits a key while the verifier is unreachable if its tenant's persisted verification
// is still valid.
func (r *Resolver) fallback(ctx context.Context, h []byte, verr error) (*Admission, error) {
	unavailable := fmt.Errorf("%w: %w", ErrUnavailable, verr)
	var t *store.Tenant
	l, err := r.st.LookupLicense(ctx, h)
	switch {
	case err == nil && !l.RevokedAt.IsZero():
		return nil, ErrInvalid
	case err == nil:
		if t, err = r.st.GetTenant(ctx, l.TenantID); err != nil {
			t = nil
		}
	case errors.Is(err, store.ErrNotFound):
		return nil, unavailable
	}
	if t == nil {
		ct, _, cerr := r.cachedTenant(h, err)
		if cerr != nil {
			return nil, unavailable
		}
		t = ct
	}
	if !r.now().Before(t.LicenseValidUntil) {
		return nil, unavailable
	}
	r.log.Warn("licence verifier unavailable, admitting with the persisted verification", "tenant", t.ID,
		"valid_until", t.LicenseValidUntil, "err", verr)
	return &Admission{Tenant: t, MaxServers: t.MaxServers, Fallback: true}, nil
}
