package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestLoadTopologyAcceptsPlainNeighbourLists(t *testing.T) {
	path := writeTemp(t, "topo.json", `{"type":"topo","config":{"A":["B","C"],"B":["A"]}}`)

	topo, err := LoadTopology(path)
	if err != nil {
		t.Fatalf("LoadTopology returned %v", err)
	}
	links := topo.Neighbors("A")
	if len(links) != 2 || links["B"] != defaultLinkCost || links["C"] != defaultLinkCost {
		t.Errorf("neighbours of A = %v, want B and C at cost %v", links, defaultLinkCost)
	}
}

func TestLoadTopologyAcceptsExplicitWeights(t *testing.T) {
	path := writeTemp(t, "topo.json", `{"type":"topo","config":{"A":{"B":2.5},"B":{"A":2.5}}}`)

	topo, err := LoadTopology(path)
	if err != nil {
		t.Fatalf("LoadTopology returned %v", err)
	}
	if got := topo.Neighbors("A")["B"]; got != 2.5 {
		t.Errorf("cost A->B = %v, want 2.5", got)
	}
}

func TestNeighborsOfAnUnknownNodeIsEmptyNotNil(t *testing.T) {
	path := writeTemp(t, "topo.json", `{"type":"topo","config":{"A":["B"]}}`)
	topo, err := LoadTopology(path)
	if err != nil {
		t.Fatalf("LoadTopology returned %v", err)
	}

	links := topo.Neighbors("Z")
	if links == nil {
		t.Fatal("Neighbors must return an empty map, never nil")
	}
	if len(links) != 0 {
		t.Errorf("neighbours of an unknown node = %v, want none", links)
	}
}

func TestNeighborsReturnsACopyTheCallerCannotCorrupt(t *testing.T) {
	path := writeTemp(t, "topo.json", `{"type":"topo","config":{"A":["B"]}}`)
	topo, err := LoadTopology(path)
	if err != nil {
		t.Fatalf("LoadTopology returned %v", err)
	}

	topo.Neighbors("A")["B"] = 999

	if got := topo.Neighbors("A")["B"]; got != defaultLinkCost {
		t.Errorf("cost A->B = %v after a caller mutated its copy, want %v", got, defaultLinkCost)
	}
}

func TestLoadTopologyRejectsAnEmptyConfiguration(t *testing.T) {
	path := writeTemp(t, "topo.json", `{"type":"topo","config":{}}`)

	if _, err := LoadTopology(path); err == nil {
		t.Fatal("expected an error for a topology declaring no nodes")
	}
}

func TestEndpointResolvesNamesAndRawAddresses(t *testing.T) {
	path := writeTemp(t, "names.json", `{"type":"names","config":{"A":"127.0.0.1:5001"}}`)
	names, err := LoadNames(path)
	if err != nil {
		t.Fatalf("LoadNames returned %v", err)
	}

	if addr, ok := names.Endpoint("A"); !ok || addr != "127.0.0.1:5001" {
		t.Errorf("Endpoint(A) = %q, %v; want the configured address", addr, ok)
	}
	// Another team may address us by raw endpoint without being in our table.
	if addr, ok := names.Endpoint("10.0.0.9:5000"); !ok || addr != "10.0.0.9:5000" {
		t.Errorf("Endpoint of a raw address = %q, %v; want it to resolve to itself", addr, ok)
	}
	if _, ok := names.Endpoint("Z"); ok {
		t.Error("an unknown name must not resolve")
	}
}
