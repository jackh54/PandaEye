"""Training: gradient boosting with class weights and calibrated probabilities, grouped by player.

Pipeline (all splits by player UUID, stratified by "has a cheat window"):

1. held-out test players (``test_fraction``) are set aside and written to ``heldout.parquet``; nothing
   below sees them. ``evaluate.py`` runs the gate on them.
2. operating threshold: out-of-fold P(cheat) for every training window from ``threshold_folds``
   player-grouped folds (each fold scored by a model fitted without its players). The threshold is the
   lowest P(cheat) whose out-of-fold FPR is <= ``threshold_fpr_margin`` x the heuristic baseline's FPR on
   the same windows (placed midway between adjacent scores so float32 rounding in ONNX cannot flip a
   decision). The margin hedges against the final model (fitted on more data) being more confident and
   against sampling noise; the gate re-checks FPR on the held-out players anyway.
3. final model on all training players: ``CalibratedClassifierCV(HistGradientBoostingClassifier(
   class_weight=...), method="sigmoid")`` with StratifiedGroupKFold folds, so each calibrator is fitted on
   players its booster never saw. Sigmoid calibration converts to ONNX exactly (isotonic does not).
4. ONNX export (threshold in the metadata) + parity check against scikit-learn on all rows used.
"""

from __future__ import annotations

import json
import logging
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import joblib
import numpy as np
import pandas as pd

from . import __version__
from . import features as F
from .baseline import ALERT_THRESHOLD, BaselineModel
from .dataset import Dataset, assert_disjoint, player_split, save_windows
from .export_onnx import build_metadata, export_model, verify_parity

log = logging.getLogger(__name__)

MODEL_FILE = "model.joblib"
ONNX_FILE = "model.onnx"
META_FILE = "model.json"
BASELINE_FILE = "baseline.json"
HELDOUT_FILE = "heldout.parquet"
REPORT_FILE = "train_report.json"


# Monotonic constraints (domain knowledge): more violations / misses / flags never make a window less
# suspicious. Undefined values use defaults at or below the valid range, so "undefined" is the least
# suspicious end for every constrained feature. timer_offset_slope: a falling offset floor = timer.
MONOTONE_INCREASING = (
    *F.FLAG_FEATURES.values(),
    "flag_movement_severity_max",
    "multi_target_ticks",
    "hitbox_miss_share",
    "noswing_share",
    "snap_count",
    "snap_share",
    "reach_p50",
    "reach_p95",
    "reach_max",
    "velocity_vertical_ignored_share",
    "velocity_h_shortfall_share",
    "place_face_invalid_share",
    "place_ray_miss_share",
    "place_cursor_mismatch_share",
    "place_reach_max",
    "dig_ray_miss_share",
    "inv_move_ticks",
    "inv_rotate_ticks",
    "gap_burst_count",
    "cs_movement_candidate_rate",
    "cs_speed_max_stat",
    "cs_fly_max_stat",
)
MONOTONE_DECREASING = ("timer_offset_slope",)


def monotonic_constraints() -> list[int]:
    cst = [0] * F.N_FEATURES
    for n in MONOTONE_INCREASING:
        cst[F.FEATURE_INDEX[n]] = 1
    for n in MONOTONE_DECREASING:
        cst[F.FEATURE_INDEX[n]] = -1
    return cst


@dataclass
class TrainConfig:
    max_iter: int = 300
    learning_rate: float = 0.08
    max_leaf_nodes: int = 15
    min_samples_leaf: int = 20
    l2_regularization: float = 1.0
    # 63 bins: scikit-learn computes weighted bin quantiles one percentile at a time when sample weights
    # (class weights) are present, which costs ~4x more at the default 255 bins.
    max_bins: int = 63
    class_weight: str | None = "balanced"
    monotonic: bool = True
    calibration_folds: int = 3
    test_fraction: float = 0.25
    threshold_folds: int = 3
    threshold_fpr_margin: float = 0.75
    seed: int = 1
    baseline_threshold: float = ALERT_THRESHOLD
    model_name: str = "gbm1"


