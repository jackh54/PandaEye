package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestYAMLScalarsKeepTheirText(t *testing.T) {
	for _, v := range []string{"12345678901234567890123456789012345678", "0123", "1e3", "0x1F", "007", "true", "2026-01-02"} {
		out, err := parseYAML([]byte("license.pepper: " + v + "\nowner.id: '" + v + "'\n"))
		if err != nil || out["license.pepper"] != v || out["owner.id"] != v {
			t.Errorf("%s -> %q %q err=%v", v, out["license.pepper"], out["owner.id"], err)
		}
	}
	out, err := parseYAML([]byte("a: ~\nb: [1, 02, x]\n"))
	if err != nil || out["a"] != "" || out["b"] != "1,02,x" {
		t.Errorf("null/list: %v %v", out, err)
	}
}

func TestYAMLDuplicateKeyRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte("nats:\n  dir: /a\nnats.dir: /b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(RoleAll, p, nil); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("err = %v", err)
	}
}

func TestNATSURLErrorRedactsCredentials(t *testing.T) {
	env := []string{"PE_NATS_URL=nats://user:hunter2-secret@nats.example.com:42x22", "PE_DB_DRIVER=postgres", "PE_DB_DSN=x",
		"PE_OUTBOUND_KEY=0123456789abcdef0123456789abcdef", "PE_LICENSE_PEPPER=0123456789abcdef0123456789abcdef"}
	_, err := Load(RoleGateway, "", env)
	if err == nil || strings.Contains(err.Error(), "hunter2") || !strings.Contains(err.Error(), "nats.example.com") {
		t.Fatalf("err = %v", err)
	}
}

func TestLeaseRangeBounded(t *testing.T) {
	start := time.Now()
	if _, err := parseLeases("0-50000000"); err == nil {
		t.Fatal("a lease range past MaxPartitions must be rejected")
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("took %v", d)
	}
}
