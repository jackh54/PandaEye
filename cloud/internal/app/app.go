// Package app builds the dependencies every PandaEye service shares (configuration, logger, store,
// event bus, metrics, resolved secrets) and runs services with graceful shutdown.
//
// Service packages (gateway, worker, verdict, api) and component packages (license, telemetry, ml)
// import app; app never imports them. cmd/pandaeye constructs each service with New(*Deps) and
// passes them to Run.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/config"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/jackh54/PandaEye/cloud/internal/netutil"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/store/sqlstore"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
)

// Service is a long-running component. Run blocks until ctx is cancelled (then it shuts down within
// ServiceStopTimeout and returns nil) or a fatal error occurs.
type Service interface {
	Run(ctx context.Context) error
}

// Secrets are the resolved process secrets.
type Secrets struct {
	// OutboundKey signs SignedOutbound and derives the webhook encryption key. In `all` without
	// PE_OUTBOUND_KEY it is generated once and kept in the database (meta), so stored webhooks stay
	// readable across restarts.
	OutboundKey identity.Secret
	// LicensePepper keys licence lookups (identity.LicenseKeyHMAC); generated and kept in the database
	// in `all` without PE_LICENSE_PEPPER. Empty in roles that do not need it.
	LicensePepper identity.Secret
}

// Deps is what services are built from.
type Deps struct {
	Config  *config.Config
	Log     *slog.Logger
	Metrics *metrics.Metrics
	Store   store.Store
	Bus     *stream.Bus // nil for `migrate`, and for `replay` without PE_NATS_URL
	Secrets Secrets
	Version string
	Started time.Time

	sharedMu sync.Mutex
	shared   map[string]any
	closers  []func() error
}

// Options tunes New.
type Options struct {
	Version string
	Logger  *slog.Logger // default: NewLogger(os.Stderr, cfg.LogLevel)
	// HTTPClient fetches the Cloudflare ranges (PE_TRUSTED_PROXIES_PRESET); default 5 s timeout.
	HTTPClient *http.Client
}

// New builds the dependencies of cfg.Role: logger, metrics, store (migrated in `all`, otherwise its
// schema must be current), secrets and, except for `migrate`, the event bus. Close releases them.
func New(ctx context.Context, cfg *config.Config, opts Options) (*Deps, error) {
	log := opts.Logger
	if log == nil {
		log = NewLogger(os.Stderr, cfg.LogLevel)
	}
	d := &Deps{Config: cfg, Log: log, Metrics: metrics.New(), Version: opts.Version, Started: time.Now(), shared: map[string]any{}}
	ok := false
	defer func() {
		if !ok {
			d.Close()
		}
	}()
	if cfg.TrustedProxyPreset == config.PresetCloudflare {
		refreshCloudflare(ctx, cfg, opts.HTTPClient, log)
	}
	st, err := sqlstore.Open(ctx, cfg.DB.Driver, cfg.DB.DSN.Reveal(), sqlstore.Options{})
	if err != nil {
		return nil, err
	}
	d.Store = st
	d.closers = append(d.closers, st.Close)
	if cfg.Role == config.RoleAll {
		n, err := st.Migrate(ctx)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			log.Info("database migrated", "applied", n)
		}
	} else if cfg.Role != config.RoleMigrate {
		if err := st.CheckSchema(ctx); err != nil {
			return nil, err
		}
	}
	if cfg.Role != config.RoleMigrate {
		if d.Secrets, err = resolveSecrets(ctx, cfg, st); err != nil {
			return nil, err
		}
	}
	if cfg.Role != config.RoleMigrate && !(cfg.Role == config.RoleReplay && cfg.NATS.Embedded()) {
		bus, err := stream.Open(ctx, stream.ConfigFrom(cfg), log.With("component", "stream"), d.Metrics)
		if err != nil {
			return nil, err
		}
		d.Bus = bus
		d.closers = append(d.closers, bus.Close)
	}
	ok = true
	return d, nil
}

func refreshCloudflare(ctx context.Context, cfg *config.Config, client *http.Client, log *slog.Logger) {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	ps, err := netutil.FetchCloudflare(ctx, client, netutil.CloudflareIPv4URL, netutil.CloudflareIPv6URL)
	if err != nil {
		log.Warn("cloudflare ranges not refreshed, using the embedded list", "err", err)
		return
	}
	cfg.TrustedProxies.SetCloudflare(ps)
	log.Info("cloudflare ranges refreshed", "ranges", len(ps))
}

// resolveSecrets takes the configured secrets, generating and persisting missing ones in `all`.
func resolveSecrets(ctx context.Context, cfg *config.Config, st store.MetaStore) (Secrets, error) {
	s := Secrets{OutboundKey: cfg.OutboundKey, LicensePepper: cfg.License.Pepper}
	if cfg.Role != config.RoleAll {
		return s, nil
	}
	fill := func(dst *identity.Secret, key string) error {
		if !dst.IsZero() {
			return nil
		}
		v, err := st.PutMetaIfAbsent(ctx, key, identity.RandomKey(32))
		if err != nil {
			return fmt.Errorf("app: %s: %w", key, err)
		}
		*dst = identity.Secret(v)
		return nil
	}
	if err := fill(&s.OutboundKey, store.MetaOutboundKey); err != nil {
		return s, err
	}
	return s, fill(&s.LicensePepper, store.MetaLicensePepper)
}

// Shared returns the value build produced for key, building it once per Deps. Components that
// several services of one process use (`all`: one licence verifier for gateway and api, one
// telemetry sink for worker, verdict and api) share it through here. A failed build is retried on
// the next call.
func (d *Deps) Shared(key string, build func() (any, error)) (any, error) {
	d.sharedMu.Lock()
	defer d.sharedMu.Unlock()
	if v, ok := d.shared[key]; ok {
		return v, nil
	}
	v, err := build()
	if err != nil {
		return nil, err
	}
	d.shared[key] = v
	return v, nil
}

// OnClose registers fn to run in Close (before the bus and store close, in reverse order).
func (d *Deps) OnClose(fn func() error) {
	d.sharedMu.Lock()
	defer d.sharedMu.Unlock()
	d.closers = append(d.closers, fn)
}

// Close releases everything in reverse order of creation: registered closers, then the bus (drained)
// and the store.
func (d *Deps) Close() error {
	d.sharedMu.Lock()
	closers := d.closers
	d.closers = nil
	d.sharedMu.Unlock()
	var errs []error
	for i := len(closers) - 1; i >= 0; i-- {
		errs = append(errs, closers[i]())
	}
	return errors.Join(errs...)
}
