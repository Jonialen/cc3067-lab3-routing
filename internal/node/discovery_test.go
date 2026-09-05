package node_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uvg/cc3067-lab3/internal/config"
	"github.com/uvg/cc3067-lab3/internal/node"
)

// startLoneNode boots a single node whose configured neighbours are all
// unreachable, which is what a wrong address in the name table looks like from
// the inside: the hellos leave, nothing ever answers.
func startLoneNode(t *testing.T, id string, neighbors ...string) *node.Node {
	t.Helper()
	n, _ := startLoneNodeLogging(t, id, neighbors...)
	return n
}

// startLoneNodeLogging is startLoneNode with the node console captured, for
// the diagnostics the node emits without being asked.
func startLoneNodeLogging(t *testing.T, id string, neighbors ...string) (*node.Node, *safeBuffer) {
	t.Helper()

	links := map[string]float64{}
	names := &config.Names{Type: "names", Config: map[string]string{
		id: fmt.Sprintf("127.0.0.1:%d", freePort(t)),
	}}
	for _, ne := range neighbors {
		links[ne] = 1
		// A port nobody is listening on: the address resolves, the connection
		// never completes.
		names.Config[ne] = fmt.Sprintf("127.0.0.1:%d", freePort(t))
	}
	topo := &config.Topology{Type: "topo", Config: map[string]map[string]float64{id: links}}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	logs := &safeBuffer{}
	n, err := node.New(node.Options{ID: id, Mode: node.ModeLSR, Topology: topo, Names: names, LogOutput: logs})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := n.Start(ctx); err != nil {
		t.Fatalf("start node: %v", err)
	}
	return n, logs
}

// safeBuffer collects console output from the node goroutines.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestAConfiguredNeighbourThatNeverAnswersIsReportedAsSilent(t *testing.T) {
	// This is the exact shape of the class failure: the neighbour is in our
	// configuration, our hellos go out, and the round trip never closes
	// because the address is stale. Nothing errors; the link simply never
	// exists, and we stop announcing it without ever saying so.
	n := startLoneNode(t, "B", "A", "C", "E")

	states := n.NeighborStates()
	if len(states) != 3 {
		t.Fatalf("want all three configured neighbours known, got %v", states)
	}
	for _, s := range states {
		if s.Alive {
			t.Errorf("neighbour %s cannot be alive, nothing is listening", s.ID)
		}
		if !s.LastSeen.IsZero() {
			t.Errorf("neighbour %s was never seen, LastSeen must be zero", s.ID)
		}
	}
}

func TestSilentNeighborsListsThemSortedByID(t *testing.T) {
	n := startLoneNode(t, "B", "E", "A", "C")

	silent := n.SilentNeighbors()
	want := []string{"A", "C", "E"}
	if len(silent) != len(want) {
		t.Fatalf("want %v, got %v", want, silent)
	}
	for i := range want {
		if silent[i] != want[i] {
			t.Fatalf("want %v sorted, got %v", want, silent)
		}
	}
}

func TestSilentNeighborsExcludesOneThatAnswered(t *testing.T) {
	net := startNetwork(t, node.ModeLSR, map[string][]string{
		"A": {"B"},
		"B": {"A"},
	})
	net.awaitRoute("A", "B", 5*time.Second)

	if silent := net.nodes["A"].SilentNeighbors(); len(silent) != 0 {
		t.Fatalf("a neighbour that answered must not be silent, got %v", silent)
	}
}

func TestTheNodeWarnsAboutASilentNeighbourWithoutBeingAsked(t *testing.T) {
	// The diagnostic is only useful if it surfaces on its own: nobody runs a
	// consistency command for a problem they do not yet know they have.
	_, logs := startLoneNodeLogging(t, "B", "E")

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logs.String(), "neighbour E") &&
			strings.Contains(logs.String(), "never answered a hello") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the node never warned about its silent neighbour; log was:\n%s", logs.String())
}
