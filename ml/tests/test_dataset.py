"""Loading, label parsing, exclusions, the overlap join and the leakage-free group split."""

import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qs, urlsplit

import numpy as np
import pandas as pd
import pytest

from pandaeye_ml import dataset as D
from pandaeye_ml import features as F
from pandaeye_ml.train import group_folds

W = F.WINDOW_US
T0 = 1_767_571_200_000_000  # 2026-01-05T00:00:00Z in us
P1 = "11111111-1111-4111-8111-111111111111"
P2 = "22222222-2222-4222-8222-222222222222"
P3 = "33333333-3333-4333-8333-333333333333"


def make_windows(rows):
    """rows: list of (tenant, player, window index[, protocol, version])."""
    meta = []
    for r in rows:
        tenant, player, k = r[:3]
        protocol = r[3] if len(r) > 3 else 770
        version = r[4] if len(r) > 4 else F.FEATURE_VERSION
        meta.append(
            {
                "tenant_id": tenant,
                "player": player,
                "server_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
                "session_id": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
                "window_start_us": T0 + k * W,
                "window_end_us": T0 + (k + 1) * W,
                "client_protocol": protocol,
                "tick_client": int(protocol >= 768),
                "hot_loaded": 0,
                "feature_version": version,
            }
        )
    feats = pd.DataFrame(np.tile(F.DEFAULTS, (len(rows), 1)), columns=list(F.FEATURE_NAMES))
    return D.normalize_windows(pd.concat([pd.DataFrame(meta), feats], axis=1))


def label(tenant, player, lab, start, end, **kw):
    return {"tenant_id": tenant, "player_uuid": player, "label": lab, "window_start": start, "window_end": end, **kw}


# --- helpers ------------------------------------------------------------------------------


def test_to_unix_us_units_and_iso():
    assert D.to_unix_us(1_767_571_200) == T0  # seconds
    assert D.to_unix_us(1_767_571_200_000) == T0  # milliseconds
    assert D.to_unix_us(T0) == T0  # microseconds
    assert D.to_unix_us("2026-01-05T00:00:00Z") == T0
    assert D.to_unix_us("2026-01-05T01:00:00+01:00") == T0
    assert D.to_unix_us("2026-01-05T00:00:00.000250Z") == T0 + 250
    assert D.to_unix_us(str(T0)) == T0
    with pytest.raises(ValueError):
        D.to_unix_us(None)


def test_normalize_category_and_uuid():
    assert D.normalize_category("CATEGORY_HEURISTICS") == "HEURISTICS"
    assert D.normalize_category("heuristics") == "HEURISTICS"
    assert D.normalize_category(7) == "WORLD_INTERACTION"
    assert D.normalize_category("7") == "WORLD_INTERACTION"
    assert D.normalize_category(None) == D.UNKNOWN_CATEGORY
    assert D.normalize_category("nonsense") == D.UNKNOWN_CATEGORY
    assert D.normalize_uuid(P1.upper()) == P1
    assert D.normalize_uuid(P1.replace("-", "")) == P1
    assert D.normalize_uuid(bytes.fromhex(P1.replace("-", ""))) == P1


