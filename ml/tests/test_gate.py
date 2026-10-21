"""Gate logic: worse fails, better passes, equal TPR with higher FPR fails."""

import numpy as np
import pytest

from pandaeye_ml.evaluate import gate, operating_point, tpr_at_fpr
from pandaeye_ml.train import select_threshold

N_POS, N_NEG = 200, 800


def labels():
    return np.r_[np.ones(N_POS, int), np.zeros(N_NEG, int)]


def flags(tp: int, fp: int) -> np.ndarray:
    """Scores in {0, 1}: `tp` positives and `fp` negatives flagged (threshold 0.5)."""
    s = np.zeros(N_POS + N_NEG)
    s[:tp] = 1
    s[N_POS : N_POS + fp] = 1
    return s


def run(model_tp, model_fp, base_tp, base_fp, **kw):
    y = labels()
    return gate(y, flags(model_tp, model_fp), 0.5, flags(base_tp, base_fp) * 20, 15.0, **kw)


def test_operating_point_counts():
    op = operating_point(labels(), flags(50, 8), 0.5)
    assert (op.tp, op.fp, op.fn, op.tn) == (50, 8, 150, 792)
    assert op.tpr == 0.25 and op.fpr == 0.01


def test_better_model_passes():
    r = run(model_tp=120, model_fp=8, base_tp=60, base_fp=8)
    assert r.passed, r.reasons
    r = run(model_tp=120, model_fp=4, base_tp=60, base_fp=8)  # higher TPR and lower FPR
    assert r.passed, r.reasons


def test_worse_model_fails():
    r = run(model_tp=40, model_fp=8, base_tp=60, base_fp=8)
    assert not r.passed
    assert any("does not beat" in m for m in r.reasons)


def test_equal_tpr_with_higher_fpr_fails():
    r = run(model_tp=60, model_fp=12, base_tp=60, base_fp=8)
    assert not r.passed
    assert any("raises false positives" in m for m in r.reasons)
    assert any("does not beat" in m for m in r.reasons)


def test_higher_tpr_with_higher_fpr_fails():
    r = run(model_tp=150, model_fp=9, base_tp=60, base_fp=8)
    assert not r.passed and any("raises false positives" in m for m in r.reasons)


def test_equal_tpr_equal_fpr_fails_and_min_gain():
    assert not run(model_tp=60, model_fp=8, base_tp=60, base_fp=8).passed
    assert not run(model_tp=62, model_fp=8, base_tp=60, base_fp=8, min_tpr_gain=0.05).passed
    assert run(model_tp=80, model_fp=8, base_tp=60, base_fp=8, min_tpr_gain=0.05).passed


def test_insufficient_data_fails():
    r = run(model_tp=120, model_fp=8, base_tp=60, base_fp=8, min_positives=500)
    assert not r.passed and any("insufficient" in m for m in r.reasons)
    groups = np.r_[np.repeat(["a", "b"], N_POS // 2), np.repeat(["c"], N_NEG)]
    r = run(model_tp=120, model_fp=8, base_tp=60, base_fp=8, groups=groups, min_players_per_class=3)
    assert not r.passed and any("players" in m for m in r.reasons)


def test_matched_fpr_lookup_never_exceeds_target():
    rng = np.random.default_rng(1)
    y = labels()
    s = np.r_[rng.normal(1, 1, N_POS), rng.normal(0, 1, N_NEG)]
    for target in (0.0, 0.01, 0.05, 0.2):
        tpr, thr = tpr_at_fpr(y, s, target)
        op = operating_point(y, s, thr)
        assert op.fpr <= target + 1e-12 and op.tpr == pytest.approx(tpr)
    # a model no better than chance cannot pass the matched gate against a useful baseline
    base = np.r_[rng.normal(2, 1, N_POS), rng.normal(0, 1, N_NEG)] * 10
    b_thr = 15.0
    noise = rng.random(N_POS + N_NEG)
    _, m_thr = tpr_at_fpr(y, noise, operating_point(y, base, b_thr).fpr)
    assert not gate(y, noise, m_thr, base, b_thr).passed


def test_select_threshold_respects_fpr_and_sits_between_scores():
    rng = np.random.default_rng(2)
    y = labels()
    p = np.r_[rng.beta(5, 2, N_POS), rng.beta(2, 5, N_NEG)].astype(np.float32)
    for max_fpr in (0.0, 0.005, 0.02, 0.1):
        t = select_threshold(y, p, max_fpr)
        fpr = np.mean(p[y == 0] >= t)
        assert fpr <= max_fpr + 1e-12
        assert not np.any(np.isclose(p, t, rtol=0, atol=1e-9)), "threshold must not coincide with a score"
    assert select_threshold(y, p, 1.0) <= p.min()
