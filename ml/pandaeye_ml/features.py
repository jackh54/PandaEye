"""Feature vector v1: machine-readable mirror of ``ml/FEATURES.md``.

FEATURES.md is the contract (the Go worker implements ``cloud/internal/ml/features.go`` from it).
This module holds the parts that code needs: the ordered names, defaults and clip ranges. A test
parses the table in FEATURES.md and asserts that it matches this module exactly, so the two cannot
drift apart silently. Changing names, order, defaults, clipping or definitions means bumping
``FEATURE_VERSION``.
"""

from __future__ import annotations

import math
from dataclasses import dataclass

import numpy as np
import pandas as pd

FEATURE_VERSION = 1

# Window geometry (normalised cloud time, microseconds).
WINDOW_US = 30_000_000
GRACE_US = 2_000_000

# Tick clients (CHECKS.md section 0): protocol >= 768 (1.21.2). The v1 model is trained on and
# applied to tick clients only.
MIN_TICK_CLIENT_PROTOCOL = 768


@dataclass(frozen=True)
class FeatureSpec:
    name: str
    group: str
    unit: str
    default: float
    lo: float
    hi: float


_F = FeatureSpec

_FLAG_CHECKS = (
    "timer.balance",
    "movement.speed",
    "movement.fly",
    "movement.step",
    "movement.groundspoof",
    "movement.noslow",
    "dig.speed",
    "protocol.pitch",
    "protocol.position",
    "protocol.slot",
    "protocol.selfhit",
    "protocol.cursor",
    "protocol.flood",
    "protocol.abilities",
    "inventory.closed",
)


def flag_feature_name(check_id: str) -> str:
    return "flag_" + check_id.replace(".", "_")


