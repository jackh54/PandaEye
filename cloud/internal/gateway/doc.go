// Package gateway is the plugin WebSocket server (ARCHITECTURE 5.3): handshake, licences, server identity, batches to the event stream, backpressure, outbound delivery and the verdict sweep, metering of online players, /status.
//
// This is the foundation placeholder: New and Run have their final signatures; Run reports
// ErrNotImplemented until the package is implemented.
package gateway

import (
	"context"
	"errors"

	"github.com/jackh54/PandaEye/cloud/internal/app"
)

// ErrNotImplemented is returned by Run until the service exists.
var ErrNotImplemented = errors.New("gateway: not implemented")

// Service is the gateway service.
type Service struct{ d *app.Deps }

var _ app.Service = (*Service)(nil)

// New builds the service from the shared dependencies.
func New(d *app.Deps) (*Service, error) { return &Service{d: d}, nil }

// Run serves until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error { return ErrNotImplemented }
