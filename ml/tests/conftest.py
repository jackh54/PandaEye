import os

# Shared 4-CPU machine: keep native thread pools small before numpy/sklearn are imported.
os.environ.setdefault("OMP_NUM_THREADS", "2")
os.environ.setdefault("OPENBLAS_NUM_THREADS", "1")
os.environ.setdefault("MKL_NUM_THREADS", "1")

from pathlib import Path  # noqa: E402

import numpy as np  # noqa: E402
import pytest  # noqa: E402

from pandaeye_ml import dataset as D  # noqa: E402
from pandaeye_ml import synth  # noqa: E402
from pandaeye_ml.train import TrainConfig  # noqa: E402

ML_DIR = Path(__file__).resolve().parents[1]
REPO_DIR = ML_DIR.parent

# Small, fast settings for tests (the defaults are sized for real data). The end-to-end run trains
# without class weights: scikit-learn's weighted bin quantiles make weighted fits ~4x slower; the class
# weighted path is exercised by the ONNX tests (WEIGHTED).
FAST = dict(max_iter=150, min_samples_leaf=10, learning_rate=0.1, calibration_folds=2, threshold_folds=2, class_weight=None)
WEIGHTED = dict(FAST, max_iter=40, class_weight="balanced")
SMALL_SYNTH = dict(players=700, windows_per_session=(4, 10), seed=3)


@pytest.fixture(scope="session")
def synth_dir(tmp_path_factory) -> dict[str, Path]:
    out = tmp_path_factory.mktemp("synth")
    return synth.write(out, synth.SynthConfig(**SMALL_SYNTH))


@pytest.fixture(scope="session")
def synth_dataset(synth_dir) -> D.Dataset:
    windows = D.load_windows_file(synth_dir["windows"])
    labels = D.parse_label_records(D.read_ndjson(synth_dir["labels"]))
    trusted = D.load_tenant_allowlist(synth_dir["trusted"])
    erased = D.load_erasures(synth_dir["erased"])
    return D.build_dataset(windows, labels, trusted, erased)


@pytest.fixture(scope="session")
def fast_config() -> TrainConfig:
    return TrainConfig(**WEIGHTED)


@pytest.fixture(scope="session")
def small_model(synth_dataset, fast_config):
    """A calibrated model fitted on part of the synthetic data (for ONNX tests)."""
    from pandaeye_ml.train import fit_calibrated

    ds = synth_dataset
    rng = np.random.default_rng(0)
    players = np.unique(ds.groups)
    keep = set(rng.choice(players, size=len(players) // 2, replace=False))
    mask = np.array([p in keep for p in ds.groups])
    sub = ds.subset(mask)
    model = fit_calibrated(sub.X, sub.y, sub.groups, fast_config)
    return model, ds.subset(~mask).X
