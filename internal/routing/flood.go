package routing

// Flood selects the neighbours a packet must be relayed to under controlled
// flooding: every neighbour except previousHop, the one that just handed us
// the packet. Callers resolve previousHop from the packet's "via" header (or
// "from", for a packet arriving straight from its origin) into local id
// space before calling this, since Flood itself knows nothing about wire
// addresses.
//
// Split horizon (excluding the previous hop) is not required for correctness —
// the duplicate cache already stops the packet — but it halves the traffic on
// every link and makes the traces in the report readable.
//
// Like Dijkstra, this is a pure function so that Link State Routing can reuse
// it verbatim to disseminate link-state packets.
func Flood(neighbors []string, previousHop string) []string {
	targets := make([]string, 0, len(neighbors))
	for _, n := range neighbors {
		if n == previousHop {
			continue
		}
		targets = append(targets, n)
	}
	return targets
}
