package routing

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/uvg/cc3067-lab3/internal/protocol"
)

// Timings of the link-state protocol. They are deliberately short so that a
// classroom demo converges while the audience is still watching.
const (
	// lspInterval is how often a node re-announces its own links, even when
	// nothing changed. Periodic refresh is what lets other nodes age out the
	// announcements of a node that died.
	lspInterval = 8 * time.Second
	// lspMaxAge is how long a foreign announcement is trusted without being
	// refreshed. It must be a comfortable multiple of lspInterval so that one
	// lost packet does not evict a healthy node.
	lspMaxAge = 25 * time.Second
	// ageInterval is how often the database is swept for expired entries.
	ageInterval = 3 * time.Second
	// seenTTL is how long a flooded LSP identifier is remembered.
	seenTTL = 60 * time.Second
	// minAnnounceInterval rate-limits announcements triggered by a changed
	// link cost. A discovered or lost neighbour is a topology change and is
	// announced immediately; a cost that merely drifted can wait, because the
	// periodic refresh will carry it anyway.
	minAnnounceInterval = 5 * time.Second
)

// costChangeThreshold is how much a measured cost must move, as a fraction of
// the previous value, before it is worth telling the whole network about.
// Below this the difference cannot change anyone's shortest path in practice,
// and announcing it would only add traffic.
const costChangeThreshold = 0.5

// lsdbEntry is one announcement plus the moment it was last refreshed.
type lsdbEntry struct {
	lsp       protocol.LinkStatePacket
	refreshed time.Time
}

// LSRAlgorithm implements Link State Routing.
//
// It is a composition of the two other algorithms rather than a rewrite:
//   - Flood disseminates each node's link-state packet to the whole network,
//   - Dijkstra turns the resulting database into a forwarding table.
//
// The node itself only ever announces what it can measure directly — its own
// neighbours — which is the defining property of a link-state protocol.
type LSRAlgorithm struct {
	fabric Fabric
	table  *SafeTable
	seen   *SeenCache

	mu sync.Mutex
	// links are the costs to our own neighbours, as measured by hello/echo.
	links map[string]float64
	// lsdb is the link-state database: the latest announcement of every node.
	lsdb map[string]lsdbEntry
	// seq is our own announcement counter, incremented on every change.
	seq int
	// lastAnnounce is when we last flooded our own link state, used to rate
	// limit announcements caused by cost jitter.
	lastAnnounce time.Time
}

// NewLSR builds the link-state algorithm.
func NewLSR(fabric Fabric) *LSRAlgorithm {
	return &LSRAlgorithm{
		fabric: fabric,
		table:  NewSafeTable(),
		seen:   NewSeenCache(seenTTL),
		links:  map[string]float64{},
		lsdb:   map[string]lsdbEntry{},
	}
}

// Proto identifies packets emitted by this algorithm.
func (l *LSRAlgorithm) Proto() protocol.Proto { return protocol.ProtoLSR }

// Start launches the two periodic duties of the routing plane: refreshing our
// own announcement, and expiring announcements that stopped being refreshed.
func (l *LSRAlgorithm) Start(ctx context.Context) {
	go l.refreshLoop(ctx)
	go l.ageLoop(ctx)
}

func (l *LSRAlgorithm) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(lspInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.announce()
		}
	}
}

func (l *LSRAlgorithm) ageLoop(ctx context.Context) {
	ticker := time.NewTicker(ageInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if l.expire() {
				l.recompute()
			}
		}
	}
}

// Forward returns the single next hop chosen by the computed shortest path.
func (l *LSRAlgorithm) Forward(pkt *protocol.Packet) []string {
	route, ok := l.table.Lookup(pkt.To)
	if !ok {
		l.fabric.Logf("no route to %s yet, dropping %s", pkt.To, pkt)
		return nil
	}
	return []string{route.NextHop}
}

// HandleInfo consumes a link-state packet: store it if it is newer than what
// we hold, recompute our table, and pass it on to the rest of the network.
func (l *LSRAlgorithm) HandleInfo(pkt *protocol.Packet) {
	// Cheap first filter: an identical copy of a packet we already processed
	// arrives on every alternative path through the network.
	if l.seen.Seen(pkt.HeaderString(protocol.HeaderMsgID)) {
		return
	}

	lsp, err := protocol.DecodeLSP(pkt.Payload)
	if err != nil {
		l.fabric.Logf("discarding unreadable link-state packet from %s: %v", pkt.From, err)
		return
	}
	if lsp.Origin == "" || lsp.Origin == l.fabric.ID() {
		// Our own announcement came back around the ring; nothing to learn.
		return
	}

	accepted, structural := l.store(lsp)
	if !accepted {
		// Already known or stale. Not re-flooding it is what terminates the
		// flood; the sequence number is the authority, not the TTL.
		return
	}

	// Only a changed neighbour set is worth a console line. Announcements that
	// merely carry refreshed costs would otherwise bury everything else.
	if structural {
		l.fabric.Logf("topology update: %s is linked to %v", lsp.Origin, neighborNames(lsp.Neighbors))
	}
	l.recompute()
	l.relay(pkt)
}

// OnLinkUp records or updates the measured cost of a neighbour and, when that
// is new information, re-announces our links to the network.
func (l *LSRAlgorithm) OnLinkUp(neighbor string, cost float64) {
	l.mu.Lock()
	previous, existed := l.links[neighbor]
	l.links[neighbor] = cost

	// A neighbour appearing changes the topology, and the network must hear
	// about it at once. A cost that merely drifted is not urgent: it is
	// announced only when it moved enough to matter, and never more often than
	// the rate limit, or the measurement noise of a fast link would keep every
	// node flooding the network forever.
	newNeighbor := !existed
	costMoved := existed &&
		relativeChange(previous, cost) > costChangeThreshold &&
		time.Since(l.lastAnnounce) > minAnnounceInterval
	l.mu.Unlock()

	if newNeighbor {
		l.fabric.Logf("link to %s up (cost %.2f)", neighbor, cost)
	}
	if newNeighbor || costMoved {
		l.announce()
	}
	// Recomputing is local and cheap, so it always runs: our own table stays
	// accurate even when the change was not worth announcing.
	l.recompute()
}

