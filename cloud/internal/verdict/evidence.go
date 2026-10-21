package verdict

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// maxEvidenceSignals is how many signals the evidence lists (ARCHITECTURE 5.6: top 20).
const maxEvidenceSignals = 20

// Evidence is the evidence_json of a verdict (docs/API.md `evidence`).
type Evidence struct {
	Score      float64          `json:"score"`
	Confidence float64          `json:"confidence"`
	Leniency   int              `json:"leniency"`
	Thresholds EvidenceLevels   `json:"thresholds"`
	Families   []EvidenceFamily `json:"families"`
	Checks     []EvidenceCheck  `json:"checks"`
	Signals    []EvidenceSignal `json:"signals"`
	Session    EvidenceSession  `json:"session"`
}

// EvidenceLevels are the thresholds at the tenant's leniency.
type EvidenceLevels struct {
	Alert    float64 `json:"alert"`
	Mitigate float64 `json:"mitigate"`
	Kick     float64 `json:"kick"`
	Ban      float64 `json:"ban"`
}

// EvidenceFamily is one family's capped contribution.
type EvidenceFamily struct {
	Family       string  `json:"family"`
	Contribution float64 `json:"contribution"`
	Cap          float64 `json:"cap"`
}

// EvidenceCheck is one check's contribution.
type EvidenceCheck struct {
	Check        string  `json:"check"`
	Contribution float64 `json:"contribution"`
	Cap          float64 `json:"cap"`
	Confidence   float64 `json:"confidence"`
	Count        int     `json:"count"`
}

// EvidenceSignal is one of the strongest recent signals.
type EvidenceSignal struct {
	Check      string             `json:"check"`
	Category   string             `json:"category"`
	Points     float64            `json:"points"`
	Confidence float64            `json:"confidence"`
	Info       string             `json:"info"`
	Data       map[string]float64 `json:"data"`
	Time       string             `json:"time"`
	ClipID     string             `json:"clip_id"`
	Local      bool               `json:"local"`
}

// EvidenceSession describes the verdict's session.
type EvidenceSession struct {
	SessionID string  `json:"session_id"`
	ServerID  string  `json:"server_id"`
	StartedAt *string `json:"started_at"` // null when the session start is unknown
	HotLoaded bool    `json:"hot_loaded"`
	Exempt    bool    `json:"exempt"`
}

// CategoryName returns the API form of a category: the enum name without its prefix.
func CategoryName(c pb.Category) string { return strings.TrimPrefix(c.String(), "CATEGORY_") }

// ActionName returns the API form of an action: lowercase without its prefix ("ban").
func ActionName(a pb.Action) string {
	return strings.ToLower(strings.TrimPrefix(a.String(), "ACTION_"))
}

func round3(v float64) float64 { return math.Round(finite(v)*1000) / 1000 }

func rfc3339(us int64) string { return time.UnixMicro(us).UTC().Format(time.RFC3339Nano) }

