package api

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/config"
)

// backupFirstDelay is how long after start the first online backup runs.
const backupFirstDelay = time.Minute

const (
	backupPrefix  = "pandaeye-"
	backupSuffix  = ".db.gz"
	rawSuffix     = ".db" // the VACUUM INTO copy; also the uncompressed backups of earlier releases
	partialSuffix = ".partial"
	backupLayout  = "20060102T150405Z"
	// The disk guard skips a backup while the backup directory's file system has less than
	// max(2 x the database size, backupMinFree) available: the uncompressed copy alone is about as
	// large as the database.
	backupMinFree = 1 << 30
)

// backupStore is implemented by the sqlite store (sqlstore.Store.Backup and Size).
type backupStore interface {
	Backup(ctx context.Context, path string) error
	Size(ctx context.Context) (int64, error)
}

// diskFree returns the bytes available on the file system holding dir (replaced in tests).
var diskFree = freeBytes

// lowSpaceError is returned by backupOnce when the disk guard skipped the backup.
type lowSpaceError struct{ free, need int64 }

func (e *lowSpaceError) Error() string {
	return fmt.Sprintf("backup skipped: %d bytes free, %d needed", e.free, e.need)
}

// runBackups writes an online copy of a sqlite database every PE_DB_BACKUP_INTERVAL and keeps the
// newest PE_DB_BACKUP_KEEP. sqlite is single-node (role all), so no lease is needed.
func (s *Service) runBackups(ctx context.Context) error {
	db := s.d.Config.DB
	bs, ok := s.d.Store.(backupStore)
	if !ok || db.Driver != config.DriverSQLite || db.BackupInterval <= 0 {
		return nil
	}
	dir := backupDir(db)
	if dir == "" {
		s.log.Warn("online backups disabled: set PE_DB_BACKUP_DIR for this PE_DB_DSN")
		return nil
	}
	s.log.Info("online backups enabled", "dir", dir, "every", db.BackupInterval.String(), "keep", db.BackupKeep)
	t := time.NewTimer(backupFirstDelay)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		s.backup(ctx, bs, dir, db.BackupKeep)
		t.Reset(db.BackupInterval)
	}
}

// backup runs backupOnce and logs its outcome in one line (a warning when the disk guard skipped it).
func (s *Service) backup(ctx context.Context, bs backupStore, dir string, keep int) {
	path, err := backupOnce(ctx, bs, dir, keep, s.now())
	var low *lowSpaceError
	switch {
	case errors.As(err, &low):
		s.log.Warn("online backup skipped: low disk space", "dir", dir, "free_bytes", low.free, "need_bytes", low.need)
	case err != nil:
		if ctx.Err() == nil {
			s.log.Warn("online backup failed", "err", err)
		}
	default:
		s.log.Info("online backup written", "path", path)
	}
}

// backupDir is PE_DB_BACKUP_DIR, else "backups" next to the database file; "" when the DSN names
// no file (an in-memory database).
func backupDir(db config.DB) string {
	if db.BackupDir != "" {
		return db.BackupDir
	}
	p := db.DSN.Reveal()
	if strings.HasPrefix(p, "file:") {
		u, err := url.Parse(p)
		if err != nil {
			return ""
		}
		p = u.Opaque
		if p == "" {
			p = u.Path
		}
		if p, err = url.PathUnescape(p); err != nil {
			return ""
		}
	}
	if p == "" || p == ":memory:" || strings.HasPrefix(p, ":memory:") {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "backups")
}

// backupOnce writes pandaeye-<UTC time>.db.gz into dir and deletes all but the newest keep copies.
// The copy goes through .partial files (VACUUM INTO, then gzip, fsync and rename), so a crash never
// leaves a truncated copy under a backup name; the next run deletes such leftovers. It writes nothing
// and returns a *lowSpaceError while the disk guard fails (checkBackupSpace).
func backupOnce(ctx context.Context, bs backupStore, dir string, keep int, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("backup dir: %w", err)
	}
	// Leftovers and copies beyond a lowered keep go before the free space is measured. A failure
	// repeats in the prune after the copy, which reports it.
	_ = pruneBackups(dir, keep)
	if err := checkBackupSpace(ctx, bs, dir); err != nil {
		return "", err
	}
	stem := filepath.Join(dir, backupPrefix+now.UTC().Format(backupLayout))
	raw, final := stem+rawSuffix+partialSuffix, stem+backupSuffix
	partial := final + partialSuffix
	if err := bs.Backup(ctx, raw); err != nil {
		_ = os.Remove(raw)
		return "", err
	}
	err := gzipFile(ctx, raw, partial)
	if err == nil {
		err = os.Rename(partial, final)
	}
	_ = os.Remove(raw)
	if err != nil {
		_ = os.Remove(partial)
		return "", fmt.Errorf("backup: %w", err)
	}
	syncDir(dir)
	return final, pruneBackups(dir, keep)
}

// checkBackupSpace is the disk guard: a *lowSpaceError when dir's file system has less than
// max(2 x the database size, backupMinFree) available. A file system that cannot report its free
// space does not stop backups.
func checkBackupSpace(ctx context.Context, bs backupStore, dir string) error {
	free, err := diskFree(dir)
	if err != nil {
		return nil
	}
	size, err := bs.Size(ctx)
	if err != nil {
		return fmt.Errorf("backup: database size: %w", err)
	}
	if need := max(2*size, backupMinFree); free < need {
		return &lowSpaceError{free: free, need: need}
	}
	return nil
}

// gzipFile writes src gzip-compressed to dst (mode 0600, fsynced); it stops when ctx ends.
func gzipFile(ctx context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(f, 256<<10)
	zw := gzip.NewWriter(bw)
	_, err = io.Copy(zw, ctxReader{ctx: ctx, r: in})
	if err == nil {
		err = zw.Close()
	}
	if err == nil {
		err = bw.Flush()
	}
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// ctxReader ends a copy when ctx ends, so a shutdown does not wait for a large backup.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// syncDir makes the rename durable (best effort).
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

// pruneBackups deletes the leftovers of interrupted backups and the oldest copies beyond keep,
// counting the uncompressed pandaeye-<UTC>.db copies of earlier releases with the .db.gz ones; other
// files are left alone.
func pruneBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("backup prune: %w", err)
	}
	var names, remove []string
	for _, e := range entries {
		n := e.Name()
		if !e.Type().IsRegular() {
			continue
		}
		if partial, ok := backupName(n); ok && partial {
			remove = append(remove, n)
		} else if ok {
			names = append(names, n)
		}
	}
	if len(names) > keep {
		sort.Strings(names) // the timestamp layout sorts chronologically, a .db before the .db.gz of its second
		remove = append(remove, names[:len(names)-keep]...)
	}
	for _, n := range remove {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			return fmt.Errorf("backup prune: %w", err)
		}
	}
	return nil
}

// backupName reports whether n is a name backupOnce writes (pandaeye-<UTC>.db.gz, or the .db of an
// earlier release) and whether it is the .partial file of one.
func backupName(n string) (partial, ok bool) {
	rest, ok := strings.CutPrefix(n, backupPrefix)
	if !ok {
		return false, false
	}
	rest, partial = strings.CutSuffix(rest, partialSuffix)
	ts, ok := strings.CutSuffix(rest, backupSuffix)
	if !ok {
		ts, ok = strings.CutSuffix(rest, rawSuffix)
	}
	if !ok {
		return false, false
	}
	_, err := time.Parse(backupLayout, ts)
	return partial, err == nil
}
