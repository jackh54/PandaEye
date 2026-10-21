package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
)

// Error codes of {"error": "...", "code": "..."} bodies.
const (
	codeBadRequest       = "bad_request"
	codeInvalidCursor    = "invalid_cursor"
	codeUnauthenticated  = "unauthenticated"
	codeInvalidToken     = "invalid_token"
	codeAssertionMissing = "assertion_required"
	codeInvalidAssertion = "invalid_assertion"
	codeForbidden        = "forbidden"
	codeScopeRequired    = "scope_required"
	codeUntrustedPeer    = "untrusted_peer"
	codeNotFound         = "not_found"
	codeMethodNotAllowed = "method_not_allowed"
	codeConflict         = "conflict"
	codeTooLarge         = "too_large"
	codeRateLimited      = "rate_limited"
	codeInvalidLicense   = "invalid_license"
	codeInvalidWebhook   = "invalid_webhook"
	codeWebhookTest      = "webhook_test_failed"
	codeNotConnected     = "not_connected"
	codeAppealLimit      = "appeal_limit"
	codeUnavailable      = "unavailable"
	codeInternal         = "internal"
)

// apiError is an error with its HTTP status and machine code.
type apiError struct {
	status     int
	code       string
	msg        string
	retryAfter time.Duration
}

func (e *apiError) Error() string { return e.msg }

func errf(status int, code, format string, args ...any) *apiError {
	return &apiError{status: status, code: code, msg: fmt.Sprintf(format, args...)}
}

func badRequest(format string, args ...any) *apiError {
	return errf(http.StatusBadRequest, codeBadRequest, format, args...)
}

func notFound(what string) *apiError {
	return errf(http.StatusNotFound, codeNotFound, "%s not found", what)
}

func conflict(format string, args ...any) *apiError {
	return errf(http.StatusConflict, codeConflict, format, args...)
}

func forbidden(format string, args ...any) *apiError {
	return errf(http.StatusForbidden, codeForbidden, format, args...)
}

func unavailable(format string, args ...any) *apiError {
	return &apiError{status: http.StatusServiceUnavailable, code: codeUnavailable, msg: fmt.Sprintf(format, args...), retryAfter: 5 * time.Second}
}

// asAPIError maps an error to its response; unexpected errors become 500s (reported is false then
// and the caller logs the cause).
func asAPIError(err error) (e *apiError, expected bool) {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
		return ae, true
	case errors.Is(err, store.ErrNotFound):
		return notFound("resource"), true
	case errors.Is(err, store.ErrConflict):
		return conflict("the resource changed or already exists"), true
	case errors.Is(err, store.ErrBadCursor):
		return errf(http.StatusBadRequest, codeInvalidCursor, "invalid cursor"), true
	case errors.Is(err, store.ErrInvalid):
		return badRequest("invalid value"), true
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return unavailable("the request timed out"), false
	}
	return errf(http.StatusInternalServerError, codeInternal, "internal error"), false
}

func writeError(w http.ResponseWriter, e *apiError) {
	if e.retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int((e.retryAfter+time.Second-1)/time.Second)))
	}
	writeJSON(w, e.status, map[string]string{"error": e.msg, "code": e.code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		data = []byte(`{"error":"internal error","code":"internal"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}

// maxBody bounds JSON request bodies.
const maxBody = 256 << 10

// decodeBody decodes a JSON object body strictly (unknown fields are errors). An empty body
// decodes as {}.
func decodeBody(r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		return errf(http.StatusUnsupportedMediaType, codeBadRequest, "request body must be application/json")
	}
	body := http.MaxBytesReader(nil, r.Body, maxBody)
	data, err := io.ReadAll(body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return errf(http.StatusRequestEntityTooLarge, codeTooLarge, "request body too large")
		}
		return badRequest("could not read the request body")
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		data = []byte("{}")
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var se *json.SyntaxError
		var te *json.UnmarshalTypeError
		switch {
		case errors.As(err, &se):
			return badRequest("invalid JSON")
		case errors.As(err, &te):
			return badRequest("field %q has the wrong type", te.Field)
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			return badRequest("unknown field %s", strings.TrimPrefix(err.Error(), "json: unknown field "))
		}
		return badRequest("invalid JSON body")
	}
	if dec.More() {
		return badRequest("trailing data after the JSON body")
	}
	return nil
}
