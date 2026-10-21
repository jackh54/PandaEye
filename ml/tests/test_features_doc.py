"""FEATURES.md (the contract the Go worker implements) must match features.py exactly."""

import re

import numpy as np
import pytest

from pandaeye_ml import features as F

from .conftest import ML_DIR

DOC = (ML_DIR / "FEATURES.md").read_text(encoding="utf-8")


def _num(s: str) -> float:
    return float(s.strip())


def parse_feature_table(doc: str) -> list[dict]:
    rows = []
    for line in doc.splitlines():
        if not re.match(r"^\| \d+ \| `", line):
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        assert len(cells) == 8, f"feature row must have 8 cells (no '|' inside cells): {line}"
        idx, name, unit, definition, events, min_n, default, clip = cells
        m = re.fullmatch(r"\[\s*([-0-9.e]+)\s*,\s*([-0-9.e]+)\s*\]", clip)
        assert m, f"bad clip cell {clip!r} in {line}"
        rows.append(
            {
                "index": int(idx),
                "name": name.strip("`"),
                "unit": unit,
                "definition": definition,
                "events": events,
                "min_n": min_n,
                "default": _num(default),
                "lo": _num(m.group(1)),
                "hi": _num(m.group(2)),
            }
        )
    return rows


ROWS = parse_feature_table(DOC)


def test_table_has_every_feature_in_order():
    assert [r["index"] for r in ROWS] == list(range(len(ROWS)))
    assert [r["name"] for r in ROWS] == list(F.FEATURE_NAMES)
    assert len(ROWS) == F.N_FEATURES


@pytest.mark.parametrize("i", range(F.N_FEATURES))
def test_row_matches_spec(i):
    row, spec = ROWS[i], F.FEATURES[i]
    assert row["unit"] == spec.unit, spec.name
    assert row["default"] == spec.default, spec.name
    assert (row["lo"], row["hi"]) == (spec.lo, spec.hi), spec.name
    assert row["definition"] and row["events"] and row["min_n"], spec.name


def test_version_and_count_statements():
    m = re.search(r"\*\*Feature version: (\d+)\*\*", DOC)
    assert m and int(m.group(1)) == F.FEATURE_VERSION
    m = re.search(r"`N = (\d+)` features", DOC)
    assert m and int(m.group(1)) == F.N_FEATURES


def test_group_summary_matches_specs():
    m = re.search(r"Group summary: (.+?)\.\n\n", DOC, re.S)
    assert m
    summary = " ".join(m.group(1).split())
    ranges = re.findall(r"(\w[\w ]*?)(?: \([^)]*\))? (\d+)-(\d+)", summary)
    groups = {}
    for spec_i, spec in enumerate(F.FEATURES):
        groups.setdefault(spec.group, []).append(spec_i)
    doc_ranges = sorted((int(a), int(b)) for _, a, b in ranges)
    code_ranges = sorted((min(v), max(v)) for v in groups.values())
    assert doc_ranges == code_ranges


def test_metadata_table_lists_meta_columns():
    section = DOC.split("## 5. Metadata columns", 1)[1].split("## 6.", 1)[0]
    names = re.findall(r"^\| `(\w+)` \|", section, re.M)
    assert names == list(F.META_COLUMNS)


def test_clickhouse_ddl():
    ddl = re.search(r"```sql\n(.+?)```", DOC, re.S).group(1)
    assert "CREATE TABLE IF NOT EXISTS feature_windows" in ddl
    assert re.search(r"TTL time \+ INTERVAL 180 DAY", ddl)
    assert "features         Array(Float32)" in ddl
    assert re.search(r"ORDER BY \(tenant_id, time", ddl)
    for col in F.META_COLUMNS:
        assert re.search(rf"^\s+{col}\s+\S", ddl, re.M), col
    # erasure statement names the columns the DDL defines
    assert "DELETE WHERE tenant_id = ? AND player = ?" in DOC


def test_every_local_check_has_a_flag_feature():
    checks_doc = (ML_DIR.parent / "docs" / "CHECKS.md")
    if not checks_doc.exists():
        pytest.skip("docs/CHECKS.md not available")
    text = checks_doc.read_text(encoding="utf-8")
    local = re.findall(r"^\| ([a-z]+\.[a-z]+) \| local \|", text, re.M)
    assert local, "no local checks parsed from CHECKS.md section 4"
    for c in local:
        assert c in F.FLAG_FEATURES, f"local check {c} has no flag_ feature"


def test_sanitize_rules():
    x = np.tile(F.DEFAULTS, (3, 1))
    i_share = F.FEATURE_INDEX["hitbox_miss_share"]
    i_reach = F.FEATURE_INDEX["reach_p95"]
    x[0, i_share] = np.nan  # -> default
    x[1, i_share] = 1.7  # -> clipped to 1
    x[2, i_reach] = 99.0  # -> clipped to 8
    out = F.sanitize(x)
    assert out.dtype == np.float32
    assert out[0, i_share] == F.FEATURES[i_share].default
    assert out[1, i_share] == 1.0
    assert out[2, i_reach] == 8.0
    # defaults outside the clip range are preserved (e.g. -1 = undefined)
    assert out[0, i_reach] == -1.0
    assert np.array_equal(F.sanitize(out), out)
