// Package config loads the two files every node needs to boot: the topology
// (who is connected to whom) and the name table (where each node listens).
//
// Both files follow the {"type": ..., "config": ...} shape used across the
// course so that configurations can be swapped between teams.
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Topology describes the adjacency of the network as weighted links.
//
// Only Dijkstra consumes the whole topology. Flooding and LSR read just the
// entry for the local node, because they are only allowed to know their own
// neighbours.
type Topology struct {
	Type string `json:"type"`
	// Config maps a node id to its neighbours and the cost of reaching them.
	Config map[string]map[string]float64 `json:"config"`
}

// Names maps a node id to the TCP endpoint ("host:port") it listens on.
type Names struct {
	Type   string            `json:"type"`
	Config map[string]string `json:"config"`
}

// defaultLinkCost is used when a topology declares neighbours as a plain list
// instead of giving each link an explicit weight.
const defaultLinkCost = 1.0

// UnmarshalJSON accepts both supported topology shapes:
//
//	{"config": {"A": ["B", "C"]}}          unweighted, every link costs 1
//	{"config": {"A": {"B": 1, "C": 2.5}}}  explicit per-link weights
func (t *Topology) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type   string                     `json:"type"`
		Config map[string]json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse topology: %w", err)
	}

	t.Type = raw.Type
	t.Config = make(map[string]map[string]float64, len(raw.Config))

	for node, entry := range raw.Config {
		weighted := map[string]float64{}
		if err := json.Unmarshal(entry, &weighted); err == nil {
			t.Config[node] = weighted
			continue
		}

		var neighbours []string
		if err := json.Unmarshal(entry, &neighbours); err != nil {
			return fmt.Errorf("parse neighbours of %q: %w", node, err)
		}
		weighted = make(map[string]float64, len(neighbours))
		for _, n := range neighbours {
			weighted[n] = defaultLinkCost
		}
		t.Config[node] = weighted
	}
	return nil
}

// LoadTopology reads and validates a topology file.
func LoadTopology(path string) (*Topology, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read topology: %w", err)
	}
	var topo Topology
	if err := json.Unmarshal(data, &topo); err != nil {
		return nil, err
	}
	if len(topo.Config) == 0 {
		return nil, fmt.Errorf("topology %q declares no nodes", path)
	}
	return &topo, nil
}

// LoadNames reads and validates a name table.
func LoadNames(path string) (*Names, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read names: %w", err)
	}
	var names Names
	if err := json.Unmarshal(data, &names); err != nil {
		return nil, fmt.Errorf("parse names: %w", err)
	}
	if len(names.Config) == 0 {
		return nil, fmt.Errorf("name table %q declares no nodes", path)
	}
	return &names, nil
}

// Neighbors returns the links declared for node id, or an empty map when the
// node is absent from the topology.
func (t *Topology) Neighbors(id string) map[string]float64 {
	links, ok := t.Config[id]
	if !ok {
		return map[string]float64{}
	}
	out := make(map[string]float64, len(links))
	for k, v := range links {
		out[k] = v
	}
	return out
}

// Endpoint resolves the address a node listens on. Ids that already look like
// an endpoint (they contain a colon, e.g. "10.0.0.4:5000") resolve to
// themselves, which lets other teams address us by raw IP without appearing in
// our name table.
func (n *Names) Endpoint(id string) (string, bool) {
	if addr, ok := n.Config[id]; ok {
		return addr, true
	}
	for i := range id {
		if id[i] == ':' {
			return id, true
		}
	}
	return "", false
}
