package graph

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/vikranthBala/cartographer/internal/collector"
)

// Node is a live connection with stable identity and timing metadata.
type Node struct {
	collector.EnrichedConn
	ID        string
	FirstSeen time.Time
	LastSeen  time.Time
}

// ChangeType describes what happened to a node in this delta.
type ChangeType int

const (
	NodeAdded ChangeType = iota
	NodeUpdated
	NodeRemoved
)

// Change is a single node mutation.
type Change struct {
	Type ChangeType
	Node Node
}

// Delta is a batch of changes emitted after each upsert or eviction cycle.
type Delta struct {
	Changes []Change
}

func (d Delta) IsEmpty() bool { return len(d.Changes) == 0 }

// Store holds the live node graph and emits deltas downstream.
type Store struct {
	mu     sync.RWMutex
	nodes  map[string]*Node // identityKey → Node
	deltas chan Delta
	ttl    time.Duration
}

// NewStore creates a Store ready to be started with Run.
// ttl controls how long a node can go unseen before eviction.
func NewStore(ttl time.Duration) *Store {
	return &Store{
		nodes:  make(map[string]*Node),
		deltas: make(chan Delta, 64),
		ttl:    ttl,
	}
}

// Run consumes the classifier output channel and drives the eviction ticker.
// It closes the deltas channel when it exits so the TUI loop terminates cleanly.
func (s *Store) Run(ctx context.Context, in <-chan collector.EnrichedConn) {
	ticker := time.NewTicker(s.ttl / 2)
	defer ticker.Stop()
	defer close(s.deltas)

	for {
		select {
		case <-ctx.Done():
			return

		case conn, ok := <-in:
			if !ok {
				return
			}
			if change, changed := s.upsert(conn); changed {
				s.emit(Delta{Changes: []Change{change}})
			}

		case <-ticker.C:
			if evicted := s.evict(); len(evicted) > 0 {
				s.emit(Delta{Changes: evicted})
			}
		}
	}
}

// Deltas returns the read-only channel the TUI subscribes to.
func (s *Store) Deltas() <-chan Delta {
	return s.deltas
}

// Snapshot returns the full current node list — call once on TUI init.
func (s *Store) Snapshot() []Node {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Node, 0, len(s.nodes))
	for _, n := range s.nodes {
		out = append(out, *n)
	}
	return out
}

// ── internal ──────────────────────────────────────────────────────────────────

func (s *Store) upsert(conn collector.EnrichedConn) (Change, bool) {
	key := conn.Key()
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.nodes[key]; ok {
		// Only emit NodeUpdated when something meaningful changes.
		changed := existing.ServiceLabel != conn.ServiceLabel ||
			existing.Category != conn.Category ||
			existing.State != conn.State

		existing.EnrichedConn = conn
		existing.LastSeen = now

		if changed {
			return Change{Type: NodeUpdated, Node: *existing}, true
		}
		return Change{}, false
	}

	node := &Node{
		EnrichedConn: conn,
		ID:           uuid.NewString(),
		FirstSeen:    now,
		LastSeen:     now,
	}
	s.nodes[key] = node
	return Change{Type: NodeAdded, Node: *node}, true
}

func (s *Store) evict() []Change {
	cutoff := time.Now().Add(-s.ttl)

	s.mu.Lock()
	defer s.mu.Unlock()

	var removed []Change
	for key, node := range s.nodes {
		if node.LastSeen.Before(cutoff) {
			removed = append(removed, Change{Type: NodeRemoved, Node: *node})
			delete(s.nodes, key)
		}
	}
	return removed
}

// emit is non-blocking — drops the delta if the TUI is too slow to consume.
func (s *Store) emit(d Delta) {
	select {
	case s.deltas <- d:
	default:
	}
}
