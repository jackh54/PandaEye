package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReviewYAMLScalars(t *testing.T) {
	for _, v := range []string{"12345678901234567890123456789012345678", "0123", "1e3", "0x1F", "007", "true", "~", "2026-01-02"} {
		out, err := parseYAML([]byte("license.pepper: " + v + "\nowner.id: " + v + "\n"))
		t.Logf("%-40s -> pepper %q owner %q err=%v", v, out["license.pepper"], out["owner.id"], err)
	}
}

func TestReviewNATSURLLeak(t *testing.T) {
	env := []string{"PE_NATS_URL=nats://user:hunter2-secret@nats.example.com:42x22", "PE_DB_DRIVER=postgres", "PE_DB_DSN=x",
		"PE_OUTBOUND_KEY=0123456789abcdef0123456789abcdef", "PE_LICENSE_PEPPER=0123456789abcdef0123456789abcdef"}
	_, err := Load(RoleGateway, "", env)
	t.Logf("err: %v", err)
}

func TestReviewLeasesHugeRange(t *testing.T) {
	start := time.Now()
	_, err := parseLeases("0-50000000")
	t.Logf("parseLeases(0-50000000) took %v err=%v", time.Since(start), err)
}

func TestReviewDuplicateYAMLKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(p, []byte("nats:\n  dir: /a\nnats.dir: /b\n"), 0o600)
	seen := map[string]int{}
	for i := 0; i < 50; i++ {
		c, err := Load(RoleAll, p, nil)
		if err != nil {
			t.Fatal(err)
		}
		seen[c.NATS.Dir]++
	}
	t.Logf("nats.dir outcomes over 50 loads: %v", seen)
}
