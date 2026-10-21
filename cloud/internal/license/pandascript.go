package license

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"
)

// PandascriptOptions configures the pandascript verifier. Zero values take the documented defaults.
type PandascriptOptions struct {
	URL     string // PE_LICENSE_URL (https)
	Product string // PE_LICENSE_PRODUCT
	Pepper  []byte // keys the cache (PE_LICENSE_PEPPER); random when empty
	Client  *http.Client
	Log     *slog.Logger

	RatePerS   float64       // global outbound requests per second (20)
	ValidTTL   time.Duration // cache of valid answers (10 min)
	InvalidTTL time.Duration // cache of invalid answers (1 min)
	StaleTTL   time.Duration // a key verified valid this recently stays valid while the verifier fails (24 h)
	Timeout    time.Duration // one verification including the rate limit wait (10 s)
	MaxEntries int           // cache entries before expired ones are pruned (10 000)
	Now        func() time.Time
}

// Pandascript verifies keys with the pandascript licence worker:
// GET {URL}/api/client/verify?product={Product} with "Authorization: Bearer <key>".
type Pandascript struct {
	o   PandascriptOptions
	mac []byte
	lim *rate.Limiter
	sf  singleflight.Group
	m   *verifyMetrics

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	lic       License
	expires   time.Time // the answer may be served from cache until then
	validAt   time.Time // last time the key was verified valid (zero: never)
	validLic  License   // the answer at validAt
	hasAnswer bool
}

// NewPandascript returns a pandascript verifier.
func NewPandascript(o PandascriptOptions) *Pandascript {
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 5 * time.Second}
	}
	client := *o.Client
	// Never follow redirects: the bearer key must only reach the configured host.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	o.Client = &client
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&o.ValidTTL, 10*time.Minute)
	def(&o.InvalidTTL, time.Minute)
	def(&o.StaleTTL, 24*time.Hour)
	def(&o.Timeout, 10*time.Second)
	if o.RatePerS <= 0 {
		o.RatePerS = 20
	}
	if o.MaxEntries <= 0 {
		o.MaxEntries = 10_000
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	o.URL = strings.TrimRight(o.URL, "/")
	return &Pandascript{o: o, mac: cacheKeyMAC(o.Pepper), lim: rate.NewLimiter(rate.Limit(o.RatePerS), max(1, int(o.RatePerS))),
		cache: map[string]cacheEntry{}}
}

// Verify implements Verifier.
func (p *Pandascript) Verify(ctx context.Context, key string) (License, error) {
	if !ValidFormat(key) {
		p.m.observe("format")
		return License{}, nil
	}
	id := string(identity.HMACSHA256(p.mac, []byte(key)))
	if l, ok := p.cached(id); ok {
		if l.Valid {
			p.m.observe("cached_valid")
		} else {
			p.m.observe("cached_invalid")
		}
		return l, nil
	}
	ch := p.sf.DoChan(id, func() (any, error) {
		if l, ok := p.cached(id); ok {
			return l, nil
		}
		// Detached from the first caller: other callers share this request.
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.o.Timeout)
		defer cancel()
		return p.verifyRemote(fctx, id, key)
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return License{}, r.Err
		}
		return r.Val.(License), nil
	case <-ctx.Done():
		return License{}, fmt.Errorf("%w: %w", ErrUnavailable, ctx.Err())
	}
}

func (p *Pandascript) cached(id string) (License, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.cache[id]
	if !ok || !e.hasAnswer || !p.o.Now().Before(e.expires) {
		return License{}, false
	}
	return e.lic, true
}

func (p *Pandascript) verifyRemote(ctx context.Context, id, key string) (License, error) {
	lic, err := p.fetch(ctx, key)
	now := p.o.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.cache[id]
	if err != nil {
		if !e.validAt.IsZero() && now.Sub(e.validAt) < p.o.StaleTTL {
			p.m.observe("stale")
			p.o.Log.Warn("licence verifier unavailable, using the last valid answer", "license_fp", fingerprintOf(id), "err", err)
			return e.validLic, nil
		}
		p.m.observe("error")
		return License{}, err
	}
	e.lic, e.hasAnswer = lic, true
	if lic.Valid {
		e.expires = now.Add(p.o.ValidTTL)
		e.validAt, e.validLic = now, lic
		p.m.observe("valid")
	} else {
		e.expires = now.Add(p.o.InvalidTTL)
		e.validAt = time.Time{} // a definitive "invalid" ends any stale validity
		p.m.observe("invalid")
	}
	if len(p.cache) >= p.o.MaxEntries {
		p.prune(now)
	}
	p.cache[id] = e
	return lic, nil
}

// prune drops entries that can neither be served nor used as a stale fallback.
func (p *Pandascript) prune(now time.Time) {
	for k, e := range p.cache {
		if !now.Before(e.expires) && (e.validAt.IsZero() || now.Sub(e.validAt) >= p.o.StaleTTL) {
			delete(p.cache, k)
		}
	}
}

// verifyResponse is the licence worker's JSON answer.
type verifyResponse struct {
	Valid      *bool           `json:"valid"`
	ProductID  json.RawMessage `json:"productId"`
	MaxServers *int            `json:"maxServers"`
}

func (p *Pandascript) fetch(ctx context.Context, key string) (License, error) {
	if err := p.lim.Wait(ctx); err != nil {
		return License{}, fmt.Errorf("%w: outbound rate limit: %w", ErrUnavailable, err)
	}
	u := p.o.URL + "/api/client/verify?product=" + url.QueryEscape(p.o.Product)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return License{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	resp, err := p.o.Client.Do(req)
	if err != nil {
		return License{}, fmt.Errorf("%w: request failed: %w", ErrUnavailable, scrubURLError(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return License{}, fmt.Errorf("%w: reading the answer: %w", ErrUnavailable, err)
	}
	var r verifyResponse
	jsonErr := json.Unmarshal(body, &r)
	switch {
	case resp.StatusCode == http.StatusOK && jsonErr == nil && r.Valid != nil && *r.Valid:
		if !p.productMatches(r.ProductID) {
			return License{}, nil // valid key of another product
		}
		l := License{Valid: true}
		if r.MaxServers != nil && *r.MaxServers > 0 {
			l.MaxServers = *r.MaxServers
		}
		return l, nil
	case resp.StatusCode == http.StatusUnauthorized && jsonErr == nil && r.Valid != nil && !*r.Valid:
		return License{}, nil
	default:
		// 429, 5xx and anything unexpected are retryable, never "invalid".
		return License{}, fmt.Errorf("%w: unexpected answer (HTTP %d)", ErrUnavailable, resp.StatusCode)
	}
}

func (p *Pandascript) productMatches(raw json.RawMessage) bool {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s == p.o.Product
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String() == p.o.Product
	}
	return false
}

// scrubURLError drops the URL from client errors (it holds no secret, but keeps logs short).
func scrubURLError(err error) error {
	if ue, ok := err.(*url.Error); ok {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// fingerprintOf is Fingerprint for a cache id (the key's HMAC).
func fingerprintOf(id string) string { return hex.EncodeToString([]byte(id[:4])) }
