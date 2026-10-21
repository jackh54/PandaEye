"""Heuristic baseline: what the shipped checks would score for one window, from the same features.

The gate (evaluate.py) compares the model against this baseline, so it mirrors the verdict scoring of
ARCHITECTURE 5.6 / CHECKS.md section 4 as closely as window aggregates allow:

- per check, an estimate of the violations the check would emit inside the window (from the shares and
  counts that summarise the same behaviour), times the check's raw points (Signal.points are raw points;
  confidence is applied separately and is not part of S),
- capped per check (``cap``) and per family (family caps), summed over families: ``S_window``.

``S_window`` ignores decay and history (the accumulators of a real session integrate several windows), so
it is a per-window approximation, documented as such. The operating threshold defaults to the base ALERT
threshold (15 points at leniency 3): a window "is flagged" when the heuristics alone would put a fresh
player at ALERT. ``BaselineModel`` adds a one-feature logistic calibration (on log1p(S)) so probabilistic
metrics can be reported; it does not change the ranking or the operating point.
"""

from __future__ import annotations

import json
import math
from dataclasses import dataclass
from pathlib import Path

import numpy as np
import pandas as pd

from . import features as F

ALERT_THRESHOLD = 15.0
MITIGATE_THRESHOLD = 30.0
LENIENCY_MULTIPLIERS = {1: 0.6, 2: 0.8, 3: 1.0, 4: 1.3, 5: 1.7}

# check id -> (family, cap) from CHECKS.md section 4 (autoclicker.cps is disabled by default: omitted).
CHECK_CAPS: dict[str, tuple[str, float]] = {
    "timer.balance": ("timer", 60),
    "movement.speed": ("movement", 50),
    "movement.fly": ("movement", 60),
    "movement.step": ("movement", 50),
    "movement.groundspoof": ("movement", 40),
    "movement.noslow": ("movement", 40),
    "dig.speed": ("dig", 50),
    "protocol.pitch": ("protocol", 200),
    "protocol.position": ("protocol", 200),
    "protocol.slot": ("protocol", 20),
    "protocol.selfhit": ("protocol", 200),
    "protocol.cursor": ("protocol", 15),
    "protocol.flood": ("protocol", 20),
    "protocol.abilities": ("protocol", 30),
    "inventory.closed": ("inventory", 20),
    "reach.distance": ("reach", 80),
    "killaura.hitbox": ("killaura", 80),
    "killaura.multi": ("killaura", 120),
    "killaura.noswing": ("killaura", 80),
    "aim.gcd": ("aim", 30),
    "aim.snap": ("aim", 50),
    "aim.precision": ("aim", 30),
    "velocity.vertical": ("velocity", 60),
    "velocity.horizontal": ("velocity", 50),
    "autoclicker.consistency": ("autoclicker", 30),
    "autoclicker.pattern": ("autoclicker", 30),
    "scaffold.face": ("scaffold", 80),
    "scaffold.ray": ("scaffold", 80),
    "scaffold.cursor": ("scaffold", 60),
    "scaffold.reach": ("scaffold", 50),
    "dig.ray": ("dig", 60),
    "inventory.move": ("inventory", 30),
    "inventory.rotate": ("inventory", 30),
    "latency.pingspoof": ("latency", 18),
    "latency.blink": ("latency", 18),
}

FAMILY_CAPS: dict[str, float] = {
    "timer": 60,
    "movement": 80,
    "protocol": 200,
    "inventory": 40,
    "reach": 80,
    "killaura": 120,
    "aim": 45,
    "velocity": 60,
    "autoclicker": 40,
    "scaffold": 100,
    "dig": 70,
    "latency": 25,
}

# Raw points per forwarded local flag; severity-scaled checks use the midpoint of their range.
LOCAL_FLAG_POINTS: dict[str, float] = {
    "timer.balance": 6,
    "movement.speed": 4,  # 2..6
    "movement.fly": 7,  # 4..10
    "movement.step": 6,  # 4..8
    "movement.groundspoof": 4.5,  # 3..6
    "movement.noslow": 4.5,  # 3..6
    "dig.speed": 5,  # 3..7
    "protocol.pitch": 25,
    "protocol.position": 25,
    "protocol.slot": 10,
    "protocol.selfhit": 25,
    "protocol.cursor": 3,
    "protocol.flood": 3,
    "protocol.abilities": 5,
    "inventory.closed": 3,
}


def _cols(df: pd.DataFrame | np.ndarray) -> dict[str, np.ndarray]:
    if isinstance(df, np.ndarray):
        if df.ndim != 2 or df.shape[1] != F.N_FEATURES:
            raise ValueError(f"expected (n, {F.N_FEATURES}) features")
        return {n: df[:, i].astype(np.float64) for i, n in enumerate(F.FEATURE_NAMES)}
    return {n: df[n].to_numpy(dtype=np.float64) for n in F.FEATURE_NAMES}


