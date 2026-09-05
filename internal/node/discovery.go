package node

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/uvg/cc3067-lab3/internal/protocol"
)

// minCost is the floor applied to a measured link cost. A round trip on
// loopback can measure as effectively zero, and a zero-weight edge makes every
// path through it look free, which distorts the shortest-path result.
const minCost = 0.01

// costSmoothing is the weight given to a fresh round-trip sample when updating
// a link's cost. Raw measurements on a fast link swing wildly from one probe to
// the next; feeding that jitter straight into the routing plane would make
// every node re-announce its links continuously. An exponential moving average
// keeps the cost responsive to a real change while ignoring noise.
const costSmoothing = 0.25

// silentGrace is how long a configured neighbour may go without ever
// answering before the node says so out loud. A few probe intervals is enough
// to rule out a slow start and short enough to catch the problem while the
// network is still being set up, which is the only moment the warning is
// actionable.
const silentGrace = 3 * helloInterval

// discoveryLoop is the routing plane's neighbour monitor. It probes every
// configured neighbour on a fixed interval and declares silent ones down, so
// each algorithm receives link events instead of having to poll.
func (n *Node) discoveryLoop(ctx context.Context) {
	started := time.Now()

	// Probe once immediately: waiting a full interval before the first hello
	// would leave the network unusable for several seconds after boot.
	n.probeAll()

	ticker := time.NewTicker(helloInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.reapDead()
			n.warnSilentNeighbors(started)
			n.probeAll()
		}
	}
}

// warnSilentNeighbors names the configured neighbours that have never
// answered a single hello, once each, after the grace period has passed.
//
// This is the counterpart to the topology consistency check in the routing
// plane, and it covers the case that one cannot see. A link both endpoints
// fail to establish is perfectly symmetric — neither declares it — so it looks
// consistent from every angle while simply being absent from the map. The only
// node that can notice is this one, because it is the only one that knows the
// link was supposed to exist: it is in our configuration and it never came up.
func (n *Node) warnSilentNeighbors(started time.Time) {
	if time.Since(started) < silentGrace {
		return
	}

	for _, id := range n.SilentNeighbors() {
		n.mu.Lock()
		alreadyWarned := n.warnedSilent[id]
		n.warnedSilent[id] = true
		n.mu.Unlock()

		if alreadyWarned {
			continue
		}
		addr, _ := n.names.Endpoint(id)
		n.Logf("neighbour %s (%s) has never answered a hello: it is configured as our neighbour "+
			"but the link was never established, so we are not announcing it to the network. "+
			"Check that the address is current and that %s is running", id, addr, id)
	}
}

// nowSeconds is the sender's timestamp format required by the protocol:
// fractional Unix seconds, so a t0 header round-trips without needing the
// two clocks involved to be synchronised.
func nowSeconds() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

// helloPayload builds the object every hello/echo carries, per the protocol.
func (n *Node) helloPayload() protocol.HelloPayload {
	port, _ := strconv.Atoi(n.selfPort)
	return protocol.HelloPayload{ListenPort: port}
}

// probeAll sends a hello to every configured neighbour.
func (n *Node) probeAll() {
	for _, neighbor := range n.configuredNeighbors() {
		pkt, err := protocol.NewObject(n.alg.Proto(), protocol.TypeHello, n.selfAddr, n.Address(neighbor), n.helloPayload())
		if err != nil {
			n.Logf("cannot build hello for %s: %v", neighbor, err)
			continue
		}
		pkt.SetHeader(protocol.HeaderT0, nowSeconds())
		pkt.SetHeader(protocol.HeaderVia, n.selfAddr)

		if err := n.SendTo(neighbor, pkt); err != nil {
			// An unreachable neighbour is expected while the network is coming
			// up, so this is only worth reporting in verbose mode.
			if n.verbose {
				n.Logf("hello to %s failed: %v", neighbor, err)
			}
		}
	}
}

// handleHello answers a probe with an echo that carries the original
// timestamp back, so the sender can measure the round trip without keeping
// any per-probe state of its own.
func (n *Node) handleHello(pkt *protocol.Packet) {
	from := n.LocalID(pkt.From)
	n.markAlive(from)

	reply, err := protocol.NewObject(n.alg.Proto(), protocol.TypeEcho, n.selfAddr, pkt.From, n.helloPayload())
	if err != nil {
		n.Logf("cannot build echo for %s: %v", pkt.From, err)
		return
	}
	if t0, ok := pkt.HeaderFloat(protocol.HeaderT0); ok {
		reply.SetHeader(protocol.HeaderT0, t0)
	}
	reply.SetHeader(protocol.HeaderVia, n.selfAddr)

	if err := n.SendTo(from, reply); err != nil && n.verbose {
		n.Logf("echo to %s failed: %v", pkt.From, err)
	}
}

