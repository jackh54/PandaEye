package license

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/config"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
)

func TestValidFormat(t *testing.T) {
	for key, want := range map[string]bool{
		"dev-license-key": true, "A1b2.C3_d4~+/=": true, "": false, "has space": false, "tab\tkey": false,
		"ünïcode": false, string(make([]byte, MaxKeyLen+1)): false,
	} {
		if ValidFormat(key) != want {
			t.Errorf("ValidFormat(%q) = %v", key, !want)
		}
	}
	if fp := Fingerprint([]byte("pepper"), "key"); len(fp) != 8 || fp == "key" {
		t.Fatalf("fingerprint %q", fp)
	}
}

func TestStatic(t *testing.T) {
	v, err := FromConfig(config.License{Mode: config.LicenseStatic, Static: []config.StaticLicense{
		{Key: "alpha-key", MaxServers: 3, Plan: "pro"}, {Key: "beta-key"},
	}}, []byte("pepper-pepper-pepper-pepper-pepper"), nil, metrics.New())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for key, want := range map[string]License{
		"alpha-key": {Valid: true, MaxServers: 3, Plan: "pro"},
		"beta-key":  {Valid: true, Plan: "free"},
		"gamma-key": {},
		"bad key":   {},
	} {
		got, err := v.Verify(ctx, key)
		if err != nil || got != want {
			t.Errorf("Verify(%q) = %+v, %v; want %+v", key, got, err, want)
		}
	}
}

// fakeWorker is a pandascript licence worker.
type fakeWorker struct {
	t        *testing.T
	requests atomic.Int64
	mu       sync.Mutex
	status   int    // answer status
	body     string // answer body
	delay    time.Duration
}

