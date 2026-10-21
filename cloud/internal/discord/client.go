package discord

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"golang.org/x/time/rate"
)

// Client limits (ARCHITECTURE 5.6, 5.12).
const (
	requestTimeout     = 5 * time.Second
	DefaultPerMinute   = 10              // posts per minute per tenant
	maxRetryWait       = 5 * time.Second // a 429 asking for a longer wait is reported, not waited for
	maxAttempts        = 3
	maxResponseBody    = 64 << 10
	limiterIdleTimeout = time.Hour
)

// Errors.
var (
	// ErrRateLimited: the tenant's post budget is used up or Discord asked to slow down.
	ErrRateLimited = errors.New("discord: rate limited")
	// ErrRedirect: Discord answered with a redirect, which is never followed.
	ErrRedirect = errors.New("discord: redirect refused")
)

// StatusError is a non-2xx answer from Discord.
type StatusError struct {
	WebhookID string
	Code      int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("discord: webhook %s: HTTP %d", e.WebhookID, e.Code)
}

// RateLimitError is a 429 from Discord; errors.Is(err, ErrRateLimited) holds.
type RateLimitError struct {
	WebhookID  string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("discord: webhook %s: rate limited, retry after %s", e.WebhookID, e.RetryAfter)
}

func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// Options tunes New.
type Options struct {
	Log       *slog.Logger // default: discard
	PerMinute int          // posts per minute per tenant; default DefaultPerMinute
	Username  string       // shown as the webhook's author; default "PandaEye"
}

// Client posts to Discord webhooks. It is safe for concurrent use; one instance per process
// (Shared) keeps the per-tenant budget for every service.
type Client struct {
	http     *http.Client
	dial     *guardedDialer
	log      *slog.Logger
	perMin   int
	username string
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) error

	mu       sync.Mutex
	limiters map[string]*limiterEntry
	blocked  map[string]time.Time // webhook id -> no posts before (Discord 429)
	failing  map[string]bool      // webhook id -> failure already logged
	pruned   time.Time
}

type limiterEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

// New returns a client.
func New(opts Options) *Client {
	return newClient(opts, nil, nil)
}

