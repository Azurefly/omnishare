package api

import (
	"context"
	"errors"
	"time"
)

// Clients poll the unified workspace and device list every few seconds. Without
// memory of recent outcomes every poll re-proved every route of every bound
// device with a fresh TCP connection, and an offline device cost a new SYN (up
// to a 3s dial timeout) per route per request, forever. Remember verified
// identities briefly and back off from routes that just failed, so an idle or
// partially offline workspace stays cheap. Trust is unchanged: a route is only
// used after its identity was proven, and the cache is dropped on any failure.
const (
	routeVerifiedTTL = 20 * time.Second
	routeBackoffMin  = 10 * time.Second
	routeBackoffMax  = 60 * time.Second
	routeHealthLimit = 512
)

type routeHealth struct {
	identity peerIdentity
	verified time.Time
	failures int
	until    time.Time
	err      error
}

// routeProbe returns the proven identity behind base, reusing a recent proof or
// a recent failure instead of dialing the route again.
func (s *Server) routeProbe(ctx context.Context, base string) (peerIdentity, error) {
	now := time.Now()
	s.peers.mu.Lock()
	if h, ok := s.peers.health[base]; ok {
		if !h.verified.IsZero() && now.Sub(h.verified) < routeVerifiedTTL {
			id := h.identity
			s.peers.mu.Unlock()
			return id, nil
		}
		if h.err != nil && now.Before(h.until) {
			err := h.err
			s.peers.mu.Unlock()
			return peerIdentity{}, err
		}
	}
	s.peers.mu.Unlock()
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	id, err := s.readIdentity(probe, base, true)
	cancel()
	s.recordRoute(ctx, base, id, err)
	return id, err
}

// recordRoute stores the outcome of using base. A failure caused by the caller
// giving up (client disconnect, aggregate deadline) says nothing about the
// route and is not recorded.
func (s *Server) recordRoute(ctx context.Context, base string, id peerIdentity, err error) {
	if err != nil && ctx.Err() != nil {
		return
	}
	s.recordOutcome(base, id, err)
}

// A device with several dead routes can exhaust a whole request budget before
// every route was tried, so per-route memory alone still re-dials it on each
// poll. Remember "no route answered" per device as well. A deadline counts as
// an outage; only a caller that went away (Canceled) is not evidence.
func peerHealthKey(id string) string { return "device:" + id }

func (s *Server) peerBackoff(id string) error {
	s.peers.mu.Lock()
	defer s.peers.mu.Unlock()
	if h, ok := s.peers.health[peerHealthKey(id)]; ok && h.err != nil && time.Now().Before(h.until) {
		return h.err
	}
	return nil
}

func (s *Server) recordPeer(ctx context.Context, id string, err error) {
	if err != nil && errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	s.recordOutcome(peerHealthKey(id), peerIdentity{}, err)
}

func (s *Server) recordOutcome(key string, id peerIdentity, err error) {
	s.peers.mu.Lock()
	defer s.peers.mu.Unlock()
	if s.peers.health == nil || len(s.peers.health) >= routeHealthLimit {
		s.peers.health = map[string]routeHealth{}
	}
	if err == nil {
		s.peers.health[key] = routeHealth{identity: id, verified: time.Now()}
		return
	}
	h := s.peers.health[key]
	h.identity, h.verified = peerIdentity{}, time.Time{}
	h.failures++
	backoff := routeBackoffMin << min(h.failures-1, 4)
	if backoff > routeBackoffMax {
		backoff = routeBackoffMax
	}
	h.until = time.Now().Add(backoff)
	h.err = err
	s.peers.health[key] = h
}

// resetRouteHealthLocked forgets all remembered outcomes, e.g. when the user
// asks for an explicit refresh or the set of bound devices changes. The caller
// holds s.peers.mu.
func (s *Server) resetRouteHealthLocked() {
	s.peers.health = map[string]routeHealth{}
}
