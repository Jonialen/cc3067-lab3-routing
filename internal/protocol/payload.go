package protocol

import (
	"bytes"
	"encoding/json"
)

// HelloPayload is the object carried by "hello" and "echo" packets.
type HelloPayload struct {
	ListenPort int `json:"listen_port"`
}

// NeighborEntry is one link inside a LinkStatePacket, in the canonical wire
// shape: a list of {id, weight} objects.
type NeighborEntry struct {
	ID     string  `json:"id"`
	Weight float64 `json:"weight"`
}

// LinkStatePacket is the routing information a node floods across the network
// under Link State Routing. Each node announces only the links it can observe
// directly; the full topology is reconstructed by collecting every LSP.
//
// It travels JSON-encoded as the object payload of an "info" packet.
type LinkStatePacket struct {
	// Origin is the address of the node that owns and signed this
	// announcement.
	Origin string `json:"origin"`
	// Seq increases on every new announcement from Origin. A node keeps only
	// the highest sequence number it has seen, which is what makes flooding
	// converge instead of looping forever.
	Seq int `json:"seq"`
	// AgeS is how many seconds have passed since Origin produced this
	// announcement. We always flood a freshly built one, so it is 0.
	AgeS float64 `json:"age_s"`
	// Neighbors lists each link Origin can reach directly and its cost. It is
	// always emitted as a list of {id, weight} objects; DecodeLSPFlexible
	// accepts the equivalent shapes other implementations may send instead.
	Neighbors []NeighborEntry `json:"neighbors"`
}

// DecodeLSPFlexible parses a link-state packet out of an "info" payload. It
// accepts our own canonical shape plus the variants the protocol explicitly
// recommends tolerating: the payload as a JSON-encoded string, "links"
// instead of "neighbors", a neighbours dictionary ({id: cost}) instead of a
// list, and {node, cost} instead of {id, weight}.
func DecodeLSPFlexible(raw json.RawMessage) (LinkStatePacket, error) {
	data := []byte(raw)
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		data = []byte(asString)
	}

	var generic map[string]json.RawMessage
	if err := json.Unmarshal(data, &generic); err != nil {
		return LinkStatePacket{}, err
	}

	var lsp LinkStatePacket
	if v, ok := generic["origin"]; ok {
		_ = json.Unmarshal(v, &lsp.Origin)
	}
	if v, ok := generic["seq"]; ok {
		_ = json.Unmarshal(v, &lsp.Seq)
	}
	if v, ok := generic["age_s"]; ok {
		_ = json.Unmarshal(v, &lsp.AgeS)
	}

	neighborsRaw, ok := generic["neighbors"]
	if !ok {
		neighborsRaw, ok = generic["links"]
	}
	if ok {
		lsp.Neighbors = decodeNeighbors(neighborsRaw)
	}
	return lsp, nil
}

// decodeNeighbors accepts every neighbour-list shape the protocol allows a
// receiver to tolerate, sniffing the JSON kind rather than trying decoders in
// sequence, so an empty list or an empty dictionary decodes correctly too.
func decodeNeighbors(raw json.RawMessage) []NeighborEntry {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}

	switch trimmed[0] {
	case '{':
		var dict map[string]float64
		if err := json.Unmarshal(trimmed, &dict); err != nil {
			return nil
		}
		out := make([]NeighborEntry, 0, len(dict))
		for id, cost := range dict {
			out = append(out, NeighborEntry{ID: id, Weight: cost})
		}
		return out

	case '[':
		var idWeight []struct {
			ID     string  `json:"id"`
			Weight float64 `json:"weight"`
		}
		if err := json.Unmarshal(trimmed, &idWeight); err == nil && allHaveID(idWeight) {
			out := make([]NeighborEntry, len(idWeight))
			for i, e := range idWeight {
				out[i] = NeighborEntry{ID: e.ID, Weight: e.Weight}
			}
			return out
		}

		var nodeCost []struct {
			Node string  `json:"node"`
			Cost float64 `json:"cost"`
		}
		if err := json.Unmarshal(trimmed, &nodeCost); err == nil {
			out := make([]NeighborEntry, len(nodeCost))
			for i, e := range nodeCost {
				out[i] = NeighborEntry{ID: e.Node, Weight: e.Cost}
			}
			return out
		}
	}
	return nil
}

func allHaveID(entries []struct {
	ID     string  `json:"id"`
	Weight float64 `json:"weight"`
}) bool {
	if len(entries) == 0 {
		return true
	}
	for _, e := range entries {
		if e.ID == "" {
			return false
		}
	}
	return true
}
