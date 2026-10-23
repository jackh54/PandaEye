# PandaEye ML pipeline

Python pipeline that trains the optional ML check of the PandaEye cloud (ARCHITECTURE 5.10): a gradient
boosting classifier over per-player 30-second feature windows, exported to ONNX and served by the Go worker
through ONNX Runtime (`-tags onnx` builds).

> **No production model ships with PandaEye.** Blueprint rule: *add ML only once you have labelled data.*
> There is no real labelled data yet, so nothing here has been trained on real players. The only data
> this repository can produce is synthetic (`synth`), which exists to test the pipeline and says so in
> every file it writes. A model trained on it is marked `pandaeye.trained_on_synthetic=true`, the gate
> report says `"deployable": false`, and the Go scorer must refuse to load it.

The feature vector is the contract with the Go worker: [`FEATURES.md`](FEATURES.md) (feature version 1,
94 features, ClickHouse `feature_windows` DDL, label join rules, ONNX I/O and metadata contract).

## Layout

| Path | What |
|---|---|
| `FEATURES.md` | feature vector v1: ordered names, definitions, units, events, defaults, clipping; DDL; label join; model contract |
| `pandaeye_ml/features.py` | machine-readable mirror of the feature table (a test keeps it identical to FEATURES.md) |
| `pandaeye_ml/dataset.py` | load windows (ClickHouse via clickhouse-connect, parquet/CSV, or a `PE_FEATURES_DIR` directory) and labels (API NDJSON export or file); exclusions; overlap join; player-grouped split |
| `pandaeye_ml/baseline.py` | heuristic baseline: per-window score mirroring the verdict scoring (raw points, check and family caps) |
| `pandaeye_ml/train.py` | HistGradientBoosting + class weights + sigmoid calibration, out-of-fold threshold selection, ONNX export |
| `pandaeye_ml/export_onnx.py` | skl2onnx export, `pandaeye.*` metadata, onnxruntime parity check |
| `pandaeye_ml/evaluate.py` | **the gate** (exit code 1 unless the model beats the baseline without raising FPR), JSON report |
| `pandaeye_ml/synth.py` | SYNTHETIC data generator, pipeline testing only |
| `pandaeye_ml/cli.py` | `python -m pandaeye_ml export / train / onnx / evaluate / synth` |
| `tests/` | pytest suite |

## Setup

Python 3.13 (3.11+ works). The venv lives in `ml/.venv` and is never committed.

```sh
make -C ml venv          # python3.13 -m venv ml/.venv && pip install -r ml/requirements.txt
make -C ml test          # or, from the repo root: ml/.venv/bin/python -m pytest ml
make -C ml synth-demo    # SYNTHETIC end-to-end run into ml/build/demo (about 45 s on 2 threads)
```

`requirements.txt` pins exact versions. `protobuf` is held at 6.x because skl2onnx 1.20.0 writes numpy
booleans into tree attributes that protobuf 7 rejects.

## Workflow (real data, once labels exist)

1. **Export** feature windows and labels. Secrets come from environment variables, never flags:

   ```sh
   export PE_CLICKHOUSE_DSN='clickhouse://user:password@clickhouse:8123/pandaeye'
   export PE_API_KEY='pe_<id>_<secret>'          # API key with scope "read"
   python -m pandaeye_ml export --out data/ --api-url https://<api-host> \
       --since 2026-01-01T00:00:00Z --trusted-tenants trusted.txt
   ```

   This writes `data/feature_windows.parquet` (FINAL-deduplicated rows of `feature_version = 1`) and
   `data/labels.ndjson` (`GET /api/v1/export/labels`, already without erased subjects). Both files
   contain player UUIDs (personal data): they are written with mode 0600 and must not be committed or
   shared, and an erasure request means deleting them or re-exporting.

   **Windows recorded to files** (`PE_FEATURES_DIR`, no ClickHouse; FEATURES.md section 6): train on
   the directory itself and export only the labels:

   ```sh
   python -m pandaeye_ml export --out data/ --api-url https://<api-host> --labels-only \
       --erasures-from /path/to/PE_FEATURES_DIR
   ```

   `--labels-only` needs no ClickHouse DSN. `--erasures-from` also writes `data/erased.txt`, the
   directory's erasure journal in the `--erased` format, for copies of the windows taken off the node
   before an erasure (pass it to `train --erased`). It drops the journal's time bound, so all rows of
   those players are excluded. The directory and every copy of it hold player UUIDs, like the files
   above.

2. **Train**:

   ```sh
   python -m pandaeye_ml train --windows data/feature_windows.parquet --labels data/labels.ndjson \
       --trusted-tenants trusted.txt --erased erased.txt --out models/gbm1
   ```

   - `--trusted-tenants` is required: labels and windows come only from allow-listed tenants (paid plan,
     or older than 30 days with >= 50 distinct players; ARCHITECTURE 5.10/5.11).
   - `--erased` (optional) drops `tenant_id,player_uuid` (or bare `player_uuid`) subjects, for exports
     made before an erasure.
   - `--windows` also takes a `PE_FEATURES_DIR` directory (`--windows data/ml`). Every `windows-*.csv`
     (the current hour; an unfinished last line is ignored) and `windows-*.csv.gz` in it is read. A file
     the cloud compresses or deletes during the load is followed or skipped, so the live directory works.
   - Windows are cut to the labelled (tenant, player) pairs and to `--since`/`--until` (window start) as
     they load, file by file for a directory, so memory follows what is kept. `windows_in` in the report
     therefore counts only windows of labelled players. Rows are then de-duplicated as for a single file.
   - A directory's `erasures.jsonl` is applied automatically: rows of an erased player from before the
     erasure are dropped (FEATURES.md section 6), also for an erasure recorded while the files load. A
     malformed journal line stops the run.
   - `--exclude-servers` / `--include-servers` (comma-separated server ids, from
     `plugins/PandaEye/server-id` or `GET /api/v1/servers`) filter windows before the label join. Keep
     lobby and hub servers out: visits there are short, and local movement checks are often exempt, so
     their flag features read 0 whatever the player does. Labels are per player and time, so a cheat
     verdict from a game server would also label the player's lobby windows in that interval, and the
     model would learn lobby behaviour as cheating. `train_report.json` counts the dropped windows
     (`windows_server_filtered`). The gate then measures only the servers kept, while the worker scores
     every server's windows, so check the model's signals on the excluded servers in shadow (step 4)
     before giving it points.
   - Windows of non-tick clients (`client_protocol < 768`) are excluded (`--all-clients` keeps them).
     The v1 model applies to tick clients only.
   - Windows are labelled by overlap with label intervals, ambiguous windows are dropped, and unlabelled
     windows are excluded (FEATURES.md section 7).
   - 25% of the players are held out (`heldout.parquet`) and never touched by training or threshold
     selection. Every split is by player UUID.
   - The deploy threshold is chosen from out-of-fold predictions on the training players, at 0.75x the
     heuristic baseline's FPR on the same windows (`--threshold-fpr-margin`).
   - Output: `model.onnx` (deploy artifact, parity-checked against scikit-learn, max abs diff < 1e-5),
     `model.joblib`, `model.json` (metadata + config), `baseline.json`, `heldout.parquet`,
     `train_report.json`.

