//go:build linux || darwin || freebsd

package api

import "syscall"

// freeBytes returns the bytes available to an unprivileged user on the file system holding dir.
func freeBytes(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