// buildEvidence assembles the evidence of a verdict from the player's state.
func buildEvidence(pl *player, sess *session, sc Score, th Thresholds, leniency int) Evidence {
	ev := Evidence{
		Score: round3(sc.S), Confidence: round3(sc.C), Leniency: NormalizeLeniency(leniency),
		Thresholds: EvidenceLevels{Alert: round3(th.Alert), Mitigate: round3(th.Mitigate), Kick: round3(th.Kick), Ban: round3(th.Ban)},
		Families:   []EvidenceFamily{}, Checks: []EvidenceCheck{}, Signals: []EvidenceSignal{},
		Session: EvidenceSession{SessionID: sess.id, ServerID: sess.server, HotLoaded: sess.hotLoaded, Exempt: sess.exempt},
	}
	if sess.startT > 0 {
		s := rfc3339(sess.startT)
		ev.Session.StartedAt = &s
	}
	for _, f := range sc.Families {
		ev.Families = append(ev.Families, EvidenceFamily{Family: f.Family, Contribution: round3(f.Contribution), Cap: f.Cap})
	}
	active := map[string]bool{}
	for _, c := range sc.Checks {
		active[c.Check] = c.Acc >= 1
		ev.Checks = append(ev.Checks, EvidenceCheck{Check: c.Check, Contribution: round3(c.Contribution), Cap: c.Cap,
			Confidence: round3(c.Confidence), Count: c.Count})
	}
	// The strongest recent signals, preferring those of checks that still contribute; listed in time
	// order.
	recent := slices.Clone(pl.recent)
	slices.SortStableFunc(recent, func(a, b sigRecord) int {
		ai, bi := active[a.check], active[b.check]
		if ai != bi {
			if ai {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(b.points, a.points), cmp.Compare(b.t, a.t))
	})
	recent = recent[:min(len(recent), maxEvidenceSignals)]
	slices.SortStableFunc(recent, func(a, b sigRecord) int { return cmp.Compare(a.t, b.t) })
	for _, r := range recent {
		data := map[string]float64{}
		for k, v := range r.data {
			data[k] = finite(v)
		}
		ev.Signals = append(ev.Signals, EvidenceSignal{Check: r.check, Category: CategoryName(r.category), Points: round3(r.points),
			Confidence: round3(r.conf), Info: r.info, Data: data, Time: rfc3339(r.t), ClipID: r.clip, Local: r.local})
	}
	return ev
}

func (ev Evidence) marshal() string {
	b, err := json.Marshal(ev)
	if err != nil { // only non-finite floats can fail, and every float is sanitised
		return "{}"
	}
	return string(b)
}

// categoryLabels are the human category names used in verdict reasons.
var categoryLabels = map[pb.Category]string{
	pb.Category_CATEGORY_AUTO_CLICKER:      "Auto clicker",
	pb.Category_CATEGORY_HEURISTICS:        "Combat heuristics",
	pb.Category_CATEGORY_INVALID_PROTOCOL:  "Invalid protocol",
	pb.Category_CATEGORY_MOVEMENT:          "Movement",
	pb.Category_CATEGORY_TICK_SPEED:        "Tick speed",
	pb.Category_CATEGORY_INVENTORY:         "Inventory",
	pb.Category_CATEGORY_WORLD_INTERACTION: "World interaction",
	pb.Category_CATEGORY_LATENCY_ABUSE:     "Latency abuse",
}

// reason is the short human summary of a verdict, e.g. "Combat heuristics (killaura.hitbox x7,
// aim.snap x3)": the dominant category and its strongest checks (at most 3). It only uses
// characters the plugin keeps in %reason% placeholders.
func reason(sc Score) string {
	label, ok := categoryLabels[sc.Dominant]
	if !ok {
		label = "Suspicious behaviour"
	}
	var parts []string
	for _, c := range sc.Contributing() {
		if c.Category != sc.Dominant {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s x%d", c.Check, c.Count))
		if len(parts) == 3 {
			break
		}
	}
	if len(parts) == 0 {
		return label
	}
	return label + " (" + strings.Join(parts, ", ") + ")"
}

// mitigationsFor returns the mitigations of a dominant category (ARCHITECTURE 5.6).
func mitigationsFor(c pb.Category) []pb.Mitigation {
	switch c {
	case pb.Category_CATEGORY_HEURISTICS, pb.Category_CATEGORY_AUTO_CLICKER:
		return []pb.Mitigation{pb.Mitigation_MITIGATION_HIT_CANCEL, pb.Mitigation_MITIGATION_DAMAGE_REDUCE, pb.Mitigation_MITIGATION_VELOCITY_BOOST}
	case pb.Category_CATEGORY_MOVEMENT, pb.Category_CATEGORY_TICK_SPEED:
		return []pb.Mitigation{pb.Mitigation_MITIGATION_SETBACK}
	case pb.Category_CATEGORY_WORLD_INTERACTION:
		return []pb.Mitigation{pb.Mitigation_MITIGATION_PLACE_CANCEL}
	}
	return []pb.Mitigation{pb.Mitigation_MITIGATION_HIT_CANCEL}
}

// mitigationStrength = clamp((S - MITIGATE) / (KICK - MITIGATE), 0.3, 1).
func mitigationStrength(s float64, th Thresholds) float64 {
	v := (s - th.Mitigate) / (th.Kick - th.Mitigate)
	return round3(min(1, max(0.3, finite(v))))
}

// clipIDs returns the committed clips of the contributing checks, sorted, without duplicates.
func clipIDs(contributing []CheckContribution) []string {
	set := map[string]bool{}
	for _, c := range contributing {
		for _, id := range c.Clips {
			set[id] = true
		}
	}
	return slices.Sorted(maps.Keys(set))
}
