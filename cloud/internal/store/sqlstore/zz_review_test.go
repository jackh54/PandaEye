package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

func reviewStores(t *testing.T) map[string]*Store {
	out := map[string]*Store{"sqlite": openSQLiteTest(t)}
	dsn := os.Getenv("PE_TEST_POSTGRES_DSN")
	if dsn == "" {
		return out
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	schema := "pe_review_" + strings.ToLower(identity.NewTenantID()[2:])
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
	out["postgres"] = s
	return out
}

func TestReviewNULAndBlobs(t *testing.T) {
	ctx := context.Background()
	for name, s := range reviewStores(t) {
		t.Run(name, func(t *testing.T) {
			tid := identity.NewTenantID()
			tn := store.NewTenant(tid, "n", "slug-"+tid[2:8], time.Now())
			if err := s.CreateTenant(ctx, tn); err != nil {
				t.Fatal(err)
			}
			srv := identity.NewUUID().String()
			err := s.CreateServer(ctx, &store.Server{TenantID: tid, ID: srv, Name: "evil\x00name", SecretHash: []byte{1}})
			t.Logf("CreateServer with NUL in name: err=%v", err)
			player := identity.NewUUID().String()
			err = s.UpsertPlayer(ctx, tid, player, "Ste\x00ve", srv, time.Now())
			t.Logf("UpsertPlayer with NUL in name: err=%v", err)
			ev, _ := json.Marshal(map[string]string{"info": "a\x00b"})
			_, err = s.UpsertVerdict(ctx, &store.Verdict{ID: identity.NewUUID().String(), TenantID: tid, ServerID: srv, PlayerUUID: player,
				Action: pb.Action_ACTION_ALERT, Evidence: ev})
			t.Logf("UpsertVerdict with \\u0000 in evidence (%s): err=%v", ev, err)
			vid := identity.NewUUID().String()
			if _, err := s.UpsertVerdict(ctx, &store.Verdict{ID: vid, TenantID: tid, ServerID: srv, PlayerUUID: player, Action: pb.Action_ACTION_ALERT}); err != nil {
				t.Fatal(err)
			}
			_, err = s.ApplyAck(ctx, store.Ack{TenantID: tid, ServerID: srv, VerdictID: vid, Note: "note\x00x"})
			t.Logf("ApplyAck with NUL in note: err=%v", err)

			// nil vs empty blob round trip.
			empty := []byte{}
			if _, err := s.UpdateTenant(ctx, tid, store.TenantPatch{DiscordWebhookEnc: &empty}, time.Now()); err != nil {
				t.Fatal(err)
			}
			got, _ := s.GetTenant(ctx, tid)
			t.Logf("webhook set to []byte{}: read back nil=%v len=%d", got.DiscordWebhookEnc == nil, len(got.DiscordWebhookEnc))
			var nilb []byte
			if _, err := s.UpdateTenant(ctx, tid, store.TenantPatch{DiscordWebhookEnc: &nilb}, time.Now()); err != nil {
				t.Fatal(err)
			}
			got, _ = s.GetTenant(ctx, tid)
			t.Logf("webhook set to nil: read back nil=%v len=%d", got.DiscordWebhookEnc == nil, len(got.DiscordWebhookEnc))
		})
	}
}

func TestReviewAckAfterOverturn(t *testing.T) {
	ctx := context.Background()
	for name, s := range reviewStores(t) {
		t.Run(name, func(t *testing.T) {
			tid := identity.NewTenantID()
			if err := s.CreateTenant(ctx, store.NewTenant(tid, "n", "slug-"+tid[2:8], time.Now())); err != nil {
				t.Fatal(err)
			}
			srv, player, vid := identity.NewUUID().String(), identity.NewUUID().String(), identity.NewUUID().String()
			if _, err := s.UpsertVerdict(ctx, &store.Verdict{ID: vid, TenantID: tid, ServerID: srv, PlayerUUID: player, Action: pb.Action_ACTION_BAN}); err != nil {
				t.Fatal(err)
			}
			// Staff overturns before the plugin's ack arrives.
			if err := s.SetVerdictStatus(ctx, tid, vid, store.StatusOverturned, store.StatusOpen); err != nil {
				t.Fatal(err)
			}
			res, err := s.ApplyAck(ctx, store.Ack{TenantID: tid, ServerID: srv, VerdictID: vid, Executed: true})
			if err != nil {
				t.Fatal(err)
			}
			b, err := s.GetBan(ctx, tid, vid)
			t.Logf("verdict status=%s, BanCreated=%v, ban err=%v active=%v", res.Verdict.Status, res.BanCreated, err, b != nil && b.Active)
		})
	}
}

func TestReviewDSNInErrors(t *testing.T) {
	for _, dsn := range []string{
		"postgres://pe:supersecret1@127.0.0.1:1/db?sslmode=disable",
		"postgres://pe:supersecret2@127.0.0.1:99999/db?sslmode=disable",
		"host=127.0.0.1 port=1 user=pe password=supersecret3 sslmode=disable",
		"host=127.0.0.1 port=abc user=pe password=supersecret4",
		"postgres://pe:supersecret5@127.0.0.1:55432/db?sslmode=bogus",
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, err := Open(ctx, DriverPostgres, dsn, Options{})
		cancel()
		t.Logf("leak=%v err=%v", err != nil && strings.Contains(err.Error(), "supersecret"), err)
	}
}

func TestReviewNameSearchUnicode(t *testing.T) {
	ctx := context.Background()
	for name, s := range reviewStores(t) {
		tid := identity.NewTenantID()
		if err := s.CreateTenant(ctx, store.NewTenant(tid, "n", "slug-"+tid[2:8], time.Now())); err != nil {
			t.Fatal(err)
		}
		p := identity.NewUUID().String()
		if err := s.UpsertPlayer(ctx, tid, p, "Äpfel", "", time.Now()); err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{"Äp", "äp", "ÄP"} {
			got, err := s.FindPlayersByName(ctx, tid, q, 10)
			t.Logf("%s: FindPlayersByName(%q) -> %d err=%v", name, q, len(got), err)
		}
	}
}

func TestReviewConcurrentMigratePostgres(t *testing.T) {
	dsn := os.Getenv("PE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip()
	}
	admin, _ := sql.Open("pgx", dsn)
	defer admin.Close()
	failures := 0
	for round := 0; round < 10; round++ {
		schema := "pe_mig_" + strings.ToLower(identity.NewTenantID()[2:])
		admin.Exec("CREATE SCHEMA " + schema)
		var stores []*Store
		for i := 0; i < 6; i++ {
			s, err := Open(context.Background(), DriverPostgres, withSearchPath(dsn, schema), Options{MaxConns: 2})
			if err != nil {
				t.Fatal(err)
			}
			stores = append(stores, s)
		}
		errs := make(chan error, len(stores))
		start := make(chan struct{})
		for _, s := range stores {
			go func(s *Store) { <-start; _, err := s.Migrate(context.Background()); errs <- err }(s)
		}
		close(start)
		for range stores {
			if err := <-errs; err != nil {
				failures++
				if failures == 1 {
					t.Logf("concurrent migrate error: %v", err)
				}
			}
		}
		for _, s := range stores {
			s.Close()
		}
		admin.Exec("DROP SCHEMA " + schema + " CASCADE")
	}
	t.Logf("failed Migrate calls: %d of 60", failures)
}
