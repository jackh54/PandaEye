import { describe, expect, it } from "vitest";
import { toLiteBansStats, toVerdict } from "../src/lib/pandaeye";
import { signedInClient, tenantFixture, verdictFixture } from "./helpers";

const STATS = {
  lookback_s: 604800,
  last_7d: {
    cheat_bans: 12,
    players: 11,
    flagged_before: { alert: 2, mitigate: 1, kick: 0, ban: 3 },
    not_flagged: 5,
    median_lead_time_s: 5400,
    would_kick_or_ban: 7,
    would_kick_or_ban_banned: 4,
    overturns: 1,
    verdicts_overturned: 2,
  },
  last_30d: {
    cheat_bans: 40,
    players: 36,
    flagged_before: { alert: 5, mitigate: 2, kick: 1, ban: 10 },
    not_flagged: 18,
    median_lead_time_s: null,
    would_kick_or_ban: 20,
    would_kick_or_ban_banned: 13,
    overturns: 3,
    verdicts_overturned: 4,
  },
};

function overviewApi(api: Awaited<ReturnType<typeof signedInClient>>["api"]) {
  return api
    .json("GET", "/api/v1/tenant", tenantFixture())
    .json("GET", "/api/v1/stats/overview", { last_24h: {}, last_7d: {} })
    .json("GET", "/api/v1/stats/latency", {})
    .json("GET", "/api/v1/servers", { servers: [] })
    .json("GET", "/api/v1/verdicts", { verdicts: [verdictFixture({ status: "overturned", decided_by: "litebans" })] })
    .json("GET", "/api/v1/appeals", { appeals: [] });
}

describe("LiteBans decisions and accuracy", () => {
  it("normalises decided_by and the agreement statistics", () => {
    expect(toVerdict(verdictFixture({ decided_by: "litebans" })).decidedBy).toBe("litebans");
    expect(toVerdict(verdictFixture({ decided_by: "STAFF" })).decidedBy).toBe("staff");
    expect(toVerdict(verdictFixture({ decided_by: "<b>" })).decidedBy).toBeNull();
    expect(toVerdict(verdictFixture()).decidedBy).toBeNull(); // an API without the field
    const s = toLiteBansStats(STATS);
    expect(s.lookbackS).toBe(604800);
    expect(s.windows.map((w) => w.label)).toEqual(["7 d", "30 d"]);
    expect(s.windows[0]).toMatchObject({ cheatBans: 12, players: 11, notFlagged: 5, medianLeadTimeS: 5400, wouldKickOrBan: 7, wouldKickOrBanBanned: 4 });
    expect(s.windows[0]!.flaggedBefore).toEqual({ alert: 2, mitigate: 1, kick: 0, ban: 3 });
    expect(s.windows[1]!.medianLeadTimeS).toBeNull();
    expect(toLiteBansStats({}).windows[0]).toMatchObject({ cheatBans: 0, medianLeadTimeS: null });
  });

  it("overview shows the Accuracy (LiteBans) card", async () => {
    const { client, api } = await signedInClient("viewer");
    overviewApi(api).json("GET", "/api/v1/stats/litebans", STATS);
    const html = await (await client.get("/")).text();
    expect(html).toContain("Accuracy (LiteBans)");
    expect(html).toContain("6 / 11 (55%)");
    expect(html).toContain("2 alert · 1 mitigate · 0 kick · 3 ban");
    expect(html).toContain("1 h 30 min");
    expect(html).toContain("4 / 7");
    expect(html).toContain("Would kick/ban, banned within 7 days");
    expect(html).toContain("2 verdicts overturned");
    // The recent verdict LiteBans overturned is marked as such.
    expect(html).toContain("LiteBans: overturned");
    expect(api.callsTo("GET", "/api/v1/stats/litebans")).toHaveLength(1);
  });

  it("overview explains an empty comparison and degrades when the API cannot answer", async () => {
    const empty = await signedInClient("viewer");
    overviewApi(empty.api).json("GET", "/api/v1/stats/litebans", { lookback_s: 604800, last_7d: { cheat_bans: 0 }, last_30d: { cheat_bans: 0 } });
    const html = await (await empty.client.get("/")).text();
    expect(html).toContain("No LiteBans bans for cheating yet");
    expect(html).not.toContain("Median lead time");

    const old = await signedInClient("viewer");
    overviewApi(old.api); // an API without the endpoint answers 404
    const res = await old.client.get("/");
    expect(res.status).toBe(200);
    expect(await res.text()).toContain("The LiteBans comparison could not be loaded");
  });

  it("verdict detail tells a LiteBans decision from a staff decision", async () => {
    const { client, api } = await signedInClient("staff");
    api.json("GET", "/api/v1/verdicts/v-lb", verdictFixture({ id: "v-lb", status: "confirmed", decided_by: "litebans" }));
    let html = await (await client.get("/verdicts/v-lb")).text();
    expect(html).toContain("LiteBans: confirmed");
    expect(html).toContain("Confirmed by a LiteBans ban for cheating");
    expect(html).toContain("Confirming it here makes it a staff decision");
    // Staff may still confirm (take over) and overturn it.
    expect(html).not.toMatch(/<button[^>]*disabled[^>]*>\s*Confirm: cheating/);
    expect(html).not.toMatch(/<button[^>]*disabled[^>]*>\s*Overturn/);

    api.json("GET", "/api/v1/verdicts/v-lo", verdictFixture({ id: "v-lo", status: "overturned", decided_by: "litebans" }));
    html = await (await client.get("/verdicts/v-lo")).text();
    expect(html).toContain("LiteBans: overturned");
    expect(html).toContain("Overturned by a LiteBans unban (false ban or accepted appeal)");
    expect(html).toMatch(/<button[^>]*disabled[^>]*>\s*Confirm: cheating/);

    api.json("GET", "/api/v1/verdicts/v-st", verdictFixture({ id: "v-st", status: "confirmed", decided_by: "staff" }));
    html = await (await client.get("/verdicts/v-st")).text();
    expect(html).toContain("Confirmed by staff");
    expect(html).not.toContain("LiteBans:");
    expect(html).toMatch(/<button[^>]*disabled[^>]*>\s*Confirm: cheating/);

    api.json("GET", "/api/v1/verdicts/v-open", verdictFixture({ id: "v-open", status: "open", decided_by: "" }));
    html = await (await client.get("/verdicts/v-open")).text();
    expect(html).not.toContain("<dt>Decision</dt>");
    expect(html).not.toContain("LiteBans:");
  });
});
