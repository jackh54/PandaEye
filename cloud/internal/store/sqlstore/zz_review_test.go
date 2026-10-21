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
