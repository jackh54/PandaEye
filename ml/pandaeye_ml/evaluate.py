"""THE GATE: does the model beat the heuristic baseline on held-out players without raising FPR?

ML phase gate (blueprint): "beats the heuristics on a held-out set without raising false positives".

Procedure on the held-out windows (players never seen in training or threshold selection):

1. Baseline: heuristic score per window (baseline.py) at its operating threshold (default: base ALERT
   threshold, 15 raw points) -> ``FPR_b``, ``TPR_b``.
2. Model: P(cheat) from the exported ONNX file (the artifact that would be deployed). Its operating
   point depends on ``threshold_mode``:
   - ``matched`` (default): the threshold with the highest TPR among those whose held-out FPR is <=
     ``FPR_b`` ("TPR at an equal or lower FPR"). This compares the two detectors at the baseline's
     operating point; the threshold is read off the held-out ROC (one parameter, reported as such).
   - ``fixed``: the deploy threshold from the model metadata (``pandaeye.threshold``), chosen on
     training players only. Stricter: it also tests that the shipped threshold transfers.
3. Pass iff ``FPR_m <= FPR_b`` AND ``TPR_m > TPR_b + min_tpr_gain`` AND the held-out set has enough
   positives/negatives/players of each class. Anything else fails (exit code 1).

Both operating points are always reported. ``deployable`` is true only when the gate passed, the data is
not synthetic, and the deploy threshold itself keeps FPR <= ``FPR_b`` on the held-out players (otherwise
re-export with ``onnx --threshold``). ``model_sha256`` is the SHA-256 of the ONNX file that was scored
(null with ``--sklearn``); the Go loader refuses a model unless its gate report matches the file. Also
reported (not gating): ROC-AUC and PR-AUC of both scores, per-category and per-archetype TPRs, FPR by label
source, and a player-level bootstrap of the TPR/FPR differences at the gate's operating point.
"""

from __future__ import annotations

import hashlib
import json
import logging
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import numpy as np
import pandas as pd

from . import __version__
from . import features as F
from .baseline import BaselineModel
from .export_onnx import OnnxScorer, check_feature_contract
from .train import BASELINE_FILE, HELDOUT_FILE, META_FILE, MODEL_FILE, ONNX_FILE

log = logging.getLogger(__name__)

REPORT_FILE = "gate_report.json"


@dataclass
class OperatingPoint:
    threshold: float
    tp: int
    fp: int
    tn: int
    fn: int

    @property
    def tpr(self) -> float:
        return self.tp / (self.tp + self.fn) if (self.tp + self.fn) else 0.0

    @property
    def fpr(self) -> float:
        return self.fp / (self.fp + self.tn) if (self.fp + self.tn) else 0.0

    def to_dict(self) -> dict[str, Any]:
        d = asdict(self)
        d.update(tpr=self.tpr, fpr=self.fpr)
        return d


def operating_point(y: np.ndarray, scores: np.ndarray, threshold: float) -> OperatingPoint:
    y = np.asarray(y).astype(int)
    flagged = np.asarray(scores, dtype=np.float64) >= threshold
    return OperatingPoint(
        threshold=float(threshold),
        tp=int(np.sum(flagged & (y == 1))),
        fp=int(np.sum(flagged & (y == 0))),
        tn=int(np.sum(~flagged & (y == 0))),
        fn=int(np.sum(~flagged & (y == 1))),
    )


def tpr_at_fpr(y: np.ndarray, scores: np.ndarray, max_fpr: float) -> tuple[float, float]:
    """Best TPR over thresholds whose FPR <= max_fpr on this data, and that threshold."""
    y = np.asarray(y).astype(int)
    s = np.asarray(scores, dtype=np.float64)
    cand = np.unique(np.concatenate([s, [np.inf]]))
    best = (0.0, float("inf"))
    n_pos, n_neg = max(1, int((y == 1).sum())), max(1, int((y == 0).sum()))
    neg = np.sort(s[y == 0])
    pos = np.sort(s[y == 1])
    fp = len(neg) - np.searchsorted(neg, cand, side="left")
    tp = len(pos) - np.searchsorted(pos, cand, side="left")
    ok = fp / n_neg <= max_fpr + 1e-12
    if ok.any():
        tprs = np.where(ok, tp / n_pos, -1.0)
        i = int(np.argmax(tprs))
        best = (float(tprs[i]), float(cand[i]))
    return best


@dataclass
class GateResult:
    passed: bool
    reasons: list[str]
    baseline: OperatingPoint
    model: OperatingPoint
    criteria: dict[str, Any] = field(default_factory=dict)

    def to_dict(self) -> dict[str, Any]:
        return {
            "passed": self.passed,
            "reasons": self.reasons,
            "criteria": self.criteria,
            "baseline": self.baseline.to_dict(),
            "model": self.model.to_dict(),
        }