3. **Evaluate (the gate)**:

   ```sh
   python -m pandaeye_ml evaluate --model-dir models/gbm1      # exit 0 = pass, 1 = fail
   ```

   This scores the held-out windows with the ONNX file, through onnxruntime, exactly as Go will.

   - **Baseline:** its FPR and TPR at its operating threshold, 15 raw points (the base ALERT threshold;
     `--baseline-threshold`).
   - **Model, default mode `matched`:** its best TPR at an FPR equal to or below the baseline's.
   - **Model, mode `--threshold-mode fixed`:** evaluated at the deploy threshold from its metadata
     (stricter).
   - **Pass rule:** model FPR <= baseline FPR, model TPR > baseline TPR (+ `--min-tpr-gain`), and at
     least `--min-positives`/`--min-negatives` windows (50) and `--min-players` players per class (5).

   `gate_report.json` adds ROC-AUC and PR-AUC for both, TPR per category (the verdict's dominant
   category), FPR per label source (`overturned` vs `manual`), both operating points, and a player-level
   bootstrap of the TPR/FPR differences. `deployable` is true only if the gate passed, the data was real,
   and the deploy threshold itself kept FPR at or below the baseline's on the held-out players. If only
   the last condition fails, set a new threshold (0 < p <= 1) with
   `python -m pandaeye_ml onnx --model-dir models/gbm1 --threshold <p>` and evaluate again with
   `--threshold-mode fixed`. `model_sha256` is the SHA-256 of the `model.onnx` that was scored (null with
   `--sklearn`); a re-export changes it, so only the report of the last evaluation matches the file.

4. **Deploy** (only with `"deployable": true`). Copy exactly two files to the worker host, side by
   side: `model.onnx` and its gate report under the name `model.gate.json` (copy `gate_report.json` to
   that name, or evaluate with `--report models/gbm1/model.gate.json`). Nothing else from the model
   directory: `heldout.parquet` holds player UUIDs. Run a worker built with `-tags onnx` (cgo, not
   static: `deploy/pterodactyl.md` "ML scoring") with `PE_ML_MODEL=/path/model.onnx` and
   `PE_ML_ONNXRUNTIME_LIB=/path/libonnxruntime.so.<version>` (OPERATIONS section 3). The scorer
   refuses the model unless the gate report says `deployable: true` and `synthetic: false`, has feature
   version 1 and a `model_sha256` equal to the file's SHA-256. It checks
   `pandaeye.feature_version`/`pandaeye.feature_names` against its own constants, refuses synthetic
   models, scores tick-client windows and flags windows with `P(cheat) >= pandaeye.threshold`.

   **Points stay 0 until `PE_ML_POINTS` is set**, so the model runs in shadow by default. Flagged
   windows are counted in `pandaeye_signals_total{check="ml.<model_name>"}` and every scored window's
   P(cheat) goes to the `pandaeye_worker_ml_p_cheat` histogram, but no signal reaches the verdict
   service: shadow flags are not in evidence, the API or the dashboard, and cannot move a score or
   trigger an alert. A model that fails to load in shadow is logged as an error and leaves ML off
   (`pandaeye_worker_ml_model_loaded` 0) instead of stopping the node; with points > 0 it stops the
   process. Shadow results are aggregates only (per-window scores are not recorded): compare the flag
   rate with the heuristics' before giving the model points (the `ml` family cap is 50, CHECKS.md
   section 4).

## Feature-window files (no ClickHouse)

A `pandaeye all` node without ClickHouse records the windows to local files when `PE_FEATURES_DIR` is set
(for example `data/ml`, a directory of its own outside the repository clone; OPERATIONS section 3 has the
retention and disk sizing). Signals, verdicts and sessions are not recorded there.

| File | Content |
|---|---|
| `windows-YYYYMMDDTHHZ.csv` | the UTC hour being written: the header row, then one row per window, appended |
| `windows-YYYYMMDDTHHZ.csv.gz` | a past hour: gzip of its complete csv, header included (the `.csv` is removed) |
| `erasures.jsonl` | one `{"tenant_id","player_uuid","erased_at_us"}` line per erasure |
| `erasures.done` | how far the `.csv.gz` files have been cleaned (internal) |

The columns are the metadata of FEATURES.md section 5, then `worker_unix_us`, then the 94 features in
table order. Values are written as ClickHouse stores them: canonical lowercase UUIDs (the nil UUID for an
invalid server or session id), `0`/`1` flags, and floats in the shortest form that parses back to the same
float32. Files written before a feature-version change keep that version's columns. Each file loads
unchanged with `load_windows_file` (`.csv` or `.csv.gz`); the Go golden file
`cloud/internal/telemetry/testdata/windows-20260105T10Z.csv` and `tests/test_go_windows.py` keep it that way.
A window re-emitted after a restart can appear twice; `dedupe_windows` keeps the newest `worker_unix_us`.

