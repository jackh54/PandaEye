"""End to end through the CLI: synth -> train (+ ONNX export) -> evaluate, in a temp dir."""

import json

import numpy as np
import pandas as pd
import pytest

from pandaeye_ml import cli
from pandaeye_ml import dataset as D
from pandaeye_ml import export_onnx as E

from .conftest import FAST, SMALL_SYNTH

TRAIN_FLAGS = [
    "--max-iter", str(FAST["max_iter"]),
    "--min-samples-leaf", str(FAST["min_samples_leaf"]),
    "--learning-rate", str(FAST["learning_rate"]),
    "--class-weight", "none",
    "--calibration-folds", str(FAST["calibration_folds"]),
    "--threshold-folds", str(FAST["threshold_folds"]),
]


@pytest.fixture(scope="module")
def run_dir(tmp_path_factory):
    d = tmp_path_factory.mktemp("e2e")
    assert cli.main([
        "synth", "--out", str(d / "data"), "--players", str(SMALL_SYNTH["players"]),
        "--min-windows", str(SMALL_SYNTH["windows_per_session"][0]),
        "--max-windows", str(SMALL_SYNTH["windows_per_session"][1]), "--seed", str(SMALL_SYNTH["seed"]),
    ]) == 0
    data = d / "data"
    assert cli.main([
        "train",
        "--windows", str(data / "SYNTHETIC_feature_windows.parquet"),
        "--labels", str(data / "SYNTHETIC_labels.ndjson"),
        "--trusted-tenants", str(data / "SYNTHETIC_trusted_tenants.txt"),
        "--erased", str(data / "SYNTHETIC_erased.txt"),
        "--out", str(d / "model"), *TRAIN_FLAGS,
    ]) == 0
    return d


def test_train_outputs(run_dir):
    model = run_dir / "model"
    for f in ("model.onnx", "model.joblib", "model.json", "baseline.json", "heldout.parquet", "train_report.json"):
        assert (model / f).exists(), f
    report = json.loads((model / "train_report.json").read_text())
    assert report["synthetic"] is True
    assert report["onnx_parity_max_abs_diff"] < E.DEFAULT_ATOL
    md = E.read_metadata(model / "model.onnx")
    assert md["pandaeye.trained_on_synthetic"] == "true"
    assert 0.0 < float(md["pandaeye.threshold"]) <= 1.0
    oof = report["threshold_selection_out_of_fold"]
    assert oof["model_fpr"] <= oof["target_fpr"] + 1e-12


def test_heldout_players_are_complete_and_disjoint(run_dir):
    data = run_dir / "data"
    ds = D.build_dataset(
        D.load_windows_file(data / "SYNTHETIC_feature_windows.parquet"),
        D.parse_label_records(D.read_ndjson(data / "SYNTHETIC_labels.ndjson")),
        D.load_tenant_allowlist(data / "SYNTHETIC_trusted_tenants.txt"),
        D.load_erasures(data / "SYNTHETIC_erased.txt"),
    )
    held = pd.read_parquet(run_dir / "model" / "heldout.parquet")
    held_players = set(held["player"])
    assert held_players and held_players < set(ds.frame["player"])
    # every labelled window of a held-out player is held out, so no player is on both sides
    assert len(held) == int(ds.frame["player"].isin(held_players).sum())
    erased = {line.split(",")[1] for line in (data / "SYNTHETIC_erased.txt").read_text().splitlines()
              if line and not line.startswith("#")}
    assert not erased & set(ds.frame["player"])
    assert not held["tenant_id"].eq("synthetic-tenant-untrusted").any()


def test_gate_passes_on_synthetic_and_is_never_deployable(run_dir, capsys):
    report_path = run_dir / "gate.json"
    code = cli.main(["evaluate", "--model-dir", str(run_dir / "model"), "--report", str(report_path)])
    report = json.loads(report_path.read_text())
    assert code == 0, report["gate"]["reasons"]
    assert report["gate"]["passed"] is True
    assert report["scored_with"] == "onnxruntime"
    assert report["synthetic"] is True and report["deployable"] is False
    g = report["gate"]
    assert g["model"]["fpr"] <= g["baseline"]["fpr"]
    assert g["model"]["tpr"] > g["baseline"]["tpr"]
    assert report["per_category"] and report["per_archetype"]
    assert report["model"]["roc_auc"] > report["baseline"]["roc_auc"]
    assert "SYNTHETIC" in report["warning"]


def test_fixed_threshold_mode_and_onnx_reexport(run_dir):
    model = run_dir / "model"
    # A deploy threshold of 1.0 flags nothing: FPR 0 but no TPR gain -> the fixed-mode gate fails.
    assert cli.main(["onnx", "--model-dir", str(model), "--threshold", "1.0"]) == 0
    assert E.read_metadata(model / "model.onnx")["pandaeye.threshold"] == "1.0"
    code = cli.main(["evaluate", "--model-dir", str(model), "--threshold-mode", "fixed",
                     "--report", str(run_dir / "fixed.json")])
    rep = json.loads((run_dir / "fixed.json").read_text())
    assert code == 1 and rep["gate"]["threshold_mode"] == "fixed"
    assert rep["gate"]["model"]["tp"] == 0 and rep["gate"]["model"]["fp"] == 0


def test_model_trained_on_noise_fails_the_gate(run_dir, tmp_path):
    """A model worse than the heuristics (labels shuffled across players) must exit 1."""
    data = run_dir / "data"
    labels = D.read_ndjson(data / "SYNTHETIC_labels.ndjson")
    rng = np.random.default_rng(0)
    values = [r["label"] for r in labels]
    rng.shuffle(values)
    for r, v in zip(labels, values):
        r["label"] = v
    D.write_ndjson(labels, tmp_path / "shuffled.ndjson")
    assert cli.main([
        "train", "--windows", str(data / "SYNTHETIC_feature_windows.parquet"),
        "--labels", str(tmp_path / "shuffled.ndjson"),
        "--trusted-tenants", str(data / "SYNTHETIC_trusted_tenants.txt"),
        "--out", str(tmp_path / "model"), *TRAIN_FLAGS,
    ]) == 0
    # Evaluate the noise model on the REAL held-out windows of the good run.
    code = cli.main(["evaluate", "--model-dir", str(tmp_path / "model"),
                     "--heldout", str(run_dir / "model" / "heldout.parquet"),
                     "--report", str(tmp_path / "gate.json")])
    rep = json.loads((tmp_path / "gate.json").read_text())
    assert code == 1, rep["gate"]
    assert rep["gate"]["passed"] is False


def test_train_requires_allowlist(run_dir):
    data = run_dir / "data"
    with pytest.raises(SystemExit):
        cli.main(["train", "--windows", str(data / "SYNTHETIC_feature_windows.parquet"),
                  "--labels", str(data / "SYNTHETIC_labels.ndjson"), "--out", str(run_dir / "x")])
