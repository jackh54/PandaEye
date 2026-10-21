// Package worker is the partition consumer of pe.events.<NN> (ARCHITECTURE 5.5): sessions, player
// state, the cloud checks, clips, signals to pe.signals.<NN>, plus `pandaeye replay`.
//
// This is the foundation placeholder: New, Run and Replay have their final signatures and report
// ErrNotImplemented until the package is implemented.
package worker

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
)

// ErrNotImplemented is returned until the worker exists.
var ErrNotImplemented = errors.New("worker: not implemented")

// Service is the worker service.
type Service struct{ d *app.Deps }

var _ app.Service = (*Service)(nil)

// New builds the service from the shared dependencies.
func New(d *app.Deps) (*Service, error) { return &Service{d: d}, nil }

// Run consumes the partitions this process leases until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error { return ErrNotImplemented }

// ReplayOptions selects what `pandaeye replay` re-checks.
type ReplayOptions struct {
	TenantID string
	Player   string // canonical UUID
	Since    time.Time
	Until    time.Time
	ClipID   string // replay a stored clip instead of stream data
}

// Replay re-runs the checks over stored stream data or a clip and writes the signals to out.
func Replay(ctx context.Context, d *app.Deps, o ReplayOptions, out io.Writer) error {
	return ErrNotImplemented
}
