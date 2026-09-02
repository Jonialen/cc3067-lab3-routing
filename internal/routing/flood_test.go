package routing

import (
	"testing"
	"time"

	"github.com/uvg/cc3067-lab3/internal/protocol"
)

func TestFloodExcludesThePreviousHop(t *testing.T) {
	pkt := protocol.New(protocol.ProtoFlooding, protocol.TypeMessage, "A", "D", "hi")
	pkt.SetHeader(protocol.HeaderHop, "B")

	targets := Flood([]string{"B", "C", "E"}, pkt)

	for _, target := range targets {
		if target == "B" {
			t.Fatalf("packet was sent back to its previous hop: %v", targets)
		}
	}
	if len(targets) != 2 {
		t.Errorf("targets = %v, want C and E", targets)
	}
}

func TestFloodExcludesTheOriginator(t *testing.T) {
	// A neighbour that originated the packet already has it.
	pkt := protocol.New(protocol.ProtoFlooding, protocol.TypeMessage, "C", "D", "hi")

	targets := Flood([]string{"B", "C"}, pkt)

	if len(targets) != 1 || targets[0] != "B" {
		t.Errorf("targets = %v, want only B", targets)
	}
}

func TestSeenCacheReportsRepeatedIdentifiers(t *testing.T) {
	cache := NewSeenCache(time.Minute)

	if cache.Seen("abc") {
		t.Fatal("first sighting must not be reported as a duplicate")
	}
	if !cache.Seen("abc") {
		t.Fatal("second sighting must be reported as a duplicate")
	}
	if cache.Seen("def") {
		t.Fatal("a different identifier must not be reported as a duplicate")
	}
}

func TestSeenCacheIgnoresEmptyIdentifiers(t *testing.T) {
	cache := NewSeenCache(time.Minute)

	if cache.Seen("") || cache.Seen("") {
		t.Fatal("an empty identifier carries no information and must never match")
	}
	if cache.Len() != 0 {
		t.Errorf("cache stored %d empty identifiers, want 0", cache.Len())
	}
}
