// Package discord posts verdict alerts to tenant Discord webhooks (ARCHITECTURE 5.6 "Discord", 5.12).
//
// Webhook URLs are accepted only in the exact form Discord issues them (ParseWebhookURL), stored
// encrypted (Seal, Open), shown masked (Mask) and logged by webhook id only. The Client refuses
// redirects and connects only to public addresses of the Discord hosts, checked after DNS
// resolution; errors never contain the URL or its token.
package discord

import (
	"errors"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
)

// ErrInvalidWebhook is returned for anything that is not a Discord webhook URL of the accepted form.
var ErrInvalidWebhook = errors.New("discord: invalid webhook URL")

// Hosts that may appear in a webhook URL (exact match, lower case).
var webhookHosts = map[string]bool{
	"discord.com":        true,
	"discordapp.com":     true,
	"ptb.discord.com":    true,
	"canary.discord.com": true,
}

var (
	webhookPath = regexp.MustCompile(`^/api/webhooks/([0-9]{17,20})/([A-Za-z0-9_-]{60,80})$`)
	snowflake   = regexp.MustCompile(`^[0-9]{17,20}$`)
)

const maxWebhookURL = 512

// Webhook is a validated webhook URL. Its String, LogValue and Masked forms never show the token.
type Webhook struct {
	// ID is the webhook's numeric id (safe to log).
	ID    string
	host  string
	token string
	query string // canonical (sorted, encoded) query: only wait and thread_id
}

// ParseWebhookURL accepts only https://{discord.com|discordapp.com|ptb.discord.com|canary.discord.com}
// /api/webhooks/<17-20 digits>/<60-80 of [A-Za-z0-9_-]>, with no userinfo, port, fragment or escapes
// and no query parameters except one `wait` (true/false) and one `thread_id` (17-20 digits).
func ParseWebhookURL(raw string) (Webhook, error) {
	if len(raw) > maxWebhookURL || !strings.HasPrefix(raw, "https://") {
		return Webhook{}, ErrInvalidWebhook
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		// No whitespace, control or non-ASCII bytes; no escapes, fragments, userinfo or backslashes.
		if c <= ' ' || c >= 0x7f || c == '%' || c == '#' || c == '@' || c == '\\' {
			return Webhook{}, ErrInvalidWebhook
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.RawPath != "" {
		return Webhook{}, ErrInvalidWebhook
	}
	if u.Host != u.Hostname() || u.Port() != "" || !webhookHosts[u.Host] {
		return Webhook{}, ErrInvalidWebhook
	}
	m := webhookPath.FindStringSubmatch(u.Path)
	if m == nil {
		return Webhook{}, ErrInvalidWebhook
	}
	w := Webhook{ID: m[1], host: u.Host, token: m[2]}
	if u.ForceQuery {
		return Webhook{}, ErrInvalidWebhook
	}
	if u.RawQuery != "" {
		vals, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return Webhook{}, ErrInvalidWebhook
		}
		for k, vs := range vals {
			if len(vs) != 1 {
				return Webhook{}, ErrInvalidWebhook
			}
			switch k {
			case "wait":
				if vs[0] != "true" && vs[0] != "false" {
					return Webhook{}, ErrInvalidWebhook
				}
			case "thread_id":
				if !snowflake.MatchString(vs[0]) {
					return Webhook{}, ErrInvalidWebhook
				}
			default:
				return Webhook{}, ErrInvalidWebhook
			}
		}
		keys := make([]string, 0, len(vals))
		for k := range vals {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = k + "=" + vals.Get(k)
		}
		w.query = strings.Join(parts, "&")
	}
	return w, nil
}

// URL returns the canonical webhook URL (with the token).
func (w Webhook) URL() string {
	s := "https://" + w.host + "/api/webhooks/" + w.ID + "/" + w.token
	if w.query != "" {
		s += "?" + w.query
	}
	return s
}

// Masked returns the URL with the token hidden, as the API shows it.
func (w Webhook) Masked() string {
	if w.ID == "" {
		return ""
	}
	return "https://" + w.host + "/api/webhooks/" + w.ID + "/****"
}

// String returns the masked URL.
func (w Webhook) String() string { return w.Masked() }

// LogValue logs the webhook id only.
func (w Webhook) LogValue() slog.Value { return slog.StringValue(w.ID) }

// Mask returns the masked form of a webhook URL ("" when raw is empty, "****" when it is not a
// valid webhook URL).
func Mask(raw string) string {
	if raw == "" {
		return ""
	}
	w, err := ParseWebhookURL(raw)
	if err != nil {
		return "****"
	}
	return w.Masked()
}

// WebhookID returns the webhook id of a URL, or "" when it is not a valid webhook URL.
func WebhookID(raw string) string {
	w, err := ParseWebhookURL(raw)
	if err != nil {
		return ""
	}
	return w.ID
}

// sealPurpose derives the webhook encryption key from PE_OUTBOUND_KEY.
const sealPurpose = "webhook"

// SealKey returns the webhook encryption key derived from an outbound key.
func SealKey(outboundKey []byte) []byte { return identity.DeriveKey(outboundKey, sealPurpose) }

// Seal encrypts a webhook URL for tenants.discord_webhook_enc: AES-GCM under a key derived from
// the current PE_OUTBOUND_KEY, bound to the tenant id.
func Seal(outboundKey []byte, tenantID, webhookURL string) ([]byte, error) {
	return identity.Seal(SealKey(outboundKey), []byte(webhookURL), []byte(tenantID))
}

// Open decrypts a sealed webhook URL with the first of outboundKeys (app.Secrets.OutboundKeys:
// current key, then the rotation key) that opens it.
func Open(outboundKeys [][]byte, tenantID string, sealed []byte) (string, error) {
	for _, k := range outboundKeys {
		if len(k) == 0 {
			continue
		}
		if pt, err := identity.Open(SealKey(k), sealed, []byte(tenantID)); err == nil {
			return string(pt), nil
		}
	}
	return "", identity.ErrDecrypt
}
