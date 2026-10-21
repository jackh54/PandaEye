"""ONNX export of a trained model, parity verification with onnxruntime, and model metadata.

The Go scorer (``cloud/internal/ml``, build tag ``onnx``) loads the file with ONNX Runtime. Contract
(FEATURES.md section 8): input ``features`` float32 [batch, N], output ``probabilities`` float32
[batch, 2] with P(cheat) in column 1, metadata keys ``pandaeye.*``.
"""

from __future__ import annotations

import json
from collections.abc import Mapping
from pathlib import Path
from typing import Any

import numpy as np

from . import features as F

INPUT_NAME = "features"
PROBA_OUTPUT = "probabilities"
# Conservative opsets so that older ONNX Runtime shared libraries (as bundled for onnxruntime_go) load it.
TARGET_OPSET = {"": 17, "ai.onnx.ml": 3}
DEFAULT_ATOL = 1e-5


class ParityError(AssertionError):
    pass


def to_onnx_model(model: Any, n_features: int = F.N_FEATURES):
    from skl2onnx import to_onnx
    from skl2onnx.common.data_types import FloatTensorType

    onx = to_onnx(
        model,
        initial_types=[(INPUT_NAME, FloatTensorType([None, n_features]))],
        target_opset=dict(TARGET_OPSET),
        options={id(model): {"zipmap": False}},
    )
    outputs = [o.name for o in onx.graph.output]
    if PROBA_OUTPUT not in outputs:
        raise RuntimeError(f"unexpected ONNX outputs {outputs}")
    return onx


def set_metadata(onx, metadata: Mapping[str, Any]) -> None:
    existing = {p.key: p for p in onx.metadata_props}
    for k, v in metadata.items():
        value = v if isinstance(v, str) else json.dumps(v)
        if k in existing:
            existing[k].value = value
        else:
            p = onx.metadata_props.add()
            p.key, p.value = k, value


def build_metadata(
    *,
    model_name: str,
    threshold: float,
    trained_on_synthetic: bool,
    created_at: str,
    extra: Mapping[str, Any] | None = None,
) -> dict[str, str]:
    md = {
        "pandaeye.feature_version": str(F.FEATURE_VERSION),
        "pandaeye.feature_names": json.dumps(list(F.FEATURE_NAMES)),
        "pandaeye.n_features": str(F.N_FEATURES),
        "pandaeye.model_name": model_name,
        "pandaeye.threshold": repr(float(threshold)),
        "pandaeye.min_client_protocol": str(F.MIN_TICK_CLIENT_PROTOCOL),
        "pandaeye.trained_on_synthetic": "true" if trained_on_synthetic else "false",
        "pandaeye.created_at": created_at,
        "pandaeye.input": INPUT_NAME,
        "pandaeye.output": f"{PROBA_OUTPUT}[:, 1] = P(cheat)",
    }
    for k, v in (extra or {}).items():
        md[f"pandaeye.{k}"] = v if isinstance(v, str) else json.dumps(v)
    return md


def export_model(model: Any, path: str | Path, metadata: Mapping[str, Any]) -> Path:
    import onnx

    onx = to_onnx_model(model)
    set_metadata(onx, metadata)
    onx.producer_name = "pandaeye-ml"
    onx.doc_string = "PandaEye cheat classifier; see ml/FEATURES.md for the input contract."
    onnx.checker.check_model(onx)
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(onx.SerializeToString())
    return path


def read_metadata(path: str | Path) -> dict[str, str]:
    import onnx

    m = onnx.load(str(path), load_external_data=False)
    return {p.key: p.value for p in m.metadata_props}


def check_feature_contract(metadata: Mapping[str, str]) -> None:
    """Raise if a model's declared features differ from this pipeline's (what the Go scorer must do)."""
    version = metadata.get("pandaeye.feature_version")
    if version != str(F.FEATURE_VERSION):
        raise ValueError(f"model feature_version {version!r} != {F.FEATURE_VERSION}")
    names = json.loads(metadata.get("pandaeye.feature_names", "[]"))
    if list(names) != list(F.FEATURE_NAMES):
        raise ValueError("model feature_names differ from FEATURES.md v1")


class OnnxScorer:
    """P(cheat) from an ONNX file through onnxruntime (single-threaded, CPU)."""

    def __init__(self, path: str | Path):
        import onnxruntime as ort

        so = ort.SessionOptions()
        so.intra_op_num_threads = 1
        so.inter_op_num_threads = 1
        self.session = ort.InferenceSession(str(path), sess_options=so, providers=["CPUExecutionProvider"])
        self.metadata = dict(self.session.get_modelmeta().custom_metadata_map)

    def predict_proba(self, X: np.ndarray, batch: int = 65536) -> np.ndarray:
        X = np.ascontiguousarray(X, dtype=np.float32)
        out = []
        for i in range(0, len(X), batch):
            res = self.session.run([PROBA_OUTPUT], {INPUT_NAME: X[i : i + batch]})
            out.append(np.asarray(res[0], dtype=np.float32)[:, 1])
        return np.concatenate(out) if out else np.zeros(0, dtype=np.float32)


def verify_parity(model: Any, onnx_path: str | Path, X: np.ndarray, atol: float = DEFAULT_ATOL) -> float:
    """Max abs difference of P(cheat) between scikit-learn and ONNX Runtime; raises above ``atol``."""
    X32 = np.ascontiguousarray(X, dtype=np.float32)
    p_sk = model.predict_proba(X32.astype(np.float64))[:, 1]
    p_onnx = OnnxScorer(onnx_path).predict_proba(X32)
    if p_sk.shape != p_onnx.shape:
        raise ParityError(f"shape mismatch {p_sk.shape} vs {p_onnx.shape}")
    diff = float(np.max(np.abs(p_sk - p_onnx))) if len(X32) else 0.0
    if not np.isfinite(diff) or diff > atol:
        raise ParityError(f"ONNX output differs from scikit-learn by {diff:.3g} (> {atol:g})")
    return diff
