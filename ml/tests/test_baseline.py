"""The heuristic baseline mirrors CHECKS.md section 4 scoring (raw points, check caps, family caps)."""

import re

import numpy as np
import pandas as pd
import pytest

from pandaeye_ml import baseline as B
from pandaeye_ml import features as F

from .conftest import REPO_DIR

CHECKS_MD = REPO_DIR / "docs" / "CHECKS.md"


def frame(**overrides):
    row = dict(zip(F.FEATURE_NAMES, F.DEFAULTS))
    row.update(overrides)
    return pd.DataFrame([row])


def test_neutral_window_scores_zero():
    assert B.score(frame())[0] == 0.0
    assert B.score(np.tile(F.DEFAULTS, (4, 1))).tolist() == [0.0] * 4


def test_certain_local_flag_crosses_alert():
    s = B.score(frame(flag_protocol_pitch=1))[0]
    assert s == 25.0 and s >= B.ALERT_THRESHOLD


def test_check_and_family_caps():
    assert B.score(frame(flag_protocol_pitch=100))[0] == 200.0  # protocol family cap
    # killaura.multi cap 120 = killaura family cap; noswing adds nothing beyond the family cap
    s = B.score(frame(multi_target_ticks=50, attack_count=100, noswing_share=1.0))[0]
    assert s == 120.0


def test_share_based_estimates_are_monotone():
    base = dict(attack_eligible_count=40, attack_count=40)
    low = B.score(frame(hitbox_miss_share=0.2, **base))[0]
    mid = B.score(frame(hitbox_miss_share=0.5, **base))[0]
    high = B.score(frame(hitbox_miss_share=0.9, **base))[0]
    assert low == 0 < mid < high
    assert B.score(frame(reach_p50=2.6, reach_p95=2.95, reach_max=3.0, **base))[0] == 0
    assert B.score(frame(reach_p50=3.4, reach_p95=3.6, reach_max=3.7, **base))[0] > B.ALERT_THRESHOLD


def test_calibration_roundtrip(tmp_path):
    rng = np.random.default_rng(0)
    s = np.concatenate([rng.exponential(2, 200), rng.exponential(20, 200)])
    y = np.r_[np.zeros(200), np.ones(200)]
    m = B.BaselineModel(threshold=15).fit(s, y)
    p = m.proba_from_score(np.array([0.0, 100.0]))
    assert p[0] < 0.5 < p[1]
    path = m.save(tmp_path / "b.json")
    m2 = B.BaselineModel.load(path)
    assert m2.threshold == 15 and m2.coef == pytest.approx(m.coef)
    assert B.threshold_for_leniency(15, 4) == pytest.approx(19.5)


@pytest.mark.skipif(not CHECKS_MD.exists(), reason="docs/CHECKS.md not available")
def test_catalog_matches_checks_md():
    text = CHECKS_MD.read_text(encoding="utf-8")
    section = text.split("## 4. Scoring summary", 1)[1]
    rows = re.findall(r"^\| ([a-z]+\.[a-z]+)( \(off\))? \| (local|cloud) \| (\w+) \| [^|]+ \| [^|]+ \| [^|]+ \| (\d+) \|", section, re.M)
    assert rows
    doc_caps = {name: float(cap) for name, off, _, _, cap in rows if not off}
    assert set(doc_caps) == set(B.CHECK_CAPS), "baseline must cover every enabled check"
    for name, cap in doc_caps.items():
        family, code_cap = B.CHECK_CAPS[name]
        assert code_cap == cap, name
        assert family == name.split(".")[0], name
    fam = re.search(r"Family caps \([^)]*\): (.+?)\.", section, re.S).group(1)
    doc_fam = {k: float(v) for k, v in re.findall(r"(\w+) (\d+)", fam)}
    doc_fam.pop("ml", None)  # the ML family is the model itself
    assert doc_fam == {k: float(v) for k, v in B.FAMILY_CAPS.items()}
    local = {name for name, _, where, _, _ in rows if where == "local"}
    assert local == set(B.LOCAL_FLAG_POINTS)
