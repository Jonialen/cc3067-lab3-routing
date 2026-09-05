package routing

import (
	"testing"
	"time"
)

// edges is shorthand for building a link set in these tests.
func edges(pairs ...[3]any) []Edge {
	out := make([]Edge, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, Edge{From: p[0].(string), To: p[1].(string), Cost: p[2].(float64)})
	}
	return out
}

func TestAsymmetriesAcceptsAGraphWhereEveryLinkIsDeclaredTwice(t *testing.T) {
	got := Asymmetries(edges(
		[3]any{"A", "B", 4.0}, [3]any{"B", "A", 4.0},
		[3]any{"B", "C", 1.0}, [3]any{"C", "B", 1.0},
	))
	if len(got) != 0 {
		t.Fatalf("a fully symmetric graph must report nothing, got %v", got)
	}
}

func TestAsymmetriesReportsALinkOnlyOneEndpointDeclares(t *testing.T) {
	// C announces D, but D's own announcement does not mention C. This is the
	// failure shape seen in the class test: the link is unusable in the D->C
	// direction and no node reports an error about it.
	got := Asymmetries(edges(
		[3]any{"C", "D", 3.0}, [3]any{"C", "B", 1.0},
		[3]any{"B", "C", 1.0},
		[3]any{"D", "F", 2.0},
	))
	if len(got) != 1 {
		t.Fatalf("want exactly one asymmetry, got %v", got)
	}
	want := Asymmetry{Declared: "C", Missing: "D", Cost: 3.0}
	if got[0] != want {
		t.Fatalf("want %+v, got %+v", want, got[0])
	}
}

func TestAsymmetriesIgnoresNodesThatHaveNotAnnouncedAnything(t *testing.T) {
	// We know of E only because C mentions it; E's own announcement has not
	// reached us. That is a node we have not heard from, not a node refusing
	// to declare a link, and reporting it would bury the real findings.
	got := Asymmetries(edges(
		[3]any{"C", "E", 5.0}, [3]any{"C", "B", 1.0},
		[3]any{"B", "C", 1.0},
	))
	if len(got) != 0 {
		t.Fatalf("a node that never announced must not be reported, got %v", got)
	}
}

func TestAsymmetriesAreOrderedForStableOutput(t *testing.T) {
	// Map iteration feeds this function, so the result must be sorted or the
	// console would print the same finding in a different order every time.
	in := edges(
		[3]any{"C", "D", 3.0},
		[3]any{"A", "B", 4.0},
		[3]any{"B", "A", 4.0}, [3]any{"B", "C", 1.0},
		[3]any{"C", "B", 1.0},
		[3]any{"D", "F", 2.0}, [3]any{"D", "A", 7.0},
		[3]any{"A", "C", 2.0},
		[3]any{"F", "D", 2.0},
	)
	first := Asymmetries(in)
	for i := 0; i < 20; i++ {
		if got := Asymmetries(in); len(got) != len(first) {
			t.Fatalf("unstable length: %v vs %v", first, got)
		} else {
			for j := range got {
				if got[j] != first[j] {
					t.Fatalf("unstable order at %d: %v vs %v", j, first, got)
				}
			}
		}
	}
	for i := 1; i < len(first); i++ {
		if first[i-1].Declared > first[i].Declared {
			t.Fatalf("not sorted by declaring node: %v", first)
		}
	}
}

func TestAsymmetriesIgnoresASelfLoop(t *testing.T) {
	got := Asymmetries(edges([3]any{"A", "A", 0.0}, [3]any{"A", "B", 1.0}, [3]any{"B", "A", 1.0}))
	if len(got) != 0 {
		t.Fatalf("a self loop is not an asymmetry, got %v", got)
	}
}

func TestAsymmetryWatchStaysQuietWhileTheNetworkIsStillConverging(t *testing.T) {
	// Announcements arrive one at a time, so a graph is briefly asymmetric on
	// every boot. Reporting that would train everyone to ignore the warning.
	w := newAsymmetryWatch()
	found := []Asymmetry{{Declared: "C", Missing: "D", Cost: 3}}
	start := time.Now()

	if due := w.due(found, start, 30*time.Second); len(due) != 0 {
		t.Fatalf("a freshly observed asymmetry must not be reported, got %v", due)
	}
	if due := w.due(found, start.Add(29*time.Second), 30*time.Second); len(due) != 0 {
		t.Fatalf("still inside the grace period, got %v", due)
	}
}

func TestAsymmetryWatchReportsOneThatOutlivesTheGracePeriod(t *testing.T) {
	w := newAsymmetryWatch()
	found := []Asymmetry{{Declared: "C", Missing: "D", Cost: 3}}
	start := time.Now()
	w.due(found, start, 30*time.Second)

	due := w.due(found, start.Add(31*time.Second), 30*time.Second)
	if len(due) != 1 || due[0].Declared != "C" || due[0].Missing != "D" {
		t.Fatalf("a persistent asymmetry must be reported once, got %v", due)
	}
}

func TestAsymmetryWatchReportsEachFindingOnlyOnce(t *testing.T) {
	// The sweep runs every few seconds; repeating the same line forever would
	// make the console useless for everything else.
	w := newAsymmetryWatch()
	found := []Asymmetry{{Declared: "C", Missing: "D", Cost: 3}}
	start := time.Now()
	w.due(found, start, 30*time.Second)
	w.due(found, start.Add(31*time.Second), 30*time.Second)

	if due := w.due(found, start.Add(60*time.Second), 30*time.Second); len(due) != 0 {
		t.Fatalf("the same asymmetry must not be reported twice, got %v", due)
	}
}

func TestAsymmetryWatchRearmsAfterAFindingIsResolved(t *testing.T) {
	// If the link becomes symmetric and later breaks again, that is new
	// information and deserves a new line.
	w := newAsymmetryWatch()
	found := []Asymmetry{{Declared: "C", Missing: "D", Cost: 3}}
	start := time.Now()
	w.due(found, start, 30*time.Second)
	w.due(found, start.Add(31*time.Second), 30*time.Second)

	w.due(nil, start.Add(40*time.Second), 30*time.Second)

	w.due(found, start.Add(50*time.Second), 30*time.Second)
	due := w.due(found, start.Add(81*time.Second), 30*time.Second)
	if len(due) != 1 {
		t.Fatalf("a recurring asymmetry must be reported again, got %v", due)
	}
}