def check_points(features: pd.DataFrame | np.ndarray) -> pd.DataFrame:
    """Estimated raw points per check per window (before check and family caps)."""
    f = _cols(features)
    fl = np.floor
    pts: dict[str, np.ndarray] = {}

    # reach.distance: excess > 0.05 on 2 of the last 5 eligible attacks.
    n_att = f["attack_eligible_count"]
    e50 = f["reach_p50"] - 3.0
    e95 = f["reach_p95"] - 3.0
    share = np.where(e50 > 0.05, 0.5, np.where(e95 > 0.05, 0.25, 0.0))
    hits = n_att * share
    k = np.where(hits >= 2, fl(hits / 2), 0)
    excess = np.where(e50 > 0.05, e50, e95)
    pts["reach.distance"] = k * (3 + 10 * np.minimum(np.maximum(excess, 0), 1.5) / 1.5)

    # killaura.hitbox: >= 3 of the last 8 attacks missed.
    misses = n_att * f["hitbox_miss_share"]
    pts["killaura.hitbox"] = np.where((f["hitbox_miss_share"] >= 3 / 8) & (misses >= 3), fl(misses / 3), 0) * 5
    # killaura.multi: one violation per multi-target tick.
    pts["killaura.multi"] = f["multi_target_ticks"] * 10
    # killaura.noswing: >= 3 of the last 10 attacks without swing.
    ns = f["attack_count"] * f["noswing_share"]
    pts["killaura.noswing"] = np.where((f["noswing_share"] >= 0.3) & (ns >= 3), fl(ns / 3), 0) * 6

    # aim.gcd: calibrated, combat off-grid share > 0.4 over >= 30 deltas, idle still >= 90% on-grid.
    gcd = (
        (f["gcd_calibrated"] >= 0.5)
        & (f["gcd_combat_ongrid_share"] >= 0)
        & (1 - f["gcd_combat_ongrid_share"] > 0.4)
        & (f["rot_combat_count"] >= 30)
        & (f["gcd_idle_ongrid_share"] >= 0.9)
    )
    pts["aim.gcd"] = np.where(gcd, 4, 0)
    # aim.snap: >= 3 snaps in the last 20 attacks.
    pts["aim.snap"] = fl(f["snap_count"] / 3) * 5
    # aim.precision: mean < 1.2 deg and sd < 0.6 over 25 attacks (once per 60 s).
    prec = (
        (f["aim_precision_mean"] >= 0)
        & (f["aim_precision_mean"] < 1.2)
        & (f["aim_precision_sd"] >= 0)
        & (f["aim_precision_sd"] < 0.6)
        & (n_att >= 10)
    )
    pts["aim.precision"] = np.where(prec, 4, 0)

    # velocity.vertical: >= 4 of the last 6 eligible velocities ignored.
    ign = f["velocity_count"] * f["velocity_vertical_ignored_share"]
    pts["velocity.vertical"] = np.where((f["velocity_vertical_ignored_share"] >= 4 / 6) & (ign >= 4), fl(ign / 4), 0) * 6
    # velocity.horizontal: >= 4 of the last 6 matched velocities with shortfall > 0.15.
    sh = f["velocity_h_count"] * f["velocity_h_shortfall_share"]
    pts["velocity.horizontal"] = np.where((f["velocity_h_shortfall_share"] >= 4 / 6) & (sh >= 4), fl(sh / 4), 0) * 5

    # autoclicker.consistency / pattern: per 40-click window at 8..16 CPS.
    cps = f["click_cps_mean"]
    cps_ok = (cps >= 8) & (cps <= 16) & (f["click_count"] >= 40)
    blocks = fl(f["click_count"] / 40)
    cons = cps_ok & (f["click_interval_mode_share"] >= 0.95) & (f["click_interval_sd"] >= 0) & (f["click_interval_sd"] < 0.25)
    pts["autoclicker.consistency"] = np.where(cons, blocks, 0) * 4
    patt = cps_ok & (f["click_interval_distinct"] >= 2) & (f["click_pattern_share"] >= 0.9)
    pts["autoclicker.pattern"] = np.where(patt, blocks, 0) * 4

    # scaffold.*
    n_pl = f["place_eligible_count"]
    pts["scaffold.face"] = np.round(n_pl * f["place_face_invalid_share"]) * 8
    rm = n_pl * f["place_ray_miss_share"]
    pts["scaffold.ray"] = np.where((f["place_ray_miss_share"] >= 0.3) & (rm >= 3), fl(rm / 3), 0) * 6
    cm = n_pl * f["place_cursor_mismatch_share"]
    pts["scaffold.cursor"] = np.where((f["place_cursor_mismatch_share"] >= 0.3) & (cm >= 3), fl(cm / 3), 0) * 5
    rex = f["place_reach_max"] - (4.5 + 0.6)
    pts["scaffold.reach"] = np.where(rex > 0, 3 + 5 * np.minimum(rex, 1), 0)
    # dig.ray: >= 4 of the last 10 digs missed.
    dm = f["dig_eligible_count"] * f["dig_ray_miss_share"]
    pts["dig.ray"] = np.where((f["dig_ray_miss_share"] >= 0.4) & (dm >= 4), fl(dm / 4), 0) * 5

    # inventory.move (>= 3 ticks in 5 s) / inventory.rotate (>= 4 in 5 s).
    pts["inventory.move"] = fl(f["inv_move_ticks"] / 3) * 4
    pts["inventory.rotate"] = fl(f["inv_rotate_ticks"] / 4) * 3

    # latency.pingspoof: keep-alive RTT > pong + 150 ms and > 1.6x pong (=> delta > 0.6 * pong).
    ping = np.maximum(f["ping_median_ms"], 0)
    spoof = (f["ka_paired_count"] >= 4) & (f["ka_pong_delta_ms"] > 150) & (f["ka_pong_delta_ms"] > 0.6 * ping)
    pts["latency.pingspoof"] = np.where(spoof, 3, 0)
    # latency.blink: >= 3 blinks in 60 s.
    pts["latency.blink"] = fl(f["gap_burst_count"] / 3) * 3

    # Forwarded local flags.
    for check, p in LOCAL_FLAG_POINTS.items():
        pts[check] = pts.get(check, 0) + f[F.FLAG_FEATURES[check]] * p

    out = pd.DataFrame({c: np.asarray(pts.get(c, np.zeros(len(n_att))), dtype=np.float64) for c in CHECK_CAPS})
    return out.clip(lower=0)