def gate(
    y: np.ndarray,
    model_scores: np.ndarray,
    model_threshold: float,
    baseline_scores: np.ndarray,
    baseline_threshold: float,
    *,
    groups: np.ndarray | None = None,
    min_positives: int = 50,
    min_negatives: int = 50,
    min_players_per_class: int = 5,
    min_tpr_gain: float = 0.0,
) -> GateResult:
    y = np.asarray(y).astype(int)
    b = operating_point(y, baseline_scores, baseline_threshold)
    m = operating_point(y, model_scores, model_threshold)
    reasons: list[str] = []
    n_pos, n_neg = int((y == 1).sum()), int((y == 0).sum())
    if n_pos < min_positives:
        reasons.append(f"insufficient held-out cheat windows: {n_pos} < {min_positives}")
    if n_neg < min_negatives:
        reasons.append(f"insufficient held-out legit windows: {n_neg} < {min_negatives}")
    if groups is not None:
        g = pd.DataFrame({"g": np.asarray(groups), "y": y}).groupby("g")["y"].max()
        pp, pn = int((g == 1).sum()), int((g == 0).sum())
        if pp < min_players_per_class:
            reasons.append(f"insufficient held-out cheat players: {pp} < {min_players_per_class}")
        if pn < min_players_per_class:
            reasons.append(f"insufficient held-out legit players: {pn} < {min_players_per_class}")
    if m.fpr > b.fpr:
        reasons.append(f"model raises false positives: FPR {m.fpr:.4f} > baseline {b.fpr:.4f}")
    if not m.tpr > b.tpr + min_tpr_gain:
        reasons.append(
            f"model does not beat the baseline: TPR {m.tpr:.4f} <= baseline {b.tpr:.4f}"
            + (f" + {min_tpr_gain:g}" if min_tpr_gain else "")
        )
    criteria = {
        "rule": "pass iff model FPR <= baseline FPR and model TPR > baseline TPR + min_tpr_gain",
        "min_tpr_gain": min_tpr_gain,
        "min_positives": min_positives,
        "min_negatives": min_negatives,
        "min_players_per_class": min_players_per_class,
    }
    return GateResult(not reasons, reasons, b, m, criteria)


def _auc(y: np.ndarray, s: np.ndarray) -> dict[str, float | None]:
    from sklearn.metrics import average_precision_score, roc_auc_score

    if len(np.unique(y)) < 2:
        return {"roc_auc": None, "pr_auc": None}
    return {"roc_auc": float(roc_auc_score(y, s)), "pr_auc": float(average_precision_score(y, s))}


def _breakdown(df: pd.DataFrame, col: str, flag_m: np.ndarray, flag_b: np.ndarray) -> dict[str, Any]:
    out: dict[str, Any] = {}
    if col not in df.columns:
        return out
    pos = df["y"].to_numpy() == 1
    values = df[col].fillna("").astype(str).to_numpy()
    for v in sorted(set(values[pos])):
        sel = pos & (values == v)
        if not sel.any():
            continue
        out[v or "UNSPECIFIED"] = {
            "windows": int(sel.sum()),
            "players": int(df.loc[sel, "player"].nunique()),
            "model_tpr": float(flag_m[sel].mean()),
            "baseline_tpr": float(flag_b[sel].mean()),
        }
    return out


def _fpr_by_source(df: pd.DataFrame, flag_m: np.ndarray, flag_b: np.ndarray) -> dict[str, Any]:
    out: dict[str, Any] = {}
    if "label_source" not in df.columns:
        return out
    neg = df["y"].to_numpy() == 0
    src = df["label_source"].fillna("").astype(str).to_numpy()
    for v in sorted(set(src[neg])):
        sel = neg & (src == v)
        out[v or "unknown"] = {
            "windows": int(sel.sum()),
            "model_fpr": float(flag_m[sel].mean()),
            "baseline_fpr": float(flag_b[sel].mean()),
        }
    return out


def bootstrap_deltas(
    y: np.ndarray, flag_m: np.ndarray, flag_b: np.ndarray, groups: np.ndarray, n: int = 200, seed: int = 7
) -> dict[str, Any]:
    """Player-level bootstrap of (TPR_m - TPR_b) and (FPR_m - FPR_b), 95% percentile intervals."""
    rng = np.random.default_rng(seed)
    df = pd.DataFrame({"g": groups, "y": y.astype(int), "m": flag_m.astype(int), "b": flag_b.astype(int)})
    df["tp_m"], df["tp_b"] = df.m * df.y, df.b * df.y
    df["fp_m"], df["fp_b"] = df.m * (1 - df.y), df.b * (1 - df.y)
    df["pos"], df["neg"] = df.y, 1 - df.y
    per = df.groupby("g")[["tp_m", "tp_b", "fp_m", "fp_b", "pos", "neg"]].sum().to_numpy(dtype=np.float64)
    if len(per) < 2:
        return {"iterations": 0}
    dt, df_ = [], []
    for _ in range(n):
        s = per[rng.integers(0, len(per), len(per))].sum(axis=0)
        if s[4] == 0 or s[5] == 0:
            continue
        dt.append((s[0] - s[1]) / s[4])
        df_.append((s[2] - s[3]) / s[5])
    if not dt:
        return {"iterations": 0}

    def ci(a: list[float]) -> list[float]:
        return [float(np.percentile(a, 2.5)), float(np.percentile(a, 97.5))]

    return {"iterations": len(dt), "delta_tpr_95ci": ci(dt), "delta_fpr_95ci": ci(df_)}


