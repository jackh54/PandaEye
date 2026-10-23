"""Dataset assembly: feature windows + labels -> labelled, filtered, group-split training data.

Sources
- Feature windows: ClickHouse ``feature_windows`` (clickhouse-connect), a parquet/CSV export (wide: one
  column per feature; or a ``features`` array column as stored in ClickHouse), or a ``PE_FEATURES_DIR``
  directory of hourly CSV files written by the cloud (``windows-YYYYMMDDTHHZ.csv[.gz]`` plus an
  ``erasures.jsonl`` journal, FEATURES.md section 6).
- Labels: the API NDJSON export ``GET /api/v1/export/labels`` (bearer API key) or an NDJSON file.

Rules (FEATURES.md section 7): erased subjects and untrusted tenants are excluded, windows of filtered
servers (e.g. lobbies), non-tick clients and other feature versions are dropped, windows are labelled by
overlap with a label interval of the same (tenant, player), ambiguous windows are dropped, unlabelled
windows are excluded, and splits are by player UUID so no player is on both sides.
"""

from __future__ import annotations

import hashlib
import io
import ipaddress
import json
import logging
import math
import os
import urllib.parse
import urllib.request
import uuid as uuidlib
import zlib
from collections.abc import Collection, Iterable, Iterator
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import numpy as np
import pandas as pd

from . import features as F

log = logging.getLogger(__name__)

CATEGORY_NAMES = {
    1: "AUTO_CLICKER",
    2: "HEURISTICS",
    3: "INVALID_PROTOCOL",
    4: "MOVEMENT",
    5: "TICK_SPEED",
    6: "INVENTORY",
    7: "WORLD_INTERACTION",
    8: "LATENCY_ABUSE",
}
UNKNOWN_CATEGORY = "UNSPECIFIED"
SYNTHETIC_TENANT_PREFIX = "synthetic-"
# PE_FEATURES_DIR layout (FEATURES.md section 6): hourly window files and the erasure journal.
WINDOW_FILE_GLOBS = ("windows-*.csv", "windows-*.csv.gz")
ERASURE_JOURNAL = "erasures.jsonl"

KEY_COLUMNS = ("tenant_id", "player", "session_id", "window_start_us")
LABEL_COLUMNS = (
    "tenant_id",
    "player",
    "y",
    "label",
    "source",
    "start_us",
    "end_us",
    "category",
    "created_us",
    "verdict_id",
    "synthetic",
    "archetype",
)


# ---------------------------------------------------------------------------------------------
# Small helpers
# ---------------------------------------------------------------------------------------------


def normalize_uuid(value: Any) -> str:
    """Canonical lowercase 8-4-4-4-12 string for a UUID given as str, bytes(16) or uuid.UUID."""
    if isinstance(value, uuidlib.UUID):
        return str(value)
    if isinstance(value, (bytes, bytearray, memoryview)):
        b = bytes(value)
        if len(b) != 16:
            raise ValueError(f"UUID bytes must be 16 long, got {len(b)}")
        return str(uuidlib.UUID(bytes=b))
    if isinstance(value, str):
        return str(uuidlib.UUID(value.strip()))
    raise ValueError(f"not a UUID: {value!r}")


def to_unix_us(value: Any) -> int:
    """Unix microseconds from an integer (s, ms or us by magnitude) or an RFC 3339 / ISO 8601 string."""
    if value is None:
        raise ValueError("missing timestamp")
    if isinstance(value, bool):
        raise ValueError("boolean is not a timestamp")
    if isinstance(value, (int, np.integer)):
        v = int(value)
    elif isinstance(value, (float, np.floating)):
        if not math.isfinite(value):
            raise ValueError("non-finite timestamp")
        v = int(round(float(value)))
    elif isinstance(value, str):
        s = value.strip()
        if s.lstrip("-").isdigit():
            return to_unix_us(int(s))
        if s.endswith(("Z", "z")):
            s = s[:-1] + "+00:00"
        dt = datetime.fromisoformat(s)
        if dt.tzinfo is None:
            dt = dt.replace(tzinfo=timezone.utc)
        delta = dt - datetime(1970, 1, 1, tzinfo=timezone.utc)
        return (delta.days * 86_400 + delta.seconds) * 1_000_000 + delta.microseconds
    else:
        raise ValueError(f"unsupported timestamp: {value!r}")
    a = abs(v)
    if a >= 10**14:  # microseconds (>= 1973 in us)
        return v
    if a >= 10**11:  # milliseconds
        return v * 1_000
    return v * 1_000_000