def make_estimator(cfg: TrainConfig):
    from sklearn.ensemble import HistGradientBoostingClassifier

    return HistGradientBoostingClassifier(
        max_iter=cfg.max_iter,
        learning_rate=cfg.learning_rate,
        max_leaf_nodes=cfg.max_leaf_nodes,
        min_samples_leaf=cfg.min_samples_leaf,
        l2_regularization=cfg.l2_regularization,
        max_bins=cfg.max_bins,
        class_weight=cfg.class_weight,
        monotonic_cst=monotonic_constraints() if cfg.monotonic else None,
        early_stopping=False,  # its internal validation split is not grouped by player
        random_state=cfg.seed,
    )


def group_folds(y: np.ndarray, groups: np.ndarray, n_splits: int, seed: int) -> list[tuple[np.ndarray, np.ndarray]]:
    from sklearn.model_selection import StratifiedGroupKFold

    n_groups = len(np.unique(groups))
    n_splits = min(n_splits, n_groups)
    if n_splits < 2:
        raise ValueError("need at least 2 players for calibration folds")
    splitter = StratifiedGroupKFold(n_splits=n_splits, shuffle=True, random_state=seed)
    folds = list(splitter.split(np.zeros(len(y)), y, groups))
    for tr, te in folds:
        assert_disjoint(groups[tr], groups[te])
        if len(np.unique(y[tr])) < 2 or len(np.unique(y[te])) < 2:
            raise ValueError("a calibration fold lacks one class; more labelled players of both classes are needed")
    return folds


def fit_calibrated(X: np.ndarray, y: np.ndarray, groups: np.ndarray, cfg: TrainConfig):
    from sklearn.calibration import CalibratedClassifierCV

    folds = group_folds(y, groups, cfg.calibration_folds, cfg.seed)
    model = CalibratedClassifierCV(make_estimator(cfg), method="sigmoid", cv=folds, ensemble=True)
    model.fit(np.asarray(X, dtype=np.float32).astype(np.float64), y)
    return model


def out_of_fold_proba(X: np.ndarray, y: np.ndarray, groups: np.ndarray, cfg: TrainConfig) -> np.ndarray:
    """P(cheat) for every row from a model that never saw the row's player (float32, as ONNX outputs)."""
    p = np.full(len(y), np.nan, dtype=np.float32)
    for tr, te in group_folds(y, groups, cfg.threshold_folds, cfg.seed + 101):
        m = fit_calibrated(X[tr], y[tr], groups[tr], cfg)
        p[te] = m.predict_proba(np.asarray(X[te], dtype=np.float32).astype(np.float64))[:, 1].astype(np.float32)
    if np.isnan(p).any():  # pragma: no cover - folds partition the rows
        raise AssertionError("out-of-fold predictions incomplete")
    return p


def false_positive_rate(y: np.ndarray, flagged: np.ndarray) -> float:
    neg = np.asarray(y) == 0
    return float(np.mean(np.asarray(flagged)[neg])) if neg.any() else 0.0


def select_threshold(y: np.ndarray, p: np.ndarray, max_fpr: float) -> float:
    """Lowest threshold t (decision: p >= t) with FPR(t) <= max_fpr, placed between adjacent scores."""
    y = np.asarray(y)
    p = np.asarray(p, dtype=np.float64)
    neg = np.sort(p[y == 0])
    if len(p) == 0:
        return 1.0
    scores = np.unique(p)  # ascending
    if len(neg) == 0:
        return float(scores[0])
    n_neg_ge = len(neg) - np.searchsorted(neg, scores, side="left")
    fpr = n_neg_ge / len(neg)
    valid = fpr <= max_fpr + 1e-12
    if not valid.any():
        return float(np.nextafter(np.float32(scores[-1]), np.float32(np.inf)))  # flags nothing seen
    i = int(np.argmax(valid))  # first valid index; valid is a suffix since fpr is non-increasing
    if i == 0:
        return float(scores[0])
    return float((scores[i] + scores[i - 1]) / 2.0)


def _require_both_classes(name: str, y: np.ndarray) -> None:
    if len(np.unique(y)) < 2:
        raise ValueError(f"the {name} split has only one class; more labelled players are needed")


