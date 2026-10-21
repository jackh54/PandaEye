package discord

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// VerdictEmbed is what a verdict alert shows (ARCHITECTURE 5.6): player, server, action (with a
// SHADOW label when the verdict is not enforced), score, confidence, the top 5 checks and the
// evidence link. Never IP addresses.
type VerdictEmbed struct {
	Player, PlayerUUID, Server, Action string
	Shadow                             bool
	Score, Confidence                  float64
	Checks                             []string
	EvidenceURL                        string
}

// Discord limits.
const (
	maxTitle      = 256
	maxFieldName  = 256
	maxFieldValue = 1024
	maxChecks     = 5
)

type message struct {
	Username        string          `json:"username,omitempty"`
	Content         string          `json:"content,omitempty"`
	Embeds          []embed         `json:"embeds,omitempty"`
	AllowedMentions allowedMentions `json:"allowed_mentions"`
}

// allowedMentions{Parse: []} turns every mention off (@everyone, roles, users).
type allowedMentions struct {
	Parse []string `json:"parse"`
}

func noMentions() allowedMentions { return allowedMentions{Parse: []string{}} }

type embed struct {
	Title     string       `json:"title"`
	URL       string       `json:"url,omitempty"`
	Color     int          `json:"color"`
	Fields    []embedField `json:"fields"`
	Footer    *embedFooter `json:"footer,omitempty"`
	Timestamp string       `json:"timestamp,omitempty"`
}

type embedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type embedFooter struct {
	Text string `json:"text"`
}

var actionColors = map[string]int{
	"ALERT": 0xF1C40F, "MITIGATE": 0xE67E22, "KICK": 0xE74C3C, "BAN": 0x992D22, "UNMITIGATE": 0x2ECC71, "UNBAN": 0x2ECC71,
}

const shadowColor = 0x95A5A6

var canonicalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (c *Client) verdictMessage(e VerdictEmbed) message {
	action := strings.ToUpper(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(e.Action)), "ACTION_"))
	if action == "" {
		action = "VERDICT"
	}
	label := Escape(action)
	title := label
	if e.Shadow {
		label += " (SHADOW)"
		title = "[SHADOW] " + title
	}
	player := Escape(e.Player)
	if player == "" {
		player = "unknown player"
	}
	title += ": " + player
	color, ok := actionColors[action]
	if !ok {
		color = actionColors["ALERT"]
	}
	if e.Shadow {
		color = shadowColor
	}
	playerValue := player
	if canonicalUUID.MatchString(e.PlayerUUID) {
		playerValue += "\n`" + e.PlayerUUID + "`"
	}
	fields := []embedField{
		{Name: "Player", Value: playerValue, Inline: true},
		{Name: "Server", Value: orDash(Escape(e.Server)), Inline: true},
		{Name: "Action", Value: label, Inline: true},
		{Name: "Score", Value: fmt.Sprintf("%.1f", finite(e.Score)), Inline: true},
		{Name: "Confidence", Value: fmt.Sprintf("%.0f%%", 100*clamp01(e.Confidence)), Inline: true},
	}
	var checks []string
	for _, ch := range e.Checks {
		if s := Escape(ch); s != "" {
			checks = append(checks, s)
		}
		if len(checks) == maxChecks {
			break
		}
	}
	if len(checks) > 0 {
		fields = append(fields, embedField{Name: "Top checks", Value: strings.Join(checks, ", ")})
	}
	link := safeLink(e.EvidenceURL)
	if link != "" {
		fields = append(fields, embedField{Name: "Evidence", Value: "[Open in the dashboard](" + link + ")"})
	}
	for i := range fields {
		fields[i].Name = truncate(fields[i].Name, maxFieldName)
		fields[i].Value = truncate(fields[i].Value, maxFieldValue)
	}
	footer := "PandaEye"
	if e.Shadow {
		footer += " · shadow: not enforced"
	}
	return message{
		Username: c.username,
		Embeds: []embed{{
			Title: truncate(title, maxTitle), URL: link, Color: color, Fields: fields,
			Footer: &embedFooter{Text: footer}, Timestamp: c.now().UTC().Format(time.RFC3339),
		}},
		AllowedMentions: noMentions(),
	}
}

// markdownSpecial are the characters Discord markdown (and mention/emoji syntax) gives a meaning.
const markdownSpecial = "\\*_~`|>#-[]()<@:!.+="

// Escape makes s inert in Discord markdown: control characters become spaces and every markdown
// character is backslash-escaped.
func Escape(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range strings.ToValidUTF8(s, "�") {
		switch {
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == ' ' || r == ' ':
			b.WriteByte(' ')
		case r < 0x80 && strings.ContainsRune(markdownSpecial, r):
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// truncate cuts s to at most n runes without leaving a dangling escape backslash.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)[:n-1]
	// Drop a trailing unpaired backslash so the ellipsis is not escaped.
	bs := 0
	for i := len(r) - 1; i >= 0 && r[i] == '\\'; i-- {
		bs++
	}
	if bs%2 == 1 {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// safeLink returns u when it is an absolute http(s) URL that can sit inside a markdown link.
func safeLink(u string) string {
	if u == "" || len(u) > 512 {
		return ""
	}
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" || p.User != nil {
		return ""
	}
	s := p.String()
	if strings.ContainsAny(s, " ()<>\"'`") {
		return ""
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "\\-"
	}
	return s
}

func finite(f float64) float64 {
	if f != f || f > 1e12 || f < -1e12 {
		return 0
	}
	return f
}

func clamp01(f float64) float64 {
	switch {
	case f != f || f < 0:
		return 0
	case f > 1:
		return 1
	}
	return f
}
