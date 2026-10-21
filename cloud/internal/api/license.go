package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/license"
	"github.com/jackh54/PandaEye/cloud/internal/store"
)

// licenseValidity is how long a successful verification may be used while the verifier is
// unreachable (ARCHITECTURE 5.3).
const licenseValidity = 24 * time.Hour

type licenseReq struct {
	LicenseKey string `json:"license_key"`
}

type rebindReq struct {
	LicenseKey   string `json:"license_key"`
	RevokeOthers bool   `json:"revoke_others"`
}

// verifyLicense checks a key with the licence verifier. Failed attempts are limited per client IP.
func (s *Service) verifyLicense(ctx context.Context, key string, rc *reqCtx) (license.License, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 256 {
		return license.License{}, badRequest("license_key is required")
	}
	ipk := ipKey(rc.p.clientIP)
	vctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	l, err := s.verifier.Verify(vctx, key)
	if err != nil {
		s.log.Warn("licence verification unavailable", "err", err)
		return l, unavailable("the licence could not be verified right now; retry later")
	}
	if !l.Valid {
		if ok, wait := s.licenseFails.allow(ipk, rc.now); !ok {
			return l, &apiError{status: http.StatusTooManyRequests, code: codeRateLimited, msg: "too many invalid licence keys", retryAfter: wait}
		}
		return l, errf(http.StatusUnauthorized, codeInvalidLicense, "the licence key is not valid")
	}
	return l, nil
}

// handleAuthLicense verifies a licence key and returns its tenant, creating the tenant and the
// licence row on first use (dashboard /setup; service token, no assertion).
func (s *Service) handleAuthLicense(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	var req licenseReq
	if err := decodeBody(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	lic, err := s.verifyLicense(ctx, req.LicenseKey, rc)
	if err != nil {
		return err
	}
	hmac := identity.LicenseKeyHMAC(s.d.Secrets.LicensePepper.Bytes(), strings.TrimSpace(req.LicenseKey))
	var (
		t       *store.Tenant
		l       *store.License
		created bool
	)
	for attempt := 0; ; attempt++ {
		id := identity.NewTenantID()
		nt := store.NewTenant(id, "Tenant "+id[2:8], identity.Slug("tenant "+id[2:]), rc.now)
		if lic.Plan != "" {
			nt.Plan = lic.Plan
		}
		nt.MaxServers = lic.MaxServers
		t, l, created, err = s.d.Store.EnsureLicenseTenant(ctx, hmac, nt, rc.now)
		if errors.Is(err, store.ErrConflict) && attempt < 3 {
			continue // slug collision: another id
		}
		if err != nil {
			return err
		}
		break
	}
	if !l.RevokedAt.IsZero() {
		return errf(http.StatusUnauthorized, codeInvalidLicense, "the licence key was revoked for this tenant")
	}
	if err := s.recordLicense(ctx, t, lic, rc.now); err != nil {
		return err
	}
	action := "license.verify"
	if created {
		action = "tenant.create"
	}
	s.audit(ctx, t.ID, kindService, action, t.ID, map[string]any{"plan": lic.Plan, "max_servers": lic.MaxServers}, rc.now)
	writeJSON(w, http.StatusOK, map[string]any{"tenant_id": t.ID, "slug": t.Slug, "created": created})
	return nil
}

// recordLicense stores the verification (max servers, validity), follows the licence's plan and
// republishes the tenant settings.
func (s *Service) recordLicense(ctx context.Context, t *store.Tenant, lic license.License, now time.Time) error {
	if err := s.d.Store.RecordLicenseCheck(ctx, t.ID, lic.MaxServers, now, now.Add(licenseValidity)); err != nil {
		return err
	}
	if lic.Plan != "" && lic.Plan != t.Plan {
		plan := lic.Plan
		var err error
		if t, err = s.d.Store.UpdateTenant(ctx, t.ID, store.TenantPatch{Plan: &plan}, now); err != nil {
			return err
		}
	}
	if err := app.PublishTenant(ctx, s.d.Store, s.d.Bus, t.ID); err != nil {
		s.log.Warn("tenant settings not published after licence check", "err", err)
	}
	return nil
}

// handleAuthRebind binds an additional or rotated licence key to the caller's tenant (owner),
// optionally revoking every other key of the tenant.
func (s *Service) handleAuthRebind(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	var req rebindReq
	if err := decodeBody(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	t, err := s.d.Store.GetTenant(ctx, rc.tenant())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return notFound("tenant")
		}
		return err
	}
	lic, err := s.verifyLicense(ctx, req.LicenseKey, rc)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.code == codeInvalidLicense {
			ae.status = http.StatusBadRequest // the caller is authenticated; the key is what is wrong
		}
		return err
	}
	hmac := identity.LicenseKeyHMAC(s.d.Secrets.LicensePepper.Bytes(), strings.TrimSpace(req.LicenseKey))
	if err := s.d.Store.CreateLicense(ctx, t.ID, hmac, rc.now); err != nil {
		if !errors.Is(err, store.ErrConflict) {
			return err
		}
		existing, lerr := s.d.Store.LookupLicense(ctx, hmac)
		if lerr != nil {
			return lerr
		}
		if existing.TenantID != t.ID {
			return conflict("the licence key is bound to another tenant")
		}
		if !existing.RevokedAt.IsZero() {
			return conflict("the licence key was revoked for this tenant")
		}
	}
	revoked := 0
	if req.RevokeOthers {
		ls, err := s.d.Store.ListLicenses(ctx, t.ID)
		if err != nil {
			return err
		}
		for _, l := range ls {
			if bytes.Equal(l.KeyHMAC, hmac) || !l.RevokedAt.IsZero() {
				continue
			}
			if err := s.d.Store.RevokeLicense(ctx, t.ID, l.KeyHMAC, rc.now); err != nil {
				return err
			}
			revoked++
		}
	}
	if err := s.recordLicense(ctx, t, lic, rc.now); err != nil {
		return err
	}
	s.auditRC(r, rc, "license.rebind", t.ID, map[string]any{"revoked": revoked, "plan": lic.Plan})
	writeJSON(w, http.StatusOK, map[string]any{"tenant_id": t.ID, "slug": t.Slug, "revoked": revoked})
	return nil
}
