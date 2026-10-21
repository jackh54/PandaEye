package identity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSecretNeverPrints(t *testing.T) {
	s := Secret("super-secret-value")
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("x", "token", s, "nested", struct{ S Secret }{s})
	js, _ := json.Marshal(map[string]Secret{"k": s})
	for _, out := range []string{s.String(), fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s", s, s, s, s), logs.String(), string(js)} {
		if strings.Contains(out, "super-secret") {
			t.Fatalf("secret leaked: %q", out)
		}
	}
	if s.Reveal() != "super-secret-value" || !s.Equal("super-secret-value") || s.Equal("other") {
		t.Fatal("Reveal/Equal broken")
	}
	if Secret("").String() != "" {
		t.Fatal("empty secret should print empty")
	}
}

func TestParseCanonicalUUID(t *testing.T) {
	good := "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	u, err := ParseCanonicalUUID(good)
	if err != nil || u.String() != good {
		t.Fatalf("parse %q: %v %v", good, u, err)
	}
	for _, bad := range []string{
		"", "*", ">", "0A1B2C3D-4E5F-4A6B-8C7D-9E0F1A2B3C4D", "0a1b2c3d4e5f4a6b8c7d9e0f1a2b3c4d",
		"0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4", "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4dd", "0a1b2c3d_4e5f-4a6b-8c7d-9e0f1a2b3c4d",
		"0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4g", "{0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c}", "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c.*",
	} {
		if _, err := ParseCanonicalUUID(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
		if ValidServerID(bad) {
			t.Errorf("ValidServerID accepted %q", bad)
		}
	}
	if ValidServerID("00000000-0000-0000-0000-000000000000") {
		t.Error("nil uuid accepted as server id")
	}
	if !ValidServerID(good) {
		t.Error("valid server id rejected")
	}
	if p, err := ParseUUID("0A1B2C3D4E5F4A6B8C7D9E0F1A2B3C4D"); err != nil || p != u {
		t.Errorf("ParseUUID human form: %v %v", p, err)
	}
	if _, err := UUIDFromBytes(make([]byte, 15)); err == nil {
		t.Error("15-byte uuid accepted")
	}
	if b, err := UUIDFromBytes(u.Bytes()); err != nil || b != u {
		t.Error("bytes round trip")
	}
}

func TestTenantID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewTenantID()
		if !ValidTenantID(id) {
			t.Fatalf("generated invalid tenant id %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate tenant id %q", id)
		}
		seen[id] = true
	}
	for _, bad := range []string{"", "t_", "t_abc", "T_abcdefghijklmnopqrst", "t_abcdefghijklmnopqrs1", "t_abcdefghijklmnopqrs*", "t_abcdefghijklmnopqrst2", "x_abcdefghijklmnopqrst", "t_ABCDEFGHIJKLMNOPQRST"} {
		if ValidTenantID(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"My Network":              "my-network",
		"  --Café  Crème-- ":      "cafe-creme",
		"SMP_01!!":                "smp-01",
		"":                        "tenant",
		"!!!":                     "tenant",
		"日本":                      "tenant",
		strings.Repeat("ab ", 30): "ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-a",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
		if got := Slug(in); len(got) > maxSlugLen || strings.HasSuffix(got, "-") {
			t.Errorf("Slug(%q) = %q not normalised", in, got)
		}
	}
}

func TestServerSecret(t *testing.T) {
	s := NewServerSecret()
	if s.Len() < 43 {
		t.Fatalf("secret too short: %d", s.Len())
	}
	h := HashSecret(s.Reveal())
	if !CheckSecret(h, s.Reveal()) || CheckSecret(h, s.Reveal()+"x") || CheckSecret(h[:31], s.Reveal()) || CheckSecret(nil, "") {
		t.Fatal("CheckSecret")
	}
	if NewServerSecret() == s {
		t.Fatal("secrets repeat")
	}
}

func TestHMACHelpers(t *testing.T) {
	pepper := []byte("0123456789abcdef0123456789abcdef")
	a := LicenseKeyHMAC(pepper, "KEY-1")
	if !bytes.Equal(a, LicenseKeyHMAC(pepper, "KEY-1")) || bytes.Equal(a, LicenseKeyHMAC(pepper, "KEY-2")) || len(a) != 32 {
		t.Fatal("licence HMAC not deterministic/distinct")
	}
	key := []byte("outbound-key-outbound-key-outbound-key")
	payload := []byte("payload")
	mac := SignOutbound(key, payload)
	if !VerifyOutbound(key, payload, mac) {
		t.Fatal("verify own mac")
	}
	if VerifyOutbound(key, []byte("payloaX"), mac) || VerifyOutbound([]byte("other"), payload, mac) || VerifyOutbound(key, payload, mac[:31]) {
		t.Fatal("forged mac accepted")
	}
	if bytes.Equal(DeriveKey(key, "a"), DeriveKey(key, "b")) || len(DeriveKey(key, "a")) != 32 {
		t.Fatal("DeriveKey")
	}
}

func TestAppealCode(t *testing.T) {
	key := []byte("outbound-key-outbound-key-outbound-key")
	c := AppealCode(key, "v1")
	if len(c) != 10 || c != AppealCode(key, "v1") || c == AppealCode(key, "v2") || c == AppealCode([]byte("k2"), "v1") {
		t.Fatalf("appeal code %q", c)
	}
	if !EqualAppealCode(c, strings.ToLower(c)) || EqualAppealCode(c, c[:9]) {
		t.Fatal("EqualAppealCode")
	}
	if !EqualAppealCode("0123456789", "O1234-56789") {
		t.Fatal("look-alike O not folded")
	}
}

