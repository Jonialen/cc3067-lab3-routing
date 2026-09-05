package routing

import (
	"sort"
	"time"
)

// Asymmetry is one link that only one of its two endpoints declares.
//
// A link-state graph is built from directed edges: AddEdge records from -> to,
// and a node only ever declares the links it measured itself. A healthy
// network therefore declares every link twice, once from each end. When only
// one end declares it, the link is usable in that direction alone, and no node
// involved has any way to notice: each one announced exactly what it measured,
// each one ran Dijkstra correctly, and the two arrived at different tables.
//
// That silence is what makes this check worth running. It turns a
// disagreement no single node can observe into a line on the console.
type Asymmetry struct {
	// Declared is the node that announces the link.
	Declared string
	// Missing is the node on the other end, which does not announce it back.
	Missing string
	// Cost is the weight Declared published for the link.
	Cost float64
}

// Asymmetries reports every link in edges that only one endpoint declares,
// sorted by declaring node and then by the missing one so the output is
// reproducible between calls.
//
// A node that has not announced anything at all is skipped: it contributes no
// outgoing edge, and we cannot distinguish a link it refuses to declare from
// one whose announcement has not reached us yet. Reporting those would fill
// the result with every node beyond our horizon while the network is still
// converging, and bury the findings that matter.
func Asymmetries(edges []Edge) []Asymmetry {
	declares := map[string]map[string]float64{}
	for _, e := range edges {
		if e.From == e.To {
			continue
		}
		if _, ok := declares[e.From]; !ok {
			declares[e.From] = map[string]float64{}
		}
		declares[e.From][e.To] = e.Cost
	}

	found := make([]Asymmetry, 0)
	for from, links := range declares {
		for to, cost := range links {
			// Only the other endpoint's own announcement can contradict this
			// one. If it never announced, it is silent, not contradicting.
			back, announced := declares[to]
			if !announced {
				continue
			}
			if _, mutual := back[from]; mutual {
				continue
			}
			found = append(found, Asymmetry{Declared: from, Missing: to, Cost: cost})
		}
	}

	sort.Slice(found, func(i, j int) bool {
		if found[i].Declared != found[j].Declared {
			return found[i].Declared < found[j].Declared
		}
		return found[i].Missing < found[j].Missing
	})
	return found
}

// asymmetryWatch decides when an asymmetry is worth putting on the console.
//
// Two things make a raw finding unusable as a warning. A network that is still
// converging is asymmetric by construction, because announcements arrive one
// at a time; and the sweep that produces findings runs every few seconds, so
// an unfiltered report would repeat the same line forever. The watch solves
// both: a finding must outlive a grace period before it is reported, and it is
// reported once, re-arming only if it is resolved and comes back.
type asymmetryWatch struct {
	firstSeen map[string]time.Time
	warned    map[string]bool
}

func newAsymmetryWatch() *asymmetryWatch {
	return &asymmetryWatch{
		firstSeen: map[string]time.Time{},
		warned:    map[string]bool{},
	}
}

// due records the current findings against now and returns the ones that have
// persisted longer than grace and have not been reported yet.
func (w *asymmetryWatch) due(found []Asymmetry, now time.Time, grace time.Duration) []Asymmetry {
	current := make(map[string]bool, len(found))
	report := make([]Asymmetry, 0)

	for _, a := range found {
		key := a.Declared + "->" + a.Missing
		current[key] = true
		if _, seen := w.firstSeen[key]; !seen {
			w.firstSeen[key] = now
		}
		if w.warned[key] || now.Sub(w.firstSeen[key]) <= grace {
			continue
		}
		w.warned[key] = true
		report = append(report, a)
	}

	// Forget the findings that resolved, so the same link warns again if it
	// breaks a second time.
	for key := range w.firstSeen {
		if !current[key] {
			delete(w.firstSeen, key)
			delete(w.warned, key)
		}
	}
	return report
}
