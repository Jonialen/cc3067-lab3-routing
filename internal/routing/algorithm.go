package routing

import (
	"context"

	"github.com/uvg/cc3067-lab3/internal/protocol"
)

// Fabric is the narrow view of the node that a routing algorithm is allowed to
// use. Algorithms decide *where* a packet goes; the fabric is what actually
// puts bytes on a socket. Keeping the boundary this thin is what lets the same
// Dijkstra and Flood code be exercised standalone and inside LSR.
type Fabric interface {
	// ID is the local node identifier.
	ID() string
	// Neighbors lists the directly connected nodes currently considered up.
	Neighbors() []string
	// SendTo delivers a packet to a directly connected neighbour.
	SendTo(neighbor string, pkt *protocol.Packet) error
	// Logf writes a line to the node console.
	Logf(format string, args ...any)
	// Address resolves a local id to the address it must be written as on the
	// wire ("*" and an id that is already an address resolve to themselves).
	Address(id string) string
	// LocalID resolves a wire address back to the id this node knows it by,
	// falling back to the address itself when it names no configured node.
	LocalID(addr string) string
}

// Algorithm is the routing plane strategy. The forwarding plane is identical
// for all three implementations; only these decisions differ.
type Algorithm interface {
	// Proto is the identifier written into outgoing packets.
	Proto() protocol.Proto

	// Start launches any periodic routing work (hellos, LSP refresh, ageing)
	// and returns immediately. It stops when ctx is cancelled.
	Start(ctx context.Context)

	// Forward returns the neighbours a data packet must be relayed to. An
	// empty result means the packet is dropped, which is the correct outcome
	// for an unreachable destination.
	Forward(pkt *protocol.Packet) []string

	// HandleInfo consumes a routing information packet (a link-state packet,
	// a distance vector, a neighbour table) addressed to the routing plane.
	HandleInfo(pkt *protocol.Packet)

	// OnLinkUp reports a neighbour reachable at the measured cost.
	OnLinkUp(neighbor string, cost float64)

	// OnLinkDown reports a neighbour that stopped answering.
	OnLinkDown(neighbor string)

	// Table exposes the current routing table for the console.
	Table() Table

	// Topology exposes every link this algorithm currently knows about, for
	// diagnostics. Dijkstra and LSR know the whole graph; Flooding only ever
	// knows its own direct links.
	Topology() []Edge
}

// Edge is one directed, weighted link of a known topology: From can reach To
// at the given cost. Two nodes on a real link each report their own
// direction, so both usually show up, possibly at slightly different costs —
// each side measured its own round trip.
type Edge struct {
	From string
	To   string
	Cost float64
}
