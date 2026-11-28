package main

import (
	"context"
	"time"

	"github.com/tryatlas/cli/internal/capture/store"
	capsync "github.com/tryatlas/cli/internal/capture/sync"
	"github.com/tryatlas/cli/internal/listener"
)

// atlas-cli serve in a workspace folder drains its member Projects' stores
// too (FR-11, ADR-0018): a hook drains its own store after each turn, but a
// turn that could not reach the cloud waits for the next hook in that
// Project. A member with its own listener, or whose writer lock is held, is
// left to whoever holds it.

// memberDrainEvery is how often serve drains the member stores.
const memberDrainEvery = 2 * capsync.Tick

// drainMembersOnce drains each member store that no one else holds and
// answers how many it drained. It stops at the first pass that finds the
// cloud out of reach: the others would find the same.
func drainMembersOnce(ctx context.Context, c *capsync.Client, ws string) int {
	n := 0
	for _, m := range linkedMembers(ctx, ws) {
		if ctx.Err() != nil {
			return n
		}
		if rf := listener.ReadRunFile(m); rf != nil && alive(rf.PID) {
			continue // its own listener drains it
		}
		rep, ok := drainMember(ctx, c, m)
		if !ok {
			continue
		}
		n++
		if rep.Outcome == capsync.Offline || rep.Outcome == capsync.SignedOut || rep.Outcome == capsync.RateLimited {
			return n
		}
	}
	return n
}

// drainMember drains one member store under its writer lock; false when
// the store is held, missing or local.
func drainMember(ctx context.Context, c *capsync.Client, dir string) (capsync.Report, bool) {
	if !store.Exists(dir) {
		return capsync.Report{}, false
	}
	lock, err := store.Lock(dir)
	if err != nil {
		return capsync.Report{}, false
	}
	defer func() { _ = lock.Release() }()
	st, err := store.Open(dir, false)
	if err != nil {
		return capsync.Report{}, false
	}
	defer st.Close()
	b, err := st.Binding(ctx)
	if err != nil {
		return capsync.Report{}, false
	}
	if _, ok := capsync.TargetOf(b); !ok {
		return capsync.Report{}, false
	}
	rep, err := capsync.Drain(ctx, c, st, capsync.Budget, time.Now)
	return rep, err == nil
}

// drainMembers drains the member stores now and every memberDrainEvery,
// until ctx ends.
func drainMembers(ctx context.Context, c *capsync.Client, ws string) {
	t := time.NewTicker(memberDrainEvery)
	defer t.Stop()
	for {
		drainMembersOnce(ctx, c, ws)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
