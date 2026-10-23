"""Window files written by the Go file sink (PE_FEATURES_DIR) load unchanged with the single-file loader.

The fixture is the Go golden file: TestFileGolden in cloud/internal/telemetry/files_test.go fails when the
sink's output changes, so this test always reads what the sink writes.
"""

import csv
import gzip
import shutil

import numpy as np

from pandaeye_ml import dataset as D
from pandaeye_ml import features as F

from .conftest import REPO_DIR

FIXTURE = REPO_DIR / "cloud" / "internal" / "telemetry" / "testdata" / "windows-20260105T10Z.csv"
HEADER = [*F.META_COLUMNS, "worker_unix_us", *F.FEATURE_NAMES]


def _cells():
    with open(FIXTURE, newline="", encoding="utf-8") as fh:
        rows = list(csv.reader(fh))
    return rows[0], rows[1:]


def test_header_is_the_contract():
    header, rows = _cells()
    assert header == HEADER
    assert rows and all(len(r) == len(HEADER) for r in rows)


def test_loader_reads_csv_and_gzip_unchanged(tmp_path):
    header, rows = _cells()
    gz = tmp_path / FIXTURE.name.replace(".csv", ".csv.gz")
    with open(FIXTURE, "rb") as src, gzip.open(gz, "wb") as dst:
        shutil.copyfileobj(src, dst)
    want = np.array([[np.float32(c) for c in r[len(header) - F.N_FEATURES :]] for r in rows], dtype=np.float32)
    for path in (FIXTURE, gz):
        df = D.load_windows_file(path)
        assert len(df) == len(rows)
        got = df[list(F.FEATURE_NAMES)].to_numpy(dtype=np.float64).astype(np.float32)
        # Bit for bit: the Go sink writes the shortest decimal that parses back to the same float32.
        assert np.array_equal(got.view(np.uint32), want.view(np.uint32))
        for col in ("player", "server_id", "session_id"):
            assert df[col].tolist() == [r[header.index(col)] for r in rows]
        assert df.loc[1, "session_id"] == "00000000-0000-0000-0000-000000000000"  # invalid id -> nil UUID
        assert df["worker_unix_us"].tolist() == [int(r[header.index("worker_unix_us")]) for r in rows]
        assert df["tick_client"].tolist() == [1, 0] and df["hot_loaded"].tolist() == [0, 1]
        assert (df["feature_version"] == F.FEATURE_VERSION).all()