def normalize_category(value: Any) -> str:
    if value is None or (isinstance(value, float) and math.isnan(value)):
        return UNKNOWN_CATEGORY
    if isinstance(value, (int, np.integer)) and not isinstance(value, bool):
        return CATEGORY_NAMES.get(int(value), UNKNOWN_CATEGORY)
    s = str(value).strip().upper()
    if s.isdigit():
        return CATEGORY_NAMES.get(int(s), UNKNOWN_CATEGORY)
    if s.startswith("CATEGORY_"):
        s = s[len("CATEGORY_") :]
    return s if s in CATEGORY_NAMES.values() else UNKNOWN_CATEGORY


def _write_private(path: Path, writer) -> None:
    """Write a file that contains personal data (player UUIDs) with mode 0600."""
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    writer(path)
    try:
        os.chmod(path, 0o600)
    except OSError:  # pragma: no cover - non-POSIX filesystems
        pass


# ---------------------------------------------------------------------------------------------
# Feature windows
# ---------------------------------------------------------------------------------------------


def _expand_feature_array(df: pd.DataFrame) -> pd.DataFrame:
    arrays = df["features"].tolist()
    lengths = {len(a) for a in arrays}
    if lengths and lengths != {F.N_FEATURES}:
        raise ValueError(f"features arrays must have length {F.N_FEATURES}, found lengths {sorted(lengths)}")
    mat = np.asarray(arrays, dtype=np.float64).reshape(len(df), F.N_FEATURES)
    wide = pd.DataFrame(mat, columns=list(F.FEATURE_NAMES), index=df.index)
    return pd.concat([df.drop(columns=["features"]), wide], axis=1)


def normalize_windows(df: pd.DataFrame) -> pd.DataFrame:
    """Validate and normalise a window frame (wide or array form) into the canonical wide form."""
    df = df.copy()
    if "features" in df.columns and not all(n in df.columns for n in F.FEATURE_NAMES):
        df = _expand_feature_array(df)
    missing_meta = [c for c in ("tenant_id", "player", "window_start_us") if c not in df.columns]
    if missing_meta:
        raise ValueError(f"window data lacks required columns: {missing_meta}")
    missing_feat = [n for n in F.FEATURE_NAMES if n not in df.columns]
    if missing_feat:
        raise ValueError(f"window data lacks feature columns: {missing_feat[:5]}")
    df["tenant_id"] = df["tenant_id"].astype(str)
    df["player"] = [normalize_uuid(p) for p in df["player"]]
    for col in ("server_id", "session_id"):
        if col in df.columns:
            df[col] = [normalize_uuid(v) if v not in (None, "") and not pd.isna(v) else "" for v in df[col]]
        else:
            df[col] = ""
    df["window_start_us"] = df["window_start_us"].astype(np.int64)
    if "window_end_us" not in df.columns:
        df["window_end_us"] = df["window_start_us"] + F.WINDOW_US
    df["window_end_us"] = df["window_end_us"].astype(np.int64)
    if "client_protocol" not in df.columns:
        df["client_protocol"] = -1
    df["client_protocol"] = df["client_protocol"].astype(np.int64)
    if "tick_client" not in df.columns:
        df["tick_client"] = (df["client_protocol"] >= F.MIN_TICK_CLIENT_PROTOCOL).astype(np.int64)
    if "hot_loaded" not in df.columns:
        df["hot_loaded"] = 0
    if "feature_version" not in df.columns:
        raise ValueError("window data lacks feature_version")
    df["feature_version"] = df["feature_version"].astype(np.int64)
    for c in ("tick_client", "hot_loaded"):
        df[c] = df[c].astype(np.int64)
    feats = F.sanitize(df.loc[:, list(F.FEATURE_NAMES)].to_numpy(dtype=np.float64))
    rest = df.drop(columns=list(F.FEATURE_NAMES))
    wide = pd.DataFrame(feats.astype(np.float64), columns=list(F.FEATURE_NAMES), index=df.index)
    return pd.concat([rest, wide], axis=1).reset_index(drop=True)


def dedupe_windows(df: pd.DataFrame) -> pd.DataFrame:
    """Keep one row per (tenant, player, session, window_start), preferring the newest worker write."""
    if "worker_unix_us" in df.columns:
        df = df.sort_values("worker_unix_us", kind="stable")
    out = df.drop_duplicates(subset=list(KEY_COLUMNS), keep="last")
    dropped = len(df) - len(out)
    if dropped:
        log.info("dropped %d duplicate windows", dropped)
    return out.sort_values(["tenant_id", "player", "window_start_us"], kind="stable").reset_index(drop=True)


