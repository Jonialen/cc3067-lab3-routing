package routing

import (
	"sync"
	"testing"

	"github.com/uvg/cc3067-lab3/internal/protocol"
)

// fakeFabric records what an algorithm sends without touching the network.
type fakeFabric struct {
	id string

	mu        sync.Mutex
	neighbors []string
	sent      []*protocol.Packet
}

func (f *fakeFabric) ID() string { return f.id }

func (f *fakeFabric) Neighbors() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.neighbors...)
}

func (f *fakeFabric) SendTo(neighbor string, pkt *protocol.Packet) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, pkt)
	return nil
}

func (f *fakeFabric) Logf(string, ...any) {}

// announcements counts the distinct link-state packets this fabric emitted for
// origin, ignoring the copies flooding sends to each neighbour.
func (f *fakeFabric) announcements(t *testing.T, origin string) int {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()

	seen := map[string]bool{}
	for _, pkt := range f.sent {
		if pkt.Type != protocol.TypeInfo {
			continue
		}
		lsp, err := protocol.DecodeLSP(pkt.Payload)
		if err != nil {
			t.Fatalf("emitted an unreadable link-state packet: %v", err)
		}
		if lsp.Origin == origin {
			seen[pkt.HeaderString(protocol.HeaderMsgID)] = true
		}
	}
	return len(seen)
}

func TestLSRAnnouncesANewNeighbourImmediately(t *testing.T) {
	fabric := &fakeFabric{id: "A", neighbors: []string{"B"}}
	lsr := NewLSR(fabric)

	lsr.OnLinkUp("B", 1)

	if got := fabric.announcements(t, "A"); got != 1 {
		t.Errorf("announcements after discovering a neighbour = %d, want 1", got)
	}
}

func TestLSRDoesNotReannounceOnMeasurementJitter(t *testing.T) {
	// This is the regression that matters: raw round-trip samples on a fast
	// link swing by more than half from one probe to the next. If every swing
	// triggered an announcement, the nodes would flood each other forever.
	fabric := &fakeFabric{id: "A", neighbors: []string{"B"}}
	lsr := NewLSR(fabric)

	lsr.OnLinkUp("B", 1)
	baseline := fabric.announcements(t, "A")

	for _, jittered := range []float64{0.4, 2.1, 0.3, 1.8, 0.5, 2.4, 0.2} {
		lsr.OnLinkUp("B", jittered)
	}

	if got := fabric.announcements(t, "A"); got != baseline {
		t.Errorf("announcements = %d after cost jitter, want %d; the rate limit is not holding",
			got, baseline)
	}
}

func TestLSRAnnouncesALostNeighbour(t *testing.T) {
	fabric := &fakeFabric{id: "A", neighbors: []string{"B"}}
	lsr := NewLSR(fabric)
	lsr.OnLinkUp("B", 1)
	before := fabric.announcements(t, "A")

	lsr.OnLinkDown("B")

	if got := fabric.announcements(t, "A"); got <= before {
		t.Errorf("announcements = %d after losing a neighbour, want more than %d", got, before)
	}
}

func TestLSRBuildsItsTableFromTheLinkStateDatabase(t *testing.T) {
	// A knows only B. It must learn the route to C purely from B's
	// announcement, which is the whole point of link state routing.
	fabric := &fakeFabric{id: "A", neighbors: []string{"B"}}
	lsr := NewLSR(fabric)
	lsr.OnLinkUp("B", 1)

	payload, err := protocol.EncodeLSP(protocol.LinkStatePacket{
		Origin:    "B",
		Seq:       1,
		Neighbors: map[string]float64{"A": 1, "C": 1},
	})
	if err != nil {
		t.Fatalf("EncodeLSP returned %v", err)
	}
	lsr.HandleInfo(protocol.New(protocol.ProtoLSR, protocol.TypeInfo, "B", "*", payload))

	route, ok := lsr.Table()["C"]
	if !ok {
		t.Fatalf("A never learned a route to C; table = %v", lsr.Table())
	}
	if route.NextHop != "B" {
		t.Errorf("next hop to C = %q, want %q", route.NextHop, "B")
	}
}

func TestLSRIgnoresAStaleAnnouncement(t *testing.T) {
	// An older sequence number must never overwrite what we already hold, or
	// a packet looping through the network could resurrect a dead topology.
	fabric := &fakeFabric{id: "A", neighbors: []string{"B"}}
	lsr := NewLSR(fabric)

	fresh, err := protocol.EncodeLSP(protocol.LinkStatePacket{
		Origin: "B", Seq: 5, Neighbors: map[string]float64{"A": 1, "C": 1},
	})
	if err != nil {
		t.Fatalf("EncodeLSP returned %v", err)
	}
	stale, err := protocol.EncodeLSP(protocol.LinkStatePacket{
		Origin: "B", Seq: 2, Neighbors: map[string]float64{"A": 1},
	})
	if err != nil {
		t.Fatalf("EncodeLSP returned %v", err)
	}

	lsr.HandleInfo(protocol.New(protocol.ProtoLSR, protocol.TypeInfo, "B", "*", fresh))
	lsr.HandleInfo(protocol.New(protocol.ProtoLSR, protocol.TypeInfo, "B", "*", stale))

	if got := lsr.Database()["B"].Seq; got != 5 {
		t.Errorf("stored sequence for B = %d, want 5; a stale announcement overwrote it", got)
	}
}