FEATURES: tuple[FeatureSpec, ...] = (
    # --- context -------------------------------------------------------------------------
    _F("active_seconds", "context", "s", 0, 0, 30),
    _F("move_count", "context", "count", 0, 0, 3000),
    _F("focus_share", "context", "share", 0, 0, 1),
    _F("server_tick_ms_max", "context", "ms", 50, 0, 1000),
    _F("server_stall_ms", "context", "ms", 0, 0, 30000),
    _F("env_special_share", "context", "share", 0, 0, 1),
    # --- clicks (autoclicker.*) ----------------------------------------------------------
    _F("click_count", "clicks", "count", 0, 0, 2000),
    _F("click_cps_mean", "clicks", "1/s", -1, 0, 60),
    _F("click_cps_peak", "clicks", "1/s", 0, 0, 60),
    _F("click_interval_count", "clicks", "count", 0, 0, 2000),
    _F("click_interval_mean", "clicks", "ticks", -1, 1, 10),
    _F("click_interval_sd", "clicks", "ticks", -1, 0, 5),
    _F("click_interval_skew", "clicks", "1", 0, -10, 10),
    _F("click_interval_kurtosis", "clicks", "1", 0, 1, 100),
    _F("click_interval_distinct", "clicks", "count", 0, 1, 10),
    _F("click_interval_mode_share", "clicks", "share", 0, 0, 1),
    _F("click_pattern_share", "clicks", "share", 0, 0, 1),
    _F("click_doubles_share", "clicks", "share", 0, 0, 1),
    # --- aim (aim.gcd, aim.snap, aim.precision) ------------------------------------------
    _F("rot_combat_count", "aim", "count", 0, 0, 2000),
    _F("rot_combat_yaw_mean", "aim", "deg/tick", -1, 0, 180),
    _F("rot_combat_yaw_sd", "aim", "deg/tick", -1, 0, 180),
    _F("rot_combat_pitch_mean", "aim", "deg/tick", -1, 0, 180),
    _F("rot_combat_pitch_sd", "aim", "deg/tick", -1, 0, 180),
    _F("rot_combat_yaw_accel_mean", "aim", "deg/tick^2", -1, 0, 360),
    _F("rot_idle_count", "aim", "count", 0, 0, 2000),
    _F("rot_idle_yaw_mean", "aim", "deg/tick", -1, 0, 180),
    _F("rot_idle_yaw_sd", "aim", "deg/tick", -1, 0, 180),
    _F("rot_idle_pitch_mean", "aim", "deg/tick", -1, 0, 180),
    _F("rot_idle_pitch_sd", "aim", "deg/tick", -1, 0, 180),
    _F("gcd_calibrated", "aim", "bool", 0, 0, 1),
    _F("gcd_combat_ongrid_share", "aim", "share", -1, 0, 1),
    _F("gcd_idle_ongrid_share", "aim", "share", -1, 0, 1),
    _F("snap_count", "aim", "count", 0, 0, 600),
    _F("snap_share", "aim", "share", 0, 0, 1),
    _F("aim_precision_mean", "aim", "deg", -1, 0, 90),
    _F("aim_precision_sd", "aim", "deg", -1, 0, 90),
    # --- combat (reach.distance, killaura.*) ---------------------------------------------
    _F("attack_count", "combat", "count", 0, 0, 2000),
    _F("attack_eligible_count", "combat", "count", 0, 0, 2000),
    _F("attack_target_distinct", "combat", "count", 0, 0, 100),
    _F("reach_p50", "combat", "blocks", -1, 0, 8),
    _F("reach_p95", "combat", "blocks", -1, 0, 8),
    _F("reach_max", "combat", "blocks", -1, 0, 8),
    _F("hitbox_miss_share", "combat", "share", 0, 0, 1),
    _F("noswing_share", "combat", "share", 0, 0, 1),
    _F("multi_target_ticks", "combat", "count", 0, 0, 600),
    # --- velocity (velocity.vertical, velocity.horizontal) -------------------------------
    _F("velocity_count", "velocity", "count", 0, 0, 200),
    _F("velocity_v_taken_ratio", "velocity", "ratio", -1, 0, 2),
    _F("velocity_vertical_ignored_share", "velocity", "share", 0, 0, 1),
    _F("velocity_jump_reset_share", "velocity", "share", -1, 0, 1),
    _F("velocity_h_count", "velocity", "count", 0, 0, 200),
    _F("velocity_h_taken_ratio", "velocity", "ratio", -1, 0, 2),
    _F("velocity_h_shortfall_share", "velocity", "share", 0, 0, 1),
    # --- movement: forwarded local flags, timer, CheckStats ------------------------------
    *(_F(flag_feature_name(c), "movement", "count", 0, 0, 100) for c in _FLAG_CHECKS),
    _F("flag_movement_severity_max", "movement", "share", 0, 0, 1),
    _F("timer_offset_slope", "movement", "ms/s", 0, -200, 200),
    _F("cs_movement_candidate_rate", "movement", "1/1000", -1, 0, 1000),
    _F("cs_movement_discard_share", "movement", "share", -1, 0, 1),
    _F("cs_speed_max_stat", "movement", "blocks/tick", -1, -0.5, 2),
    _F("cs_fly_max_stat", "movement", "blocks/tick", -1, -0.5, 5),
    # --- world interaction (scaffold.*, dig.*) -------------------------------------------
    _F("place_count", "world", "count", 0, 0, 1000),
    _F("place_eligible_count", "world", "count", 0, 0, 1000),
    _F("place_face_invalid_share", "world", "share", 0, 0, 1),
    _F("place_ray_miss_share", "world", "share", 0, 0, 1),
    _F("place_cursor_mismatch_share", "world", "share", 0, 0, 1),
    _F("place_reach_max", "world", "blocks", -1, 0, 10),
    _F("place_peak_per_s", "world", "1/s", 0, 0, 40),
    _F("dig_count", "world", "count", 0, 0, 1000),
    _F("dig_eligible_count", "world", "count", 0, 0, 2000),
    _F("dig_ray_miss_share", "world", "share", 0, 0, 1),
    _F("dig_peak_per_s", "world", "1/s", 0, 0, 60),
    # --- inventory (inventory.move, inventory.rotate) ------------------------------------
    _F("inv_click_count", "inventory", "count", 0, 0, 2000),
    _F("inv_click_interval_min_ms", "inventory", "ms", -1, 0, 5000),
    _F("inv_move_ticks", "inventory", "count", 0, 0, 600),
    _F("inv_rotate_ticks", "inventory", "count", 0, 0, 600),
    # --- latency (latency.pingspoof, latency.blink) --------------------------------------
    _F("ping_median_ms", "latency", "ms", -1, 0, 5000),
    _F("ping_jitter_ms", "latency", "ms", -1, 0, 5000),
    _F("ka_pong_delta_ms", "latency", "ms", 0, -1000, 5000),
    _F("ka_paired_count", "latency", "count", 0, 0, 100),
    _F("gap_count", "latency", "count", 0, 0, 60),
    _F("gap_burst_count", "latency", "count", 0, 0, 60),
)