def _synthetic_rows(df: pd.DataFrame) -> pd.Series:
    """Windows marked synthetic: a ``synthetic-`` tenant or a truthy ``synthetic`` column."""
    mask = df["tenant_id"].astype(str).str.startswith(SYNTHETIC_TENANT_PREFIX)
    if "synthetic" in df.columns:
        mask = mask | pd.to_numeric(df["synthetic"], errors="coerce").fillna(0).astype(bool)
    return mask


def _filter_windows(
    df: pd.DataFrame,
    pairs: Collection[tuple[str, str]] | None,
    since_us: int | None,
    until_us: int | None,
) -> pd.DataFrame:
    keep = pd.Series(True, index=df.index)
    if since_us is not None:
        keep &= df["window_start_us"] >= since_us
    if until_us is not None:
        keep &= df["window_start_us"] < until_us
    if pairs is not None:
        # Synthetic rows survive the pair filter so build_dataset still sees them and taints the dataset.
        keys = pd.Series(list(zip(df["tenant_id"], df["player"])), index=df.index, dtype=object)
        keep &= keys.isin(pairs) | _synthetic_rows(df)
    return df[keep]


def _read_window_csv(path: Path) -> pd.DataFrame | None:
    """One file of a features directory, or None when not even its header is complete.

    A plain ``.csv`` is the hour being written: an unterminated last line is a row the writer has not
    finished, and is ignored. A ``.csv.gz`` is a finished hour and must be complete.
    """
    try:
        if path.name.endswith(".gz"):
            return pd.read_csv(path)
        data = path.read_bytes()
        end = data.rfind(b"\n") + 1
        if end == 0:
            return None
        if end < len(data):
            log.info("%s: ignoring an incomplete last line (%d bytes)", path, len(data) - end)
        return pd.read_csv(io.BytesIO(data[:end]))
    except FileNotFoundError:
        raise
    except (OSError, EOFError, zlib.error, pd.errors.ParserError, pd.errors.EmptyDataError) as exc:
        raise ValueError(f"cannot read window file {path}: {exc}") from exc


def load_windows_dir(
    directory: str | Path,
    *,
    pairs: Collection[tuple[str, str]] | None = None,
    since_us: int | None = None,
    until_us: int | None = None,
) -> pd.DataFrame:
    """Load a ``PE_FEATURES_DIR``: every ``windows-*.csv`` and ``windows-*.csv.gz`` in it.

    Each file is filtered (as in load_windows_file, plus the directory's ``erasures.jsonl``) before the
    files are concatenated, so memory follows the rows kept rather than the size of the directory. The
    result is de-duplicated like a single file.

    The cloud keeps working meanwhile: a file it removes after the listing is skipped, files it creates
    (the ``.csv.gz`` of an hour it compressed) are read in another pass, and an erasure it journals before
    the last file is read applies too.
    """
    d = Path(directory)
    files = _window_files(d)
    if not files:
        raise ValueError(f"no windows-*.csv or windows-*.csv.gz files in {d}")
    journal = _dir_journal(d)
    frames = []
    rows = erased = 0
    seen: set[Path] = set()
    while files:
        for path in files:
            seen.add(path)
            try:
                raw = _read_window_csv(path)
            except FileNotFoundError:  # compressed (its .csv.gz is listed next) or deleted (retention)
                log.info("%s: removed while loading", path)
                continue
            if raw is None:
                continue
            df = normalize_windows(raw)
            rows += len(df)
            df = _filter_windows(df, pairs, since_us, until_us)
            gone = journal.erased(df)
            n_gone = int(gone.sum())
            erased += n_gone
            if len(df) > n_gone:
                frames.append(df[~gone])
        files = [p for p in _window_files(d) if p not in seen]
    out = pd.concat(frames, ignore_index=True) if frames else None
    latest = _dir_journal(d)
    if out is not None and latest != journal:
        gone = latest.erased(out)
        erased += int(gone.sum())
        out = out[~gone]
    log.info("%s: %d files, %d rows, %d kept, %d erased (%s)", d, len(seen), rows,
             0 if out is None else len(out), erased, ERASURE_JOURNAL)
    if out is None or out.empty:
        return normalize_windows(pd.DataFrame(columns=[*F.META_COLUMNS, *F.FEATURE_NAMES]))
    return dedupe_windows(out)


