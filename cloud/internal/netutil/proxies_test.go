package netutil

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

func TestContains(t *testing.T) {
	var none TrustedProxies
	if !none.Empty() || none.Contains(addr("10.0.0.1")) {
		t.Fatal("zero value must trust nobody")
	}
	tp := NewTrustedProxies(netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("::ffff:192.168.1.0/120"), netip.MustParsePrefix("fd00::/8"))
	for in, want := range map[string]bool{
		"10.1.2.3": true, "11.0.0.1": false, "192.168.1.9": true, "::ffff:10.0.0.1": true, "fd12::1": true, "fe80::1": false,
	} {
		if got := tp.Contains(addr(in)); got != want {
			t.Errorf("Contains(%s) = %v", in, got)
		}
	}
	if tp.Contains(netip.Addr{}) {
		t.Error("invalid address trusted")
	}
	if tp.IsCloudflare(addr("104.16.0.1")) {
		t.Error("cloudflare without preset")
	}
	tp.SetCloudflare(CloudflarePrefixes())
	if !tp.IsCloudflare(addr("104.16.0.1")) || !tp.Contains(addr("2606:4700::1")) || tp.IsCloudflare(addr("10.1.2.3")) {
		t.Error("cloudflare preset")
	}
	if len(tp.Prefixes()) != 3+22 {
		t.Errorf("prefixes %d", len(tp.Prefixes()))
	}
	if !tp.PeerAllowed(addr("1.1.1.1"), false) || tp.PeerAllowed(addr("1.1.1.1"), true) || !tp.PeerAllowed(addr("10.0.0.2"), true) {
		t.Error("PeerAllowed")
	}
}

func TestClientIP(t *testing.T) {
	tp := NewTrustedProxies(netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("127.0.0.1/32"))
	tp.SetCloudflare(CloudflarePrefixes())
	cases := []struct {
		name string
		peer string
		h    http.Header
		want string
	}{
		{"untrusted peer ignores headers", "203.0.113.9", hdr("X-Forwarded-For", "1.2.3.4", "CF-Connecting-IP", "5.6.7.8"), "203.0.113.9"},
		{"trusted peer, single hop", "10.0.0.1", hdr("X-Forwarded-For", "198.51.100.7"), "198.51.100.7"},
		{"right-most untrusted wins over spoofed left", "10.0.0.1", hdr("X-Forwarded-For", "6.6.6.6, 198.51.100.7, 10.2.2.2"), "198.51.100.7"},
		{"multiple headers concatenate", "10.0.0.1", hdr("X-Forwarded-For", "6.6.6.6", "X-Forwarded-For", "198.51.100.7"), "198.51.100.7"},
		{"all hops trusted gives left-most", "10.0.0.1", hdr("X-Forwarded-For", "10.9.9.9, 10.8.8.8"), "10.9.9.9"},
		{"no header gives the peer", "10.0.0.1", hdr(), "10.0.0.1"},
		{"malformed hop stops at the last checked one", "10.0.0.1", hdr("X-Forwarded-For", "1.2.3.4, garbage, 10.3.3.3"), "10.3.3.3"},
		{"hop with port", "10.0.0.1", hdr("X-Forwarded-For", "198.51.100.7:4711"), "198.51.100.7"},
		{"ipv6 hop", "10.0.0.1", hdr("X-Forwarded-For", "[2001:db8::5]"), "2001:db8::5"},
		{"CF-Connecting-IP from cloudflare", "104.16.1.1", hdr("CF-Connecting-IP", "198.51.100.8", "X-Forwarded-For", "6.6.6.6"), "198.51.100.8"},
		{"CF-Connecting-IP ignored from other proxies", "10.0.0.1", hdr("CF-Connecting-IP", "6.6.6.6", "X-Forwarded-For", "198.51.100.7"), "198.51.100.7"},
		{"bad CF-Connecting-IP falls back to XFF", "2606:4700::9", hdr("CF-Connecting-IP", "nope", "X-Forwarded-For", "198.51.100.7"), "198.51.100.7"},
		{"local proxy behind cloudflare", "127.0.0.1", hdr("X-Forwarded-For", "198.51.100.7, 104.16.1.1"), "198.51.100.7"},
		{"mapped peer", "::ffff:203.0.113.9", hdr("X-Forwarded-For", "1.2.3.4"), "203.0.113.9"},
	}
	for _, c := range cases {
		if got := tp.ClientIP(addr(c.peer), c.h); got != addr(c.want) {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "198.51.100.7")
	if got, ok := tp.RequestClientIP(r); !ok || got != addr("198.51.100.7") {
		t.Errorf("RequestClientIP %v %v", got, ok)
	}
	for in, want := range map[string]string{"1.2.3.4:80": "1.2.3.4", "[::1]:80": "::1", "[::ffff:1.2.3.4]:9": "1.2.3.4", "5.6.7.8": "5.6.7.8"} {
		if a, ok := PeerAddr(in); !ok || a != addr(want) {
			t.Errorf("PeerAddr(%q) = %v", in, a)
		}
	}
	if _, ok := PeerAddr("nope"); ok {
		t.Error("bad remote addr accepted")
	}
}

func TestFetchCloudflare(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v4":
			w.Write([]byte("1.0.0.0/24\n2.0.0.0/16\n"))
		case "/v6":
			w.Write([]byte("2001:db8::/32\n"))
		case "/bad":
			w.Write([]byte("<html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ps, err := FetchCloudflare(ctx, srv.Client(), srv.URL+"/v4", srv.URL+"/v6")
	if err != nil || len(ps) != 3 {
		t.Fatalf("fetch %v %v", ps, err)
	}
	for _, bad := range [][2]string{{"/v4", "/bad"}, {"/v4", "/missing"}, {"/empty", "/v6"}} {
		if _, err := FetchCloudflare(ctx, srv.Client(), srv.URL+bad[0], srv.URL+bad[1]); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	if len(CloudflarePrefixes()) != 22 {
		t.Fatal("embedded list")
	}
	if _, err := parsePrefixList(stringsReader("0.0.0.0/0\n")); err == nil {
		t.Fatal("0.0.0.0/0 accepted")
	}
}

func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }
