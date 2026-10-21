"""Command line: ``python -m pandaeye_ml <export|train|onnx|evaluate|synth> ...``.

Secrets never go on the command line: the ClickHouse DSN and the API key are read from environment
variables whose names are flags (defaults ``PE_CLICKHOUSE_DSN`` and ``PE_API_KEY``).
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import sys
from pathlib import Path

from . import __version__
from . import features as F

log = logging.getLogger("pandaeye_ml")


def _env(name: str, required: bool = True) -> str | None:
    value = os.environ.get(name)
    if required and not value:
        raise SystemExit(f"environment variable {name} is not set")
    return value


def cmd_export(args: argparse.Namespace) -> int:
    from .dataset import fetch_labels_api, load_windows_clickhouse, save_windows, to_unix_us, write_ndjson

    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    since_us = to_unix_us(args.since) if args.since else None
    until_us = to_unix_us(args.until) if args.until else None
    tenants = None
    if args.trusted_tenants:
        from .dataset import load_tenant_allowlist

        tenants = load_tenant_allowlist(args.trusted_tenants)
    windows = load_windows_clickhouse(
        _env(args.clickhouse_dsn_env), since_us=since_us, until_us=until_us, tenants=tenants
    )
    wpath = save_windows(windows, out / "feature_windows.parquet")
    records = fetch_labels_api(args.api_url, _env(args.api_key_env), since=args.since)
    lpath = write_ndjson(records, out / "labels.ndjson")
    print(json.dumps({"windows": str(wpath), "window_rows": len(windows), "labels": str(lpath), "label_rows": len(records)}))
    return 0


def _load_inputs(args: argparse.Namespace):
    from .dataset import build_dataset, load_erasures, load_tenant_allowlist, load_windows_file, parse_label_records, read_ndjson

    if not args.trusted_tenants:
        raise SystemExit("--trusted-tenants is required (labels only from trusted tenants, ARCHITECTURE 5.10)")
    windows = load_windows_file(args.windows)
    labels = parse_label_records(read_ndjson(args.labels))
    trusted = load_tenant_allowlist(args.trusted_tenants)
    erased = load_erasures(args.erased)
    return build_dataset(windows, labels, trusted, erased, tick_clients_only=not args.all_clients)


def cmd_train(args: argparse.Namespace) -> int:
    from .train import TrainConfig, train

    ds = _load_inputs(args)
    if ds.synthetic:
        log.warning("SYNTHETIC data detected: the model will be marked trained_on_synthetic=true (never deploy it)")
    cfg = TrainConfig(
        max_iter=args.max_iter,
        learning_rate=args.learning_rate,
        max_leaf_nodes=args.max_leaf_nodes,
        min_samples_leaf=args.min_samples_leaf,
        max_bins=args.max_bins,
        class_weight=None if args.class_weight == "none" else args.class_weight,
        monotonic=not args.no_monotonic,
        calibration_folds=args.calibration_folds,
        test_fraction=args.test_fraction,
        threshold_folds=args.threshold_folds,
        threshold_fpr_margin=args.threshold_fpr_margin,
        seed=args.seed,
        baseline_threshold=args.baseline_threshold,
        model_name=args.model_name,
    )
    report = train(ds, cfg, args.out)
    print(json.dumps({"out": str(args.out), "threshold": report["threshold"], "splits": report["splits"],
                      "onnx_parity_max_abs_diff": report["onnx_parity_max_abs_diff"], "synthetic": report["synthetic"]}))
    return 0


def cmd_onnx(args: argparse.Namespace) -> int:
    import numpy as np

    from .export_onnx import export_model, read_metadata, verify_parity
    from .train import HELDOUT_FILE, META_FILE, ONNX_FILE, heldout_frame, load_sklearn_model

    model_dir = Path(args.model_dir)
    doc = json.loads((model_dir / META_FILE).read_text(encoding="utf-8"))
    meta = doc["metadata"]
    if args.threshold is not None:
        if not 0.0 <= args.threshold <= 1.0:
            raise SystemExit("--threshold must be in [0, 1]")
        meta["pandaeye.threshold"] = repr(float(args.threshold))
        doc["metadata"] = meta
        (model_dir / META_FILE).write_text(json.dumps(doc, indent=2) + "\n", encoding="utf-8")
    model = load_sklearn_model(model_dir)
    path = export_model(model, model_dir / ONNX_FILE, meta)
    X = F.feature_frame(heldout_frame(model_dir)) if (model_dir / HELDOUT_FILE).exists() else np.zeros((1, F.N_FEATURES), np.float32)
    diff = verify_parity(model, path, X)
    print(json.dumps({"onnx": str(path), "parity_max_abs_diff": diff, "metadata_keys": sorted(read_metadata(path))}))
    return 0


def cmd_evaluate(args: argparse.Namespace) -> int:
    from .evaluate import evaluate_model_dir

    report, code = evaluate_model_dir(
        args.model_dir,
        heldout=args.heldout,
        report_path=args.report,
        baseline_threshold=args.baseline_threshold,
        model_threshold=args.model_threshold,
        use_sklearn=args.sklearn,
        threshold_mode=args.threshold_mode,
        min_positives=args.min_positives,
        min_negatives=args.min_negatives,
        min_players_per_class=args.min_players,
        min_tpr_gain=args.min_tpr_gain,
    )
    g = report["gate"]
    print(json.dumps({
        "passed": g["passed"],
        "reasons": g["reasons"],
        "baseline": {k: g["baseline"][k] for k in ("threshold", "tpr", "fpr")},
        "model": {k: g["model"][k] for k in ("threshold", "tpr", "fpr")},
        "threshold_mode": g["threshold_mode"],
        "deploy_threshold_fpr_within_baseline": report["operating_points"]["model_deploy_threshold"]["fpr_within_baseline"],
        "synthetic": report["synthetic"],
        "deployable": report["deployable"],
        "report": str(args.report or Path(args.model_dir) / "gate_report.json"),
    }))
    if report["synthetic"]:
        print("WARNING: synthetic data - this result only shows that the pipeline runs.", file=sys.stderr)
    return code


def cmd_synth(args: argparse.Namespace) -> int:
    from .synth import SYNTHETIC_NOTICE, SynthConfig, write

    cfg = SynthConfig(players=args.players, seed=args.seed,
                      windows_per_session=(args.min_windows, args.max_windows))
    paths = write(args.out, cfg)
    print(SYNTHETIC_NOTICE, file=sys.stderr)
    print(json.dumps({k: str(v) for k, v in paths.items()}))
    return 0


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="pandaeye-ml", description="PandaEye ML pipeline (feature version %d)" % F.FEATURE_VERSION)
    p.add_argument("--version", action="version", version=__version__)
    p.add_argument("-v", "--verbose", action="store_true")
    sub = p.add_subparsers(dest="command", required=True)

    e = sub.add_parser("export", help="export feature windows (ClickHouse) and labels (API) to files")
    e.add_argument("--out", required=True)
    e.add_argument("--api-url", required=True, help="API base URL, e.g. https://api.example.invalid")
    e.add_argument("--api-key-env", default="PE_API_KEY", help="env var holding the API key (scope read)")
    e.add_argument("--clickhouse-dsn-env", default="PE_CLICKHOUSE_DSN", help="env var holding the ClickHouse DSN")
    e.add_argument("--since", help="RFC 3339 or Unix time; windows and labels from this time")
    e.add_argument("--until", help="RFC 3339 or Unix time; windows before this time")
    e.add_argument("--trusted-tenants", help="optional allow-list file: only export these tenants' windows")
    e.set_defaults(func=cmd_export)

    def data_args(sp: argparse.ArgumentParser) -> None:
        sp.add_argument("--windows", required=True, help="feature windows (.parquet or .csv)")
        sp.add_argument("--labels", required=True, help="labels NDJSON (API export format)")
        sp.add_argument("--trusted-tenants", required=True, help="tenant allow-list, one id per line")
        sp.add_argument("--erased", help="erasure list: 'tenant_id,player_uuid' or 'player_uuid' per line")
        sp.add_argument("--all-clients", action="store_true", help="keep non-tick clients (v1 models must not)")

    t = sub.add_parser("train", help="train, calibrate, pick the threshold, export ONNX (+ parity check)")
    data_args(t)
    t.add_argument("--out", required=True, help="model directory")
    t.add_argument("--max-iter", type=int, default=300)
    t.add_argument("--learning-rate", type=float, default=0.08)
    t.add_argument("--max-leaf-nodes", type=int, default=15)
    t.add_argument("--min-samples-leaf", type=int, default=20)
    t.add_argument("--max-bins", type=int, default=63)
    t.add_argument("--class-weight", default="balanced", choices=["balanced", "none"])
    t.add_argument("--no-monotonic", action="store_true", help="drop the monotonic constraints on violation features")
    t.add_argument("--calibration-folds", type=int, default=3)
    t.add_argument("--test-fraction", type=float, default=0.25)
    t.add_argument("--threshold-folds", type=int, default=3, help="player-grouped folds for out-of-fold threshold selection")
    t.add_argument("--threshold-fpr-margin", type=float, default=0.75,
                   help="target out-of-fold FPR = margin x baseline FPR on the training players")
    t.add_argument("--seed", type=int, default=1)
    t.add_argument("--baseline-threshold", type=float, default=15.0, help="heuristic operating point (raw points)")
    t.add_argument("--model-name", default="gbm1", help="signal check id becomes ml.<name>")
    t.set_defaults(func=cmd_train)

    o = sub.add_parser("onnx", help="re-export model.onnx from model.joblib and verify parity")
    o.add_argument("--model-dir", required=True)
    o.add_argument("--threshold", type=float, help="set a new deploy threshold (P(cheat)) in the metadata")
    o.set_defaults(func=cmd_onnx)

    v = sub.add_parser("evaluate", help="THE GATE: exit 1 unless the model beats the baseline without raising FPR")
    v.add_argument("--model-dir", required=True)
    v.add_argument("--heldout", help="held-out windows (default: <model-dir>/heldout.parquet)")
    v.add_argument("--report", help="JSON report path (default: <model-dir>/gate_report.json)")
    v.add_argument("--baseline-threshold", type=float, help="override the baseline operating threshold")
    v.add_argument("--threshold-mode", choices=["matched", "fixed"], default="matched",
                   help="matched: model at its best TPR with FPR <= baseline FPR on the held-out set; "
                        "fixed: model at the deploy threshold from its metadata")
    v.add_argument("--model-threshold", type=float, help="evaluate the model at this explicit threshold")
    v.add_argument("--sklearn", action="store_true", help="score with model.joblib instead of model.onnx")
    v.add_argument("--min-positives", type=int, default=50)
    v.add_argument("--min-negatives", type=int, default=50)
    v.add_argument("--min-players", type=int, default=5, help="minimum held-out players per class")
    v.add_argument("--min-tpr-gain", type=float, default=0.0)
    v.set_defaults(func=cmd_evaluate)

    s = sub.add_parser("synth", help="write a SYNTHETIC dataset (pipeline testing only)")
    s.add_argument("--out", required=True)
    s.add_argument("--players", type=int, default=1000)
    s.add_argument("--min-windows", type=int, default=6, help="windows per session, lower bound")
    s.add_argument("--max-windows", type=int, default=20, help="windows per session, upper bound")
    s.add_argument("--seed", type=int, default=1)
    s.set_defaults(func=cmd_synth)
    return p


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    logging.basicConfig(level=logging.INFO if args.verbose else logging.WARNING, format="%(levelname)s %(name)s: %(message)s")
    return int(args.func(args) or 0)


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())
