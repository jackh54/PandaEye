//go:build tools

// Package tools pins dependencies that the spec plans for but that no package imports yet (or imports
// only behind build tags), so `go mod tidy` keeps them in go.mod at the reviewed versions.
package tools

import (
	_ "github.com/ClickHouse/clickhouse-go/v2"
	_ "github.com/coder/websocket"
	_ "github.com/dchest/siphash"
	_ "github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/klauspost/compress/zstd"
	_ "github.com/nats-io/nats-server/v2/server"
	_ "github.com/nats-io/nats.go/jetstream"
	_ "github.com/prometheus/client_golang/prometheus"
	_ "github.com/yalue/onnxruntime_go"
	_ "go.yaml.in/yaml/v3"
	_ "golang.org/x/sync/singleflight"
	_ "golang.org/x/time/rate"
	_ "google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)
