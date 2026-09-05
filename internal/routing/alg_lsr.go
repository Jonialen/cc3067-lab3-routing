package routing

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/uvg/cc3067-lab3/internal/protocol"
)

// Timings of the link-state protocol, as agreed for the class network.
const (
	// lspInterval is how often a node re-announces its own links, even when
	// nothing changed. Periodic refresh is what lets other nodes age out the
	// announcements of a node that died.
	lspInterval = 10 * time.Second
	// lspMaxAge is how long a foreign announcement is trusted without being
	// refreshed.
	lspMaxAge = 30 * time.Second
	// ageInterval is how often the database is swept for expired entries.
	ageInterval = 3 * time.Second
	// seenTTL is how long a flooded LSP identifier is remembered.
	seenTTL = 60 * time.Second
	// minAnnounceInterval rate-limits announcements triggered by a changed
	// link cost. A discovered or lost neighbour is a topology change and is
	// announced immediately; a cost that merely drifted can wait, because the
	// periodic refresh will carry it anyway.
	minAnnounceInterval = 5 * time.Second
	// asymmetryGrace is how long a link may stay declared by only one of its
	// endpoints before it is reported. It must comfortably exceed the refresh
	// interval: every boot is asymmetric for a moment, because announcements
	// arrive one at a time, and warning about that would be noise.
	asymmetryGrace = 3 * lspInterval
	// seqResetGap is how far below the last known sequence number a fresh one
	// must fall before it is trusted as a restarted origin rather than a
	// stale duplicate. See store.
	seqResetGap = 16
)

// costChangeThreshold is how much a measured cost must move, as a fraction of
// the previous value, before it is worth telling the whole network about.
// Below this the difference cannot change anyone's shortest path in practice,
// and announcing it would only add traffic.
const costChangeThreshold = 0.5

// Announcement is one node's link-state record as kept in our database: the
// origin and its neighbours, already resolved to the local id space, unlike
// the wire LinkStatePacket, which names them by address.
type Announcement struct {
	Origin    string
	Seq       int
	Neighbors map[string]float64
}

// lsdbEntry is one announcement plus the moment it was last refreshed.
type lsdbEntry struct {
	entry     Announcement
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
	// asymmetry tracks links declared by only one endpoint, so a persistent
	// one is reported and a transient one is not.
	asymmetry *asymmetryWatch
}

// NewLSR builds the link-state algorithm.
func NewLSR(fabric Fabric) *LSRAlgorithm {
	return &LSRAlgorithm{
		fabric:    fabric,
		table:     NewSafeTable(),
		seen:      NewSeenCache(seenTTL),
		links:     map[string]float64{},
		lsdb:      map[string]lsdbEntry{},
		asymmetry: newAsymmetryWatch(),
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
			l.reportAsymmetries()
		}
	}
}