def train(dataset: Dataset, cfg: TrainConfig, out_dir: str | Path) -> dict[str, Any]:
    out = Path(out_dir)
    out.mkdir(parents=True, exist_ok=True)
    frame = dataset.frame
    if len(frame) == 0:
        raise ValueError("no labelled windows")
    _require_both_classes("full", frame["y"].to_numpy())

    test_mask = player_split(frame["player"], frame["y"], cfg.test_fraction, cfg.seed)
    train_ds, test_ds = dataset.subset(~test_mask), dataset.subset(test_mask)
    assert_disjoint(train_ds.groups, test_ds.groups)
    for name, ds in (("training", train_ds), ("held-out", test_ds)):
        _require_both_classes(name, ds.y)

    X_tr, y_tr, g_tr = train_ds.X, train_ds.y, train_ds.groups
    baseline = BaselineModel(threshold=cfg.baseline_threshold)
    s_tr = baseline.score(X_tr)
    baseline.fit(s_tr, y_tr)

    p_oof = out_of_fold_proba(X_tr, y_tr, g_tr, cfg)
    baseline_fpr = false_positive_rate(y_tr, baseline.flagged(s_tr))
    target_fpr = baseline_fpr * cfg.threshold_fpr_margin
    threshold = select_threshold(y_tr, p_oof, target_fpr)
    pos = y_tr == 1
    oof_summary = {
        "windows": int(len(y_tr)),
        "folds": cfg.threshold_folds,
        "baseline_fpr": baseline_fpr,
        "baseline_tpr": float(np.mean(baseline.flagged(s_tr)[pos])) if pos.any() else 0.0,
        "target_fpr": target_fpr,
        "model_fpr": false_positive_rate(y_tr, p_oof >= threshold),
        "model_tpr": float(np.mean(p_oof[pos] >= threshold)) if pos.any() else 0.0,
    }

    log.info("fitting final model on %d windows / %d players", len(train_ds), len(np.unique(g_tr)))
    model = fit_calibrated(X_tr, y_tr, g_tr, cfg)

    created_at = datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")
    metadata = build_metadata(
        model_name=cfg.model_name,
        threshold=threshold,
        trained_on_synthetic=dataset.synthetic,
        created_at=created_at,
        extra={
            "pipeline_version": __version__,
            "training_windows": str(len(train_ds)),
            "training_players": str(len(np.unique(g_tr))),
            "baseline_threshold": repr(float(cfg.baseline_threshold)),
        },
    )

    joblib.dump(model, out / MODEL_FILE)
    baseline.save(out / BASELINE_FILE)
    onnx_path = export_model(model, out / ONNX_FILE, metadata)
    check_X = np.vstack([X_tr, test_ds.X])
    parity = verify_parity(model, onnx_path, check_X)
    save_windows(test_ds.frame, out / HELDOUT_FILE)

    meta_doc = {
        "metadata": metadata,
        "config": asdict(cfg),
        "onnx_parity_max_abs_diff": parity,
        "synthetic": dataset.synthetic,
        "files": {"onnx": ONNX_FILE, "sklearn": MODEL_FILE, "baseline": BASELINE_FILE, "heldout": HELDOUT_FILE},
    }
    (out / META_FILE).write_text(json.dumps(meta_doc, indent=2) + "\n", encoding="utf-8")

    def split_stats(ds: Dataset) -> dict[str, int]:
        return {
            "windows": int(len(ds)),
            "players": int(len(np.unique(ds.groups))),
            "positives": int((ds.y == 1).sum()),
            "negatives": int((ds.y == 0).sum()),
        }

    report = {
        "pipeline_version": __version__,
        "feature_version": F.FEATURE_VERSION,
        "synthetic": dataset.synthetic,
        "dataset": dataset.stats,
        "splits": {"train": split_stats(train_ds), "heldout": split_stats(test_ds)},
        "threshold": threshold,
        "threshold_selection_out_of_fold": oof_summary,
        "onnx_parity_max_abs_diff": parity,
        "config": asdict(cfg),
        "created_at": created_at,
    }
    (out / REPORT_FILE).write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    return report


def load_sklearn_model(model_dir: str | Path):
    return joblib.load(Path(model_dir) / MODEL_FILE)


def heldout_frame(model_dir: str | Path) -> pd.DataFrame:
    return pd.read_parquet(Path(model_dir) / HELDOUT_FILE)
