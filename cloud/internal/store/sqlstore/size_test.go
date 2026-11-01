package sqlstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
)

func TestSizeReport(t *testing.T) {
	for name, s := range reviewStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			t0 := time.Unix(1_700_000_000, 0).UTC()
			tenant := identity.NewTenantID()
			if err := s.CreateTenant(ctx, store.NewTenant(tenant, "n", "size-"+tenant[2:], t0)); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				player := identity.NewUUID().String()
				if err := s.UpsertPlayer(ctx, tenant, player, fmt.Sprintf("Player%d", i), "", t0); err != nil {
					t.Fatal(err)
				}
			}
			size, err := s.Size(ctx)
			if err != nil || size <= 0 {
				t.Fatalf("size %d %v", size, err)
			}
			r, err := s.SizeReport(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if r.Bytes <= 0 || r.FreeBytes < 0 || r.FreeBytes > r.Bytes {
				t.Fatalf("bytes %d, free %d", r.Bytes, r.FreeBytes)
			}
			if (name == "sqlite") != (r.WALBytes > 0) {
				t.Fatalf("wal bytes %d", r.WALBytes)
			}
			if r.Rows["tenants"] != 1 || r.Rows["players"] != 3 || r.Rows["verdicts"] != 0 || r.Rows["schema_migrations"] < 1 {
				t.Fatalf("rows %v", r.Rows)
			}
			for _, table := range []string{"meta", "servers", "player_names", "clips", "probe_stats", "audit_log", "erasures"} {
				if _, ok := r.Rows[table]; !ok {
					t.Errorf("table %s missing from %v", table, r.Rows)
				}
			}
			for table := range r.Rows {
				if strings.HasPrefix(table, "sqlite_") {
					t.Errorf("internal table %s reported", table)
				}
				if r.TableBytes[table] <= 0 {
					t.Errorf("table %s: %d bytes", table, r.TableBytes[table])
				}
			}
			if len(r.TableBytes) != len(r.Rows) {
				t.Errorf("table bytes %v for rows %v", r.TableBytes, r.Rows)
			}
		})
	}
}

// Deleted rows show up as free pages in a sqlite file, which keeps its size; a checkpoint empties
// the -wal file.
func TestSizeReportFreePages(t *testing.T) {
	ctx := context.Background()
	s := openSQLiteTest(t)
	t0 := time.Unix(1_700_000_000, 0).UTC()
	tenant := identity.NewTenantID()
	if err := s.CreateTenant(ctx, store.NewTenant(tenant, "n", "free", t0)); err != nil {
		t.Fatal(err)
	}
	big := json.RawMessage(`"` + strings.Repeat("x", 64<<10) + `"`)
	for i := 0; i < 32; i++ {
		if err := s.InsertAudit(ctx, &store.AuditEntry{TenantID: tenant, Principal: "system", Action: "a", Detail: big, CreatedAt: t0}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.SizeReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.Rows["audit_log"] != 32 || before.TableBytes["audit_log"] < 32*64<<10 || before.WALBytes <= 0 {
		t.Fatalf("audit_log: %d rows, %d bytes, wal %d", before.Rows["audit_log"], before.TableBytes["audit_log"], before.WALBytes)
	}
	if _, err := s.PurgeAudit(ctx, tenant, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var busy, frames, done int
	if err := s.w.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &done); err != nil || busy != 0 {
		t.Fatalf("checkpoint: busy %d, %v", busy, err)
	}
	after, err := s.SizeReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Rows["audit_log"] != 0 || after.Bytes != before.Bytes || after.FreeBytes < 32*64<<10 || after.WALBytes != 0 {
		t.Fatalf("after purge: %d rows, %d bytes, %d free, wal %d (before %d bytes)", after.Rows["audit_log"], after.Bytes, after.FreeBytes,
			after.WALBytes, before.Bytes)
	}
}
