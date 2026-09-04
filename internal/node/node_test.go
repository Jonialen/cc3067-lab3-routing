package node_test

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/uvg/cc3067-lab3/internal/config"
	"github.com/uvg/cc3067-lab3/internal/node"
	"github.com/uvg/cc3067-lab3/internal/protocol"
)

// network is a set of nodes started on loopback for one test.
type network struct {
	t         *testing.T
	nodes     map[string]*node.Node
	delivered map[string]chan *protocol.Packet
}

// freePort reserves an ephemeral port and releases it, so the node can bind it.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// startNetwork boots one node per entry of adjacency, all running mode.
func startNetwork(t *testing.T, mode node.Mode, adjacency map[string][]string) *network {
	t.Helper()

	topo := &config.Topology{Type: "topo", Config: map[string]map[string]float64{}}
	for id, neighbours := range adjacency {
		links := map[string]float64{}
		for _, n := range neighbours {
			links[n] = 1
		}
		topo.Config[id] = links
	}

	names := &config.Names{Type: "names", Config: map[string]string{}}
	for id := range adjacency {
		names.Config[id] = fmt.Sprintf("127.0.0.1:%d", freePort(t))
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	net := &network{
		t:         t,
		nodes:     map[string]*node.Node{},
		delivered: map[string]chan *protocol.Packet{},
	}
	for id := range adjacency {
		inbox := make(chan *protocol.Packet, 16)
		net.delivered[id] = inbox

		n, err := node.New(node.Options{
			ID:        id,
			Mode:      mode,
			Topology:  topo,
			Names:     names,
			OnDeliver: func(pkt *protocol.Packet) { inbox <- pkt },
		})
		if err != nil {
			t.Fatalf("create node %s: %v", id, err)
		}
		if err := n.Start(ctx); err != nil {
			t.Fatalf("start node %s: %v", id, err)
		}
		net.nodes[id] = n
	}
	return net
}

// awaitRoute blocks until id has a route to dest, or fails the test.
func (n *network) awaitRoute(id, dest string, timeout time.Duration) {
	n.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, ok := n.nodes[id].Table()[dest]; ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	n.t.Fatalf("node %s never learned a route to %s; table = %v", id, dest, n.nodes[id].Table())
}

// awaitDelivery waits for one packet delivered to id.
func (n *network) awaitDelivery(id string, timeout time.Duration) *protocol.Packet {
	n.t.Helper()
	select {
	case pkt := <-n.delivered[id]:
		return pkt
	case <-time.After(timeout):
		n.t.Fatalf("node %s never received the message", id)
		return nil
	}
}

func TestLSRConvergesAndRoutesOverTheShortestPath(t *testing.T) {
	// A -- B -- C: A must learn that C is reachable through B, even though it
	// was never told C exists.
	net := startNetwork(t, node.ModeLSR, map[string][]string{
		"A": {"B"},
		"B": {"A", "C"},
		"C": {"B"},
	})

	net.awaitRoute("A", "C", 5*time.Second)

	if hop := net.nodes["A"].Table()["C"].NextHop; hop != "B" {
		t.Errorf("next hop from A to C = %q, want %q", hop, "B")
	}

	if err := net.nodes["A"].Send("C", "hola desde A"); err != nil {
		t.Fatalf("Send returned %v", err)
	}

	pkt := net.awaitDelivery("C", 5*time.Second)
	text, err := pkt.PayloadText()
	if err != nil || text != "hola desde A" {
		t.Errorf("payload = %q (err %v), want %q", text, err, "hola desde A")
	}
	// The trace records one address per hop (A, then B), by address rather
	// than id — that is what the wire protocol requires.
	if got := pkt.Trace(); len(got) != 2 {
		t.Errorf("trace = %v, want 2 hops (A then B)", got)
	}
}

func TestFloodingDeliversExactlyOnceDespiteACycle(t *testing.T) {
	// A triangle plus a tail. Without duplicate suppression, D would receive
	// the message twice: once through B and once through C.
	net := startNetwork(t, node.ModeFlooding, map[string][]string{
		"A": {"B", "C"},
		"B": {"A", "C", "D"},
		"C": {"A", "B", "D"},
		"D": {"B", "C"},
	})

	// Flooding only forwards to links it has seen answer a hello.
	waitForNeighbours(t, net, "A", 2, 5*time.Second)

	if err := net.nodes["A"].Send("D", "una sola vez"); err != nil {
		t.Fatalf("Send returned %v", err)
	}

	first := net.awaitDelivery("D", 5*time.Second)
	if text, err := first.PayloadText(); err != nil || text != "una sola vez" {
		t.Errorf("payload = %q (err %v), want %q", text, err, "una sola vez")
	}

	select {
	case dup := <-net.delivered["D"]:
		t.Fatalf("the same message was delivered twice: %s", dup)
	case <-time.After(1500 * time.Millisecond):
		// No second copy: the duplicate cache did its job.
	}
}

func TestDijkstraBuildsItsTableFromConfigurationAlone(t *testing.T) {
	// Static mode: the table must exist immediately, without any exchange.
	net := startNetwork(t, node.ModeDijkstra, map[string][]string{
		"A": {"B"},
		"B": {"A", "C"},
		"C": {"B"},
	})

	table := net.nodes["A"].Table()
	route, ok := table["C"]
	if !ok {
		t.Fatalf("A has no route to C at boot; table = %v", table)
	}
	if route.NextHop != "B" || route.Cost != 2 {
		t.Errorf("route to C = %+v, want next hop B at cost 2", route)
	}

	waitForNeighbours(t, net, "A", 1, 5*time.Second)
	if err := net.nodes["A"].Send("C", "estatico"); err != nil {
		t.Fatalf("Send returned %v", err)
	}
	pkt := net.awaitDelivery("C", 5*time.Second)
	if text, err := pkt.PayloadText(); err != nil || text != "estatico" {
		t.Errorf("payload = %q (err %v), want %q", text, err, "estatico")
	}
}

func TestMessageToAnUnreachableDestinationIsReportedNotDropped(t *testing.T) {
	net := startNetwork(t, node.ModeLSR, map[string][]string{
		"A": {"B"},
		"B": {"A"},
	})

	if err := net.nodes["A"].Send("Z", "nadie"); err == nil {
		t.Fatal("sending to an unknown destination must report an error")
	}
}

// waitForNeighbours blocks until id sees at least want live links.
func waitForNeighbours(t *testing.T, n *network, id string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		alive := 0
		for _, s := range n.nodes[id].NeighborStates() {
			if s.Alive {
				alive++
			}
		}
		if alive >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("node %s never saw %d live neighbours", id, want)
}