def score(features: pd.DataFrame | np.ndarray) -> np.ndarray:
    """S_window = sum over families of min(familyCap, sum over checks of min(points, cap))."""
    pts = check_points(features)
    total = np.zeros(len(pts), dtype=np.float64)
    for fam, cap in FAMILY_CAPS.items():
        checks = [c for c, (fm, _) in CHECK_CAPS.items() if fm == fam]
        fam_sum = sum(np.minimum(pts[c].to_numpy(), CHECK_CAPS[c][1]) for c in checks)
        total += np.minimum(fam_sum, cap)
    return total


@dataclass
class BaselineModel:
    threshold: float = ALERT_THRESHOLD
    coef: float = 1.0
    intercept: float = -3.0

    def score(self, features: pd.DataFrame | np.ndarray) -> np.ndarray:
        return score(features)

    def proba_from_score(self, s: np.ndarray) -> np.ndarray:
        z = self.coef * np.log1p(np.maximum(s, 0)) + self.intercept
        return 1.0 / (1.0 + np.exp(-z))

    def fit(self, s: np.ndarray, y: np.ndarray) -> BaselineModel:
        """Calibrate P(cheat) = sigmoid(a * log1p(S) + b) (Platt-style, for reporting only)."""
        from sklearn.linear_model import LogisticRegression

        y = np.asarray(y)
        if len(np.unique(y)) < 2:
            return self
        lr = LogisticRegression(C=1e4, max_iter=1000)
        lr.fit(np.log1p(np.maximum(s, 0)).reshape(-1, 1), y)
        self.coef = float(lr.coef_[0, 0])
        self.intercept = float(lr.intercept_[0])
        return self

    def flagged(self, s: np.ndarray) -> np.ndarray:
        return np.asarray(s) >= self.threshold

    def to_json(self) -> dict:
        return {
            "kind": "pandaeye-heuristic-baseline",
            "feature_version": F.FEATURE_VERSION,
            "threshold": self.threshold,
            "calibration": {"coef": self.coef, "intercept": self.intercept, "input": "log1p(score)"},
        }

    def save(self, path: str | Path) -> Path:
        path = Path(path)
        path.write_text(json.dumps(self.to_json(), indent=2) + "\n", encoding="utf-8")
        return path

    @classmethod
    def load(cls, path: str | Path) -> BaselineModel:
        d = json.loads(Path(path).read_text(encoding="utf-8"))
        cal = d.get("calibration", {})
        return cls(
            threshold=float(d.get("threshold", ALERT_THRESHOLD)),
            coef=float(cal.get("coef", 1.0)),
            intercept=float(cal.get("intercept", -3.0)),
        )


def threshold_for_leniency(base: float, leniency: int) -> float:
    if leniency not in LENIENCY_MULTIPLIERS:
        raise ValueError("leniency must be 1..5")
    return base * LENIENCY_MULTIPLIERS[leniency]


def _selfcheck() -> None:
    fams = {fm for fm, _ in CHECK_CAPS.values()}
    if fams - set(FAMILY_CAPS):
        raise AssertionError(f"families without caps: {fams - set(FAMILY_CAPS)}")
    if not all(math.isfinite(v) for v in LOCAL_FLAG_POINTS.values()):
        raise AssertionError("bad local flag points")


_selfcheck()
