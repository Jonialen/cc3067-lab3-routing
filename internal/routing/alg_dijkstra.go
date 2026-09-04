package routing

import (
	"context"
	"sync"

	"github.com/uvg/cc3067-lab3/internal/protocol"
)

// DijkstraAlgorithm routes with a shortest-path table computed once from a
// topology handed to the node at boot.
//
// This is the static mode required by the assignment: the node is told the
// whole graph instead of discovering it. It still reacts to neighbours that
// stop answering hellos by pruning their links and recomputing, so a node
// going down does not black-hole traffic.
type DijkstraAlgorithm struct {
	fabric Fabric
	table  *SafeTable

	mu sync.Mutex
	// base is the topology as configured and never mutated, so a neighbour
	// that comes back can be restored without re-reading the file.
	base Graph
	// down tracks neighbours currently considered unreachable.
	down map[string]bool
}

// NewDijkstra builds the static shortest-path algorithm over the given graph.
func NewDijkstra(fabric Fabric, topology Graph) *DijkstraAlgorithm {
	return &DijkstraAlgorithm{
		fabric: fabric,
		table:  NewSafeTable(),
		base:   topology,
		down:   map[string]bool{},
	}
}

// Proto identifies packets emitted by this algorithm.
func (d *DijkstraAlgorithm) Proto() protocol.Proto { return protocol.ProtoDijkstra }

// Start computes the initial table. There is no periodic work: the topology is
// static, and changes arrive through OnLinkUp/OnLinkDown.
func (d *DijkstraAlgorithm) Start(ctx context.Context) {
	d.recompute()
}

// Forward returns the single next hop towards the packet's destination.
func (d *DijkstraAlgorithm) Forward(pkt *protocol.Packet) []string {
	dest := d.fabric.LocalID(pkt.To)
	route, ok := d.table.Lookup(dest)
	if !ok {
		d.fabric.Logf("no route to %s, dropping %s", dest, pkt)
		return nil
	}
	return []string{route.NextHop}
}

// HandleInfo is a no-op: a statically configured node exchanges no routing
// information. Packets that arrive anyway are reported and ignored.
func (d *DijkstraAlgorithm) HandleInfo(pkt *protocol.Packet) {
	d.fabric.Logf("ignoring info packet in static dijkstra mode: %s", pkt)
}

// OnLinkUp restores a neighbour and recomputes the table if anything changed.
func (d *DijkstraAlgorithm) OnLinkUp(neighbor string, cost float64) {
	d.mu.Lock()
	changed := d.down[neighbor]
	delete(d.down, neighbor)
	d.mu.Unlock()

	if changed {
		d.fabric.Logf("link to %s restored, recomputing routes", neighbor)
		d.recompute()
	}
}

// OnLinkDown prunes a neighbour and recomputes the table.
func (d *DijkstraAlgorithm) OnLinkDown(neighbor string) {
	d.mu.Lock()
	changed := !d.down[neighbor]
	d.down[neighbor] = true
	d.mu.Unlock()

	if changed {
		d.fabric.Logf("link to %s lost, recomputing routes", neighbor)
		d.recompute()
	}
}

// Table exposes the current routes.
func (d *DijkstraAlgorithm) Table() Table { return d.table.Snapshot() }

// Topology exposes the configured graph, minus any link currently considered
// down.
func (d *DijkstraAlgorithm) Topology() []Edge {
	d.mu.Lock()
	defer d.mu.Unlock()

	edges := make([]Edge, 0)
	for from, links := range d.base {
		if d.down[from] {
			continue
		}
		for to, cost := range links {
			if d.down[to] {
				continue
			}
			edges = append(edges, Edge{From: from, To: to, Cost: cost})
		}
	}
	return edges
}

// recompute rebuilds the shortest-path table from the configured topology
// minus the links currently considered down.
func (d *DijkstraAlgorithm) recompute() {
	d.mu.Lock()
	graph := NewGraph()
	for from, links := range d.base {
		for to, cost := range links {
			// A link is usable only when neither endpoint is a neighbour we
			// have declared unreachable.
			if d.down[from] || d.down[to] {
				continue
			}
			graph.AddEdge(from, to, cost)
		}
	}
	d.mu.Unlock()

	d.table.Replace(Dijkstra(graph, d.fabric.ID()))
}