// handleEcho closes a round-trip measurement and reports the link cost to the
// routing algorithm.
func (n *Node) handleEcho(pkt *protocol.Packet) {
	cost := minCost
	if t0, ok := pkt.HeaderFloat(protocol.HeaderT0); ok {
		// The cost of a link is half the round trip: the one-way delay, in
		// milliseconds.
		rttSeconds := nowSeconds() - t0
		cost = rttSeconds * 1000 / 2
	}
	if cost < minCost {
		cost = minCost
	}
	n.markAliveWithCost(n.LocalID(pkt.From), cost)
}

// markAlive refreshes a neighbour's liveness without changing its cost. It is
// used when any packet proves a neighbour is up.
func (n *Node) markAlive(neighbor string) {
	if neighbor == "" || neighbor == n.id {
		return
	}

	n.mu.Lock()
	state, known := n.neighbors[neighbor]
	if !known {
		// A node that was not in our configuration is talking to us. Accepting
		// it is what lets a new node join a running network.
		state = &neighborState{cost: 1}
		n.neighbors[neighbor] = state
		n.Logf("discovered new neighbour %s", neighbor)
	}
	wasAlive := state.alive
	state.alive = true
	state.lastSeen = time.Now()
	cost := state.cost
	n.mu.Unlock()

	if !wasAlive {
		n.alg.OnLinkUp(neighbor, cost)
	}
}

// markAliveWithCost refreshes a neighbour and records a freshly measured cost.
func (n *Node) markAliveWithCost(neighbor string, cost float64) {
	if neighbor == "" || neighbor == n.id {
		return
	}

	n.mu.Lock()
	state, known := n.neighbors[neighbor]
	if !known {
		state = &neighborState{}
		n.neighbors[neighbor] = state
		n.Logf("discovered new neighbour %s", neighbor)
	}
	if state.alive && state.cost > 0 {
		cost = state.cost*(1-costSmoothing) + cost*costSmoothing
	}
	state.alive = true
	state.cost = cost
	state.lastSeen = time.Now()
	n.mu.Unlock()

	n.alg.OnLinkUp(neighbor, cost)
}

// reapDead declares silent neighbours down and notifies the algorithm.
func (n *Node) reapDead() {
	now := time.Now()

	n.mu.Lock()
	dead := make([]string, 0)
	for id, state := range n.neighbors {
		if state.alive && now.Sub(state.lastSeen) > deadAfter {
			state.alive = false
			dead = append(dead, id)
		}
	}
	n.mu.Unlock()

	for _, id := range dead {
		n.Logf("neighbour %s stopped answering", id)
		n.alg.OnLinkDown(id)
	}
}

// NeighborSnapshot is a read-only view of one link, for the console.
type NeighborSnapshot struct {
	ID       string
	Alive    bool
	Cost     float64
	LastSeen time.Time
}

// SilentNeighbors lists the configured neighbours that have never answered a
// single hello, sorted by id.
//
// This is the cheapest diagnostic in the node, and the one that catches the
// failure the topology consistency check cannot see. A link-state node only
// announces what it can measure, so a neighbour that never answers is silently
// dropped from our announcement: the rest of the network is told the link does
// not exist, and no node anywhere reports an error. When both endpoints fail
// to establish the link, the resulting graph is even perfectly symmetric —
// neither side declares it — so it looks consistent from every angle while
// simply being absent from the map. The only node that can notice is this one,
// because it is the only one that knows the link was supposed to exist: it is
// in our configuration, and it never came up.
func (n *Node) SilentNeighbors() []string {
	silent := make([]string, 0)
	for _, s := range n.NeighborStates() {
		if s.LastSeen.IsZero() {
			silent = append(silent, s.ID)
		}
	}
	sort.Strings(silent)
	return silent
}

// NeighborStates returns the current view of every known link.
func (n *Node) NeighborStates() []NeighborSnapshot {
	n.mu.RLock()
	defer n.mu.RUnlock()

	out := make([]NeighborSnapshot, 0, len(n.neighbors))
	for id, state := range n.neighbors {
		out = append(out, NeighborSnapshot{
			ID:       id,
			Alive:    state.alive,
			Cost:     state.cost,
			LastSeen: state.lastSeen,
		})
	}
	return out
}