// Forward returns the single next hop chosen by the computed shortest path.
func (l *LSRAlgorithm) Forward(pkt *protocol.Packet) []string {
	dest := l.fabric.LocalID(pkt.To)
	route, ok := l.table.Lookup(dest)
	if !ok {
		l.fabric.Logf("no route to %s yet, dropping %s", dest, pkt)
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

	if !pkt.ChecksumValid() {
		l.fabric.Logf("checksum mismatch on link-state packet from %s, processing anyway", pkt.From)
	}

	lsp, err := protocol.DecodeLSPFlexible(pkt.Payload)
	if err != nil {
		l.fabric.Logf("discarding unreadable link-state packet from %s: %v", pkt.From, err)
		return
	}

	origin := l.fabric.LocalID(lsp.Origin)
	if origin == "" || origin == l.fabric.ID() {
		// Our own announcement came back around the ring; nothing to learn.
		return
	}

	neighbors := make(map[string]float64, len(lsp.Neighbors))
	for _, ne := range lsp.Neighbors {
		neighbors[l.fabric.LocalID(ne.ID)] = ne.Weight
	}

	accepted, structural := l.store(origin, lsp.Seq, neighbors)
	if !accepted {
		// Already known or stale. Not re-flooding it is what terminates the
		// flood; the sequence number is the authority, not the TTL.
		return
	}

	// Only a changed neighbour set is worth a console line. Announcements that
	// merely carry refreshed costs would otherwise bury everything else.
	if structural {
		l.fabric.Logf("topology update: %s is linked to %v", origin, neighborNames(neighbors))
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

// Topology exposes the graph reconstructed from our own links plus every
// announcement in the database — the same edges recompute uses to run
// Dijkstra, so this is exactly the topology our routing table is based on.
func (l *LSRAlgorithm) Topology() []Edge {
	l.mu.Lock()
	defer l.mu.Unlock()

	self := l.fabric.ID()
	edges := make([]Edge, 0, len(l.links))
	for neighbor, cost := range l.links {
		edges = append(edges, Edge{From: self, To: neighbor, Cost: cost})
	}
	for origin, entry := range l.lsdb {
		for neighbor, cost := range entry.entry.Neighbors {
			edges = append(edges, Edge{From: origin, To: neighbor, Cost: cost})
		}
	}
	return edges
}

// Database returns a copy of the link-state database, for the console.
func (l *LSRAlgorithm) Database() map[string]Announcement {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make(map[string]Announcement, len(l.lsdb))
	for origin, entry := range l.lsdb {
		out[origin] = entry.entry
	}
	return out
}

// Asymmetries reports the links that only one of their two endpoints declares,
// over exactly the edge set recompute feeds to Dijkstra. See the Asymmetry
// type for why this is worth checking: it is the one inconsistency a
// link-state network can carry indefinitely without any node erroring.
func (l *LSRAlgorithm) Asymmetries() []Asymmetry {
	return Asymmetries(l.Topology())
}

// reportAsymmetries puts persistent one-sided links on the console. It runs on
// the ageing sweep because that is already the periodic health check of the
// database, and it stays silent unless a finding outlives the grace period.
func (l *LSRAlgorithm) reportAsymmetries() {
	for _, a := range l.asymmetry.due(l.Asymmetries(), time.Now(), asymmetryGrace) {
		l.fabric.Logf(
			"inconsistent topology: %s declares a link to %s (cost %.2f) but %s does not declare it back; "+
				"the link is unusable from %s and both nodes will compute different routes",
			a.Declared, a.Missing, a.Cost, a.Missing, a.Missing)
	}
}

// announce builds our own link-state packet and floods it to every neighbour.
func (l *LSRAlgorithm) announce() {
	l.mu.Lock()
	l.seq++
	l.lastAnnounce = time.Now()
	neighbors := make([]protocol.NeighborEntry, 0, len(l.links))
	for neighbor, cost := range l.links {
		neighbors = append(neighbors, protocol.NeighborEntry{ID: l.fabric.Address(neighbor), Weight: cost})
	}
	lsp := protocol.LinkStatePacket{
		Origin:    l.fabric.Address(l.fabric.ID()),
		Seq:       l.seq,
		AgeS:      0,
		Neighbors: neighbors,
	}
	l.mu.Unlock()

	// "to" is broadcast: a link-state packet has no single destination, it is
	// addressed to every node that will listen.
	pkt, err := protocol.NewObject(protocol.ProtoLSR, protocol.TypeInfo, l.fabric.Address(l.fabric.ID()), "*", lsp)
	if err != nil {
		l.fabric.Logf("cannot encode own link-state packet: %v", err)
		return
	}
	l.seen.Seen(pkt.HeaderString(protocol.HeaderMsgID))
	l.relay(pkt)
}

// relay pushes an info packet onward using the shared flooding primitive.
func (l *LSRAlgorithm) relay(pkt *protocol.Packet) {
	previousHop := previousHopID(l.fabric, pkt)
	for _, neighbor := range Flood(l.fabric.Neighbors(), previousHop) {
		copyPkt := pkt.Clone()
		copyPkt.SetHeader(protocol.HeaderVia, l.fabric.Address(l.fabric.ID()))
		if err := copyPkt.DecrementTTL(); err != nil {
			return
		}
		if err := l.fabric.SendTo(neighbor, copyPkt); err != nil {
			l.fabric.Logf("could not send link-state packet to %s: %v", neighbor, err)
		}
	}
}

// store inserts an announcement, resolved to local id space by the caller. It
// reports whether the announcement was new information at all, and whether it
// changed the origin's set of neighbours — a topology change, as opposed to a
// refreshed measurement.
//
// A sequence number far enough below the one on file is trusted as a
// restarted origin rather than discarded as stale: without this, a node that
// restarts resets to seq 1 and the rest of the network — still holding a much
// higher number for it — would ignore it until the old entry aged out.
func (l *LSRAlgorithm) store(origin string, seq int, neighbors map[string]float64) (accepted, structural bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	existing, known := l.lsdb[origin]
	if known && seq <= existing.entry.Seq && existing.entry.Seq-seq < seqResetGap {
		// Refresh the timer anyway: seeing the same announcement again still
		// proves the origin is alive.
		if seq == existing.entry.Seq {
			existing.refreshed = time.Now()
			l.lsdb[origin] = existing
		}
		return false, false
	}

	newEntry := Announcement{Origin: origin, Seq: seq, Neighbors: neighbors}
	structural = !known || !sameNeighborSet(existing.entry.Neighbors, neighbors)
	l.lsdb[origin] = lsdbEntry{entry: newEntry, refreshed: time.Now()}
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
		for neighbor, cost := range entry.entry.Neighbors {
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