def _window_files(d: Path) -> list[Path]:
    return sorted({p for g in WINDOW_FILE_GLOBS for p in d.glob(g) if p.is_file()})


def _dir_journal(d: Path) -> ErasureJournal:
    path = d / ERASURE_JOURNAL
    return load_erasure_journal(path) if path.exists() else ErasureJournal()  # no erasure yet


def load_windows_file(
    path: str | Path,
    *,
    pairs: Collection[tuple[str, str]] | None = None,
    since_us: int | None = None,
    until_us: int | None = None,
) -> pd.DataFrame:
    """Load windows from a .parquet/.csv/.csv.gz file or a features directory (load_windows_dir).

    Keeps rows with ``since_us <= window_start_us < until_us`` and, when ``pairs`` is given, only those
    of the labelled (tenant_id, player) pairs: the others could never be labelled.
    """
    path = Path(path)
    if path.is_dir():
        return load_windows_dir(path, pairs=pairs, since_us=since_us, until_us=until_us)
    name = path.name.lower()
    if name.endswith(".parquet"):
        df = pd.read_parquet(path)
    elif name.endswith((".csv", ".csv.gz")):
        df = pd.read_csv(path)
    else:
        raise ValueError(f"unsupported window file type: {path} (use .parquet, .csv or a features directory)")
    return dedupe_windows(_filter_windows(normalize_windows(df), pairs, since_us, until_us))


def save_windows(df: pd.DataFrame, path: str | Path) -> Path:
    path = Path(path)
    if path.name.lower().endswith(".parquet"):
        _write_private(path, lambda p: df.to_parquet(p, index=False))
    else:
        _write_private(path, lambda p: df.to_csv(p, index=False))
    return path


def clickhouse_query(
    since_us: int | None,
    until_us: int | None,
    tenants: Iterable[str] | None = None,
    feature_version: int = F.FEATURE_VERSION,
    table: str = "feature_windows",
) -> tuple[str, dict[str, Any]]:
    """SQL + server-side parameters (clickhouse-connect ``{name:Type}`` binding) for a window export."""
    if not table.replace("_", "").replace(".", "").isalnum():
        raise ValueError(f"bad table name {table!r}")
    where = ["feature_version = {fv:UInt16}"]
    params: dict[str, Any] = {"fv": int(feature_version)}
    if since_us is not None:
        where.append("window_start_us >= {since:Int64}")
        params["since"] = int(since_us)
    if until_us is not None:
        where.append("window_start_us < {until:Int64}")
        params["until"] = int(until_us)
    if tenants is not None:
        where.append("tenant_id IN {tenants:Array(String)}")
        params["tenants"] = sorted(set(tenants))
    sql = (
        "SELECT tenant_id, toString(player) AS player, toString(server_id) AS server_id, "
        "toString(session_id) AS session_id, window_start_us, window_end_us, client_protocol, "
        "tick_client, hot_loaded, feature_version, features, worker_unix_us "
        f"FROM {table} FINAL WHERE " + " AND ".join(where) + " ORDER BY tenant_id, player, window_start_us"
    )
    return sql, params


def load_windows_clickhouse(
    dsn: str | None = None,
    *,
    client: Any = None,
    since_us: int | None = None,
    until_us: int | None = None,
    tenants: Iterable[str] | None = None,
    feature_version: int = F.FEATURE_VERSION,
) -> pd.DataFrame:
    """Load windows from ClickHouse. Pass a DSN (``clickhouse://user:pass@host:8123/db``) or a client."""
    if client is None:
        if not dsn:
            raise ValueError("ClickHouse DSN required")
        import clickhouse_connect  # imported lazily: only the export path needs it

        client = clickhouse_connect.get_client(dsn=dsn)
    sql, params = clickhouse_query(since_us, until_us, tenants, feature_version)
    result = client.query(sql, parameters=params)
    df = pd.DataFrame(list(result.result_rows), columns=list(result.column_names))
    if df.empty:
        cols = list(F.META_COLUMNS) + list(F.FEATURE_NAMES)
        return pd.DataFrame(columns=cols)
    return dedupe_windows(normalize_windows(df))


# ---------------------------------------------------------------------------------------------
# Labels
# ---------------------------------------------------------------------------------------------