func TestAPIKey(t *testing.T) {
	id, secret, token := NewAPIKey()
	gid, gs, ok := ParseAPIKey(token.Reveal())
	if !ok || gid != id || gs != secret {
		t.Fatalf("round trip: %v %v %v", gid, gs, ok)
	}
	for _, bad := range []string{"", "pe__", "pe_" + id, "xx_" + id + "_" + secret.Reveal(), "pe_" + id + "_" + secret.Reveal() + "_x", "pe_" + id + "_" + strings.ToUpper(secret.Reveal()), "pe_" + id[:15] + "_" + secret.Reveal()} {
		if _, _, ok := ParseAPIKey(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestSealOpen(t *testing.T) {
	key := DeriveKey([]byte("master"), "webhook")
	sealed, err := Seal(key, []byte("https://discord.com/api/webhooks/1/x"), []byte("t_a"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := Open(key, sealed, []byte("t_a"))
	if err != nil || string(pt) != "https://discord.com/api/webhooks/1/x" {
		t.Fatalf("open: %q %v", pt, err)
	}
	if _, err := Open(key, sealed, []byte("t_b")); err != ErrDecrypt {
		t.Fatal("aad not bound")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := Open(key, sealed, []byte("t_a")); err != ErrDecrypt {
		t.Fatal("tampering not detected")
	}
	if _, err := Open(key, sealed[:5], nil); err != ErrDecrypt {
		t.Fatal("truncated accepted")
	}
	if _, err := Seal([]byte("short"), nil, nil); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestSiphashReferenceVector(t *testing.T) {
	var k PartitionKey
	for i := range k {
		k[i] = byte(i)
	}
	msg := make([]byte, 15)
	for i := range msg {
		msg[i] = byte(i)
	}
	if got := k.Hash(msg); got != 0xa129ca6149be45e5 {
		t.Fatalf("siphash-2-4 reference vector: got %#x", got)
	}
	k2, err := ParsePartitionKey(k.Hex())
	if err != nil || k2 != k {
		t.Fatal("hex round trip")
	}
	if _, err := ParsePartitionKey("zz"); err == nil {
		t.Fatal("bad key accepted")
	}
}

func TestPartitioner(t *testing.T) {
	if _, err := NewPartitioner(PartitionKey{}, 0); err == nil {
		t.Fatal("zero partitions accepted")
	}
	p, err := NewPartitioner(NewPartitionKey(), 16)
	if err != nil {
		t.Fatal(err)
	}
	counts := make([]int, 16)
	for i := 0; i < 16000; i++ {
		u := NewUUID()
		n := p.ForUUID(u)
		m, err := p.ForPlayer(u.Bytes())
		if err != nil || n != m {
			t.Fatal("ForPlayer != ForUUID")
		}
		counts[n]++
	}
	for i, c := range counts {
		if c < 800 || c > 1200 {
			t.Errorf("partition %d got %d of 16000 (skewed)", i, c)
		}
	}
	if _, err := p.ForPlayer([]byte{1, 2}); err == nil {
		t.Error("short player accepted")
	}
	sid := "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	u, _ := ParseCanonicalUUID(sid)
	if n, err := p.ForServer(sid); err != nil || n != p.ForUUID(u) {
		t.Error("ForServer must hash the server id's 16 bytes")
	}
	if _, err := p.ForServer("*"); err == nil {
		t.Error("wildcard server accepted")
	}
	if p.ForProbe(33) != 1 || !p.Valid(15) || p.Valid(16) || p.Valid(-1) || p.Count() != 16 {
		t.Error("probe/valid helpers")
	}
	for in, want := range map[int]string{0: "00", 7: "07", 15: "15", 99: "99", 100: "100"} {
		if got := FormatPartition(in); got != want {
			t.Errorf("FormatPartition(%d) = %q", in, got)
		}
	}
}

func TestVerdictID(t *testing.T) {
	player := NewUUID()
	a := VerdictID("t_aaaaaaaaaaaaaaaaaaaa", player, "s1", 4, 42)
	if a != VerdictID("t_aaaaaaaaaaaaaaaaaaaa", player, "s1", 4, 42) {
		t.Fatal("not deterministic")
	}
	for _, b := range []string{
		VerdictID("t_bbbbbbbbbbbbbbbbbbbb", player, "s1", 4, 42),
		VerdictID("t_aaaaaaaaaaaaaaaaaaaa", NewUUID(), "s1", 4, 42),
		VerdictID("t_aaaaaaaaaaaaaaaaaaaa", player, "s2", 4, 42),
		VerdictID("t_aaaaaaaaaaaaaaaaaaaa", player, "s1", 3, 42),
		VerdictID("t_aaaaaaaaaaaaaaaaaaaa", player, "s1", 4, 43),
	} {
		if a == b {
			t.Fatal("collision across inputs")
		}
	}
	u, err := ParseCanonicalUUID(a)
	if err != nil || u[6]>>4 != 5 {
		t.Fatalf("not a canonical UUIDv5: %s", a)
	}
	if DerivedVerdictID(a, 6) != DerivedVerdictID(a, 6) || DerivedVerdictID(a, 6) == DerivedVerdictID(a, 5) {
		t.Fatal("derived id")
	}
}
