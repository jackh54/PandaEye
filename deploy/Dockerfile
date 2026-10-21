# PandaEye cloud binary: static Go build (CGO_ENABLED=0) on distroless/static, non-root, multi-arch.
#
# Build context is the repository root (deploy/Dockerfile.dockerignore limits it to what is needed):
#   docker buildx build -f deploy/Dockerfile -t pandaeye:dev --load .
#   docker buildx build -f deploy/Dockerfile --platform linux/amd64,linux/arm64 \
#     --build-arg VERSION="$(git describe --tags --always --dirty)" \
#     --build-arg COMMIT="$(git rev-parse HEAD)" \
#     --build-arg BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
#     -t registry.example.com/pandaeye:1.0.0 --push .
# or `make -C deploy image` / `make -C deploy image-multiarch`.
#
# The ONNX scorer (`-tags onnx`, ARCHITECTURE 5.10) needs cgo and the onnxruntime shared library, so it
# cannot be built into this static image; this image always runs with ML disabled.

# Base images. mirror.gcr.io is a pull-through mirror of Docker Hub (upstream: docker.io/library/golang).
ARG GO_IMAGE=mirror.gcr.io/library/golang:1.26.10-trixie
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian13:nonroot

# ---------------------------------------------------------------------------------------------- build
# Runs on the build host's architecture and cross-compiles for TARGETARCH (no emulation needed).
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
# Package holding the version variables (`-X <pkg>.version=...`). -X on a missing symbol is ignored.
ARG VERSION_PKG=main
ENV CGO_ENABLED=0 GOFLAGS=-trimpath GOTOOLCHAIN=local

WORKDIR /src/cloud
COPY cloud/go.mod cloud/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cloud/ ./
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" go build \
      -ldflags="-s -w -buildid= -X ${VERSION_PKG}.version=${VERSION} -X ${VERSION_PKG}.commit=${COMMIT} -X ${VERSION_PKG}.date=${BUILD_DATE}" \
      -o /out/pandaeye ./cmd/pandaeye

# Probe for container health checks (the runtime image has no shell or curl).
COPY deploy/healthcheck/ /src/healthcheck/
RUN --mount=type=cache,target=/root/.cache/go-build \
    cd /src/healthcheck && GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -ldflags="-s -w -buildid=" -o /out/pe-healthcheck .

# Working directory for `pandaeye all` (./data/nats, ./data/pandaeye.db), owned by the nonroot user.
RUN mkdir -p /out/var/lib/pandaeye/data

# -------------------------------------------------------------------------------------------- runtime
FROM ${RUNTIME_IMAGE}
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
LABEL org.opencontainers.image.title="pandaeye" \
      org.opencontainers.image.description="PandaEye cloud anticheat (gateway, worker, verdict, api)" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}"

COPY --from=build /out/pandaeye /usr/local/bin/pandaeye
COPY --from=build /out/pe-healthcheck /usr/local/bin/pe-healthcheck
COPY --from=build --chown=65532:65532 /out/var/lib/pandaeye /var/lib/pandaeye

USER 65532:65532
WORKDIR /var/lib/pandaeye
VOLUME ["/var/lib/pandaeye/data"]
# 8080 gateway (/v1/connect, /healthz, /status), 8081 REST API. /metrics + pprof (PE_METRICS_LISTEN)
# stays on 127.0.0.1:9090 unless you change it; never publish it.
EXPOSE 8080 8081

# Default health check fits `all` and `gateway` on the default PE_GATEWAY_LISTEN; docker-compose.yml
# overrides it per role.
HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=3 \
  CMD ["/usr/local/bin/pe-healthcheck", "http://127.0.0.1:8080/healthz"]

# SIGTERM triggers the graceful shutdown (<= 10 s, ARCHITECTURE 5.1); give the runtime >= 20 s.
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/pandaeye"]
CMD ["all"]