def parse_label_records(records: Iterable[dict[str, Any]]) -> pd.DataFrame:
    """Normalise label export records (FEATURES.md section 7) into a frame with LABEL_COLUMNS."""
    rows = []
    skipped = 0
    for rec in records:
        try:
            label = str(rec.get("label", "")).strip().lower()
            if label not in ("cheat", "legit"):
                skipped += 1
                continue
            player = rec.get("player_uuid", rec.get("player"))
            evidence = rec.get("evidence") if isinstance(rec.get("evidence"), dict) else {}
            category = rec.get("category", evidence.get("category"))
            start = to_unix_us(rec["window_start"])
            end = to_unix_us(rec["window_end"])
            if end < start:
                start, end = end, start
            created = rec.get("created_at")
            rows.append(
                {
                    "tenant_id": str(rec["tenant_id"]),
                    "player": normalize_uuid(player),
                    "y": 1 if label == "cheat" else 0,
                    "label": label,
                    "source": str(rec.get("source") or ""),
                    "start_us": start,
                    "end_us": end,
                    "category": normalize_category(category) if label == "cheat" else "",
                    "created_us": to_unix_us(created) if created not in (None, "") else 0,
                    "verdict_id": str(rec.get("verdict_id") or ""),
                    "synthetic": bool(rec.get("synthetic", False))
                    or str(rec["tenant_id"]).startswith(SYNTHETIC_TENANT_PREFIX),
                    "archetype": str(rec.get("synthetic_archetype") or ""),
                }
            )
        except (KeyError, ValueError, TypeError) as exc:
            skipped += 1
            log.warning("skipping malformed label record: %s", exc)
    if skipped:
        log.info("skipped %d label records", skipped)
    df = pd.DataFrame(rows, columns=list(LABEL_COLUMNS))
    for c in ("y", "start_us", "end_us", "created_us"):
        df[c] = df[c].astype(np.int64)
    df["synthetic"] = df["synthetic"].astype(bool)
    return df


def iter_ndjson(lines: Iterable[str | bytes]) -> Iterator[dict[str, Any]]:
    for n, line in enumerate(lines, 1):
        if isinstance(line, bytes):
            line = line.decode("utf-8")
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError as exc:
            raise ValueError(f"line {n}: invalid JSON: {exc}") from exc
        if not isinstance(obj, dict):
            raise ValueError(f"line {n}: expected a JSON object")
        yield obj


def read_ndjson(path: str | Path) -> list[dict[str, Any]]:
    with open(path, encoding="utf-8") as fh:
        return list(iter_ndjson(fh))


def write_ndjson(records: Iterable[dict[str, Any]], path: str | Path) -> Path:
    path = Path(path)

    def _w(p: Path) -> None:
        with open(p, "w", encoding="utf-8") as fh:
            for rec in records:
                fh.write(json.dumps(rec, separators=(",", ":"), sort_keys=True))
                fh.write("\n")

    _write_private(path, _w)
    return path


def _is_loopback(host: str) -> bool:
    if host in ("localhost",):
        return True
    try:
        return ipaddress.ip_address(host.strip("[]")).is_loopback
    except ValueError:
        return False


def fetch_labels_api(
    base_url: str,
    api_key: str,
    since: str | None = None,
    *,
    timeout: float = 60.0,
    max_pages: int = 10_000,
) -> list[dict[str, Any]]:
    """GET {base_url}/api/v1/export/labels as NDJSON with a bearer API key, following next_cursor lines.

    Refuses plain http except to loopback (the key would travel in clear text).
    """
    if not api_key:
        raise ValueError("API key required (pass it through an environment variable, never on the command line)")
    parsed = urllib.parse.urlsplit(base_url)
    if parsed.scheme not in ("https", "http") or not parsed.hostname:
        raise ValueError(f"bad API URL {base_url!r}")
    loopback = _is_loopback(parsed.hostname)
    if parsed.scheme == "http" and not loopback:
        raise ValueError("refusing plain http for a non-loopback API URL")
    handlers: list[Any] = [urllib.request.ProxyHandler({})] if loopback else []
    opener = urllib.request.build_opener(*handlers)
    endpoint = base_url.rstrip("/") + "/api/v1/export/labels"
    records: list[dict[str, Any]] = []
    cursor: str | None = None
    for _ in range(max_pages):
        query = {}
        if since:
            query["since"] = since
        if cursor:
            query["cursor"] = cursor
        url = endpoint + ("?" + urllib.parse.urlencode(query) if query else "")
        req = urllib.request.Request(
            url,
            headers={"Authorization": f"Bearer {api_key}", "Accept": "application/x-ndjson"},
            method="GET",
        )
        cursor = None
        with opener.open(req, timeout=timeout) as resp:
            for obj in iter_ndjson(resp):
                if set(obj) == {"next_cursor"}:
                    cursor = str(obj["next_cursor"]) or None
                    continue
                records.append(obj)
        if not cursor:
            return records
    raise RuntimeError("label export pagination did not terminate")