On an erasure the node stops writing the player's rows from before the erasure and rewrites the `.csv.gz`
files without them in the background (the current hour's file when it is compressed, at most about an hour
later). Rows from after the erasure are new data and stay, as in the worker. Copies already taken off the
node are not reached.

To train from the files, copy the directory off the node (SFTP; the current hour's `.csv` may end in a
partial line, which the loader ignores) or train on it in place, then:

```sh
export PE_API_KEY='pe_<id>_<secret>'          # API key with scope "read"
python -m pandaeye_ml export --out data/ --api-url https://<api-host> --labels-only --erasures-from data/ml
python -m pandaeye_ml train --windows data/ml --labels data/labels.ndjson --trusted-tenants trusted.txt \
    --exclude-servers <hub server ids> --out models/gbm1
python -m pandaeye_ml evaluate --model-dir models/gbm1
```

The directory's `erasures.jsonl` is applied while loading (workflow step 2); `data/erased.txt` from
`--erasures-from` is for other copies of the windows. Every copy contains player UUIDs like any export:
mode 0600, never committed, deleted when no longer needed.

## The gate, precisely

Blueprint ML phase gate: *"beats the heuristics on a held-out set without raising false positives"*.

- The held-out set contains players that the model never saw; every split groups by player UUID across
  tenants.
- The baseline (`baseline.py`) estimates, from the same window features, how many violations each
  shipped check would emit in the window. It multiplies them by the check's raw points (CHECKS.md
  section 4, the same points the verdict service accumulates) and applies the check and family caps. It
  is a per-window approximation, since the real accumulators decay over minutes. A test keeps its
  catalog (caps, families, local checks) identical to the table in `docs/CHECKS.md`.
- A model worse than the baseline fails, and so does one with equal TPR but higher FPR, or too little
  held-out data. Tests cover each case, plus a model trained on shuffled labels failing end to end.

## Caveats and known limitations

- **No real labelled data exists yet, and no production model is shipped.** All numbers produced in
  this repository come from synthetic data and say nothing about real cheaters.
- **Label selection bias.** Labels come from verdicts (`confirmed` = cheat, `overturned` = legit) plus
  manual labels, so labelled windows over-represent players the heuristics already flagged. The FPR is
  measured on that set, not on all players. Random manual `legit` labels are needed for a population FPR.
- **Window-level labels are noisy.** A window overlapping a cheat verdict's interval is labelled cheat
  even if the player was not cheating in that particular window, which caps achievable TPR.
- **Low-FPR variance.** At FPRs around 1%, a few legit players dominate the false positives, so FPR
  estimates move a lot between player samples. The out-of-fold deploy threshold does not always transfer:
  on the synthetic demo the matched gate passes but the deploy threshold's held-out FPR is above the
  baseline's, so `deployable` stays false. Check the bootstrap intervals in the report.
- **Class weights are slow in scikit-learn.** With sample weights, HistGradientBoosting computes weighted
  bin quantiles one percentile at a time. `max_bins` defaults to 63 to contain this. On the synthetic
  data, `--class-weight none` was about 4x faster and at least as good at low FPR. Re-tune on real data.
- **Calibration is sigmoid (Platt).** Isotonic calibration does not convert to ONNX faithfully: a 0.017
  difference in the probe, against about 2e-7 for sigmoid.
- **Monotonic constraints.** Flag counts, miss shares, reach and similar features are constrained to
  increase suspicion (`--no-monotonic` disables this). This is domain knowledge, and it stops a cheater
  from lowering the score by adding violations.
- **Synthetic generator scope.** It works directly in feature space with hand-made distributions. It
  does not simulate events, so it cannot validate the Go feature extraction. A cross-language fixture
  (events -> expected vector) should be added once `features.go` exists.
