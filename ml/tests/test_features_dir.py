"""PE_FEATURES_DIR: hourly window files written by the cloud, the erasure journal, export --labels-only.

Every fixture here is SYNTHETIC: hand-made default vectors (as in test_dataset) or synth.generate output,
written in the cloud's file format (FEATURES.md section 6) into directories named SYNTHETIC_*.
"""

import gzip
import json
import os
import threading
from contextlib import contextmanager
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, HTTPServer

import numpy as np
import pandas as pd
import pytest

from pandaeye_ml import cli, synth
from pandaeye_ml import dataset as D
from pandaeye_ml import features as F

from .test_dataset import P1, P2, P3, T0, W, label, make_windows

HEADER = [*F.META_COLUMNS, "worker_unix_us", *F.FEATURE_NAMES]


def csv_bytes(frame: pd.DataFrame) -> bytes:
    """A window file as the cloud writes it: header, then one row per window, floats as float32."""
    lines = [",".join(HEADER)]
    for row in frame.itertuples(index=False):
        r = row._asdict()
        meta = [str(r[c]) for c in (*F.META_COLUMNS, "worker_unix_us")]
        lines.append(",".join(meta + [str(np.float32(r[n])) for n in F.FEATURE_NAMES]))
    return ("\n".join(lines) + "\n").encode()


