package gateway

import (
	"context"
	"sync"
	"time"
)

// seqDBInterval: the acked seq goes to PE_SERVERS after every ack, to the database at most this often.
const seqDBInterval = 2 * time.Second

// seqAdvancer raises the server's last acked seq after BatchAcks: KV PE_SERVERS (monotonic) right
// away, the servers row in the background, off the batch path. A lagging value only makes the plugin
// resend batches the stream de-duplicates.
type seqAdvancer struct {
	s    *session
	kick chan struct{}

	mu            sync.Mutex
	acked, kv, db uint64
}

func newSeqAdvancer(s *session) *seqAdvancer { return &seqAdvancer{s: s, kick: make(chan struct{}, 1)} }

// init sets the seq the cloud holds at the Welcome.
func (a *seqAdvancer) init(seq uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.acked, a.kv, a.db = seq, seq, seq
}

func (a *seqAdvancer) advance(seq uint64) {
	a.mu.Lock()
	if seq > a.acked {
		a.acked = seq
	}
	a.mu.Unlock()
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

func (a *seqAdvancer) loop() {
	t := time.NewTicker(seqDBInterval)
	defer t.Stop()
	for {
		select {
		case <-a.s.ctx.Done():
			return
		case <-a.kick:
			a.flush(a.s.ctx, false)
		case <-t.C:
			a.flush(a.s.ctx, true)
		}
	}
}

// flush writes the acked seq to KV, and to the database with db.
func (a *seqAdvancer) flush(ctx context.Context, db bool) {
	a.mu.Lock()
	acked, kv, dbDone := a.acked, a.kv, a.db
	a.mu.Unlock()
	s := a.s
	if acked > kv {
		kctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := s.g.d.Bus.AdvanceServerSeq(kctx, s.tenantID, s.serverID, acked)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("advancing the acked seq in PE_SERVERS failed", "seq", acked, "err", err)
			}
		} else {
			a.mu.Lock()
			a.kv = max(a.kv, acked)
			a.mu.Unlock()
		}
	}
	if db && acked > dbDone {
		dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.g.d.Store.AdvanceServerSeq(dctx, s.tenantID, s.serverID, acked, time.Now())
		cancel()
		if err != nil {
			s.log.Debug("storing the acked seq failed", "seq", acked, "err", err)
			return
		}
		a.mu.Lock()
		a.db = max(a.db, acked)
		a.mu.Unlock()
	}
}