// newClient builds a client with an optional resolver and TLS configuration (tests).
func newClient(opts Options, r resolver, tlsCfg *tls.Config) *Client {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.PerMinute <= 0 {
		opts.PerMinute = DefaultPerMinute
	}
	if opts.Username == "" {
		opts.Username = "PandaEye"
	}
	g := newGuardedDialer(r)
	tr := &http.Transport{
		Proxy:                 nil, // never through a proxy: the address checks must apply to Discord itself
		DialContext:           g.DialContext,
		TLSClientConfig:       tlsCfg,
		TLSHandshakeTimeout:   requestTimeout,
		ResponseHeaderTimeout: requestTimeout,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	c := &Client{
		http: &http.Client{
			Transport:     tr,
			Timeout:       requestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		dial: g, log: opts.Log, perMin: opts.PerMinute, username: opts.Username, now: time.Now, sleep: sleepCtx,
		limiters: map[string]*limiterEntry{}, blocked: map[string]time.Time{}, failing: map[string]bool{},
	}
	return c
}

// Shared returns the process's client (app.Deps.Shared), so the API's test messages and the verdict
// service's alerts share one per-tenant budget.
func Shared(d *app.Deps) (*Client, error) {
	v, err := d.Shared("discord", func() (any, error) {
		return New(Options{Log: d.Log.With("component", "discord")}), nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*Client), nil
}

type tenantKey struct{}

// WithTenant attaches the tenant id the post is made for; the per-tenant budget is keyed by it
// (by the webhook id when absent).
func WithTenant(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, tenantKey{}, tenantID)
}

// PostVerdict posts a verdict embed to webhookURL.
func (c *Client) PostVerdict(ctx context.Context, webhookURL string, e VerdictEmbed) error {
	w, err := ParseWebhookURL(webhookURL)
	if err != nil {
		return err
	}
	return c.post(ctx, w, c.verdictMessage(e))
}

// SendTest posts the test message sent when a tenant saves its webhook.
func (c *Client) SendTest(ctx context.Context, webhookURL string) error {
	w, err := ParseWebhookURL(webhookURL)
	if err != nil {
		return err
	}
	return c.post(ctx, w, message{
		Username:        c.username,
		Content:         "PandaEye is connected to this channel. Verdict alerts will be posted here.",
		AllowedMentions: noMentions(),
	})
}

func (c *Client) post(ctx context.Context, w Webhook, m message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	key, _ := ctx.Value(tenantKey{}).(string)
	if key == "" {
		key = "webhook:" + w.ID
	}
	now := c.now()
	if until, ok := c.blockedUntil(w.ID); ok && now.Before(until) {
		return &RateLimitError{WebhookID: w.ID, RetryAfter: until.Sub(now)}
	}
	if !c.limiter(key, now).AllowN(now, 1) {
		return fmt.Errorf("discord: webhook %s: %w (more than %d posts per minute)", w.ID, ErrRateLimited, c.perMin)
	}
	for attempt := 1; ; attempt++ {
		status, retryAfter, err := c.do(ctx, w, body)
		switch {
		case err != nil:
			return c.failed(w, err)
		case status >= 200 && status < 300:
			c.succeeded(w)
			return nil
		case status == http.StatusTooManyRequests:
			if retryAfter <= maxRetryWait && attempt < maxAttempts {
				if err := c.sleep(ctx, retryAfter); err != nil {
					return c.failed(w, err)
				}
				continue
			}
			c.block(w.ID, c.now().Add(retryAfter))
			return c.failed(w, &RateLimitError{WebhookID: w.ID, RetryAfter: retryAfter})
		case status >= 300 && status < 400:
			return c.failed(w, fmt.Errorf("discord: webhook %s: %w", w.ID, ErrRedirect))
		default:
			return c.failed(w, &StatusError{WebhookID: w.ID, Code: status})
		}
	}
}

// do sends one request and returns the status and, for 429, how long Discord asked to wait.
func (c *Client) do(ctx context.Context, w Webhook, body []byte) (int, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL(), bytes.NewReader(body))
	if err != nil {
		return 0, 0, fmt.Errorf("discord: webhook %s: bad request", w.ID)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "PandaEye (https://github.com/jackh54/PandaEye, 1)")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("discord: webhook %s: %w", w.ID, stripURL(err))
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if resp.StatusCode != http.StatusTooManyRequests {
		return resp.StatusCode, 0, nil
	}
	return resp.StatusCode, retryAfter(resp.Header, data), nil
}

// retryAfter reads Discord's wait from Retry-After / X-RateLimit-Reset-After (seconds, possibly
// fractional) and the JSON body's retry_after; the longest wins, default 1 s.
func retryAfter(h http.Header, body []byte) time.Duration {
	var d time.Duration
	consider := func(sec float64) {
		if sec > 0 && sec < 24*3600 {
			d = max(d, time.Duration(sec*float64(time.Second)))
		}
	}
	for _, k := range []string{"Retry-After", "X-RateLimit-Reset-After"} {
		if v := h.Get(k); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				consider(f)
			}
		}
	}
	var b struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if json.Unmarshal(body, &b) == nil {
		consider(b.RetryAfter)
	}
	if d == 0 {
		d = time.Second
	}
	return d
}

// stripURL removes the request URL (which carries the token) from transport errors.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return errors.New("timeout")
		}
		return ue.Err
	}
	return err
}

// failed logs the first failure of a webhook (by id) until it succeeds again and returns err.
func (c *Client) failed(w Webhook, err error) error {
	c.mu.Lock()
	first := !c.failing[w.ID]
	c.failing[w.ID] = true
	c.mu.Unlock()
	if first {
		c.log.Warn("discord webhook post failed", "webhook_id", w.ID, "err", err)
	}
	return err
}

func (c *Client) succeeded(w Webhook) {
	c.mu.Lock()
	recovered := c.failing[w.ID]
	delete(c.failing, w.ID)
	delete(c.blocked, w.ID)
	c.mu.Unlock()
	if recovered {
		c.log.Info("discord webhook post succeeded again", "webhook_id", w.ID)
	}
}

func (c *Client) block(id string, until time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blocked[id] = until
}

func (c *Client) blockedUntil(id string) (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.blocked[id]
	return t, ok
}

func (c *Client) limiter(key string, now time.Time) *rate.Limiter {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now.Sub(c.pruned) > limiterIdleTimeout {
		for k, e := range c.limiters {
			if now.Sub(e.seen) > limiterIdleTimeout {
				delete(c.limiters, k)
			}
		}
		for k, t := range c.blocked {
			if now.After(t) {
				delete(c.blocked, k)
			}
		}
		c.pruned = now
	}
	e, ok := c.limiters[key]
	if !ok {
		e = &limiterEntry{lim: rate.NewLimiter(rate.Every(time.Minute/time.Duration(c.perMin)), c.perMin)}
		c.limiters[key] = e
	}
	e.seen = now
	return e.lim
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
