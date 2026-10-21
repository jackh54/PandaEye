package app

import (
	"io"
	"log/slog"
	"strings"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
)

// NewLogger returns the JSON logger of every role. Besides identity.Secret values, which redact
// themselves, attributes whose key names a credential (token, secret, password, authorization,
// licence key, webhook URL, DSN, pepper, ...) are redacted whatever their type.
func NewLogger(w io.Writer, level slog.Leveler) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: RedactAttr}))
}

// RedactAttr is the slog.HandlerOptions.ReplaceAttr of NewLogger, exported for other handlers.
func RedactAttr(_ []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() != slog.KindGroup && SensitiveKey(a.Key) {
		return slog.String(a.Key, identity.Redacted)
	}
	return a
}

var sensitiveParts = []string{"secret", "token", "password", "passwd", "authorization", "pepper", "credential",
	"creds", "cookie", "assertion", "dsn", "license_key", "licence_key", "api_key", "apikey", "webhook"}

// SensitiveKey reports whether a log attribute key names a credential. "webhook_id" (how webhooks
// are logged) is allowed.
func SensitiveKey(key string) bool {
	k := strings.ToLower(key)
	if k == "webhook_id" {
		return false
	}
	for _, p := range sensitiveParts {
		if strings.Contains(k, p) {
			return true
		}
	}
	return k == "key" || strings.HasSuffix(k, "_key") || strings.HasSuffix(k, ".key")
}
