"""ONNX export: parity with scikit-learn, I/O contract and embedded metadata."""

import json

import numpy as np
import onnx
import pytest

from pandaeye_ml import export_onnx as E
from pandaeye_ml import features as F


@pytest.fixture(scope="module")
def exported(small_model, tmp_path_factory):
    model, X = small_model
    md = E.build_metadata(model_name="test", threshold=0.42, trained_on_synthetic=True, created_at="2026-10-21T00:00:00Z")
    path = E.export_model(model, tmp_path_factory.mktemp("onnx") / "model.onnx", md)
    return model, X, path


def test_parity_with_sklearn(exported):
    model, X, path = exported
    diff = E.verify_parity(model, path, X)
    assert diff < E.DEFAULT_ATOL
    # also on edge rows: all defaults, all clip minima, all clip maxima
    edge = np.vstack([F.DEFAULTS, F.LOWER, F.UPPER]).astype(np.float32)
    assert E.verify_parity(model, path, edge) < E.DEFAULT_ATOL


def test_io_contract(exported):
    _, X, path = exported
    m = onnx.load(str(path))
    (inp,) = m.graph.input
    assert inp.name == E.INPUT_NAME
    assert inp.type.tensor_type.elem_type == onnx.TensorProto.FLOAT
    dims = inp.type.tensor_type.shape.dim
    assert dims[1].dim_value == F.N_FEATURES
    assert E.PROBA_OUTPUT in [o.name for o in m.graph.output]
    opsets = {o.domain: o.version for o in m.opset_import}
    assert opsets[""] <= E.TARGET_OPSET[""]
    p = E.OnnxScorer(path).predict_proba(X[:7])
    assert p.shape == (7,) and p.dtype == np.float32 and np.all((p >= 0) & (p <= 1))


def test_metadata_embedded(exported):
    _, _, path = exported
    md = E.read_metadata(path)
    assert md["pandaeye.feature_version"] == str(F.FEATURE_VERSION)
    assert json.loads(md["pandaeye.feature_names"]) == list(F.FEATURE_NAMES)
    assert md["pandaeye.n_features"] == str(F.N_FEATURES)
    assert float(md["pandaeye.threshold"]) == 0.42
    assert md["pandaeye.trained_on_synthetic"] == "true"
    assert md["pandaeye.min_client_protocol"] == str(F.MIN_TICK_CLIENT_PROTOCOL)
    E.check_feature_contract(md)
    bad = dict(md, **{"pandaeye.feature_names": json.dumps(list(reversed(F.FEATURE_NAMES)))})
    with pytest.raises(ValueError):
        E.check_feature_contract(bad)
    with pytest.raises(ValueError):
        E.check_feature_contract(dict(md, **{"pandaeye.feature_version": "999"}))


def test_parity_check_detects_a_different_model(exported, small_model, synth_dataset, fast_config):
    from pandaeye_ml.train import fit_calibrated

    _, X, path = exported
    ds = synth_dataset
    other = fit_calibrated(ds.X, 1 - ds.y, ds.groups, fast_config)  # inverted labels
    with pytest.raises(E.ParityError):
        E.verify_parity(other, path, X)