def test_parse_label_records():
    recs = [
        label("t1", P1, "cheat", T0, T0 + W, category="CATEGORY_AUTO_CLICKER", source="confirmed"),
        {"tenant_id": "t1", "player": P2, "label": "LEGIT", "window_start": "2026-01-05T00:00:00Z",
         "window_end": T0 // 1000 + 60_000, "evidence": {"category": "HEURISTICS"}},
        label("t1", P3, "maybe", T0, T0 + W),  # unknown label: skipped
        {"tenant_id": "t1", "label": "cheat", "window_start": T0},  # malformed: skipped
    ]
    df = D.parse_label_records(recs)
    assert list(df["player"]) == [P1, P2]
    assert list(df["y"]) == [1, 0]
    assert df.loc[0, "category"] == "AUTO_CLICKER"
    assert df.loc[1, "category"] == ""  # legit labels carry no cheat category
    assert df.loc[1, "end_us"] == T0 + 60_000_000


# --- join -----------------------------------------------------------------------------------


def test_overlap_join_boundaries():
    windows = make_windows([("t1", P1, k) for k in range(6)])
    # Label covers [start of window 2, start of window 4] (closed): windows 2, 3 and 4 overlap.
    labels = D.parse_label_records([label("t1", P1, "cheat", T0 + 2 * W, T0 + 4 * W)])
    out, stats = D.join_labels(windows, labels)
    starts = sorted((out["window_start_us"] - T0) // W)
    assert starts == [2, 3, 4]
    assert stats["windows_unlabelled"] == 3
    # A label ending exactly where a window starts overlaps it; one starting at a window's end does not.
    labels = D.parse_label_records([label("t1", P1, "legit", T0 + W, T0 + W)])
    out, _ = D.join_labels(windows, labels)
    assert sorted((out["window_start_us"] - T0) // W) == [1]


def test_join_drops_ambiguous_and_unlabelled_and_scopes_by_tenant():
    windows = make_windows([("t1", P1, k) for k in range(4)] + [("t1", P2, 0), ("t2", P1, 0)])
    labels = D.parse_label_records(
        [
            label("t1", P1, "cheat", T0, T0 + 4 * W - 1, category="HEURISTICS", created_at=T0),
            label("t1", P1, "legit", T0 + 3 * W, T0 + 4 * W - 1, source="overturned"),  # conflicts with window 3
        ]
    )
    out, stats = D.join_labels(windows, labels)
    assert stats["windows_ambiguous"] == 1
    assert set(out["player"]) == {P1}
    assert set(out["tenant_id"]) == {"t1"}  # the t2 window of the same UUID is not labelled by t1's label
    assert sorted((out["window_start_us"] - T0) // W) == [0, 1, 2]
    assert set(out["y"]) == {1}
    assert set(out["category"]) == {"HEURISTICS"}


def test_category_from_most_recent_cheat_label():
    windows = make_windows([("t1", P1, 0)])
    labels = D.parse_label_records(
        [
            label("t1", P1, "cheat", T0, T0 + W, category="MOVEMENT", created_at=T0 + 10),
            label("t1", P1, "cheat", T0, T0 + W, category="HEURISTICS", created_at=T0 + 20),
        ]
    )
    out, _ = D.join_labels(windows, labels)
    assert out.loc[0, "category"] == "HEURISTICS"


# --- exclusions ---------------------------------------------------------------------------


def test_build_dataset_exclusions(tmp_path):
    windows = make_windows(
        [
            ("t1", P1, 0),
            ("t1", P1, 1, 47),  # 1.8 client: dropped (non-tick)
            ("t1", P1, 2, 770, 2),  # other feature version: dropped
            ("t1", P2, 0),  # erased (tenant, player)
            ("bad", P3, 0),  # untrusted tenant
            ("t1", P3, 0),  # erased everywhere (bare uuid)
        ]
    )
    labels = D.parse_label_records(
        [label("t1", P1, "cheat", T0, T0 + 10 * W), label("t1", P2, "legit", T0, T0 + W),
         label("bad", P3, "cheat", T0, T0 + W), label("t1", P3, "legit", T0, T0 + W)]
    )
    allow = tmp_path / "allow.txt"
    allow.write_text("# trusted tenants\nt1\n")
    erased = tmp_path / "erased.txt"
    erased.write_text(f"t1,{P2}\n{P3}  # erased in every tenant\n")
    ds = D.build_dataset(windows, labels, D.load_tenant_allowlist(allow), D.load_erasures(erased))
    assert list(ds.frame["player"]) == [P1]
    assert ds.stats["windows_non_tick_client"] == 1
    assert ds.stats["windows_wrong_version"] == 1
    assert ds.stats["windows_untrusted_or_erased"] == 3
    with pytest.raises(ValueError):
        D.build_dataset(windows, labels, set())
    empty = tmp_path / "empty.txt"
    empty.write_text("# nothing\n")
    with pytest.raises(ValueError):
        D.load_tenant_allowlist(empty)


LOBBY = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"


def test_server_filter_runs_before_the_join():
    windows = make_windows([("t1", P1, k) for k in range(4)] + [("t1", P2, 0)])
    windows.loc[windows["window_start_us"] < T0 + 2 * W, "server_id"] = LOBBY  # P1's windows 0-1, P2's 0
    labels = D.parse_label_records([label("t1", P1, "cheat", T0, T0 + 4 * W), label("t1", P2, "legit", T0, T0 + W)])
    ds = D.build_dataset(windows, labels, {"t1"}, servers=D.ServerFilter(exclude=D.parse_server_ids([LOBBY.upper()])))
    # The cheat label covers the lobby visit too, but those windows never reach the join.
    assert sorted((ds.frame["window_start_us"] - T0) // W) == [2, 3]
    assert set(ds.frame["player"]) == {P1}
    assert ds.stats["windows_server_filtered"] == 3
    only = D.build_dataset(windows, labels, {"t1"}, servers=D.ServerFilter(include=frozenset({LOBBY})))
    assert set(only.frame["server_id"]) == {LOBBY} and len(only) == 3
    assert "windows_server_filtered" not in D.build_dataset(windows, labels, {"t1"}, servers=D.ServerFilter()).stats
    assert D.parse_server_ids([f"{LOBBY}, {P1.upper()}", "", P1]) == {LOBBY, P1}
    with pytest.raises(ValueError, match="server ids are UUIDs"):
        D.parse_server_ids(["lobby"])


# --- split ----------------------------------------------------------------------------------


def test_group_split_has_no_leakage(synth_dataset):
    fr = synth_dataset.frame
    mask = D.player_split(fr["player"], fr["y"], 0.25, seed=5)
    test_players, train_players = set(fr["player"][mask]), set(fr["player"][~mask])
    assert test_players and train_players
    assert not test_players & train_players
    # every window of a test player is in the test split (no player is split across sides)
    assert fr[fr["player"].isin(test_players)].index.equals(fr[mask].index)
    # stratified: both classes on both sides
    assert set(fr["y"][mask]) == {0, 1} and set(fr["y"][~mask]) == {0, 1}
    # deterministic
    assert np.array_equal(mask, D.player_split(fr["player"], fr["y"], 0.25, seed=5))
    # roughly the requested share of players
    assert 0.18 < len(test_players) / fr["player"].nunique() < 0.32


def test_same_uuid_in_two_tenants_is_one_group():
    players = pd.Series([P1, P1, P2, P2, P3, P3])
    y = pd.Series([1, 1, 0, 0, 1, 0])
    for seed in range(20):
        mask = D.player_split(players, y, 0.5, seed)
        for p in (P1, P2, P3):
            assert len(set(mask[players.to_numpy() == p])) == 1


def test_calibration_folds_are_grouped(synth_dataset):
    ds = synth_dataset
    for tr, te in group_folds(ds.y, ds.groups, 3, seed=1):
        assert not set(ds.groups[tr]) & set(ds.groups[te])


# --- files ----------------------------------------------------------------------------------


def test_window_files_roundtrip_and_dedupe(tmp_path):
    w = make_windows([("t1", P1, 0), ("t1", P1, 1)])
    w["worker_unix_us"] = [1, 1]
    dup = w.iloc[[0]].copy()
    dup["worker_unix_us"] = 2
    dup[F.FEATURE_NAMES[0]] = 12.0
    both = pd.concat([w, dup], ignore_index=True)
    pq = tmp_path / "w.parquet"
    both.to_parquet(pq, index=False)
    back = D.load_windows_file(pq)
    assert len(back) == 2
    assert back.loc[back["window_start_us"] == T0, F.FEATURE_NAMES[0]].item() == 12.0  # newest write wins
    csv = tmp_path / "w.csv"
    w.to_csv(csv, index=False)
    assert len(D.load_windows_file(csv)) == 2
    # ClickHouse layout: one Array(Float32) column
    arr = w[list(F.META_COLUMNS)].copy()
    arr["features"] = list(w[list(F.FEATURE_NAMES)].to_numpy())
    arr.to_parquet(tmp_path / "a.parquet", index=False)
    back = D.load_windows_file(tmp_path / "a.parquet")
    assert np.allclose(back[list(F.FEATURE_NAMES)].to_numpy(), w[list(F.FEATURE_NAMES)].to_numpy())
    arr["features"] = [np.zeros(3)] * len(arr)
    arr.to_parquet(tmp_path / "bad.parquet", index=False)
    with pytest.raises(ValueError):
        D.load_windows_file(tmp_path / "bad.parquet")


def test_window_file_filters_pairs_and_time(tmp_path):
    w = make_windows([("t1", P1, k) for k in range(4)] + [("t1", P2, 0), ("t2", P1, 1), ("synthetic-x", P3, 1)])
    w.to_csv(tmp_path / "w.csv", index=False)
    out = D.load_windows_file(tmp_path / "w.csv", pairs={("t1", P1), ("t2", P1)}, since_us=T0 + W, until_us=T0 + 3 * W)
    assert sorted(zip(out["tenant_id"], (out["window_start_us"] - T0) // W)) == [
        ("synthetic-x", 1), ("t1", 1), ("t1", 2), ("t2", 1)
    ]
    assert len(D.load_windows_file(tmp_path / "w.csv")) == len(w)


def test_clickhouse_query_and_loader():
    sql, params = D.clickhouse_query(T0, T0 + W, tenants=["t2", "t1"])
    assert "FROM feature_windows FINAL" in sql
    assert "{fv:UInt16}" in sql and params["fv"] == F.FEATURE_VERSION
    assert params["tenants"] == ["t1", "t2"] and params["since"] == T0

    class Result:
        column_names = ["tenant_id", "player", "server_id", "session_id", "window_start_us", "window_end_us",
                        "client_protocol", "tick_client", "hot_loaded", "feature_version", "features", "worker_unix_us"]
        result_rows = [("t1", P1, P2, P3, T0, T0 + W, 770, 1, 0, 1, list(F.DEFAULTS), 5)]

    class FakeClient:
        def query(self, sql, parameters):
            self.sql, self.parameters = sql, parameters
            return Result()

    client = FakeClient()
    df = D.load_windows_clickhouse(client=client, since_us=T0)
    assert len(df) == 1 and df.loc[0, "player"] == P1
    assert client.parameters["since"] == T0
    assert np.allclose(df[list(F.FEATURE_NAMES)].to_numpy()[0], F.DEFAULTS)


# --- label export API -------------------------------------------------------------------------


def test_fetch_labels_api_bearer_and_pagination():
    seen = []
    pages = {
        None: [json.dumps(label("t1", P1, "cheat", T0, T0 + W)), json.dumps({"next_cursor": "c2"})],
        "c2": [json.dumps(label("t1", P2, "legit", T0, T0 + W))],
    }

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):  # noqa: N802
            q = parse_qs(urlsplit(self.path).query)
            seen.append((urlsplit(self.path).path, self.headers.get("Authorization"), q))
            body = "\n".join(pages[q.get("cursor", [None])[0]]).encode() + b"\n"
            self.send_response(200)
            self.send_header("Content-Type", "application/x-ndjson")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    srv = HTTPServer(("127.0.0.1", 0), Handler)
    th = threading.Thread(target=srv.serve_forever, daemon=True)
    th.start()
    try:
        url = f"http://127.0.0.1:{srv.server_address[1]}"
        recs = D.fetch_labels_api(url, "pe_test_secret", since="2026-01-01T00:00:00Z", timeout=10)
    finally:
        srv.shutdown()
    assert [r["player_uuid"] for r in recs] == [P1, P2]
    assert all(path == "/api/v1/export/labels" for path, _, _ in seen)
    assert all(auth == "Bearer pe_test_secret" for _, auth, _ in seen)
    assert seen[0][2]["since"] == ["2026-01-01T00:00:00Z"] and seen[1][2]["cursor"] == ["c2"]


def test_fetch_labels_api_refuses_plain_http_and_missing_key():
    with pytest.raises(ValueError):
        D.fetch_labels_api("http://api.example.invalid", "pe_x_y")
    with pytest.raises(ValueError):
        D.fetch_labels_api("https://api.example.invalid", "")
