package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/config"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
)

type fakeBackup struct{ err error }

func (f fakeBackup) Backup(_ context.Context, path string) error {
	if f.err != nil {
		return f.err
	}
	return os.WriteFile(path, []byte("db"), 0o600)
}

func TestBackupOnceRotatesAndLeavesOtherFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if _, err := backupOnce(context.Background(), fakeBackup{}, dir, 3, t0.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	want := []string{"notes.txt", "pandaeye-20261022T020000Z.db", "pandaeye-20261022T030000Z.db", "pandaeye-20261022T040000Z.db"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestBackupOnceFailureLeavesNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := backupOnce(context.Background(), fakeBackup{err: errors.New("disk full")}, dir, 3, time.Now()); err == nil {
		t.Fatal("want error")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("left %v", entries)
	}
}

func TestBackupDir(t *testing.T) {
	cases := []struct{ dsn, dir, want string }{
		{"./data/pandaeye.db", "", filepath.Join("data", "backups")},
		{"/srv/pe/pe.db", "", "/srv/pe/backups"},
		{"file:/srv/pe/pe.db?cache=shared", "", "/srv/pe/backups"},
		{"file:data/pe.db", "", filepath.Join("data", "backups")},
		{"file::memory:", "", ""},
		{":memory:", "", ""},
		{"./data/pandaeye.db", "/mnt/backup", "/mnt/backup"},
	}
	for _, c := range cases {
		got := backupDir(config.DB{DSN: identity.Secret(c.dsn), BackupDir: c.dir})
		if got != c.want {
			t.Errorf("backupDir(%q, %q) = %q, want %q", c.dsn, c.dir, got, c.want)
		}
	}
}
