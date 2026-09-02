package protocol

import (
	"encoding/json"
	"fmt"
)

// LinkStatePacket is the routing information a node floods across the network
// under Link State Routing. Each node announces only the links it can observe
// directly; the full topology is reconstructed by collecting every LSP.
//
// It travels JSON-encoded inside Packet.Payload of an info packet.
type LinkStatePacket struct {
	// Origin is the node that owns and signed this announcement.
	Origin string `json:"origin"`
	// Seq increases on every new announcement from Origin. A node keeps only
	// the highest sequence number it has seen, which is what makes flooding
	// converge instead of looping forever.
	Seq int `json:"seq"`
	// Neighbors maps each directly reachable node to the measured link cost.
	Neighbors map[string]float64 `json:"neighbors"`
}

// EncodeLSP serialises a link-state packet for transport inside a payload.
func EncodeLSP(lsp LinkStatePacket) (string, error) {
	data, err := json.Marshal(lsp)
	if err != nil {
		return "", fmt.Errorf("encode lsp: %w", err)
	}
	return string(data), nil
}

// DecodeLSP parses a link-state packet out of an info payload.
func DecodeLSP(payload string) (LinkStatePacket, error) {
	var lsp LinkStatePacket
	if err := json.Unmarshal([]byte(payload), &lsp); err != nil {
		return lsp, fmt.Errorf("decode lsp: %w", err)
	}
	if lsp.Neighbors == nil {
		lsp.Neighbors = map[string]float64{}
	}
	return lsp, nil
}
