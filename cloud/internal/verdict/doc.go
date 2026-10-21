// Package verdict is the verdict service (ARCHITECTURE 5.6): scoring, leniency, action policy, evidence, delivery, Discord and the leased store writer.
//
// This is the foundation placeholder: New and Run have their final signatures; Run reports
// ErrNotImplemented until the package is implemented.
package verdict

import (
	"context"
	"errors"

	"github.com/jackh54/PandaEye/cloud/internal/app"
)

// ErrNotImplemented is returned by Run until the service exists.
var ErrNotImplemented = errors.New("verdict: not implemented")

// Service is the verdict service.
type Service struct{ d *app.Deps }

var _ app.Service = (*Service)(nil)

// New builds the service from the shared dependencies.
func New(d *app.Deps) (*Service, error) { return &Service{d: d}, nil }

// Run serves until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error { return ErrNotImplemented }
