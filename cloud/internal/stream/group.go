package stream

import (
	"context"
	"errors"
	"hash/fnv"
	"sort"
	"sync"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/config"
)

// PartitionFunc does the work of one leased partition. ctx ends when the lease is lost or the group
// stops; fn must then return promptly, acking only while lease.Valid().
type PartitionFunc func(ctx context.Context, lease *Lease, partition int) error

// RunPartitions holds leases "<group>.<NN>" for the partitions sel selects (config.LeaseAll: all
// of them; config.LeaseList: the listed ones; config.LeaseAuto: a fair share, ceil(partitions /
// live members), releasing extras when members join) and runs fn for each, after the settle wait.
// When fn returns the lease is released and re-acquired on a later round. RunPartitions returns when
// ctx is done, after every fn has returned and released its lease.
func (b *Bus) RunPartitions(ctx context.Context, group string, sel config.LeaseSelection, fn PartitionFunc) error {
	return b.LeaseManager.runPartitions(ctx, group, b.cfg.Partitions, sel, fn)
}

type runningPartition struct {
	cancel context.CancelCauseFunc
	done   chan struct{}
}

func (m *LeaseManager) runPartitions(ctx context.Context, group string, n int, sel config.LeaseSelection, fn PartitionFunc) error {
	if sel.Mode == "" {
		sel.Mode = config.LeaseAuto
	}
	member := "members." + group + "." + m.ownerID + "_" + newEpoch()
	var (
		mu      sync.Mutex
		running = map[int]*runningPartition{}
		wg      sync.WaitGroup
	)
	start := func(p int, l *Lease) {
		pctx, cancel := context.WithCancelCause(ctx)
		stop := context.AfterFunc(l.Context(), func() { cancel(context.Cause(l.Context())) })
		r := &runningPartition{cancel: cancel, done: make(chan struct{})}
		running[p] = r
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(r.done)
			err := l.WaitSettled(pctx)
			if err == nil {
				err = fn(pctx, l, p)
			}
			stop()
			cancel(nil)
			rctx, rcancel := context.WithTimeout(context.Background(), m.t.Request)
			if rerr := l.Release(rctx); rerr != nil {
				m.log.Warn("lease release failed", "lease", l.Key(), "err", rerr)
			}
			rcancel()
			if err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
				m.log.Warn("partition stopped", "lease", l.Key(), "err", err)
			}
			mu.Lock()
			if running[p] == r {
				delete(running, p)
			}
			mu.Unlock()
		}()
	}
	candidates := partitionOrder(sel, n, member)
	round := func() {
		limit := len(candidates)
		if sel.Mode == config.LeaseAuto {
			limit = m.fairShare(ctx, group, member, n)
		}
		mu.Lock()
		defer mu.Unlock()
		if extra := len(running) - limit; extra > 0 {
			held := make([]int, 0, len(running))
			for p := range running {
				held = append(held, p)
			}
			sort.Sort(sort.Reverse(sort.IntSlice(held)))
			for _, p := range held[:extra] {
				m.log.Info("releasing partition for rebalancing", "lease", PartitionLeaseKey(group, p))
				running[p].cancel(ErrReleased)
			}
			return
		}
		for _, p := range candidates {
			if len(running) >= limit || ctx.Err() != nil {
				return
			}
			if _, ok := running[p]; ok {
				continue
			}
			l, err := m.TryAcquire(ctx, PartitionLeaseKey(group, p))
			if err != nil {
				if !errors.Is(err, ErrLeaseHeld) && ctx.Err() == nil {
					m.log.Warn("lease acquisition failed", "lease", PartitionLeaseKey(group, p), "err", err)
				}
				continue
			}
			start(p, l)
		}
	}
	tick := time.NewTicker(m.t.Renew)
	defer tick.Stop()
	for {
		round()
		select {
		case <-ctx.Done():
			wg.Wait()
			if sel.Mode == config.LeaseAuto {
				dctx, cancel := context.WithTimeout(context.Background(), m.t.Request)
				_ = m.kv.Delete(dctx, member)
				cancel()
			}
			return nil
		case <-tick.C:
		}
	}
}

// fairShare announces this member and returns ceil(n / live members of the group).
func (m *LeaseManager) fairShare(ctx context.Context, group, member string, n int) int {
	rctx, cancel := context.WithTimeout(ctx, m.t.Request)
	defer cancel()
	if _, err := m.kv.PutString(rctx, member, m.ownerID); err != nil {
		return n
	}
	lister, err := m.kv.ListKeysFiltered(rctx, "members."+group+".*")
	if err != nil {
		return n
	}
	members := 0
	for range lister.Keys() { // the channel closes when the listing is complete
		members++
	}
	if members < 1 {
		members = 1
	}
	return (n + members - 1) / members
}

// partitionOrder lists the selected partitions; in auto mode the list starts at a member-specific
// offset so concurrent members try different partitions first.
func partitionOrder(sel config.LeaseSelection, n int, member string) []int {
	if sel.Mode == config.LeaseList {
		return sel.Partitions
	}
	out := make([]int, n)
	off := 0
	if sel.Mode == config.LeaseAuto {
		h := fnv.New32a()
		h.Write([]byte(member))
		off = int(h.Sum32() % uint32(n))
	}
	for i := range out {
		out[i] = (i + off) % n
	}
	return out
}

// RunSingleton holds the lease on key (e.g. LeaseWriter) whenever it can and runs fn while holding it,
// after the settle wait. fn's ctx ends when the lease is lost or ctx is done. It returns when ctx is
// done and fn has returned.
func (b *Bus) RunSingleton(ctx context.Context, key string, fn func(ctx context.Context, lease *Lease) error) error {
	m := b.LeaseManager
	for {
		l, err := m.TryAcquire(ctx, key)
		if err == nil {
			lctx, cancel := context.WithCancelCause(ctx)
			stop := context.AfterFunc(l.Context(), func() { cancel(context.Cause(l.Context())) })
			err = l.WaitSettled(lctx)
			if err == nil {
				err = fn(lctx, l)
			}
			stop()
			cancel(nil)
			rctx, rcancel := context.WithTimeout(context.Background(), m.t.Request)
			if rerr := l.Release(rctx); rerr != nil {
				m.log.Warn("lease release failed", "lease", key, "err", rerr)
			}
			rcancel()
			if err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
				m.log.Warn("singleton stopped", "lease", key, "err", err)
			}
		} else if !errors.Is(err, ErrLeaseHeld) && ctx.Err() == nil {
			m.log.Warn("lease acquisition failed", "lease", key, "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(m.t.Renew):
		}
	}
}
