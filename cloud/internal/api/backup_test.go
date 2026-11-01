package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/config"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/store/sqlstore"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
)

type fakeBackup struct {
	err   error
	size  int64
	after func() // runs once the copy is written
}

func (f fakeBackup) Backup(_ context.Context, path string) error {
	if f.err != nil {
		return f.err
	}
	if err := os.WriteFile(path, []byte("db"), 0o600); err != nil {
		return err
	}
	if f.after != nil {
		f.after()
	}
	return nil
}

func (f fakeBackup) Size(context.Context) (int64, error) { return f.size, nil }

// withDiskFree makes the disk guard see free bytes on every file system until the test ends.
func withDiskFree(t *testing.T, free int64) {
	t.Helper()
	prev := diskFree
	diskFree = func(string) (int64, error) { return free, nil }
	t.Cleanup(func() { diskFree = prev })
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	return got
}

func gunzip(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func touch(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBackupOnceRotatesAndLeavesOtherFiles(t *testing.T) {
	withDiskFree(t, 1<<40)
	dir := filepath.Join(t.TempDir(), "backups")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	touch(t, dir, "notes.txt", "pandaeye-before-upgrade.db", "pandaeye-20200101T000000Z.db.bak")
	t0 := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	var last string
	for i := 0; i < 5; i++ {
		p, err := backupOnce(context.Background(), fakeBackup{}, dir, 3, t0.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		last = p
	}
	want := []string{"notes.txt", "pandaeye-20200101T000000Z.db.bak", "pandaeye-20261022T020000Z.db.gz", "pandaeye-20261022T030000Z.db.gz",
		"pandaeye-20261022T040000Z.db.gz", "pandaeye-before-upgrade.db"}
	if got := dirNames(t, dir); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if last != filepath.Join(dir, "pandaeye-20261022T040000Z.db.gz") {
		t.Fatalf("returned %s", last)
	}
	if b := gunzip(t, last); string(b) != "db" {
		t.Fatalf("backup holds %q, want the VACUUM INTO copy", b)
	}
	if fi, err := os.Stat(last); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode %v %v", fi, err)
	}
}

// Uncompressed copies of earlier releases count toward keep and go first, oldest first.
func TestBackupOncePrunesUncompressedCopies(t *testing.T) {
	withDiskFree(t, 1<<40)
	dir := t.TempDir()
	touch(t, dir, "pandaeye-20261021T120000Z.db", "pandaeye-20261021T180000Z.db", "pandaeye-20261021T060000Z.db")
	t0 := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	steps := [][]string{
		{"pandaeye-20261021T060000Z.db", "pandaeye-20261021T120000Z.db", "pandaeye-20261021T180000Z.db", "pandaeye-20261022T000000Z.db.gz"},
		{"pandaeye-20261021T120000Z.db", "pandaeye-20261021T180000Z.db", "pandaeye-20261022T000000Z.db.gz", "pandaeye-20261022T060000Z.db.gz"},
		{"pandaeye-20261021T180000Z.db", "pandaeye-20261022T000000Z.db.gz", "pandaeye-20261022T060000Z.db.gz", "pandaeye-20261022T120000Z.db.gz"},
		{"pandaeye-20261022T000000Z.db.gz", "pandaeye-20261022T060000Z.db.gz", "pandaeye-20261022T120000Z.db.gz", "pandaeye-20261022T180000Z.db.gz"},
	}
	for i, want := range steps {
		if _, err := backupOnce(context.Background(), fakeBackup{}, dir, 4, t0.Add(time.Duration(i)*6*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if got := dirNames(t, dir); !slices.Equal(got, want) {
			t.Fatalf("after backup %d: got %v, want %v", i+1, got, want)
		}
	}
	// A lowered keep prunes the surplus at the next run, before the new copy is written.
	if _, err := backupOnce(context.Background(), fakeBackup{}, dir, 1, t0.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := dirNames(t, dir); !slices.Equal(got, []string{"pandaeye-20261023T000000Z.db.gz"}) {
		t.Fatalf("keep 1: %v", got)
	}
}

func TestBackupOnceFailureLeavesNoPartialFile(t *testing.T) {
	withDiskFree(t, 1<<40)
	dir := t.TempDir()
	if _, err := backupOnce(context.Background(), fakeBackup{err: errors.New("disk full")}, dir, 3, time.Now()); err == nil {
		t.Fatal("want error")
	}
	if got := dirNames(t, dir); len(got) != 0 {
		t.Fatalf("left %v", got)
	}
	// Cancelled while compressing (shutdown): neither the raw copy nor the gzip stays.
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := backupOnce(ctx, fakeBackup{after: cancel}, dir, 3, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if got := dirNames(t, dir); len(got) != 0 {
		t.Fatalf("left %v", got)
	}
}

func TestBackupOnceDeletesLeftoversOfAnInterruptedRun(t *testing.T) {
	withDiskFree(t, 1<<40)
	dir := t.TempDir()
	touch(t, dir, "pandaeye-20261022T000000Z.db.partial", "pandaeye-20261022T060000Z.db.gz.partial", "pandaeye-20261021T000000Z.db.gz")
	if _, err := backupOnce(context.Background(), fakeBackup{}, dir, 3, time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	want := []string{"pandaeye-20261021T000000Z.db.gz", "pandaeye-20261022T120000Z.db.gz"}
	if got := dirNames(t, dir); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBackupDiskGuard(t *testing.T) {
	const gib = 1 << 30
	t0 := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		size, free int64
		need       int64 // 0: written
	}{
		{"small database, below the 1 GiB floor", 100 << 20, gib - 1, gib},
		{"small database, at the floor", 100 << 20, gib, 0},
		{"large database, below twice its size", gib, 2*gib - 1, 2 * gib},
		{"large database, twice its size free", gib, 2 * gib, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withDiskFree(t, tc.free)
			dir := t.TempDir()
			touch(t, dir, "pandaeye-20261021T000000Z.db", "pandaeye-20261021T060000Z.db.gz", "pandaeye-20261021T120000Z.db.gz",
				"pandaeye-20261021T180000Z.db.gz.partial")
			_, err := backupOnce(context.Background(), fakeBackup{size: tc.size}, dir, 2, t0)
			var low *lowSpaceError
			if tc.need == 0 {
				if err != nil {
					t.Fatal(err)
				}
				if got := dirNames(t, dir); !slices.Equal(got, []string{"pandaeye-20261021T120000Z.db.gz", "pandaeye-20261022T000000Z.db.gz"}) {
					t.Fatalf("written: %v", got)
				}
				return
			}
			if !errors.As(err, &low) || low.free != tc.free || low.need != tc.need {
				t.Fatalf("want lowSpaceError{%d, %d}, got %v", tc.free, tc.need, err)
			}
			// Nothing written, but the surplus and the leftovers are gone.
			if got := dirNames(t, dir); !slices.Equal(got, []string{"pandaeye-20261021T060000Z.db.gz", "pandaeye-20261021T120000Z.db.gz"}) {
				t.Fatalf("skipped: %v", got)
			}
		})
	}
}

func TestBackupDiskGuardUnknownFreeSpace(t *testing.T) {
	prev := diskFree
	diskFree = func(string) (int64, error) { return 0, errors.ErrUnsupported }
	t.Cleanup(func() { diskFree = prev })
	if _, err := backupOnce(context.Background(), fakeBackup{size: 1 << 40}, t.TempDir(), 3, time.Now()); err != nil {
		t.Fatalf("a file system that cannot report its free space must not stop backups: %v", err)
	}
}

func TestBackupLogsOneWarningPerSkip(t *testing.T) {
	var logs bytes.Buffer
	s := &Service{log: slog.New(slog.NewJSONHandler(&logs, nil)), now: time.Now}
	dir := t.TempDir()
	withDiskFree(t, 1<<20)
	s.backup(context.Background(), fakeBackup{size: 1 << 20}, dir, 3)
	s.backup(context.Background(), fakeBackup{size: 1 << 20}, dir, 3)
	withDiskFree(t, 1<<40)
	s.backup(context.Background(), fakeBackup{size: 1 << 20}, dir, 3)
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %q", lines)
	}
	for i, want := range []string{`"level":"WARN","msg":"online backup skipped: low disk space"`,
		`"level":"WARN","msg":"online backup skipped: low disk space"`, `"level":"INFO","msg":"online backup written"`} {
		if !strings.Contains(lines[i], want) {
			t.Fatalf("line %d: %s, want %s", i, lines[i], want)
		}
	}
	if !strings.Contains(lines[0], `"free_bytes":1048576,"need_bytes":1073741824`) {
		t.Fatalf("skip line: %s", lines[0])
	}
}

// The restore instructions: gunzip a backup and open it as the database.
func TestBackupOfARealStoreRestores(t *testing.T) {
	withDiskFree(t, 1<<40)
	ctx := context.Background()
	st := testkit.NewStore(t)
	id := identity.NewTenantID()
	if err := st.CreateTenant(ctx, store.NewTenant(id, "n", "restore", time.Now())); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p, err := backupOnce(ctx, st, dir, 3, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "pandaeye.db")
	if err := os.WriteFile(restored, gunzip(t, p), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := sqlstore.Open(ctx, sqlstore.DriverSQLite, restored, sqlstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if ids, err := c.ListTenantIDs(ctx); err != nil || !slices.Equal(ids, []string{id}) {
		t.Fatalf("restored tenants %v %v", ids, err)
	}
	if got := dirNames(t, dir); len(got) != 1 {
		t.Fatalf("backup dir %v", got)
	}
}

func TestBackupName(t *testing.T) {
	for _, tc := range []struct {
		name        string
		partial, ok bool
	}{
		{"pandaeye-20261022T000000Z.db.gz", false, true},
		{"pandaeye-20261022T000000Z.db", false, true},
		{"pandaeye-20261022T000000Z.db.partial", true, true},
		{"pandaeye-20261022T000000Z.db.gz.partial", true, true},
		{"pandaeye-before-upgrade.db", false, false},
		{"pandaeye-20261022T000000Z.db.bak", false, false},
		{"pandaeye-2026-10-22.db.gz", false, false},
		{"other-20261022T000000Z.db.gz", false, false},
		{"pandaeye.db", false, false},
	} {
		if partial, ok := backupName(tc.name); partial != tc.partial || ok != tc.ok {
			t.Errorf("backupName(%q) = %v, %v; want %v, %v", tc.name, partial, ok, tc.partial, tc.ok)
		}
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