# ---------------------------------------------------------------------------------------------
# Exclusions
# ---------------------------------------------------------------------------------------------


def _read_list_lines(path: str | Path) -> list[str]:
    out = []
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.split("#", 1)[0].strip()
            if line:
                out.append(line)
    return out


def load_tenant_allowlist(path: str | Path) -> set[str]:
    """One tenant id per line; '#' starts a comment."""
    tenants = set(_read_list_lines(path))
    if not tenants:
        raise ValueError(f"tenant allow-list {path} is empty")
    return tenants


@dataclass(frozen=True)
class ErasureList:
    pairs: frozenset[tuple[str, str]] = frozenset()  # (tenant_id, player)
    players: frozenset[str] = frozenset()  # player erased in every tenant

    def __len__(self) -> int:
        return len(self.pairs) + len(self.players)


def load_erasures(path: str | Path | None) -> ErasureList:
    """Lines ``tenant_id,player_uuid`` or ``player_uuid`` (= every tenant); '#' starts a comment."""
    if path is None:
        return ErasureList()
    pairs, players = set(), set()
    for line in _read_list_lines(path):
        parts = [p.strip() for p in line.replace("\t", ",").split(",") if p.strip()]
        if len(parts) == 1:
            players.add(normalize_uuid(parts[0]))
        elif len(parts) == 2:
            pairs.add((parts[0], normalize_uuid(parts[1])))
        else:
            raise ValueError(f"bad erasure line: {line!r}")
    return ErasureList(frozenset(pairs), frozenset(players))


@dataclass(frozen=True)
class ErasureJournal:
    """A features directory's ``erasures.jsonl``: (tenant_id, player) -> latest ``erased_at_us``.

    Unlike ErasureList it is time-bounded: rows recorded before the erasure are erased, rows recorded
    after it are new data.
    """

    erased_at: dict[tuple[str, str], int] = field(default_factory=dict)

    def __len__(self) -> int:
        return len(self.erased_at)

    def erased(self, df: pd.DataFrame) -> pd.Series:
        """Rows of an erased (tenant, player) whose window started before the erasure or that were written
        at or before it, as the cloud's sink and the worker decide."""
        if not self.erased_at or df.empty:
            return pd.Series(False, index=df.index)
        keys = pd.Series(list(zip(df["tenant_id"], df["player"])), index=df.index, dtype=object)
        cutoff = keys.map(self.erased_at)  # NaN where the pair was never erased: every comparison is False
        gone = df["window_start_us"] < cutoff
        if "worker_unix_us" in df.columns:
            gone |= pd.to_numeric(df["worker_unix_us"], errors="coerce") <= cutoff
        return gone


def load_erasure_journal(path: str | Path) -> ErasureJournal:
    """One JSON object per line: ``{"tenant_id", "player_uuid", "erased_at_us"}`` (Unix microseconds).

    Fails on any malformed line, including an incomplete last one: skipping an erasure is not safe.
    """
    erased_at: dict[tuple[str, str], int] = {}
    with open(path, encoding="utf-8") as fh:
        try:
            for rec in iter_ndjson(fh):
                tenant, at = rec["tenant_id"], rec["erased_at_us"]
                if not isinstance(tenant, str) or not tenant:
                    raise ValueError("tenant_id must be a non-empty string")
                if isinstance(at, bool) or not isinstance(at, int):
                    raise ValueError("erased_at_us must be an integer (Unix microseconds)")
                key = (tenant, normalize_uuid(rec["player_uuid"]))
                erased_at[key] = max(at, erased_at.get(key, at))
        except (KeyError, ValueError, TypeError) as exc:
            raise ValueError(f"{path}: bad erasure record ({exc}); if it is the last line, the cloud may "
                             "still be writing it: retry") from exc
    return ErasureJournal(erased_at)


