package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/config"
)

func runCmd(t *testing.T, env []string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), args, &out, &errb, env)
	return code, out.String(), errb.String()
}

func TestVersionAndUsage(t *testing.T) {
	if code, out, _ := runCmd(t, nil, "version"); code != 0 || !strings.HasPrefix(out, "pandaeye dev (go") {
		t.Fatalf("version: %d %q", code, out)
	}
	if code, _, _ := runCmd(t, nil); code != 2 {
		t.Fatal("no command must be a usage error")
	}
	if code, _, errOut := runCmd(t, nil, "frobnicate"); code != 2 || !strings.Contains(errOut, "unknown command") {
		t.Fatal("unknown command")
	}
	if code, out, _ := runCmd(t, nil, "help"); code != 0 || !strings.Contains(out, "replay") {
		t.Fatal("help")
	}
	if code, _, _ := runCmd(t, nil, "migrate", "--nope"); code != 2 {
		t.Fatal("bad flag")
	}
	if code, _, errOut := runCmd(t, []string{"PE_PARTITIONS=0"}, "all"); code != 1 || !strings.Contains(errOut, "PE_PARTITIONS") {
		t.Fatal("config errors must be reported")
	}
}

func TestMigrate(t *testing.T) {
	env := []string{"PE_DB_DSN=" + filepath.Join(t.TempDir(), "pe.db")}
	code, out, errOut := runCmd(t, env, "migrate")
	if code != 0 || !strings.Contains(out, "applied 1 migration(s); schema version 1") {
		t.Fatalf("migrate: %d %q %q", code, out, errOut)
	}
	if code, out, _ := runCmd(t, env, "migrate"); code != 0 || !strings.Contains(out, "applied 0") {
		t.Fatalf("second migrate: %q", out)
	}
}

func TestBuildServices(t *testing.T) {
	d := &app.Deps{}
	for role, want := range map[config.Role][]string{
		config.RoleAll:     {"gateway", "worker", "verdict", "api"},
		config.RoleGateway: {"gateway"},
		config.RoleVerdict: {"verdict"},
	} {
		got, err := buildServices(role, d)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, s := range got {
			names = append(names, s.Name)
		}
		if strings.Join(names, ",") != strings.Join(want, ",") {
			t.Errorf("%s: %v", role, names)
		}
	}
}

func TestReplayFlags(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	o, err := replayFlags{tenant: "t_x", player: "p", since: "2h"}.options(now)
	if err != nil || !o.Since.Equal(now.Add(-2*time.Hour)) || !o.Until.Equal(now) {
		t.Fatalf("%+v %v", o, err)
	}
	o, err = replayFlags{tenant: "t_x", clip: "c", since: "2026-01-01T00:00:00Z", until: "-1d"}.options(now)
	if err != nil || o.Since.Day() != 1 || !o.Until.Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("%+v %v", o, err)
	}
	for _, bad := range []replayFlags{{player: "p", since: "1h"}, {tenant: "t", since: "1h"}, {tenant: "t", player: "p", since: "yesterday"}, {tenant: "t", player: "p", since: "1h", until: "2h"}} {
		if _, err := bad.options(now); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if code, _, errOut := runCmd(t, []string{"PE_DB_DSN=" + filepath.Join(t.TempDir(), "x.db")}, "replay", "--player", "p"); code != 1 || !strings.Contains(errOut, "--tenant") {
		t.Fatalf("replay without tenant: %d %s", code, errOut)
	}
}
