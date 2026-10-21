package sqlstore

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

func TestAddReputationContribIfConfirmed(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { testContribIfConfirmed(t, openSQLiteTest(t)) })
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("PE_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("PE_TEST_POSTGRES_DSN not set")
		}
		admin, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { admin.Close() })
		schema := "pe_test_" + strings.ToLower(identity.NewTenantID()[2:])
		if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { admin.Exec("DROP SCHEMA " + schema + " CASCADE") })
		s, err := Open(context.Background(), DriverPostgres, withSearchPath(dsn, schema), Options{MaxConns: 4})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		if _, err := s.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		testContribIfConfirmed(t, s)
	})
}

func testContribIfConfirmed(t *testing.T, s *Store) {
	ctx := context.Background()
	now := time.Now().UTC()
	tenant := identity.NewTenantID()
	if err := s.CreateTenant(ctx, store.NewTenant(tenant, "n", tenant, now)); err != nil {
		t.Fatal(err)
	}
	player := identity.NewUUID().String()
	ban := &store.Verdict{ID: identity.NewUUID().String(), TenantID: tenant, ServerID: "s", PlayerUUID: player,
		Action: pb.Action_ACTION_BAN, CreatedAt: now}
	if _, err := s.UpsertVerdict(ctx, ban); err != nil {
		t.Fatal(err)
	}
	c := store.ReputationContrib{TenantID: tenant, PlayerUUID: player, VerdictID: ban.ID, Weight: 1, CreatedAt: now}
	try := func(want bool, step string) {
		t.Helper()
		ok, err := s.AddReputationContribIfConfirmed(ctx, c)
		if err != nil || ok != want {
			t.Fatalf("%s: %v %v", step, ok, err)
		}
	}
	try(false, "open, not executed")
	if _, err := s.ApplyAck(ctx, store.Ack{TenantID: tenant, ServerID: "s", VerdictID: ban.ID, Executed: true, At: now}); err != nil {
		t.Fatal(err)
	}
	try(false, "executed, not confirmed")
	if err := s.SetVerdictStatus(ctx, tenant, ban.ID, store.StatusConfirmed); err != nil {
		t.Fatal(err)
	}
	wrong := c
	wrong.PlayerUUID = identity.NewUUID().String()
	if ok, err := s.AddReputationContribIfConfirmed(ctx, wrong); err != nil || ok {
		t.Fatal("another player's contribution for the verdict")
	}
	try(true, "confirmed and executed")
	try(true, "idempotent")
	rep, err := s.PlayerReputation(ctx, identity.NewTenantID(), player)
	if err != nil || len(rep) != 1 || rep[0].Contributions != 1 {
		t.Fatalf("reputation %+v %v", rep, err)
	}

	// A shadow BAN never contributes.
	shadow := &store.Verdict{ID: identity.NewUUID().String(), TenantID: tenant, ServerID: "s", PlayerUUID: player,
		Action: pb.Action_ACTION_BAN, Shadow: true, CreatedAt: now}
	if _, err := s.UpsertVerdict(ctx, shadow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyAck(ctx, store.Ack{TenantID: tenant, VerdictID: shadow.ID, Executed: true, At: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetVerdictStatus(ctx, tenant, shadow.ID, store.StatusConfirmed); err != nil {
		t.Fatal(err)
	}
	c.VerdictID = shadow.ID
	try(false, "shadow ban")
}
