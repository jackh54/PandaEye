"""The synthetic generator: clearly labelled synthetic, valid feature values, deterministic."""

import json

import numpy as np

from pandaeye_ml import dataset as D
from pandaeye_ml import features as F
from pandaeye_ml import synth


def test_outputs_are_labelled_synthetic(synth_dir):
    for key in ("windows", "labels", "trusted", "erased"):
        assert synth_dir[key].name.startswith("SYNTHETIC_")
    assert synth.SYNTHETIC_NOTICE in synth_dir["readme"].read_text()
    assert synth.SYNTHETIC_NOTICE in synth_dir["trusted"].read_text()
    records = D.read_ndjson(synth_dir["labels"])
    assert records and all(r["synthetic"] is True for r in records)
    assert all(r["tenant_id"].startswith(D.SYNTHETIC_TENANT_PREFIX) for r in records)
    w = D.load_windows_file(synth_dir["windows"])
    assert (w["synthetic"] == 1).all()
    assert w["tenant_id"].str.startswith(D.SYNTHETIC_TENANT_PREFIX).all()


def test_dataset_is_flagged_synthetic(synth_dataset):
    assert synth_dataset.synthetic
    st = synth_dataset.stats
    assert st["positives"] > 100 and st["negatives"] > 100
    assert st["windows_untrusted_or_erased"] > 0 and st["windows_non_tick_client"] > 0
    assert st["windows_unlabelled"] > 0


def test_feature_values_follow_the_contract(synth_dir):
    w = D.load_windows_file(synth_dir["windows"])
    X = w[list(F.FEATURE_NAMES)].to_numpy()
    assert np.isfinite(X).all()
    in_range = (X >= F.LOWER) & (X <= F.UPPER)
    assert (in_range | (X == F.DEFAULTS)).all()
    assert np.array_equal(F.sanitize(X), X.astype(np.float32))
    # windows on the 30 s grid
    assert ((w["window_start_us"] % F.WINDOW_US) == 0).all()
    assert ((w["window_end_us"] - w["window_start_us"]) == F.WINDOW_US).all()


def test_every_archetype_and_category_appears(synth_dir):
    records = D.read_ndjson(synth_dir["labels"])
    archetypes = {r.get("synthetic_archetype") for r in records if r["label"] == "cheat"}
    assert set(synth.ARCHETYPES) <= archetypes
    assert {r["source"] for r in records} == {"confirmed", "overturned", "manual"}


def test_deterministic():
    cfg = synth.SynthConfig(players=30, windows_per_session=(2, 4), seed=11)
    a_w, a_l, _, _ = synth.generate(cfg)
    b_w, b_l, _, _ = synth.generate(cfg)
    assert a_w.equals(b_w)
    assert json.dumps(a_l, sort_keys=True, default=str) == json.dumps(b_l, sort_keys=True, default=str)