FEATURE_NAMES: tuple[str, ...] = tuple(f.name for f in FEATURES)
N_FEATURES = len(FEATURES)
FEATURE_INDEX: dict[str, int] = {n: i for i, n in enumerate(FEATURE_NAMES)}
FLAG_FEATURES: dict[str, str] = {c: flag_feature_name(c) for c in _FLAG_CHECKS}

# Metadata columns of a feature window. NOT model inputs.
META_COLUMNS: tuple[str, ...] = (
    "tenant_id",
    "player",
    "server_id",
    "session_id",
    "window_start_us",
    "window_end_us",
    "client_protocol",
    "tick_client",
    "hot_loaded",
    "feature_version",
)

DEFAULTS = np.array([f.default for f in FEATURES], dtype=np.float64)
LOWER = np.array([f.lo for f in FEATURES], dtype=np.float64)
UPPER = np.array([f.hi for f in FEATURES], dtype=np.float64)


def _check_specs() -> None:
    if len(set(FEATURE_NAMES)) != N_FEATURES:
        raise AssertionError("duplicate feature names")
    for f in FEATURES:
        if not (f.lo < f.hi) or not all(math.isfinite(v) for v in (f.default, f.lo, f.hi)):
            raise AssertionError(f"bad spec for {f.name}")
        if set(FEATURE_NAMES) & set(META_COLUMNS):
            raise AssertionError("a feature name collides with a metadata column")


_check_specs()


def sanitize(values: np.ndarray) -> np.ndarray:
    """Apply the FEATURES.md rules to an (n, N_FEATURES) array, returning float32.

    NaN/Inf become the feature default; values equal to the default are kept as is (defaults may lie
    outside the clip range on purpose, so the model can tell "undefined" from any real value); every
    other value is clipped to [lo, hi]. Mirrors what the Go worker must do before writing a window.
    """
    x = np.array(values, dtype=np.float64, copy=True)
    if x.ndim != 2 or x.shape[1] != N_FEATURES:
        raise ValueError(f"expected (n, {N_FEATURES}) array, got {x.shape}")
    bad = ~np.isfinite(x)
    x[bad] = np.broadcast_to(DEFAULTS, x.shape)[bad]
    is_default = x == DEFAULTS
    clipped = np.clip(x, LOWER, UPPER)
    x = np.where(is_default, x, clipped)
    return x.astype(np.float32)


def feature_frame(df: pd.DataFrame) -> np.ndarray:
    """Extract the model input matrix (float32, sanitized) from a frame with named feature columns."""
    missing = [n for n in FEATURE_NAMES if n not in df.columns]
    if missing:
        raise ValueError(f"missing feature columns: {missing[:5]}{'...' if len(missing) > 5 else ''}")
    return sanitize(df.loc[:, list(FEATURE_NAMES)].to_numpy(dtype=np.float64))


def markdown_table_rows() -> list[str]:
    """Name/unit/default/clip skeleton of the FEATURES.md table (used when editing the document)."""

    def fmt(v: float) -> str:
        return str(int(v)) if float(v).is_integer() else repr(float(v))

    return [
        f"| {i} | `{f.name}` | {f.unit} | ... | {fmt(f.default)} | [{fmt(f.lo)}, {fmt(f.hi)}] |"
        for i, f in enumerate(FEATURES)
    ]