func (f *fakeWorker) set(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

func (f *fakeWorker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	if r.URL.Path != "/api/client/verify" || r.URL.Query().Get("product") != "pandaeye" {
		f.t.Errorf("unexpected request %s", r.URL)
	}
	if r.Header.Get("Authorization") == "" {
		f.t.Errorf("no bearer key")
	}
	f.mu.Lock()
	status, body, delay := f.status, f.body, f.delay
	f.mu.Unlock()
	time.Sleep(delay)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprint(w, body)
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newPandascript(t *testing.T, f *fakeWorker, tune ...func(*PandascriptOptions)) (*Pandascript, *clock) {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := &clock{now: time.Unix(1_800_000_000, 0)}
	o := PandascriptOptions{URL: srv.URL + "/", Product: "pandaeye", Pepper: []byte("pepper"), Now: c.Now, RatePerS: 1000}
	for _, fn := range tune {
		fn(&o)
	}
	return NewPandascript(o), c
}

const validBody = `{"valid":true,"productId":"pandaeye","maxServers":4,"currentServers":1}`

func TestPandascriptAnswers(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		status  int
		body    string
		want    License
		wantErr bool
	}{
		{"valid", 200, validBody, License{Valid: true, MaxServers: 4}, false},
		{"numeric product id", 200, `{"valid":true,"productId":7,"maxServers":0}`, License{}, false},
		{"other product", 200, `{"valid":true,"productId":"other","maxServers":4}`, License{}, false},
		{"invalid", 401, `{"valid":false,"message":"expired"}`, License{}, false},
		{"401 without verdict", 401, `{"error":"no"}`, License{}, true},
		{"rate limited", 429, `{"valid":false}`, License{}, true},
		{"server error", 503, `oops`, License{}, true},
		{"200 but not valid", 200, `{"valid":false}`, License{}, true},
		{"forbidden", 403, `{"valid":false}`, License{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeWorker{t: t}
			f.set(tc.status, tc.body)
			p, _ := newPandascript(t, f)
			got, err := p.Verify(ctx, "key-"+tc.name[:3])
			if tc.wantErr {
				if err == nil || !errors.Is(err, ErrUnavailable) {
					t.Fatalf("want ErrUnavailable, got %+v %v", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v %v, want %+v", got, err, tc.want)
			}
		})
	}
}

func TestPandascriptCachesAndStale(t *testing.T) {
	ctx := context.Background()
	f := &fakeWorker{t: t}
	f.set(200, validBody)
	p, c := newPandascript(t, f)
	for i := 0; i < 3; i++ {
		if l, err := p.Verify(ctx, "good-key"); err != nil || !l.Valid {
			t.Fatal(l, err)
		}
	}
	if n := f.requests.Load(); n != 1 {
		t.Fatalf("valid answers must be cached: %d requests", n)
	}
	c.Add(11 * time.Minute)
	f.set(500, "down")
	if l, err := p.Verify(ctx, "good-key"); err != nil || !l.Valid || l.MaxServers != 4 {
		t.Fatalf("stale valid answer within 24 h: %+v %v", l, err)
	}
	c.Add(24 * time.Hour)
	if _, err := p.Verify(ctx, "good-key"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("after 24 h the verifier must decide: %v", err)
	}

	f.set(401, `{"valid":false}`)
	before := f.requests.Load()
	for i := 0; i < 3; i++ {
		if l, err := p.Verify(ctx, "bad-key"); err != nil || l.Valid {
			t.Fatal(l, err)
		}
	}
	if f.requests.Load()-before != 1 {
		t.Fatal("invalid answers must be cached for a minute")
	}
	c.Add(61 * time.Second)
	if _, err := p.Verify(ctx, "bad-key"); err != nil || f.requests.Load()-before != 2 {
		t.Fatal("invalid cache must expire after a minute")
	}
	if l, err := p.Verify(ctx, "not a key"); err != nil || l.Valid || f.requests.Load()-before != 2 {
		t.Fatal("malformed keys must not reach the verifier")
	}
}

func TestPandascriptInvalidEndsStaleValidity(t *testing.T) {
	ctx := context.Background()
	f := &fakeWorker{t: t}
	f.set(200, validBody)
	p, c := newPandascript(t, f)
	if l, _ := p.Verify(ctx, "k1"); !l.Valid {
		t.Fatal("valid")
	}
	c.Add(11 * time.Minute)
	f.set(401, `{"valid":false}`)
	if l, err := p.Verify(ctx, "k1"); err != nil || l.Valid {
		t.Fatal("revoked key")
	}
	c.Add(2 * time.Minute)
	f.set(503, "")
	if _, err := p.Verify(ctx, "k1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a key answered invalid must not fall back to its old validity: %v", err)
	}
}

func TestPandascriptSingleflightAndRateLimit(t *testing.T) {
	ctx := context.Background()
	f := &fakeWorker{t: t, delay: 100 * time.Millisecond}
	f.set(200, validBody)
	p, _ := newPandascript(t, f, func(o *PandascriptOptions) { o.RatePerS = 10 })
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l, err := p.Verify(ctx, "same-key"); err != nil || !l.Valid {
				t.Error(l, err)
			}
		}()
	}
	wg.Wait()
	if n := f.requests.Load(); n != 1 {
		t.Fatalf("singleflight: %d requests", n)
	}
	f.delay = 0
	start := time.Now()
	for i := 0; i < 15; i++ {
		if _, err := p.Verify(ctx, fmt.Sprintf("key-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	// 1 token was used, the bucket holds 10: 15 more requests need >= 5 refills at 10/s.
	if el := time.Since(start); el < 400*time.Millisecond {
		t.Fatalf("global rate limit not applied: %v", el)
	}
}

func TestPandascriptCallerContext(t *testing.T) {
	f := &fakeWorker{t: t, delay: 300 * time.Millisecond}
	f.set(200, validBody)
	p, _ := newPandascript(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := p.Verify(ctx, "slow-key"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("caller timeout: %v", err)
	}
	// The shared request finished in the background and its answer is cached.
	time.Sleep(400 * time.Millisecond)
	if l, err := p.Verify(context.Background(), "slow-key"); err != nil || !l.Valid || f.requests.Load() != 1 {
		t.Fatalf("%+v %v %d", l, err, f.requests.Load())
	}
}

func TestPandascriptNoRedirects(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
		fmt.Fprint(w, validBody)
	}))
	defer other.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.RequestURI(), http.StatusFound)
	}))
	defer redirect.Close()
	p := NewPandascript(PandascriptOptions{URL: redirect.URL, Product: "pandaeye"})
	if _, err := p.Verify(context.Background(), "secret-key"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a redirect is not an answer: %v", err)
	}
	if leaked.Load() {
		t.Fatal("the bearer key followed a redirect")
	}
}

func TestFromConfigModes(t *testing.T) {
	if _, err := FromConfig(config.License{Mode: "nope"}, nil, nil, nil); err == nil {
		t.Fatal("unknown mode")
	}
	if _, err := FromConfig(config.License{Mode: config.LicensePandascript}, nil, nil, nil); err == nil {
		t.Fatal("pandascript without URL")
	}
	v, err := FromConfig(config.License{Mode: config.LicensePandascript, URL: "https://licence.example", Product: "pandaeye"},
		identity.RandomKey(32), nil, metrics.New())
	if _, ok := v.(*Pandascript); err != nil || !ok {
		t.Fatal(v, err)
	}
}