def evaluate_model_dir(
    model_dir: str | Path,
    *,
    heldout: str | Path | None = None,
    report_path: str | Path | None = None,
    baseline_threshold: float | None = None,
    model_threshold: float | None = None,
    use_sklearn: bool = False,
    threshold_mode: str = "matched",
    min_positives: int = 50,
    min_negatives: int = 50,
    min_players_per_class: int = 5,
    min_tpr_gain: float = 0.0,
) -> tuple[dict[str, Any], int]:
    model_dir = Path(model_dir)
    meta_doc = json.loads((model_dir / META_FILE).read_text(encoding="utf-8"))
    metadata = meta_doc["metadata"]
    df = pd.read_parquet(Path(heldout) if heldout else model_dir / HELDOUT_FILE)
    if "y" not in df.columns:
        raise ValueError("held-out data has no labels (column y)")
    X = F.feature_frame(df)
    y = df["y"].to_numpy(dtype=np.int64)
    groups = df["player"].astype(str).to_numpy()

    if use_sklearn:
        import joblib

        model = joblib.load(model_dir / MODEL_FILE)
        p = model.predict_proba(X.astype(np.float64))[:, 1].astype(np.float32)
        scored_with = "scikit-learn"
        model_sha256 = None
    else:
        model_sha256 = hashlib.sha256((model_dir / ONNX_FILE).read_bytes()).hexdigest()
        scorer = OnnxScorer(model_dir / ONNX_FILE)
        check_feature_contract(scorer.metadata)
        metadata = scorer.metadata
        p = scorer.predict_proba(X)
        scored_with = "onnxruntime"
    deploy_thr = float(metadata["pandaeye.threshold"])

    baseline = BaselineModel.load(model_dir / BASELINE_FILE)
    if baseline_threshold is not None:
        baseline.threshold = float(baseline_threshold)
    s = baseline.score(X)
    baseline_op = operating_point(y, s, baseline.threshold)
    matched_tpr, matched_thr = tpr_at_fpr(y, p, baseline_op.fpr)
    if model_threshold is not None:
        m_thr, mode = float(model_threshold), "explicit"
    elif threshold_mode == "matched":
        m_thr, mode = matched_thr, "matched"
    elif threshold_mode == "fixed":
        m_thr, mode = deploy_thr, "fixed"
    else:
        raise ValueError("threshold_mode must be 'matched' or 'fixed'")

    result = gate(
        y,
        p,
        m_thr,
        s,
        baseline.threshold,
        groups=groups,
        min_positives=min_positives,
        min_negatives=min_negatives,
        min_players_per_class=min_players_per_class,
        min_tpr_gain=min_tpr_gain,
    )
    flag_m, flag_b = p >= m_thr, s >= baseline.threshold
    synthetic = metadata.get("pandaeye.trained_on_synthetic") == "true" or bool(meta_doc.get("synthetic"))
    if "tenant_id" in df.columns:
        synthetic = synthetic or bool(df["tenant_id"].astype(str).str.startswith("synthetic-").any())
    deploy_op = operating_point(y, p, deploy_thr)
    deploy_ok = deploy_op.fpr <= baseline_op.fpr
    report = {
        "pipeline_version": __version__,
        "feature_version": F.FEATURE_VERSION,
        "evaluated_at": datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z"),
        "model_dir": str(model_dir),
        "scored_with": scored_with,
        "model_sha256": model_sha256,
        "synthetic": synthetic,
        "deployable": bool(result.passed and not synthetic and deploy_ok),
        "gate": {**result.to_dict(), "threshold_mode": mode},
        "operating_points": {
            "baseline": baseline_op.to_dict(),
            "model_matched_fpr": {**operating_point(y, p, matched_thr).to_dict()},
            "model_deploy_threshold": {**deploy_op.to_dict(), "fpr_within_baseline": deploy_ok},
        },
        "heldout": {
            "windows": int(len(y)),
            "positives": int((y == 1).sum()),
            "negatives": int((y == 0).sum()),
            "players": int(len(np.unique(groups))),
        },
        "model": {
            "name": metadata.get("pandaeye.model_name", ""),
            "gate_threshold": m_thr,
            "deploy_threshold": deploy_thr,
            **_auc(y, p),
        },
        "baseline": {"threshold": baseline.threshold, **_auc(y, s)},
        "per_category": _breakdown(df, "category", flag_m, flag_b),
        "per_archetype": _breakdown(df, "archetype", flag_m, flag_b) if synthetic else {},
        "fpr_by_label_source": _fpr_by_source(df, flag_m, flag_b),
        "bootstrap": bootstrap_deltas(y, flag_m, flag_b, groups),
    }
    if synthetic:
        report["warning"] = "SYNTHETIC DATA: this result only shows that the pipeline runs. Never deploy this model."
    out = Path(report_path) if report_path else model_dir / REPORT_FILE
    out.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    return report, 0 if result.passed else 1
