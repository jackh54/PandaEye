// Package telemetry writes signals, verdicts, feature windows and sessions to ClickHouse
// (PE_CLICKHOUSE_DSN) in batches, or nowhere when telemetry is disabled (ARCHITECTURE 5.7).
//
// This is the foundation placeholder: New is final; the Sink methods are defined by the package.
package telemetry

import "github.com/jackh54/PandaEye/cloud/internal/app"

// Sink receives telemetry rows.
type Sink interface {
	Close() error
}

// New returns the process's sink, shared by every service of the process (app.Deps.Shared).
func New(d *app.Deps) (Sink, error) {
	s, err := d.Shared("telemetry", func() (any, error) { return nop{}, nil })
	if err != nil {
		return nil, err
	}
	return s.(Sink), nil
}

type nop struct{}

func (nop) Close() error { return nil }
