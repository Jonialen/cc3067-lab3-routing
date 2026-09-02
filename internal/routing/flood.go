package routing

import "github.com/uvg/cc3067-lab3/internal/protocol"

// Flood selects the neighbours a packet must be relayed to under controlled
// flooding: every neighbour except the one that just handed us the packet.
//
// Split horizon (excluding the previous hop) is not required for correctness —
// the duplicate cache already stops the packet — but it halves the traffic on
// every link and makes the traces in the report readable.
//
// Like Dijkstra, this is a pure function so that Link State Routing can reuse
// it verbatim to disseminate link-state packets.
func Flood(neighbors []string, pkt *protocol.Packet) []string {
	previousHop := pkt.HeaderString(protocol.HeaderHop)

	targets := make([]string, 0, len(neighbors))
	for _, n := range neighbors {
		if n == previousHop || n == pkt.From {
			continue
		}
		targets = append(targets, n)
	}
	return targets
}
