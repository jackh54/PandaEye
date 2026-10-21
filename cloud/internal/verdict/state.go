package verdict

import (
	"cmp"
	"hash/maphash"
	"slices"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// Time spans in normalised microseconds.
const (
	usPerSecond    = int64(time.Second / time.Microsecond)
	hotHalfWindow  = 60 * usPerSecond      // hot-loaded sessions count half for 60 s after seeding
	alertInterval  = 60 * usPerSecond      // ALERT at most once a minute
	sessionIdle    = 30 * 60 * usPerSecond // a session without markers or signals for 30 min is closed
	evictAfterIdle = 60 * 60 * usPerSecond // evict players without an open session for 1 h ...
	maxRecent      = 32                    // signals remembered per player for evidence
)

type playerKey struct {
	tenant string
	player identity.UUID
}

// action bits of session.actions.
func actionBit(a pb.Action) uint8 { return 1 << uint(a) }

// session is one PlayerJoin session of a player on one server (or proxy).
type session struct {
	id         string
	server     string
	serverName string
	network    string
	platform   pb.Platform
	shadowMode bool  // origin shadow_mode of the latest marker or signal
	startT     int64 // normalised session start (0 = unknown)
	marked     bool  // a JOIN/UPDATE marker was seen: the session is seeded
	hotLoaded  bool
	exempt     bool
	open       bool
	quit       bool  // closed by a QUIT marker (signals do not reopen it)
	lastSeenT  int64 // normalised time of the latest marker or signal
	closedT    int64

	actions      uint8 // MITIGATE/KICK/BAN already produced for this session
	actionsKnown bool  // actions reflect the store (sessions first seen live, or loaded)
}

func (s *session) taken(a pb.Action) bool { return s.actions&actionBit(a) != 0 }

// sigRecord is a remembered signal for the evidence.
type sigRecord struct {
	check    string
	category pb.Category
	points   float64
	conf     float64
	info     string
	data     map[string]float64
	t        int64
	clip     string
	local    bool
}

// player is the verdict state of one (tenant, player).
type player struct {
	key       playerKey
	name      string
	createdAt time.Time // wall clock (erasure comparison)
	score     scoreState
	sessions  map[string]*session
	recent    []sigRecord // newest last, at most maxRecent

	alertBase  float64 // score at the last ALERT (0 = no alert since S was last below ALERT)
	lastAlertT int64
	lastWatch  time.Time // wall clock of the last WatchRequest

	newestServer     string // origin of the newest signal or marker (BAN fallback route)
	newestServerName string
	newestNetwork    string
}

func newPlayer(k playerKey, now time.Time) *player {
	return &player{key: k, createdAt: now, sessions: map[string]*session{}}
}

func (p *player) touch(name string, o *pb.Origin) {
	if name != "" {
		p.name = name
	}
	if o.GetServerId() != "" {
		p.newestServer, p.newestServerName = o.ServerId, o.ServerName
		if o.NetworkId != "" {
			p.newestNetwork = o.NetworkId
		}
	}
}

func (p *player) remember(r sigRecord) {
	p.recent = append(p.recent, r)
	if len(p.recent) > maxRecent {
		p.recent = slices.Delete(p.recent, 0, len(p.recent)-maxRecent)
	}
}

func newSession(id string, o *pb.Origin) *session {
	return &session{id: id, server: o.GetServerId(), serverName: o.GetServerName(), network: o.GetNetworkId(),
		platform: o.GetPlatform(), shadowMode: o.GetShadowMode(), open: true}
}

func (s *session) setOrigin(o *pb.Origin) {
	if o.GetServerId() != "" {
		s.server, s.serverName, s.platform = o.ServerId, o.ServerName, o.Platform
	}
	if o.GetNetworkId() != "" {
		s.network = o.NetworkId
	}
	s.shadowMode = o.GetShadowMode()
}

// sessionForSignal returns the signal's session, creating an inferred one (open, actions unknown)
// when no marker was seen for it. A signal reopens a session that only went idle.
func (p *player) sessionForSignal(id string, o *pb.Origin, hot bool, t int64) *session {
	s := p.sessions[id]
	if s == nil {
		s = newSession(id, o)
		s.hotLoaded = hot
		p.sessions[id] = s
	}
	if !s.quit {
		s.open = true
	}
	s.lastSeenT = max(s.lastSeenT, t)
	return s
}

// join applies a JOIN marker: the session opens (seeded), and other open sessions of the player on
// the same server end (a real join replaces them).
func (p *player) join(m *pb.SessionMarker, startT int64, known bool) *session {
	o := m.GetOrigin()
	for _, other := range p.sessions {
		if other.id != m.SessionId && other.open && other.server == o.GetServerId() {
			other.open, other.closedT = false, max(other.lastSeenT, startT)
		}
	}
	s := p.sessions[m.SessionId]
	if s == nil {
		s = newSession(m.SessionId, o)
		s.actionsKnown = known
		p.sessions[m.SessionId] = s
	}
	s.setOrigin(o)
	s.startT, s.marked, s.hotLoaded, s.exempt = startT, true, m.HotLoaded, m.Exempt
	if !s.quit {
		s.open = true
	}
	s.lastSeenT = max(s.lastSeenT, startT)
	return s
}

// update applies an UPDATE marker (metadata refresh, periodic re-announce).
func (p *player) update(m *pb.SessionMarker, startT, t int64) *session {
	o := m.GetOrigin()
	s := p.sessions[m.SessionId]
	if s == nil {
		s = newSession(m.SessionId, o)
		p.sessions[m.SessionId] = s
	}
	s.setOrigin(o)
	if s.startT == 0 {
		s.startT = startT
	}
	s.marked, s.hotLoaded, s.exempt = true, s.hotLoaded || m.HotLoaded, m.Exempt
	if !s.quit {
		s.open = true
	}
	s.lastSeenT = max(s.lastSeenT, t)
	return s
}

// quit applies a QUIT marker.
func (p *player) quit(m *pb.SessionMarker, t int64) {
	s := p.sessions[m.SessionId]
	if s == nil {
		return
	}
	s.open, s.quit, s.closedT = false, true, max(t, s.lastSeenT)
}

// openSessions returns the open sessions, the most recently active first.
func (p *player) openSessions() []*session {
	var out []*session
	for _, s := range p.sessions {
		if s.open {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b *session) int {
		return cmp.Or(cmp.Compare(b.lastSeenT, a.lastSeenT), cmp.Compare(a.id, b.id))
	})
	return out
}

// housekeep closes idle sessions, forgets sessions closed for an hour and reports whether the
// player can be evicted: every accumulator < 0.5 and no session open within the last hour.
func (p *player) housekeep(now int64) (evict bool) {
	lastOpen := p.score.lastT
	for id, s := range p.sessions {
		if s.open && now-s.lastSeenT > sessionIdle {
			s.open, s.closedT = false, s.lastSeenT
		}
		if s.open {
			lastOpen = now
			continue
		}
		lastOpen = max(lastOpen, s.closedT)
		if now-s.closedT > evictAfterIdle {
			delete(p.sessions, id)
		}
	}
	return p.score.decayedMax(now) < emptyAcc && now-lastOpen > evictAfterIdle
}

// idSet remembers the hashes of the most recent applied msg ids (FIFO eviction).
type idSet struct {
	seed  maphash.Seed
	set   map[uint64]struct{}
	ring  []uint64
	next  int
	limit int
}

func newIDSet(limit int) *idSet {
	return &idSet{seed: maphash.MakeSeed(), set: make(map[uint64]struct{}, limit), ring: make([]uint64, 0, limit), limit: limit}
}

// add records id and reports whether it was new.
func (s *idSet) add(id string) bool {
	h := maphash.String(s.seed, id)
	if _, ok := s.set[h]; ok {
		return false
	}
	if len(s.ring) < s.limit {
		s.ring = append(s.ring, h)
	} else {
		delete(s.set, s.ring[s.next])
		s.ring[s.next] = h
		s.next = (s.next + 1) % s.limit
	}
	s.set[h] = struct{}{}
	return true
}
