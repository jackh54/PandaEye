# PandaEye ML pipeline. Run from ml/ (make -C ml <target>).
PYTHON ?= python3.13
VENV   := .venv
PY     := $(VENV)/bin/python
# Shared machine: keep OpenMP/BLAS thread pools small.
export OMP_NUM_THREADS ?= 2
export OPENBLAS_NUM_THREADS ?= 1
export MKL_NUM_THREADS ?= 1

DEMO := build/demo

.PHONY: venv test synth-demo clean help

help:
	@echo "targets: venv (create .venv + pinned deps), test (pytest), synth-demo (SYNTHETIC end-to-end run), clean"

$(PY):
	$(PYTHON) -m venv $(VENV)
	$(PY) -m pip install --upgrade pip
	$(PY) -m pip install -r requirements.txt

venv: $(PY)

test: $(PY)
	$(PY) -m pytest -q

# End-to-end run on SYNTHETIC data: synth -> train (+ ONNX export, parity check) -> evaluate (gate).
# The result only shows that the pipeline works; the model is marked trained_on_synthetic=true.
synth-demo: $(PY)
	rm -rf $(DEMO)
	$(PY) -m pandaeye_ml synth --out $(DEMO)/data
	$(PY) -m pandaeye_ml train \
		--windows $(DEMO)/data/SYNTHETIC_feature_windows.parquet \
		--labels $(DEMO)/data/SYNTHETIC_labels.ndjson \
		--trusted-tenants $(DEMO)/data/SYNTHETIC_trusted_tenants.txt \
		--erased $(DEMO)/data/SYNTHETIC_erased.txt \
		--out $(DEMO)/model
	$(PY) -m pandaeye_ml evaluate --model-dir $(DEMO)/model
	@echo "gate report: $(DEMO)/model/gate_report.json (SYNTHETIC - never deploy)"

clean:
	rm -rf build .pytest_cache pandaeye_ml/__pycache__ tests/__pycache__