def write_erased_list(journal: ErasureJournal, path: str | Path) -> Path:
    """Write a journal in the ``--erased`` format (load_erasures), for copies taken off the node.

    The time bound is dropped, so every row of these subjects is excluded.
    """
    lines = []
    for tenant, player in sorted(journal.erased_at):
        if not tenant or tenant != tenant.strip() or any(c in tenant for c in ",\t#"):
            raise ValueError(f"tenant id {tenant!r} cannot be written to an erasure list")
        lines.append(f"{tenant},{player}\n")
    path = Path(path)
    header = f"# erased subjects from {ERASURE_JOURNAL}: tenant_id,player_uuid (all of their rows)\n"
    _write_private(path, lambda p: p.write_text(header + "".join(lines), encoding="utf-8"))
    return path


@dataclass(frozen=True)
class ServerFilter:
    """Windows by ``server_id``: only those of ``include`` (when non-empty), never those of ``exclude``."""

    include: frozenset[str] = frozenset()
    exclude: frozenset[str] = frozenset()

    def __bool__(self) -> bool:
        return bool(self.include or self.exclude)

    def keep(self, df: pd.DataFrame) -> pd.Series:
        keep = ~df["server_id"].isin(self.exclude)
        if self.include:
            keep &= df["server_id"].isin(self.include)
        return keep


def parse_server_ids(values: Iterable[str] | None) -> frozenset[str]:
    """Server ids (canonical UUIDs) from flag values, each a comma-separated list."""
    ids = set()
    for value in values or ():
        for part in value.split(","):
            if not part.strip():
                continue
            try:
                ids.add(normalize_uuid(part))
            except ValueError as exc:
                raise ValueError(f"bad server id {part.strip()!r}: server ids are UUIDs") from exc
    return frozenset(ids)


def _excluded_mask(df: pd.DataFrame, trusted: set[str], erased: ErasureList) -> pd.Series:
    untrusted = ~df["tenant_id"].isin(trusted)
    er = df["player"].isin(erased.players)
    if erased.pairs:
        keys = pd.Series(list(zip(df["tenant_id"], df["player"])), index=df.index)
        er = er | keys.isin(erased.pairs)
    return untrusted | er


def apply_exclusions(
    windows: pd.DataFrame, labels: pd.DataFrame, trusted: set[str], erased: ErasureList
) -> tuple[pd.DataFrame, pd.DataFrame, dict[str, int]]:
    if not trusted:
        raise ValueError("a non-empty trusted-tenant allow-list is required")
    wmask = _excluded_mask(windows, trusted, erased)
    lmask = _excluded_mask(labels, trusted, erased)
    stats = {
        "windows_untrusted_or_erased": int(wmask.sum()),
        "labels_untrusted_or_erased": int(lmask.sum()),
    }
    return windows[~wmask].reset_index(drop=True), labels[~lmask].reset_index(drop=True), stats


# ---------------------------------------------------------------------------------------------
# Label join
# ---------------------------------------------------------------------------------------------


def join_labels(windows: pd.DataFrame, labels: pd.DataFrame) -> tuple[pd.DataFrame, dict[str, int]]:
    """Label windows by overlap (FEATURES.md 7). Returns labelled windows (+ y, category, label_source)."""
    w = windows.reset_index(drop=True).copy()
    w["_wid"] = np.arange(len(w), dtype=np.int64)
    lab = labels.loc[:, ["tenant_id", "player", "y", "start_us", "end_us", "category", "created_us", "source", "archetype"]]
    m = w.loc[:, ["_wid", "tenant_id", "player", "window_start_us", "window_end_us"]].merge(
        lab, on=["tenant_id", "player"], how="inner"
    )
    overlap = (m["window_start_us"] <= m["end_us"]) & (m["start_us"] < m["window_end_us"])
    m = m[overlap]
    stats = {"windows_total": int(len(w))}
    if m.empty:
        stats.update(windows_labelled=0, windows_ambiguous=0, windows_unlabelled=int(len(w)))
        out = w.iloc[0:0].drop(columns=["_wid"]).copy()
        for c, dt in (("y", np.int64), ("category", object), ("label_source", object), ("archetype", object)):
            out[c] = pd.Series(dtype=dt)
        return out, stats
    agg = m.groupby("_wid")["y"].agg(["min", "max"])
    ambiguous = agg.index[agg["min"] != agg["max"]]
    clean = agg.index[agg["min"] == agg["max"]]
    # Category/source/archetype from the most recently created overlapping label of the window's class.
    m = m[m["_wid"].isin(clean)].sort_values(["_wid", "created_us"], kind="stable")
    last = m.groupby("_wid").tail(1).set_index("_wid")
    out = w.set_index("_wid").loc[clean].copy()
    out["y"] = agg.loc[clean, "max"].astype(np.int64)
    out["category"] = last.loc[clean, "category"].where(out["y"] == 1, "")
    out["label_source"] = last.loc[clean, "source"]
    out["archetype"] = last.loc[clean, "archetype"]
    stats.update(
        windows_labelled=int(len(out)),
        windows_ambiguous=int(len(ambiguous)),
        windows_unlabelled=int(len(w) - len(agg)),
    )
    return out.reset_index(drop=True), stats


