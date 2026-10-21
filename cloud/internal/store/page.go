package store

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Page selects one page of a list: at most Limit items after Cursor (the next cursor returned by
// the previous page; empty = first page).
type Page struct {
	Limit  int
	Cursor string
}

// Page sizes (ARCHITECTURE 5.8).
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
)

// ErrBadCursor is returned for cursors that were not produced by the store.
var ErrBadCursor = errors.New("store: invalid cursor")

// Size returns the effective page size: DefaultPageLimit when unset, at most MaxPageLimit.
func (p Page) Size() int {
	switch {
	case p.Limit <= 0:
		return DefaultPageLimit
	case p.Limit > MaxPageLimit:
		return MaxPageLimit
	}
	return p.Limit
}

// EncodeCursor returns the opaque cursor for a position (time, id) in a list.
func EncodeCursor(t time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(t.UnixMicro(), 10) + "|" + id))
}

// DecodeCursor parses a cursor produced by EncodeCursor.
func DecodeCursor(c string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, "", ErrBadCursor
	}
	us, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, "", ErrBadCursor
	}
	n, err := strconv.ParseInt(us, 10, 64)
	if err != nil {
		return time.Time{}, "", ErrBadCursor
	}
	return time.UnixMicro(n).UTC(), id, nil
}
