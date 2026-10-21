package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
)

func appealBody(verdictID, code string) map[string]string {
	return map[string]string{"verdict_id": verdictID, "appeal_code": code, "message": "I did not cheat", "contact": "me@example.test"}
}

func countAppeals(t *testing.T, e *env, tenant, verdictID string) int {
	t.Helper()
	as, _, err := e.d.Store.ListAppeals(context.Background(), tenant, store.AppealFilter{VerdictID: verdictID}, store.Page{Limit: 200})
	check(t, err)
	return len(as)
}

func TestPublicAppealsUniform(t *testing.T) {
	e := newEnv(t)
	e.seed()
	ctx := context.Background()
	// Close the seeded appeal so a new one may be opened for the active ban.
	_, err := e.d.Store.DecideAppeal(ctx, tenantA, appeal1, store.AppealDenied, "user:x", "", fixedNow)
	check(t, err)
	// An inactive (overturned) ban of tenant B.
	check(t, e.d.Store.OverturnBan(ctx, tenantB, vB, store.BanOverturn{By: "user:x", At: fixedNow}))

	var bodies []string
	ip := 0
	submit := func(body any) resp {
		ip++
		return e.must(202, "POST", "/api/v1/public/appeals", body, service(), header("X-PandaEye-Client-IP", "203.0.113."+itoa(ip)))
	}
	for name, body := range map[string]map[string]string{
		"unknown verdict":           appealBody("0f000000-0000-5000-8000-0000000000ff", appealCodeFor("0f000000-0000-5000-8000-0000000000ff")),
		"malformed verdict id":      appealBody("not-a-uuid", "X"),
		"wrong code":                appealBody(vBan, "WRONGCODE1"),
		"empty code":                appealBody(vBan, ""),
		"shadow ban, right code":    appealBody(vShadow, appealCodeFor(vShadow)),
		"not a ban, right code":     appealBody(vAlert, appealCodeFor(vAlert)),
		"inactive ban, right code":  appealBody(vB, appealCodeFor(vB)),
		"right code (accepted)":     appealBody(vBan, strings.ToLower(appealCodeFor(vBan))),
		"another tenant's code":     appealBody(vBan, appealCodeFor(vB)),
		"right code, never shipped": appealBody(vMit, appealCodeFor(vMit)),
	} {
		r := submit(body)
		bodies = append(bodies, string(r.body))
		_ = name
	}
	for _, b := range bodies {
		if b != bodies[0] {
			t.Fatalf("responses differ: %q vs %q", b, bodies[0])
		}
	}
	if n := countAppeals(t, e, tenantA, vBan); n != 2 { // the seeded (denied) one and the accepted one
		t.Fatalf("appeals for the active ban: %d", n)
	}
	for _, v := range []string{vShadow, vAlert, vMit} {
		if n := countAppeals(t, e, tenantA, v); n != 0 {
			t.Errorf("appeal stored for %s", v)
		}
	}
	if n := countAppeals(t, e, tenantB, vB); n != 1 {
		t.Errorf("appeal stored for an inactive ban: %d", n)
	}
	a, _, _ := e.d.Store.ListAppeals(ctx, tenantA, store.AppealFilter{Status: store.AppealOpen}, store.Page{})
	if len(a) != 1 || a[0].PlayerUUID != player1 || a[0].Message != "I did not cheat" || a[0].Contact != "me@example.test" {
		t.Fatalf("stored appeal: %+v", a)
	}
	// A second open appeal for the same ban is over the per-verdict limit.
	if r := e.do("POST", "/api/v1/public/appeals", appealBody(vBan, appealCodeFor(vBan)), service(), header("X-PandaEye-Client-IP", "192.0.2.200")); r.status != 429 || r.code(t) != codeAppealLimit {
		t.Fatalf("second open appeal: %d %s", r.status, r.body)
	}
	// Input limits.
	long := appealBody(vBan, "x")
	long["message"] = strings.Repeat("a", store.MaxAppealMessage+1)
	if r := e.do("POST", "/api/v1/public/appeals", long, service(), header("X-PandaEye-Client-IP", "192.0.2.201")); r.status != 400 {
		t.Fatalf("message over 2000 characters: %d", r.status)
	}
	contact := appealBody(vBan, "x")
	contact["contact"] = strings.Repeat("c", store.MaxAppealContact+1)
	if r := e.do("POST", "/api/v1/public/appeals", contact, service(), header("X-PandaEye-Client-IP", "192.0.2.202")); r.status != 400 {
		t.Fatalf("contact over 200 characters: %d", r.status)
	}
	// Only with the service token.
	if r := e.do("POST", "/api/v1/public/appeals", appealBody(vBan, "x"), header("X-PandaEye-Client-IP", "192.0.2.203")); r.status != 401 {
		t.Fatalf("no token: %d", r.status)
	}
}

func TestPublicAppealsPerIPLimit(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < appealsPerIPPerHour; i++ {
		e.must(202, "POST", "/api/v1/public/appeals", appealBody("0f000000-0000-5000-8000-0000000000ff", "x"), service(), header("X-PandaEye-Client-IP", "2001:db8:1:2::1"))
	}
	r := e.do("POST", "/api/v1/public/appeals", appealBody("0f000000-0000-5000-8000-0000000000ff", "x"), service(), header("X-PandaEye-Client-IP", "2001:db8:1:2::99"))
	if r.status != 429 || r.code(t) != codeRateLimited || r.header.Get("Retry-After") == "" {
		t.Fatalf("6th appeal from the same /64 in an hour: %d %s", r.status, r.body)
	}
	e.must(202, "POST", "/api/v1/public/appeals", appealBody("0f000000-0000-5000-8000-0000000000ff", "x"), service(), header("X-PandaEye-Client-IP", "2001:db8:1:3::1"))
	// An hour later the client may appeal again.
	e.setNow(fixedNow.Add(time.Hour))
	e.must(202, "POST", "/api/v1/public/appeals", appealBody("0f000000-0000-5000-8000-0000000000ff", "x"), service(), header("X-PandaEye-Client-IP", "2001:db8:1:2::1"))
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}