# ---------------------------------------------------------------------------------------------
# Group split
# ---------------------------------------------------------------------------------------------


def _player_rank(player: str, seed: int) -> str:
    return hashlib.sha256(f"{seed}:{player}".encode()).hexdigest()


def player_split(players: pd.Series, y: pd.Series, test_fraction: float, seed: int) -> np.ndarray:
    """Boolean mask selecting the test rows. Grouped by player UUID, stratified by "has a cheat window".

    Players are ordered by a seeded hash, so the assignment is deterministic and a player keeps its side
    when unrelated players are added.
    """
    if not 0.0 < test_fraction < 1.0:
        raise ValueError("test_fraction must be in (0, 1)")
    frame = pd.DataFrame({"player": players.to_numpy(), "y": y.to_numpy()})
    per_player = frame.groupby("player")["y"].max()
    test_players: set[str] = set()
    for cls in (0, 1):
        ps = sorted(per_player.index[per_player == cls], key=lambda p: _player_rank(p, seed))
        if not ps:
            continue
        k = int(round(test_fraction * len(ps)))
        if len(ps) >= 2:
            k = min(max(k, 1), len(ps) - 1)
        test_players.update(ps[:k])
    mask = frame["player"].isin(test_players).to_numpy()
    assert_disjoint(frame["player"][mask], frame["player"][~mask])
    return mask


def assert_disjoint(a: Iterable[str], b: Iterable[str]) -> None:
    common = set(a) & set(b)
    if common:
        raise AssertionError(f"{len(common)} players appear on both sides of a split")


# ---------------------------------------------------------------------------------------------
# Dataset
# ---------------------------------------------------------------------------------------------


@dataclass
class Dataset:
    frame: pd.DataFrame
    stats: dict[str, Any] = field(default_factory=dict)
    synthetic: bool = False

    @property
    def X(self) -> np.ndarray:
        return F.feature_frame(self.frame)

    @property
    def y(self) -> np.ndarray:
        return self.frame["y"].to_numpy(dtype=np.int64)

    @property
    def groups(self) -> np.ndarray:
        return self.frame["player"].to_numpy()

    def subset(self, mask: np.ndarray) -> Dataset:
        return Dataset(self.frame[mask].reset_index(drop=True), dict(self.stats), self.synthetic)

    def __len__(self) -> int:
        return len(self.frame)


def build_dataset(
    windows: pd.DataFrame,
    labels: pd.DataFrame,
    trusted_tenants: set[str],
    erased: ErasureList | None = None,
    *,
    servers: ServerFilter | None = None,
    tick_clients_only: bool = True,
    feature_version: int = F.FEATURE_VERSION,
) -> Dataset:
    erased = erased or ErasureList()
    stats: dict[str, Any] = {"windows_in": int(len(windows)), "labels_in": int(len(labels))}
    synthetic = bool(labels["synthetic"].any()) if "synthetic" in labels.columns and len(labels) else False
    synthetic = synthetic or bool(_synthetic_rows(windows).any())

    if servers:
        keep = servers.keep(windows)
        stats["windows_server_filtered"] = int((~keep).sum())
        windows = windows[keep]
    wrong_version = windows["feature_version"] != feature_version
    stats["windows_wrong_version"] = int(wrong_version.sum())
    windows = windows[~wrong_version]
    if tick_clients_only:
        non_tick = windows["client_protocol"] < F.MIN_TICK_CLIENT_PROTOCOL
        stats["windows_non_tick_client"] = int(non_tick.sum())
        windows = windows[~non_tick]
    windows, labels, ex = apply_exclusions(windows, labels, trusted_tenants, erased)
    stats.update(ex)
    labelled, js = join_labels(windows, labels)
    stats.update(js)
    stats["positives"] = int((labelled["y"] == 1).sum()) if len(labelled) else 0
    stats["negatives"] = int((labelled["y"] == 0).sum()) if len(labelled) else 0
    stats["players"] = int(labelled["player"].nunique()) if len(labelled) else 0
    return Dataset(labelled.reset_index(drop=True), stats, synthetic)
