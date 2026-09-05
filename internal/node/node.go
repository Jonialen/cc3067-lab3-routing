// Package node wires a single router process together: the TCP transport, the
// forwarding plane, and one interchangeable routing algorithm.
//
// The two planes required by the assignment run concurrently as goroutines:
//
//	forwarding — handles every inbound and outbound packet,
//	routing    — maintains neighbour state and the routing table.
//
// They communicate through channels rather than shared mutable state, so no
// packet handling ever blocks on a routing recomputation.
package node

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/uvg/cc3067-lab3/internal/config"
	"github.com/uvg/cc3067-lab3/internal/protocol"
	"github.com/uvg/cc3067-lab3/internal/routing"
	"github.com/uvg/cc3067-lab3/internal/transport"
)

// Timings of the neighbour discovery plane.
const (
	// helloInterval is how often each neighbour is probed.
	helloInterval = 4 * time.Second
	// deadAfter is how long a neighbour may stay silent before its link is
	// declared down. It spans several hellos so a single loss is tolerated.
	deadAfter = 14 * time.Second
	// inboxSize buffers inbound packets so a burst does not block the reader.
	inboxSize = 256
	// messageSeenTTL is how long a data packet id is remembered, to break
	// flooding loops.
	messageSeenTTL = 60 * time.Second
)

// Mode selects which routing algorithm the node runs.
type Mode string

const (
	ModeDijkstra Mode = "dijkstra"
	ModeFlooding Mode = "flooding"
	ModeLSR      Mode = "lsr"
)

// ParseMode validates a mode name supplied on the command line.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeDijkstra, ModeFlooding, ModeLSR:
		return Mode(s), nil
	default:
		return "", fmt.Errorf("unknown mode %q (use dijkstra, flooding or lsr)", s)
	}
}

// Options configures a node at boot.
type Options struct {
	ID       string
	Mode     Mode
	Topology *config.Topology
	Names    *config.Names
	// Listen overrides the address taken from the name table, which is what
	// lets several nodes run on one machine during local testing.
	Listen string
	// Verbose echoes every forwarded packet to the console.
	Verbose bool
	// OnDeliver, when set, is called for every packet delivered to this node.
	// The console does not need it — delivery is already printed — but it
	// gives tests a way to observe delivery without parsing logs.
	OnDeliver func(*protocol.Packet)
	// LogOutput redirects the node console. It defaults to standard output;
	// tests set it to capture the diagnostics the node emits on its own.
	LogOutput io.Writer
}

// logOutput falls back to standard output when no writer was supplied.
func logOutput(w io.Writer) io.Writer {
	if w == nil {
		return os.Stdout
	}
	return w
}

// neighborState tracks what the discovery plane knows about one direct link.
type neighborState struct {
	alive    bool
	cost     float64
	lastSeen time.Time
}

// Node is a single router process.
type Node struct {
	id      string
	mode    Mode
	names   *config.Names
	verbose bool

	// selfAddr is this node's own address, as it must appear in the "from" of
	// every packet it originates. It is the address declared for our id in
	// the name table when there is one, so a node bound with --listen on a
	// permissive address (e.g. 0.0.0.0:5000) still advertises the real
	// address other teams can dial back.
	selfAddr string
	// selfPort completes an address that arrives without one, per the
	// protocol's "a port-less address takes the network's configured port"
	// rule.
	selfPort string

	server *transport.Server
	client *transport.Client
	logger *log.Logger

	alg routing.Algorithm

	// inbox carries packets from the connection goroutines to the single
	// forwarding goroutine, so packet handling needs no lock of its own.
	inbox chan *protocol.Packet
	seen  *routing.SeenCache

	onDeliver func(*protocol.Packet)

	mu        sync.RWMutex
	neighbors map[string]*neighborState
	// warnedSilent remembers which never-seen neighbours were already
	// reported, so the probe loop names each one once instead of every tick.
	warnedSilent map[string]bool

	wg sync.WaitGroup
}

// New builds a node from its options without starting any goroutine.
func New(opts Options) (*Node, error) {
	listen := opts.Listen
	if listen == "" {
		addr, ok := opts.Names.Endpoint(opts.ID)
		if !ok {
			return nil, fmt.Errorf("node %q is not in the name table and no --listen was given", opts.ID)
		}
		listen = addr
	}

	selfAddr := listen
	if addr, ok := opts.Names.Endpoint(opts.ID); ok {
		selfAddr = addr
	}
	_, selfPort, err := net.SplitHostPort(selfAddr)
	if err != nil {
		selfPort = ""
	}

	n := &Node{
		id:           opts.ID,
		mode:         opts.Mode,
		names:        opts.Names,
		verbose:      opts.Verbose,
		selfAddr:     selfAddr,
		selfPort:     selfPort,
		client:       transport.NewClient(),
		logger:       log.New(logOutput(opts.LogOutput), fmt.Sprintf("[%s] ", opts.ID), log.Ltime),
		inbox:        make(chan *protocol.Packet, inboxSize),
		seen:         routing.NewSeenCache(messageSeenTTL),
		neighbors:    map[string]*neighborState{},
		warnedSilent: map[string]bool{},
		onDeliver:    opts.OnDeliver,
	}
	n.server = transport.NewServer(listen, n.receive, n.Logf)

	// Every mode starts from the same premise the assignment sets out: a node
	// is told who its direct neighbours are. Only Dijkstra is additionally
	// allowed to read the rest of the topology.
	for neighbor, cost := range opts.Topology.Neighbors(opts.ID) {
		n.neighbors[neighbor] = &neighborState{cost: cost}
	}
	if len(n.neighbors) == 0 {
		n.Logf("warning: node %q has no neighbours declared in the topology", opts.ID)
	}

	switch opts.Mode {
	case ModeDijkstra:
		n.alg = routing.NewDijkstra(n, graphFrom(opts.Topology))
	case ModeFlooding:
		n.alg = routing.NewFlooding(n)
	case ModeLSR:
		n.alg = routing.NewLSR(n)
	default:
		return nil, fmt.Errorf("unsupported mode %q", opts.Mode)
	}

	return n, nil
}

// graphFrom converts a configured topology into the graph the shortest-path
// routine consumes.
func graphFrom(topo *config.Topology) routing.Graph {
	g := routing.NewGraph()
	for from, links := range topo.Config {
		for to, cost := range links {
			g.AddEdge(from, to, cost)
		}
	}
	return g
}

// Start brings up the listener, the forwarding plane, the discovery plane and
// the routing algorithm. It returns once everything is running.
func (n *Node) Start(ctx context.Context) error {
	if err := n.server.Start(ctx); err != nil {
		return err
	}
	n.Logf("listening on %s, mode=%s, neighbours=%v", n.server.Addr(), n.mode, n.configuredNeighbors())

	n.wg.Add(2)
	go func() { defer n.wg.Done(); n.forwardingLoop(ctx) }()
	go func() { defer n.wg.Done(); n.discoveryLoop(ctx) }()

	n.alg.Start(ctx)
	return nil
}

// Wait blocks until every plane has stopped.
func (n *Node) Wait() {
	n.wg.Wait()
	n.server.Wait()
	n.client.Close()
}

// receive is called by the transport on every decoded inbound packet. It only
// enqueues, so a slow routing computation never stalls a TCP connection.
func (n *Node) receive(pkt *protocol.Packet) {
	select {
	case n.inbox <- pkt:
	default:
		n.Logf("inbox full, dropping %s", pkt)
	}
}

// forwardingLoop is the forwarding plane: one goroutine draining the inbox.
func (n *Node) forwardingLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case pkt := <-n.inbox:
			n.handle(pkt)
		}
	}
}

// handle dispatches one inbound packet to the plane that owns its type.
func (n *Node) handle(pkt *protocol.Packet) {
	if n.verbose {
		n.Logf("recv %s", pkt)
	}
	if pkt.Version != protocol.Version {
		n.Logf("packet from %s declares version %d, expected %d; processing anyway", pkt.From, pkt.Version, protocol.Version)
	}
	if !pkt.ChecksumValid() {
		n.Logf("checksum mismatch on %s from %s, processing anyway", pkt.Type, pkt.From)
	}
	switch pkt.Type {
	case protocol.TypeHello:
		n.handleHello(pkt)
	case protocol.TypeEcho:
		n.handleEcho(pkt)
	case protocol.TypeInfo:
		n.markAlive(previousHopID(n, pkt))
		n.alg.HandleInfo(pkt)
	case protocol.TypeMessage:
		n.handleMessage(pkt)
	default:
		n.Logf("unknown packet type %q from %s, ignoring", pkt.Type, pkt.From)
	}
}

// previousHopID resolves the address recorded in a packet's "via" header (or
// "from", for a packet still on its first hop) into a local id.
func previousHopID(n *Node, pkt *protocol.Packet) string {
	via := pkt.HeaderString(protocol.HeaderVia)
	if via == "" {
		via = pkt.From
	}
	return n.LocalID(via)
}

// handleMessage delivers user data locally or forwards it onward.
func (n *Node) handleMessage(pkt *protocol.Packet) {
	id := pkt.HeaderString(protocol.HeaderMsgID)
	if n.seen.Seen(id) {
		// Under flooding the same message legitimately arrives several times.
		// The first copy is the one that counts.
		if n.verbose {
			n.Logf("duplicate %s, dropping", pkt)
		}
		return
	}

	if n.LocalID(pkt.To) == n.id {
		text, err := pkt.PayloadText()
		if err != nil {
			n.Logf("MESSAGE from %s carries an unreadable payload: %v", pkt.From, err)
			return
		}
		n.Logf("MESSAGE from %s (trace %s): %s",
			pkt.From, strings.Join(pkt.Trace(), ">"), text)
		if n.onDeliver != nil {
			n.onDeliver(pkt)
		}
		return
	}

	n.relay(pkt)
}

// relay hands a packet to the routing algorithm and pushes it to whichever
// neighbours the algorithm chose.
func (n *Node) relay(pkt *protocol.Packet) {
	targets := n.alg.Forward(pkt)
	if len(targets) == 0 {
		return
	}

	if err := pkt.DecrementTTL(); err != nil {
		n.Logf("ttl expired for %s, dropping", pkt)
		return
	}
	pkt.AppendTrace(n.selfAddr)
	pkt.SetHeader(protocol.HeaderVia, n.selfAddr)

	for _, target := range targets {
		if err := n.SendTo(target, pkt.Clone()); err != nil {
			n.Logf("could not forward to %s: %v", target, err)
		}
	}
}

// Send injects a user message originated by this node into the network.
func (n *Node) Send(to, text string) error {
	if to == n.id {
		return fmt.Errorf("%q is this node", to)
	}
	pkt := protocol.NewText(n.alg.Proto(), protocol.TypeMessage, n.selfAddr, n.Address(to), text)
	pkt.AppendTrace(n.selfAddr)
	pkt.SetHeader(protocol.HeaderVia, n.selfAddr)
	// Remember our own message so a flooded copy coming back is discarded.
	n.seen.Seen(pkt.HeaderString(protocol.HeaderMsgID))

	targets := n.alg.Forward(pkt)
	if len(targets) == 0 {
		return fmt.Errorf("no route to %s", to)
	}
	for _, target := range targets {
		if err := n.SendTo(target, pkt.Clone()); err != nil {
			n.Logf("could not send to %s: %v", target, err)
		}
	}
	n.Logf("sent to %s via %v", to, targets)
	return nil
}

// Table exposes the routing table for the console.
func (n *Node) Table() routing.Table { return n.alg.Table() }

// Algorithm exposes the running strategy, so the console can show details that
// only some algorithms have, such as the link-state database.
func (n *Node) Algorithm() routing.Algorithm { return n.alg }

// Mode reports which algorithm the node is running.
func (n *Node) Mode() Mode { return n.mode }

// --- routing.Fabric ---------------------------------------------------------

// ID is the local node identifier.
func (n *Node) ID() string { return n.id }

// Neighbors lists the neighbours currently answering hellos. Algorithms are
// given only live links, so a dead neighbour disappears from their view
// without every algorithm having to track liveness itself.
func (n *Node) Neighbors() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()

	live := make([]string, 0, len(n.neighbors))
	for id, state := range n.neighbors {
		if state.alive {
			live = append(live, id)
		}
	}
	return live
}

// SendTo delivers a packet to a directly connected neighbour.
func (n *Node) SendTo(neighbor string, pkt *protocol.Packet) error {
	addr, ok := n.names.Endpoint(neighbor)
	if !ok {
		return fmt.Errorf("no address known for %q", neighbor)
	}
	return n.client.Send(addr, pkt)
}

// Logf writes one line to the node console.
func (n *Node) Logf(format string, args ...any) {
	n.logger.Printf(format, args...)
}

// Address resolves a local id to the address it must be written as on the
// wire. "*" (the link-state broadcast destination) and an id that is already
// an address resolve to themselves.
func (n *Node) Address(id string) string {
	if id == "*" || id == n.id {
		return n.selfAddr
	}
	if addr, ok := n.names.Endpoint(id); ok {
		return addr
	}
	return id
}

// LocalID resolves a wire address back to the id this node knows it by. An
// address naming no configured node resolves to itself, which is how a team
// outside our name table is addressed, per the protocol.
func (n *Node) LocalID(addr string) string {
	if addr == "*" || addr == n.selfAddr {
		return n.id
	}
	return n.names.IDFor(addr, n.selfPort)
}

// configuredNeighbors lists every neighbour from the topology, alive or not.
func (n *Node) configuredNeighbors() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()

	ids := make([]string, 0, len(n.neighbors))
	for id := range n.neighbors {
		ids = append(ids, id)
	}
	return ids
}