// OnLinkDown withdraws a neighbour and re-announces the reduced link set.
func (l *LSRAlgorithm) OnLinkDown(neighbor string) {
	l.mu.Lock()
	_, existed := l.links[neighbor]
	delete(l.links, neighbor)
	l.mu.Unlock()

	if existed {
		l.fabric.Logf("link to %s down, withdrawing it", neighbor)
		l.announce()
		l.recompute()
	}
}

// Table exposes the current routes.
func (l *LSRAlgorithm) Table() Table { return l.table.Snapshot() }

// Database returns a copy of the link-state database, for the console.
func (l *LSRAlgorithm) Database() map[string]protocol.LinkStatePacket {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make(map[string]protocol.LinkStatePacket, len(l.lsdb))
	for origin, entry := range l.lsdb {
		out[origin] = entry.lsp
	}
	return out
}

// announce builds our own link-state packet and floods it to every neighbour.
func (l *LSRAlgorithm) announce() {
	l.mu.Lock()
	l.seq++
	l.lastAnnounce = time.Now()
	lsp := protocol.LinkStatePacket{
		Origin:    l.fabric.ID(),
		Seq:       l.seq,
		Neighbors: make(map[string]float64, len(l.links)),
	}
	for neighbor, cost := range l.links {
		lsp.Neighbors[neighbor] = cost
	}
	l.mu.Unlock()

	payload, err := protocol.EncodeLSP(lsp)
	if err != nil {
		l.fabric.Logf("cannot encode own link-state packet: %v", err)
		return
	}

	// "to" is broadcast: a link-state packet has no single destination, it is
	// addressed to every node that will listen.
	pkt := protocol.New(protocol.ProtoLSR, protocol.TypeInfo, l.fabric.ID(), "*", payload)
	l.seen.Seen(pkt.HeaderString(protocol.HeaderMsgID))
	l.relay(pkt)
}

// relay pushes an info packet onward using the shared flooding primitive.
func (l *LSRAlgorithm) relay(pkt *protocol.Packet) {
	for _, neighbor := range Flood(l.fabric.Neighbors(), pkt) {
		copyPkt := pkt.Clone()
		copyPkt.SetHeader(protocol.HeaderHop, l.fabric.ID())
		if err := copyPkt.DecrementTTL(); err != nil {
			return
		}
		if err := l.fabric.SendTo(neighbor, copyPkt); err != nil {
			l.fabric.Logf("could not send link-state packet to %s: %v", neighbor, err)
		}
	}
}

// store inserts an announcement. It reports whether the announcement was new
// information at all, and whether it changed the origin's set of neighbours —
// a topology change, as opposed to a refreshed measurement.
func (l *LSRAlgorithm) store(lsp protocol.LinkStatePacket) (accepted, structural bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	existing, known := l.lsdb[lsp.Origin]
	if known && lsp.Seq <= existing.lsp.Seq {
		// Refresh the timer anyway: seeing the same announcement again still
		// proves the origin is alive.
		if lsp.Seq == existing.lsp.Seq {
			existing.refreshed = time.Now()
			l.lsdb[lsp.Origin] = existing
		}
		return false, false
	}

	structural = !known || !sameNeighborSet(existing.lsp.Neighbors, lsp.Neighbors)
	l.lsdb[lsp.Origin] = lsdbEntry{lsp: lsp, refreshed: time.Now()}
	return true, structural
}

// sameNeighborSet reports whether two announcements name the same neighbours,
// ignoring their costs.
func sameNeighborSet(a, b map[string]float64) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if _, ok := b[id]; !ok {
			return false
		}
	}
	return true
}

// neighborNames lists the neighbours of an announcement in a stable order.
func neighborNames(links map[string]float64) []string {
	names := make([]string, 0, len(links))
	for id := range links {
		names = append(names, id)
	}
	sort.Strings(names)
	return names
}

// expire drops announcements that stopped being refreshed and reports whether
// anything was removed.
func (l *LSRAlgorithm) expire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	removed := false
	for origin, entry := range l.lsdb {
		if now.Sub(entry.refreshed) > lspMaxAge {
			delete(l.lsdb, origin)
			removed = true
			l.fabric.Logf("link-state packet of %s expired, removing it from the topology", origin)
		}
	}
	return removed
}

// recompute rebuilds the topology from the database plus our own links, then
// reuses the shared Dijkstra implementation to derive the forwarding table.
func (l *LSRAlgorithm) recompute() {
	l.mu.Lock()
	graph := NewGraph()
	self := l.fabric.ID()
	for neighbor, cost := range l.links {
		graph.AddEdge(self, neighbor, cost)
	}
	for origin, entry := range l.lsdb {
		for neighbor, cost := range entry.lsp.Neighbors {
			graph.AddEdge(origin, neighbor, cost)
		}
	}
	l.mu.Unlock()

	l.table.Replace(Dijkstra(graph, self))
}

// relativeChange reports how much new differs from old, as a fraction of old.
func relativeChange(old, new float64) float64 {
	if old <= 0 {
		return 1
	}
	delta := new - old
	if delta < 0 {
		delta = -delta
	}
	return delta / old
}
