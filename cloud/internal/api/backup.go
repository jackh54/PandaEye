package api

import (
	"context"
	"fmt"
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
	backupPrefix = "pandaeye-"
	backupSuffix = ".db"
	backupLayout = "20060102T150405Z"
)

// backupStore is implemented by the sqlite store (sqlstore.Store.Backup).
type backupStore interface {
	Backup(ctx context.Context, path string) error
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
		path, err := backupOnce(ctx, bs, dir, db.BackupKeep, s.now())
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("online backup failed", "err", err)
			}
		} else {
			s.log.Info("online backup written", "path", path)
		}
		t.Reset(db.BackupInterval)
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

// backupOnce writes pandaeye-<UTC time>.db into dir (through a .partial file, so a crash never
// leaves a truncated copy under a backup name) and deletes all but the newest keep copies.
func backupOnce(ctx context.Context, bs backupStore, dir string, keep int, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("backup dir: %w", err)
	}
	name := backupPrefix + now.UTC().Format(backupLayout) + backupSuffix
	final := filepath.Join(dir, name)
	partial := final + ".partial"
	_ = os.Remove(partial) // VACUUM INTO refuses an existing file
	if err := bs.Backup(ctx, partial); err != nil {
		_ = os.Remove(partial)
		return "", err
	}
	if err := os.Rename(partial, final); err != nil {
		_ = os.Remove(partial)
		return "", fmt.Errorf("backup: %w", err)
	}
	return final, pruneBackups(dir, keep)
}

// pruneBackups deletes the oldest pandaeye-*.db copies beyond keep; other files are left alone.
func pruneBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("backup prune: %w", err)
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && strings.HasPrefix(n, backupPrefix) && strings.HasSuffix(n, backupSuffix) {
			names = append(names, n)
		}
	}
	if len(names) <= keep {
		return nil
	}
	sort.Strings(names) // the timestamp layout sorts chronologically
	for _, n := range names[:len(names)-keep] {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			return fmt.Errorf("backup prune: %w", err)
		}
	}
	return nil
}
