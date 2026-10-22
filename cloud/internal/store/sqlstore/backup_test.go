package sqlstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
)

func TestSQLiteOnlineBackupIsAReadableCopy(t *testing.T) {
	ctx := context.Background()
	s := openSQLiteTest(t)
	now := time.Unix(1_700_000_000, 0)
	if err := s.CreateTenant(ctx, store.NewTenant("t_backup", "n", "backup", now)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "copy.db")
	if err := s.Backup(ctx, path); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(ctx, path); err == nil {
		t.Fatal("VACUUM INTO must refuse an existing file")
	}
	// The source stays writable after the backup.
	if err := s.CreateTenant(ctx, store.NewTenant("t_after", "n", "after", now)); err != nil {
		t.Fatal(err)
	}

	c, err := Open(ctx, DriverSQLite, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	ids, err := c.ListTenantIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "t_backup" {
		t.Fatalf("backup holds %v, want [t_backup]", ids)
	}
}
