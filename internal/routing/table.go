// Package routing holds the routing plane: the shared building blocks
// (shortest-path computation, controlled flooding, duplicate suppression) and
// the three interchangeable algorithms built on top of them.
package routing

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Route is one entry of a routing table: to reach Dest, hand the packet to
// NextHop, and the total path costs Cost.
type Route struct {
	Dest    string  `json:"dest"`
	NextHop string  `json:"next_hop"`
	Cost    float64 `json:"cost"`
}

// Table is a snapshot of the routes known by a node, keyed by destination.
type Table map[string]Route

// Sorted returns the routes ordered by destination, for stable output.
func (t Table) Sorted() []Route {
	routes := make([]Route, 0, len(t))
	for _, r := range t {
		routes = append(routes, r)
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Dest < routes[j].Dest })
	return routes
}

// String renders the table as an aligned block for the interactive console.
func (t Table) String() string {
	if len(t) == 0 {
		return "(empty routing table)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-16s %-16s %s\n", "DEST", "NEXT HOP", "COST")
	for _, r := range t.Sorted() {
		fmt.Fprintf(&b, "%-16s %-16s %.2f\n", r.Dest, r.NextHop, r.Cost)
	}
	return strings.TrimRight(b.String(), "\n")
}

// SafeTable guards a Table shared between the routing and forwarding
// goroutines: routing rewrites it wholesale, forwarding reads it per packet.
type SafeTable struct {
	mu    sync.RWMutex
	table Table
}

// NewSafeTable returns an empty concurrent table.
func NewSafeTable() *SafeTable {
	return &SafeTable{table: Table{}}
}

// Replace swaps in a newly computed table.
func (s *SafeTable) Replace(t Table) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.table = t
}

// Lookup returns the route towards dest.
func (s *SafeTable) Lookup(dest string) (Route, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.table[dest]
	return r, ok
}

// Snapshot returns a copy safe to read outside the lock.
func (s *SafeTable) Snapshot() Table {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(Table, len(s.table))
	for k, v := range s.table {
		out[k] = v
	}
	return out
}
