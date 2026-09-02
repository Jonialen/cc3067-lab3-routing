package routing

import (
	"container/heap"
	"math"
	"sort"
)

// Graph is a weighted directed adjacency structure. Both directions of a
// bidirectional link are stored explicitly, so asymmetric costs are expressible.
type Graph map[string]map[string]float64

// NewGraph returns an empty graph.
func NewGraph() Graph { return Graph{} }

// AddEdge records a directed link from -> to with the given cost. The lower
// cost wins when the same edge is declared twice by different sources.
func (g Graph) AddEdge(from, to string, cost float64) {
	if _, ok := g[from]; !ok {
		g[from] = map[string]float64{}
	}
	if _, ok := g[to]; !ok {
		g[to] = map[string]float64{}
	}
	if existing, ok := g[from][to]; !ok || cost < existing {
		g[from][to] = cost
	}
}

// Nodes returns every node in the graph, sorted for deterministic tie-breaking.
func (g Graph) Nodes() []string {
	ids := make([]string, 0, len(g))
	for id := range g {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Dijkstra computes the shortest path from source to every reachable node and
// returns the resulting routing table.
//
// This is the single implementation of the algorithm in the project. It is
// used directly when the node runs in "dijkstra" mode over a configured
// topology, and reused by Link State Routing over the topology reconstructed
// from the link-state database. That reuse is the whole point of keeping it a
// pure function of (graph, source) with no knowledge of sockets or packets.
func Dijkstra(g Graph, source string) Table {
	dist := map[string]float64{source: 0}
	// firstHop remembers which neighbour of source starts the best known path
	// to each node, which is exactly what a forwarding table needs.
	firstHop := map[string]string{}
	visited := map[string]bool{}

	pq := &nodeQueue{}
	heap.Init(pq)
	heap.Push(pq, queued{id: source, dist: 0})

	for pq.Len() > 0 {
		current := heap.Pop(pq).(queued)
		if visited[current.id] {
			continue
		}
		visited[current.id] = true

		neighbours := make([]string, 0, len(g[current.id]))
		for n := range g[current.id] {
			neighbours = append(neighbours, n)
		}
		// Sorting makes the choice between equal-cost paths reproducible
		// across runs and across nodes, which keeps demos readable.
		sort.Strings(neighbours)

		for _, next := range neighbours {
			cost := g[current.id][next]
			if cost < 0 {
				continue // negative weights are outside Dijkstra's contract
			}
			alt := current.dist + cost
			known, seen := dist[next]
			if seen && alt >= known {
				continue
			}
			dist[next] = alt
			if current.id == source {
				firstHop[next] = next
			} else {
				firstHop[next] = firstHop[current.id]
			}
			heap.Push(pq, queued{id: next, dist: alt})
		}
	}

	table := Table{}
	for dest, d := range dist {
		if dest == source || math.IsInf(d, 1) {
			continue
		}
		hop, ok := firstHop[dest]
		if !ok {
			continue
		}
		table[dest] = Route{Dest: dest, NextHop: hop, Cost: d}
	}
	return table
}

// queued is a node waiting in the priority queue with its tentative distance.
type queued struct {
	id   string
	dist float64
}

// nodeQueue is a min-heap of queued nodes ordered by tentative distance.
type nodeQueue []queued

func (q nodeQueue) Len() int           { return len(q) }
func (q nodeQueue) Less(i, j int) bool { return q[i].dist < q[j].dist }
func (q nodeQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *nodeQueue) Push(x any)        { *q = append(*q, x.(queued)) }
func (q *nodeQueue) Pop() any {
	old := *q
	n := len(old)
	item := old[n-1]
	*q = old[:n-1]
	return item
}
