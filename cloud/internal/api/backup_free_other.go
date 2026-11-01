//go:build !linux && !darwin && !freebsd

package api

import "errors"

// freeBytes cannot measure free space on this platform; the disk guard then lets backups run.
func freeBytes(string) (int64, error) { return 0, errors.ErrUnsupported }