def hour_name(us: int) -> str:
    return "windows-" + datetime.fromtimestamp(us // 1_000_000, tz=timezone.utc).strftime("%Y%m%dT%HZ") + ".csv"


def write_hour(directory, frame: pd.DataFrame, *, current: bool = False, tail: bytes = b""):
    """Write one hour: a finished .csv.gz, or the current .csv (optionally with an unfinished tail)."""
    name = hour_name(int(frame["window_start_us"].max()))
    data = csv_bytes(frame)
    if current:
        path = directory / name
        path.write_bytes(data + tail)
    else:
        path = directory / (name + ".gz")
        path.write_bytes(gzip.compress(data))
    return path


def windows(rows, worker=1):
    w = make_windows(rows)
    w["worker_unix_us"] = w["window_end_us"] + worker
    return w


@pytest.fixture
def features_dir(tmp_path):
    d = tmp_path / "SYNTHETIC_features"
    d.mkdir()
    (d / "README_SYNTHETIC.txt").write_text(synth.SYNTHETIC_NOTICE + "\n")
    return d


def one_csv(directory, frames):
    path = directory / "SYNTHETIC_one.csv"
    path.write_bytes(csv_bytes(pd.concat(frames, ignore_index=True)))
    return path


def journal_line(tenant, player, at):
    return json.dumps({"tenant_id": tenant, "player_uuid": player, "erased_at_us": at}) + "\n"


# --- loading --------------------------------------------------------------------------------


def test_dir_reads_every_hour_and_dedupes_like_a_file(features_dir):
    hour = 3_600_000_000 // W  # windows per hour
    past = windows([("t1", P1, k) for k in range(3)] + [("t1", P2, 0)])
    current = windows([("t1", P1, hour + k) for k in range(2)])
    write_hour(features_dir, past)
    # A window re-emitted after a handover lands in a later file: the newest worker write wins.
    again = windows([("t1", P2, 0)], worker=99)
    again[F.FEATURE_NAMES[0]] = 12.0
    write_hour(features_dir, pd.concat([current, again], ignore_index=True), current=True)
    (features_dir / "windows-20260105T02Z.csv.gz.tmp").write_bytes(b"not a window file")
    out = D.load_windows_file(features_dir)
    single = D.load_windows_file(one_csv(features_dir.parent, [past, current, again]))
    pd.testing.assert_frame_equal(out, single)
    assert len(out) == 6
    assert out.loc[out["player"] == P2, F.FEATURE_NAMES[0]].item() == 12.0


def test_dir_ignores_an_unfinished_last_line_of_the_current_hour(features_dir):
    w = windows([("t1", P1, k) for k in range(3)])
    full = csv_bytes(w)
    cut = full.rstrip(b"\n").rfind(b"\n") + 1  # start of the third row
    path = features_dir / hour_name(T0)
    # The writer is mid-append: the third row is cut inside its feature values.
    path.write_bytes(full[: cut + (len(full) - cut) // 2])
    out = D.load_windows_file(features_dir)
    assert sorted((out["window_start_us"] - T0) // W) == [0, 1]
    assert np.allclose(out[list(F.FEATURE_NAMES)].to_numpy(), F.DEFAULTS)  # no NaN-filled half row
    path.write_bytes(full)  # terminated: every row counts
    assert len(D.load_windows_file(features_dir)) == 3
    path.write_bytes(full[:20])  # a new hour whose header is not complete yet
    write_hour(features_dir, windows([("t1", P2, 0)]))
    assert list(D.load_windows_file(features_dir)["player"]) == [P2]
    path.write_bytes(full[: full.find(b"\n") + 1])  # header only
    assert list(D.load_windows_file(features_dir)["player"]) == [P2]
    path.write_bytes(b"")
    assert list(D.load_windows_file(features_dir)["player"]) == [P2]


def test_dir_refuses_a_broken_finished_hour_and_an_empty_dir(features_dir):
    with pytest.raises(ValueError, match="no windows"):
        D.load_windows_file(features_dir)
    gz = gzip.compress(csv_bytes(windows([("t1", P1, 0)])))
    (features_dir / "windows-20260105T00Z.csv.gz").write_bytes(gz[: len(gz) // 2])
    with pytest.raises(ValueError, match="cannot read window file"):
        D.load_windows_file(features_dir)


def test_dir_filters_each_file_before_concatenating(features_dir, monkeypatch):
    rows = [("t1", P1, k) for k in range(4)] + [("t1", P2, k) for k in range(4)] + [("t2", P1, 0)]
    syn = windows([("synthetic-t", P3, 1)])
    write_hour(features_dir, windows(rows))
    write_hour(features_dir, syn, current=True)
    concatenated = []
    dedupe = D.dedupe_windows
    monkeypatch.setattr(D, "dedupe_windows", lambda df: concatenated.append(len(df)) or dedupe(df))
    out = D.load_windows_file(features_dir, pairs={("t1", P1)}, since_us=T0 + W, until_us=T0 + 3 * W)
    # Only P1 of t1 (not P2, not t1's UUID in t2) in [window 1, window 3); the synthetic row is kept
    # through the pair filter so the dataset is still flagged synthetic.
    kept = sorted(zip(out["tenant_id"], (out["window_start_us"] - T0) // W))
    assert kept == [("synthetic-t", 1), ("t1", 1), ("t1", 2)]
    assert concatenated == [3]
    assert D.build_dataset(out, D.parse_label_records([]), {"t1"}).synthetic
    assert len(D.load_windows_file(features_dir, pairs=set(), since_us=T0 + 2 * W)) == 0


def test_dir_follows_files_the_cloud_compresses_or_deletes_while_loading(features_dir, monkeypatch):
    hour = 3_600_000_000 // W
    oldest = write_hour(features_dir, windows([("t1", P2, 0)]))
    current = windows([("t1", P1, hour + k) for k in range(2)])
    csv = write_hour(features_dir, current, current=True)
    read = D._read_window_csv

    def racing(path):
        if path == oldest:
            oldest.unlink()  # retention deleted the oldest hour after the listing
        elif path == csv:
            write_hour(features_dir, current)  # the hour ended: compressed to its .csv.gz, .csv removed
            csv.unlink()
        return read(path)

    monkeypatch.setattr(D, "_read_window_csv", racing)
    out = D.load_windows_file(features_dir)
    assert sorted((out["window_start_us"] - T0) // W) == [hour, hour + 1]
    assert set(out["player"]) == {P1}


def test_dir_applies_an_erasure_journaled_while_loading(features_dir, monkeypatch):
    hour = 3_600_000_000 // W
    write_hour(features_dir, windows([("t1", P1, 0), ("t1", P2, 0)]))
    write_hour(features_dir, windows([("t1", P1, hour)]), current=True)
    journal = features_dir / D.ERASURE_JOURNAL
    read = D._read_window_csv

    def erase_after_the_first_file(path):
        frame = read(path)
        if not journal.exists():
            journal.write_text(journal_line("t1", P1, T0 + 2 * hour * W))
        return frame

    monkeypatch.setattr(D, "_read_window_csv", erase_after_the_first_file)
    out = D.load_windows_file(features_dir)
    assert list(out["player"]) == [P2]  # P1's first hour was read before the erasure, and is dropped too


def test_dir_honours_the_erasure_journal(features_dir):
    w = windows([("t1", P1, k) for k in range(6)] + [("t2", P1, 0), ("t1", P2, 0)])
    # Clock skew: window 5 starts after the erasure but was written before it, so it is erased too.
    w.loc[(w["player"] == P1) & (w["window_start_us"] == T0 + 5 * W), "worker_unix_us"] = T0 + 2 * W
    write_hour(features_dir, w)
    (features_dir / D.ERASURE_JOURNAL).write_text(
        journal_line("t1", P1, T0 + W) + "\n" + journal_line("t1", P1.upper(), T0 + 3 * W)  # the latest wins
    )
    out = D.load_windows_file(features_dir)
    kept = sorted(zip(out["tenant_id"], out["player"], (out["window_start_us"] - T0) // W))
    # t1/P1 windows 0-2 predate the erasure; 3 and 4 are new data; t2's P1 and t1's P2 are other subjects.
    assert kept == [("t1", P1, 3), ("t1", P1, 4), ("t1", P2, 0), ("t2", P1, 0)]


def test_journal_cutoff_is_the_clouds(features_dir):
    at = T0 + 3 * W
    w = windows([("t1", P1, k) for k in (2, 3, 4)])
    # As the sink and the worker: erased when the window started before erased_at_us or was written at
    # or before it. Window 3 starts at the erasure and was written after it; window 4 was written at it.
    w.loc[w["window_start_us"] == T0 + 4 * W, "worker_unix_us"] = at
    write_hour(features_dir, w)
    (features_dir / D.ERASURE_JOURNAL).write_text(journal_line("t1", P1, at))
    assert list((D.load_windows_file(features_dir)["window_start_us"] - T0) // W) == [3]


@pytest.mark.parametrize(
    "content",
    [
        journal_line("t1", P1, T0)[:-12],  # the cloud is still appending
        json.dumps({"tenant_id": "t1", "player_uuid": P1}) + "\n",
        json.dumps({"tenant_id": "t1", "player_uuid": P1, "erased_at_us": True}) + "\n",
        json.dumps({"tenant_id": "t1", "player_uuid": P1, "erased_at_us": "2026-01-05T00:00:00Z"}) + "\n",
        json.dumps({"tenant_id": "t1", "player_uuid": "nope", "erased_at_us": T0}) + "\n",
        json.dumps({"tenant_id": None, "player_uuid": P1, "erased_at_us": T0}) + "\n",
    ],
)
def test_a_bad_journal_fails_closed(features_dir, content):
    write_hour(features_dir, windows([("t1", P1, 0)]))
    (features_dir / D.ERASURE_JOURNAL).write_text(journal_line("t1", P2, T0) + content)
    with pytest.raises(ValueError, match="bad erasure record"):
        D.load_windows_file(features_dir)


def test_journal_exports_as_an_erased_list(tmp_path):
    journal = tmp_path / D.ERASURE_JOURNAL
    journal.write_text(journal_line("t1", P1, T0) + journal_line("t2", P2.upper(), T0) + journal_line("t1", P1, T0 + 5))
    path = D.write_erased_list(D.load_erasure_journal(journal), tmp_path / "erased.txt")
    assert os.stat(path).st_mode & 0o777 == 0o600
    assert D.load_erasures(path) == D.ErasureList(frozenset({("t1", P1), ("t2", P2)}))
    with pytest.raises(ValueError):
        D.write_erased_list(D.ErasureJournal({("a,b", P1): T0}), tmp_path / "bad.txt")


# --- CLI ------------------------------------------------------------------------------------


@contextmanager
def label_api(records):
    seen = []

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):  # noqa: N802
            seen.append(self.path)
            body = "".join(json.dumps(r) + "\n" for r in records).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/x-ndjson")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    srv = HTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    try:
        yield f"http://127.0.0.1:{srv.server_address[1]}", seen
    finally:
        srv.shutdown()
        srv.server_close()


def test_export_labels_only_needs_no_clickhouse(features_dir, tmp_path, monkeypatch, capsys):
    monkeypatch.delenv("PE_CLICKHOUSE_DSN", raising=False)
    monkeypatch.setenv("PE_API_KEY", "pe_test_secret")
    (features_dir / D.ERASURE_JOURNAL).write_text(journal_line("t1", P2, T0))
    out = tmp_path / "SYNTHETIC_export"
    with label_api([label("t1", P1, "cheat", T0, T0 + W, synthetic=True)]) as (url, seen):
        with pytest.raises(SystemExit, match="PE_CLICKHOUSE_DSN"):
            cli.main(["export", "--out", str(out), "--api-url", url])
        assert not seen  # the full export still needs ClickHouse, and fails before calling the API
        assert cli.main(["export", "--out", str(out), "--api-url", url, "--labels-only",
                         "--erasures-from", str(features_dir)]) == 0
    printed = json.loads(capsys.readouterr().out.strip().splitlines()[-1])
    assert printed["label_rows"] == 1 and printed["erased_rows"] == 1 and "windows" not in printed
    assert [r["player_uuid"] for r in D.read_ndjson(out / "labels.ndjson")] == [P1]
    assert D.load_erasures(out / "erased.txt").pairs == {("t1", P2)}
    assert not (out / "feature_windows.parquet").exists()
    (features_dir / D.ERASURE_JOURNAL).unlink()  # no erasure yet: an empty list
    with label_api([]) as (url, _):
        assert cli.main(["export", "--out", str(out), "--api-url", url, "--labels-only",
                         "--erasures-from", str(features_dir)]) == 0
    assert len(D.load_erasures(out / "erased.txt")) == 0


def test_train_inputs_from_a_dir_match_the_same_windows_in_one_file(tmp_path):
    windows_, records, trusted, _ = synth.generate(synth.SynthConfig(players=80, windows_per_session=(3, 6), seed=7))
    windows_ = windows_.drop(columns=["synthetic"])  # the cloud's files have no such column
    d = tmp_path / "SYNTHETIC_features"
    d.mkdir()
    hours = windows_["window_start_us"] // 3_600_000_000
    last = hours.max()
    for h, frame in windows_.groupby(hours):
        write_hour(d, frame, current=h == last, tail=b"synthetic-tenant-a,0000" if h == last else b"")
    D.save_windows(windows_, tmp_path / "SYNTHETIC_windows.parquet")
    D.write_ndjson(records, tmp_path / "SYNTHETIC_labels.ndjson")
    (tmp_path / "SYNTHETIC_trusted.txt").write_text("\n".join(trusted) + "\n")

    def inputs(source, *extra):
        args = cli.build_parser().parse_args([
            "train", "--windows", str(source), "--labels", str(tmp_path / "SYNTHETIC_labels.ndjson"),
            "--trusted-tenants", str(tmp_path / "SYNTHETIC_trusted.txt"), "--out", str(tmp_path / "m"), *extra,
        ])
        return cli._load_inputs(args)

    from_dir, from_file = inputs(d), inputs(tmp_path / "SYNTHETIC_windows.parquet")
    pd.testing.assert_frame_equal(from_dir.frame, from_file.frame)
    assert from_dir.synthetic and from_dir.stats["positives"] > 0 and from_dir.stats["negatives"] > 0

    lobby = from_dir.frame["server_id"].iloc[0]
    filtered = inputs(d, "--exclude-servers", lobby.upper())
    assert lobby not in set(filtered.frame["server_id"])
    assert filtered.stats["windows_server_filtered"] == int((windows_["server_id"] == lobby).sum())
    only = inputs(d, "--include-servers", f"{lobby}, {P1}")
    assert set(only.frame["server_id"]) == {lobby}
    since = int(from_dir.frame["window_start_us"].median())
    late = inputs(d, "--since", str(since))
    assert (late.frame["window_start_us"] >= since).all() and 0 < len(late) < len(from_dir)
    with pytest.raises(SystemExit, match="server ids are UUIDs"):
        inputs(d, "--exclude-servers", "lobby-1")


def test_train_loads_only_labelled_pairs(features_dir, tmp_path, monkeypatch):
    write_hour(features_dir, windows([("t1", P1, k) for k in range(3)] + [("t1", P2, 0)]))
    D.write_ndjson([label("t1", P1, "cheat", T0, T0 + 3 * W), label("t2", P3, "legit", T0, T0 + W)],
                   tmp_path / "SYNTHETIC_labels.ndjson")
    (tmp_path / "SYNTHETIC_trusted.txt").write_text("t1\n")
    calls = []
    load = D.load_windows_file
    monkeypatch.setattr(D, "load_windows_file", lambda path, **kw: calls.append(kw) or load(path, **kw))
    ds = cli._load_inputs(cli.build_parser().parse_args([
        "train", "--windows", str(features_dir), "--labels", str(tmp_path / "SYNTHETIC_labels.ndjson"),
        "--trusted-tenants", str(tmp_path / "SYNTHETIC_trusted.txt"), "--out", str(tmp_path / "m"),
        "--since", "2026-01-05T00:00:30Z", "--until", str(T0 + 2 * W),
    ]))
    assert calls == [{"pairs": {("t1", P1), ("t2", P3)}, "since_us": T0 + W, "until_us": T0 + 2 * W}]
    assert ds.stats["windows_in"] == 1 and list(ds.frame["player"]) == [P1]
