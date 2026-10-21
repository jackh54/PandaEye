# PandaEye deployment helpers. Run from the repository root: make -C deploy <target>
#
#   make -C deploy env certs nats-auth image up     first start of the docker-compose stack
#   make -C deploy image-multiarch IMAGE=registry.example.com/pandaeye TAG=1.0.0
#
# Multi-platform builds need a builder that supports them (the containerd image store, or
# `docker buildx create --use --driver docker-container`).

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

ROOT := $(abspath $(CURDIR)/..)
IMAGE ?= pandaeye
TAG ?= dev
PLATFORMS ?= linux/amd64,linux/arm64
VERSION ?= $(shell git -C $(ROOT) describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git -C $(ROOT) rev-parse HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
# Package of the version variables in cmd/pandaeye (-X <pkg>.version/commit/date).
VERSION_PKG ?= main
COMPOSE := docker compose -f $(CURDIR)/docker-compose.yml

BUILD_ARGS := --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
	--build-arg BUILD_DATE=$(BUILD_DATE) --build-arg VERSION_PKG=$(VERSION_PKG)

.PHONY: help image image-multiarch image-oci env certs nats-auth nats-permissions preflight config up down lint

help: ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

image: ## Build $(IMAGE):$(TAG) for the host platform and load it into Docker
	docker buildx build -f $(CURDIR)/Dockerfile $(BUILD_ARGS) -t $(IMAGE):$(TAG) --load $(ROOT)

image-multiarch: ## Build linux/amd64 + linux/arm64 and push (IMAGE must be a registry path)
	docker buildx build -f $(CURDIR)/Dockerfile --platform $(PLATFORMS) $(BUILD_ARGS) -t $(IMAGE):$(TAG) --push $(ROOT)

image-oci: ## Build linux/amd64 + linux/arm64 into an OCI archive without pushing
	docker buildx build -f $(CURDIR)/Dockerfile --platform $(PLATFORMS) $(BUILD_ARGS) \
		--output type=oci,dest=$(CURDIR)/pandaeye-$(VERSION).oci.tar $(ROOT)

env: ## Create deploy/.env with random secrets (refuses to overwrite)
	$(CURDIR)/scripts/gen-env.sh

certs: ## Create development TLS certificates in deploy/certs (refuses to overwrite)
	$(CURDIR)/scripts/gen-dev-certs.sh

nats-auth: ## Create or refresh NATS operator/accounts and per-role creds in deploy/nats/auth
	cd $(CURDIR)/nats/gen-auth && go run . -out ../auth

nats-permissions: ## Print the per-role NATS permissions
	@cd $(CURDIR)/nats/gen-auth && go run . -print

preflight: ## Check .env, certificates and NATS creds
	$(CURDIR)/scripts/preflight.sh

config: ## Validate docker-compose.yml
	$(COMPOSE) config -q

up: preflight ## Start the stack
	$(COMPOSE) up -d

down: ## Stop the stack (volumes are kept)
	$(COMPOSE) down

lint: ## shellcheck the scripts and verify the systemd units
	shellcheck $(CURDIR)/scripts/*.sh
	systemd-analyze verify $(CURDIR)/systemd/pandaeye.service $(CURDIR)/systemd/pandaeye@.service
