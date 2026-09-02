package routing

import (
	"context"
	"sync"

	"github.com/uvg/cc3067-lab3/internal/protocol"
)

// FloodingAlgorithm relays every data packet to all neighbours except the one
// it arrived from, and keeps no routing table at all.
//
// It is the only algorithm here that needs no knowledge beyond its immediate
// neighbours, which is exactly why it is the bootstrap mechanism Link State
// Routing uses to spread link-state packets before any topology is known.
type FloodingAlgorithm struct {
	fabric Fabric

	mu sync.Mutex
	// costs records the measured cost of each live link. Flooding does not use
	// costs to decide anything; they are kept purely so the console can show
	// what the node currently sees.
	costs map[string]float64
}

// NewFlooding builds the flooding algorithm.
func NewFlooding(fabric Fabric) *FloodingAlgorithm {
	return &FloodingAlgorithm{fabric: fabric, costs: map[string]float64{}}
}

// Proto identifies packets emitted by this algorithm.
func (f *FloodingAlgorithm) Proto() protocol.Proto { return protocol.ProtoFlooding }

// Start has no periodic routing work: flooding computes nothing.
func (f *FloodingAlgorithm) Start(ctx context.Context) {}

// Forward relays the packet to every neighbour except its previous hop.
func (f *FloodingAlgorithm) Forward(pkt *protocol.Packet) []string {
	return Flood(f.fabric.Neighbors(), pkt)
}

// HandleInfo is a no-op: flooding maintains no routing state to update.
func (f *FloodingAlgorithm) HandleInfo(pkt *protocol.Packet) {}

// OnLinkUp records a reachable neighbour and its measured cost.
func (f *FloodingAlgorithm) OnLinkUp(neighbor string, cost float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.costs[neighbor] = cost
}

// OnLinkDown forgets a neighbour that stopped answering.
func (f *FloodingAlgorithm) OnLinkDown(neighbor string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.costs, neighbor)
}

// Table reports the direct neighbours. Flooding has no routing table; this
// view exists so the console can show what the node believes is around it.
func (f *FloodingAlgorithm) Table() Table {
	f.mu.Lock()
	defer f.mu.Unlock()

	table := make(Table, len(f.costs))
	for neighbor, cost := range f.costs {
		table[neighbor] = Route{Dest: neighbor, NextHop: neighbor, Cost: cost}
	}
	return table
}
