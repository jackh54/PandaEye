package gateway

import (
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/testkit"
)

// Unauthenticated upgrades are bounded per client IP before anything is allocated for them.
func TestUpgradesAreLimitedPerIPBeforeTheHello(t *testing.T) {
	o := fastOptions()
	o.MaxConnsPerIP = 3
	e := newEnv(t, o)
	for i := 0; i < 3; i++ {
		e.dial(nil) // silent, never sends a Hello
	}
	_, err := testkit.Dial(ctxT(t, 5*time.Second), e.url, nil)
	if err == nil {
		t.Fatal("a fourth open connection from one address must be refused")
	}
	resp, err := http.Get("http" + e.url[len("ws"):])
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("status %d, Retry-After %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
}

func TestAdmissionLimits(t *testing.T) {
	a := newAdmission(Options{UpgradesPerIPPerMin: 2, MaxConnsPerIP: 10, VerifyPerPrefix: 2}.withDefaults())
	ip := netip.MustParseAddr("203.0.113.7")
	now := time.Now()
	for i := 0; i < 2; i++ {
		if _, ok := a.enter(ip, now); !ok {
			t.Fatalf("upgrade %d refused", i)
		}
	}
	if _, ok := a.enter(ip, now); ok {
		t.Fatal("upgrade rate not enforced")
	}
	a.leave(ip)
	a.leave(ip)
	if len(a.open) != 0 {
		t.Fatalf("open = %v", a.open)
	}
	d1, ok1 := a.startVerify(ip)
	_, ok2 := a.startVerify(netip.MustParseAddr("203.0.113.7"))
	if _, ok3 := a.startVerify(ip); !ok1 || !ok2 || ok3 {
		t.Fatalf("in-flight licence checks per prefix: %v %v %v", ok1, ok2, ok3)
	}
	d1()
	if _, ok := a.startVerify(ip); !ok {
		t.Fatal("a finished check must free its slot")
	}
}
