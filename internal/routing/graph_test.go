package routing

import "testing"

func TestDijkstraPicksCheapestPathNotFewestHops(t *testing.T) {
	// A->B->D costs 2 and takes two hops; A->C->D costs 20. The direct-looking
	// route must lose to the cheap one.
	g := NewGraph()
	g.AddEdge("A", "B", 1)
	g.AddEdge("B", "A", 1)
	g.AddEdge("B", "D", 1)
	g.AddEdge("D", "B", 1)
	g.AddEdge("A", "C", 10)
	g.AddEdge("C", "A", 10)
	g.AddEdge("C", "D", 10)
	g.AddEdge("D", "C", 10)

	table := Dijkstra(g, "A")

	route, ok := table["D"]
	if !ok {
		t.Fatalf("expected a route to D, table = %v", table)
	}
	if route.NextHop != "B" {
		t.Errorf("next hop to D = %q, want %q", route.NextHop, "B")
	}
	if route.Cost != 2 {
		t.Errorf("cost to D = %v, want 2", route.Cost)
	}
}

func TestDijkstraExcludesSourceAndUnreachableNodes(t *testing.T) {
	g := NewGraph()
	g.AddEdge("A", "B", 1)
	g.AddEdge("B", "A", 1)
	// Z exists in the graph but nothing connects it to A.
	g.AddEdge("Z", "Y", 1)

	table := Dijkstra(g, "A")

	if _, ok := table["A"]; ok {
		t.Error("routing table must not contain a route to the node itself")
	}
	if _, ok := table["Z"]; ok {
		t.Error("routing table must not contain unreachable nodes")
	}
	if len(table) != 1 {
		t.Errorf("table = %v, want only a route to B", table)
	}
}

func TestDijkstraFirstHopIsAlwaysADirectNeighbour(t *testing.T) {
	// A chain: every route from A must start at B, the only neighbour.
	g := NewGraph()
	for _, pair := range [][2]string{{"A", "B"}, {"B", "C"}, {"C", "D"}} {
		g.AddEdge(pair[0], pair[1], 1)
		g.AddEdge(pair[1], pair[0], 1)
	}

	table := Dijkstra(g, "A")

	for dest, route := range table {
		if route.NextHop != "B" {
			t.Errorf("next hop to %s = %q, want %q", dest, route.NextHop, "B")
		}
	}
	if got := table["D"].Cost; got != 3 {
		t.Errorf("cost to D = %v, want 3", got)
	}
}

func TestAddEdgeKeepsTheCheaperOfDuplicateLinks(t *testing.T) {
	g := NewGraph()
	g.AddEdge("A", "B", 5)
	g.AddEdge("A", "B", 2)
	g.AddEdge("A", "B", 9)

	if got := g["A"]["B"]; got != 2 {
		t.Errorf("edge cost = %v, want the cheapest declaration 2", got)
	}
}
