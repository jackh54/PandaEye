package discord

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
)

const (
	token  = "Tok3n_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"
	hookID = "123456789012345678"
	good   = "https://discord.com/api/webhooks/" + hookID + "/" + token
)

func TestParseWebhookURL(t *testing.T) {
	tok60 := strings.Repeat("a", 60)
	tok80 := strings.Repeat("Z", 80)
	for _, ok := range []string{
		good,
		"https://discordapp.com/api/webhooks/" + hookID + "/" + token,
		"https://ptb.discord.com/api/webhooks/" + hookID + "/" + token,
		"https://canary.discord.com/api/webhooks/" + hookID + "/" + token,
		"https://discord.com/api/webhooks/12345678901234567/" + tok60,    // 17 digits, 60 chars
		"https://discord.com/api/webhooks/12345678901234567890/" + tok80, // 20 digits, 80 chars
		good + "?wait=true", good + "?thread_id=123456789012345678", good + "?wait=false&thread_id=123456789012345678",
	} {
		w, err := ParseWebhookURL(ok)
		if err != nil {
			t.Errorf("rejected %s", ok)
			continue
		}
		if w.URL() != ok && !strings.Contains(ok, "&") {
			t.Errorf("canonical URL %s != %s", w.URL(), ok)
		}
	}
	for _, bad := range []string{
		"",
		"http://discord.com/api/webhooks/" + hookID + "/" + token,
		"HTTPS://discord.com/api/webhooks/" + hookID + "/" + token,
		"https://example.com/api/webhooks/" + hookID + "/" + token,
		"https://discord.com.evil.tld/api/webhooks/" + hookID + "/" + token,
		"https://evil.tld/discord.com/api/webhooks/" + hookID + "/" + token,
		"https://evildiscord.com/api/webhooks/" + hookID + "/" + token,
		"https://www.discord.com/api/webhooks/" + hookID + "/" + token,
		"https://Discord.com/api/webhooks/" + hookID + "/" + token,
		"https://discord.com./api/webhooks/" + hookID + "/" + token,
		"https://user:pass@discord.com/api/webhooks/" + hookID + "/" + token,
		"https://discord.com@evil.tld/api/webhooks/" + hookID + "/" + token,
		"https://discord.com:443/api/webhooks/" + hookID + "/" + token,
		"https://discord.com:8443/api/webhooks/" + hookID + "/" + token,
		"https://discord.com:/api/webhooks/" + hookID + "/" + token,
		"https://162.159.135.232/api/webhooks/" + hookID + "/" + token,
		"https://[::1]/api/webhooks/" + hookID + "/" + token,
		"https://discord.com/api/webhooks/1234567890123456/" + token,      // 16 digits
		"https://discord.com/api/webhooks/123456789012345678901/" + token, // 21 digits
		"https://discord.com/api/webhooks/" + hookID + "/" + tok60[:59],   // 59 chars
		"https://discord.com/api/webhooks/" + hookID + "/" + tok80 + "Z",  // 81 chars
		"https://discord.com/api/webhooks/" + hookID + "/" + token + "/",  // trailing slash
		"https://discord.com/api/webhooks/" + hookID + "/" + token + "/github",
		"https://discord.com/api/v10/webhooks/" + hookID + "/" + token,
		"https://discord.com/api/webhooks/" + hookID + "/tok!n" + tok60,
		"https://discord.com/api/webhooks/" + hookID + "/%41" + tok60, // escapes
		"https://discord.com/api/webhooks/" + hookID + "/" + token + "#frag",
		"https://discord.com/api/webhooks/" + hookID + "/" + token + "?",
		"https://discord.com/api/webhooks/" + hookID + "/" + token + "?wait=yes",
		"https://discord.com/api/webhooks/" + hookID + "/" + token + "?wait=true&wait=false",
		"https://discord.com/api/webhooks/" + hookID + "/" + token + "?thread_id=abc",
		"https://discord.com/api/webhooks/" + hookID + "/" + token + "?redirect=https://evil.tld",
		"https://discord.com/api/webhooks/" + hookID + "/" + token + "?wait=true;x=1",
		" https://discord.com/api/webhooks/" + hookID + "/" + token,
		"https://discord.com/api/webhooks/" + hookID + "/" + token + "\n",
		"https://discord.com\\@evil.tld/api/webhooks/" + hookID + "/" + token,
		"https://discord.com/api/webhooks/" + hookID + "/" + token + strings.Repeat("?wait=true", 60),
		"javascript://discord.com/api/webhooks/" + hookID + "/" + token,
	} {
		if _, err := ParseWebhookURL(bad); !errors.Is(err, ErrInvalidWebhook) {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestMaskSealAndIDs(t *testing.T) {
	if m := Mask(good); m != "https://discord.com/api/webhooks/"+hookID+"/****" || strings.Contains(m, token) {
		t.Fatalf("mask %q", m)
	}
	if Mask("") != "" || Mask("junk") != "****" || WebhookID(good) != hookID || WebhookID("junk") != "" {
		t.Fatal("mask/id helpers")
	}
	w, _ := ParseWebhookURL(good)
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "webhook", w)
	if strings.Contains(buf.String(), token) || strings.Contains(w.String(), token) {
		t.Fatal("webhook token printed")
	}
	k1, k2 := []byte("outbound-key-1-0123456789abcdef0123456"), []byte("outbound-key-2-0123456789abcdef0123456")
	sealed, err := Seal(k1, "t_a", good)
	if err != nil || bytes.Contains(sealed, []byte(token)) {
		t.Fatal(err)
	}
	if u, err := Open([][]byte{k2, k1}, "t_a", sealed); err != nil || u != good {
		t.Fatalf("open with the rotation key list: %q %v", u, err)
	}
	if _, err := Open([][]byte{k1}, "t_b", sealed); !errors.Is(err, identity.ErrDecrypt) {
		t.Fatal("sealed webhook not bound to its tenant")
	}
	if _, err := Open([][]byte{k2}, "t_a", sealed); err == nil {
		t.Fatal("opened with the wrong key")
	}
}

func TestPublicAddr(t *testing.T) {
	for _, s := range []string{"162.159.135.232", "1.1.1.1", "2606:4700::6810:84e5", "8.8.8.8"} {
		if !PublicAddr(netip.MustParseAddr(s)) {
			t.Errorf("refused public %s", s)
		}
	}
	for _, s := range []string{
		"127.0.0.1", "127.1.2.3", "::1", "10.0.0.1", "172.16.5.4", "192.168.1.1", "169.254.169.254", "169.254.1.1",
		"100.64.0.1", "100.127.255.254", "100.100.100.200", "0.0.0.0", "0.1.2.3", "::", "fe80::1", "fc00::1", "fd00:ec2::254",
		"224.0.0.1", "ff02::1", "255.255.255.255", "198.18.0.1", "192.0.0.8", "192.0.2.1", "240.0.0.1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "64:ff9b::a00:1", "64:ff9b::7f00:1", "2002:a00:1::1", "2001:db8::1", "2001::1",
	} {
		if PublicAddr(netip.MustParseAddr(s)) {
			t.Errorf("allowed %s", s)
		}
	}
	if !PublicAddr(netip.MustParseAddr("64:ff9b::808:808")) || !PublicAddr(netip.MustParseAddr("2002:808:808::1")) {
		t.Error("NAT64/6to4 of a public address refused")
	}
}

// fakeResolver answers every host with addrs.
type fakeResolver struct {
	mu    sync.Mutex
	addrs []netip.Addr
	err   error
	hosts []string
}

func (f *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hosts = append(f.hosts, host)
	return f.addrs, f.err
}

// fakeDiscord is a TLS server standing in for discord.com; the client dials it after the address
// checks passed on a public address from the fake resolver.
type fakeDiscord struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies [][]byte
	paths  []string
	handle func(w http.ResponseWriter, r *http.Request, n int)
	dialed []netip.AddrPort
}

func newFakeDiscord(t *testing.T, logs io.Writer, addrs ...string) (*Client, *fakeDiscord, *fakeResolver) {
	t.Helper()
	fd := &fakeDiscord{}
	fd.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		fd.mu.Lock()
		fd.bodies = append(fd.bodies, b)
		fd.paths = append(fd.paths, r.URL.RequestURI())
		n := len(fd.bodies)
		h := fd.handle
		fd.mu.Unlock()
		if h != nil {
			h(w, r, n)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(fd.srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(fd.srv.Certificate())
	res := &fakeResolver{}
	for _, a := range addrs {
		res.addrs = append(res.addrs, netip.MustParseAddr(a))
	}
	if logs == nil {
		logs = io.Discard
	}
	c := newClient(Options{Log: app.NewLogger(logs, slog.LevelDebug)}, res, &tls.Config{RootCAs: pool, ServerName: "example.com"})
	target := fd.srv.Listener.Addr().String()
	c.dial.dialAddr = func(ctx context.Context, network string, addr netip.AddrPort) (net.Conn, error) {
		fd.mu.Lock()
		fd.dialed = append(fd.dialed, addr)
		fd.mu.Unlock()
		var d net.Dialer
		return d.DialContext(ctx, network, target)
	}
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c, fd, res
}

func TestPostVerdictEmbed(t *testing.T) {
	c, fd, res := newFakeDiscord(t, nil, "10.0.0.7", "162.159.135.232")
	e := VerdictEmbed{Player: "Steve_*01*", PlayerUUID: "aaaaaaaa-0000-4000-8000-000000000001", Server: "Hub @everyone [x](http://evil)",
		Action: "BAN", Shadow: true, Score: 133.27, Confidence: 0.934,
		Checks:      []string{"killaura.hitbox", "reach.distance", "aim.snap", "aim.gcd", "velocity.vertical", "scaffold.ray"},
		EvidenceURL: "https://dash.example.test/verdicts/v1"}
	if err := c.PostVerdict(context.Background(), good, e); err != nil {
		t.Fatal(err)
	}
	if len(res.hosts) != 1 || res.hosts[0] != "discord.com" {
		t.Fatalf("resolved %v", res.hosts)
	}
	if len(fd.dialed) != 1 || fd.dialed[0].Addr().String() != "162.159.135.232" {
		t.Fatalf("dialed %v (the private address must be skipped)", fd.dialed)
	}
	if fd.paths[0] != "/api/webhooks/"+hookID+"/"+token {
		t.Fatalf("path %s", fd.paths[0])
	}
	var m struct {
		AllowedMentions struct {
			Parse []string `json:"parse"`
		} `json:"allowed_mentions"`
		Embeds []struct {
			Title  string                         `json:"title"`
			URL    string                         `json:"url"`
			Color  int                            `json:"color"`
			Fields []struct{ Name, Value string } `json:"fields"`
		} `json:"embeds"`
	}
	raw := fd.bodies[0]
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"allowed_mentions":{"parse":[]}`)) || m.AllowedMentions.Parse == nil {
		t.Fatalf("allowed_mentions must be {parse: []}: %s", raw)
	}
	em := m.Embeds[0]
	if !strings.Contains(em.Title, "SHADOW") || em.URL != e.EvidenceURL || em.Color != shadowColor {
		t.Fatalf("embed header: %+v", em)
	}
	fields := map[string]string{}
	for _, f := range em.Fields {
		fields[f.Name] = f.Value
	}
	if fields["Player"] != "Steve\\_\\*01\\*\n`aaaaaaaa-0000-4000-8000-000000000001`" {
		t.Errorf("player field %q", fields["Player"])
	}
	if fields["Server"] != `Hub \@everyone \[x\]\(http\:\/\/evil\)` && !strings.Contains(fields["Server"], `\@everyone`) {
		t.Errorf("server field not escaped: %q", fields["Server"])
	}
	if fields["Action"] != "BAN (SHADOW)" || fields["Score"] != "133.3" || fields["Confidence"] != "93%" {
		t.Errorf("fields %v", fields)
	}
	if got := strings.Count(fields["Top checks"], ","); got != 4 || strings.Contains(fields["Top checks"], "scaffold") {
		t.Errorf("top 5 checks: %q", fields["Top checks"])
	}
	if fields["Evidence"] != "[Open in the dashboard](https://dash.example.test/verdicts/v1)" {
		t.Errorf("evidence link %q", fields["Evidence"])
	}
	// No SHADOW label for enforced verdicts.
	check(t, c.PostVerdict(context.Background(), good, VerdictEmbed{Player: "a", Action: "kick"}))
	if bytes.Contains(fd.bodies[1], []byte("SHADOW")) {
		t.Fatal("SHADOW label on an enforced verdict")
	}
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestRefusesPrivateAddresses(t *testing.T) {
	for _, addrs := range [][]string{{"127.0.0.1"}, {"10.1.2.3", "192.168.0.1"}, {"169.254.169.254"}, {"100.64.0.9"}, {"::1"}, {"fd00:ec2::254"}, {"64:ff9b::a00:1"}, {}} {
		var logs bytes.Buffer
		c, fd, _ := newFakeDiscord(t, &logs, addrs...)
		err := c.SendTest(context.Background(), good)
		if !errors.Is(err, ErrRefusedAddress) {
			t.Errorf("%v: %v", addrs, err)
		}
		if len(fd.dialed) != 0 {
			t.Errorf("%v: dialed %v", addrs, fd.dialed)
		}
		if strings.Contains(err.Error(), token) || strings.Contains(logs.String(), token) {
			t.Errorf("token in error or log: %v / %s", err, logs.String())
		}
	}
	// The dialer's Control refuses non-public sockets as well.
	if controlPublicOnly("tcp", "127.0.0.1:443", nil) == nil || controlPublicOnly("tcp", "162.159.135.232:443", nil) != nil {
		t.Fatal("dialer control")
	}
	// Only the Discord hosts on 443 are ever dialled.
	g := newGuardedDialer(&fakeResolver{addrs: []netip.Addr{netip.MustParseAddr("162.159.135.232")}})
	for _, a := range []string{"evil.tld:443", "discord.com:80", "localhost:443"} {
		if _, err := g.DialContext(context.Background(), "tcp", a); !errors.Is(err, ErrRefusedAddress) {
			t.Errorf("dialled %s: %v", a, err)
		}
	}
}

func TestRedirectsRefused(t *testing.T) {
	c, fd, _ := newFakeDiscord(t, nil, "162.159.135.232")
	fd.handle = func(w http.ResponseWriter, r *http.Request, n int) {
		http.Redirect(w, r, "https://169.254.169.254/latest/meta-data", http.StatusFound)
	}
	err := c.SendTest(context.Background(), good)
	if !errors.Is(err, ErrRedirect) {
		t.Fatalf("redirect: %v", err)
	}
	if len(fd.bodies) != 1 {
		t.Fatalf("redirect followed: %d requests", len(fd.bodies))
	}
	if strings.Contains(err.Error(), token) {
		t.Fatal("token in error")
	}
}

func TestRateLimits(t *testing.T) {
	c, fd, _ := newFakeDiscord(t, nil, "162.159.135.232")
	var slept []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	// A short 429 is waited for and retried.
	fd.handle = func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"retry_after": 0.25, "global": false}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
	check(t, c.SendTest(context.Background(), good))
	if len(fd.bodies) != 2 || len(slept) != 1 || slept[0] != time.Second {
		t.Fatalf("429 retry: %d requests, slept %v", len(fd.bodies), slept)
	}
	// A long 429 is reported and blocks the webhook until it passes.
	fd.handle = func(w http.ResponseWriter, r *http.Request, n int) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"retry_after": 30}`)
	}
	err := c.SendTest(context.Background(), good)
	var rl *RateLimitError
	if !errors.As(err, &rl) || !errors.Is(err, ErrRateLimited) || rl.RetryAfter != 30*time.Second {
		t.Fatalf("long 429: %v", err)
	}
	before := len(fd.bodies)
	if err := c.SendTest(context.Background(), good); !errors.Is(err, ErrRateLimited) || len(fd.bodies) != before {
		t.Fatalf("blocked webhook was posted to: %v", err)
	}

	// The per-tenant budget: 10 posts per minute, then refused without a request.
	c2, fd2, _ := newFakeDiscord(t, nil, "162.159.135.232")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c2.now = func() time.Time { return now }
	ctx := WithTenant(context.Background(), "t_aaaaaaaaaaaaaaaaaaaa")
	for i := 0; i < DefaultPerMinute; i++ {
		check(t, c2.PostVerdict(ctx, good, VerdictEmbed{Player: "p", Action: "ALERT"}))
	}
	if err := c2.PostVerdict(ctx, good, VerdictEmbed{Player: "p"}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("11th post in a minute: %v", err)
	}
	if len(fd2.bodies) != DefaultPerMinute {
		t.Fatalf("requests: %d", len(fd2.bodies))
	}
	// Another tenant has its own budget; a minute later the first one has room again.
	check(t, c2.PostVerdict(WithTenant(context.Background(), "t_bbbbbbbbbbbbbbbbbbbb"), good, VerdictEmbed{Player: "p"}))
	now = now.Add(time.Minute)
	check(t, c2.PostVerdict(ctx, good, VerdictEmbed{Player: "p"}))
}

func TestErrorsAndLogsNeverContainTheURL(t *testing.T) {
	var logs bytes.Buffer
	c, fd, res := newFakeDiscord(t, &logs, "162.159.135.232")
	fd.handle = func(w http.ResponseWriter, r *http.Request, n int) { w.WriteHeader(http.StatusNotFound) }
	var errs []error
	errs = append(errs, c.SendTest(context.Background(), good))
	var se *StatusError
	if !errors.As(errs[0], &se) || se.Code != 404 || se.WebhookID != hookID {
		t.Fatalf("status error: %v", errs[0])
	}
	errs = append(errs, c.SendTest(context.Background(), good)) // second failure: not logged again
	res.err = errors.New("lookup discord.com: no such host")
	errs = append(errs, c.SendTest(context.Background(), good))
	res.err = nil
	fd.srv.CloseClientConnections()
	fd.srv.Close() // connection refused
	errs = append(errs, c.SendTest(context.Background(), good))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	errs = append(errs, c.SendTest(ctx, good))
	errs = append(errs, c.SendTest(context.Background(), "https://discord.com/api/webhooks/1/"+token))
	for _, err := range errs {
		if err == nil {
			t.Fatal("expected an error")
		}
		if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "/api/webhooks") {
			t.Errorf("error contains the URL: %v", err)
		}
	}
	out := logs.String()
	if strings.Contains(out, token) || strings.Contains(out, "/api/webhooks") {
		t.Fatalf("log contains the URL: %s", out)
	}
	if n := strings.Count(out, "discord webhook post failed"); n != 1 {
		t.Fatalf("failure logged %d times (want once until success)", n)
	}
	if !strings.Contains(out, `"webhook_id":"`+hookID+`"`) {
		t.Fatalf("failure not logged by webhook id: %s", out)
	}
}

func TestEscape(t *testing.T) {
	for in, want := range map[string]string{
		"plain":            "plain",
		"*bold* _it_ ~s~":  `\*bold\* \_it\_ \~s\~`,
		"`code` ||spoil||": "\\`code\\` \\|\\|spoil\\|\\|",
		"@everyone <@123>": `\@everyone \<\@123\>`,
		"line1\nline2\r\t": "line1 line2",
		"> quote # head":   `\> quote \# head`,
		"[a](http://x)":    `\[a\]\(http\:\/\/x\)`,
		"émoji :smile:":    `émoji \:smile\:`,
	} {
		got := Escape(in)
		if strings.Contains(want, `\/`) {
			want = strings.ReplaceAll(want, `\/`, "/")
		}
		if got != want {
			t.Errorf("Escape(%q) = %q, want %q", in, got, want)
		}
	}
	long := truncate(strings.Repeat("\\*", 600), maxFieldValue)
	if n := len([]rune(long)); n > maxFieldValue {
		t.Fatalf("truncated to %d runes", n)
	}
	if strings.HasSuffix(strings.TrimSuffix(long, "…"), "\\") && !strings.HasSuffix(strings.TrimSuffix(long, "…"), "\\\\") {
		t.Fatal("dangling escape")
	}
}
