// Package cli provides the interactive console attached to a running node,
// used during the class demo to send messages and inspect routing state.
package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/uvg/cc3067-lab3/internal/node"
	"github.com/uvg/cc3067-lab3/internal/routing"
)

// Console reads commands from a reader and applies them to a node.
type Console struct {
	node *node.Node
	in   io.Reader
	out  io.Writer
}

// New builds a console bound to a node.
func New(n *node.Node, in io.Reader, out io.Writer) *Console {
	return &Console{node: n, in: in, out: out}
}

// Run reads commands until the input closes, the user quits, or ctx is
// cancelled.
//
// It reports whether the user actually asked to stop. Reaching the end of
// stdin is not such a request: detaching from a container closes stdin, and a
// router must keep routing when nobody is watching its console.
func (c *Console) Run(ctx context.Context) (quitRequested bool) {
	c.printHelp()

	lines := make(chan string)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(c.in)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()

	for {
		fmt.Fprintf(c.out, "%s> ", c.node.ID())
		select {
		case <-ctx.Done():
			return false
		case line, ok := <-lines:
			if !ok {
				return false
			}
			if c.dispatch(strings.TrimSpace(line)) {
				return true
			}
		}
	}
}

// dispatch runs one command and reports whether the console should exit.
func (c *Console) dispatch(line string) bool {
	if line == "" {
		return false
	}

	command, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimSpace(rest)

	switch command {
	case "send":
		c.send(rest)
	case "table", "routes":
		fmt.Fprintln(c.out, c.node.Table())
	case "neighbors", "neighbours":
		c.printNeighbors()
	case "lsdb":
		c.printDatabase()
	case "topology", "graph":
		c.printTopology()
	case "dijkstra":
		c.printDijkstra()
	case "check":
		c.printCheck()
	case "info":
		fmt.Fprintf(c.out, "id=%s mode=%s\n", c.node.ID(), c.node.Mode())
	case "help", "?":
		c.printHelp()
	case "quit", "exit":
		return true
	default:
		fmt.Fprintf(c.out, "unknown command %q, try \"help\"\n", command)
	}
	return false
}

// send parses "send <destination> <text>" and injects the message.
func (c *Console) send(rest string) {
	dest, text, ok := strings.Cut(rest, " ")
	if !ok || strings.TrimSpace(dest) == "" {
		fmt.Fprintln(c.out, "usage: send <destination> <text>")
		return
	}
	if err := c.node.Send(dest, strings.TrimSpace(text)); err != nil {
		fmt.Fprintf(c.out, "send failed: %v\n", err)
	}
}

func (c *Console) printNeighbors() {
	states := c.node.NeighborStates()
	if len(states) == 0 {
		fmt.Fprintln(c.out, "(no neighbours)")
		return
	}
	sort.Slice(states, func(i, j int) bool { return states[i].ID < states[j].ID })

	fmt.Fprintf(c.out, "%-16s %-8s %-8s %s\n", "NEIGHBOUR", "STATE", "COST", "LAST SEEN")
	for _, s := range states {
		state := "down"
		if s.Alive {
			state = "up"
		}
		lastSeen := "never"
		if !s.LastSeen.IsZero() {
			lastSeen = s.LastSeen.Format("15:04:05")
		}
		fmt.Fprintf(c.out, "%-16s %-8s %-8.2f %s\n", s.ID, state, s.Cost, lastSeen)
	}
}

// printCheck runs the two consistency diagnostics and reports what they find.
//
// They answer different questions and neither subsumes the other. The silent
// neighbour check compares our configuration against what discovery actually
// observed, and is the only way to notice a link both endpoints failed to
// establish: that graph is symmetric — nobody declares the link — so it looks
// perfectly consistent while simply being absent. The topology check compares
// the announcements of different nodes against each other, and catches a link
// one side declares and the other does not, which is usable in one direction
// only and makes the two nodes compute different routes.
//
// Both failures are silent by nature: every node announced exactly what it
// measured and every Dijkstra ran correctly. This command is what turns them
// into something a person can read.
func (c *Console) printCheck() {
	problems := 0

	if silent := c.node.SilentNeighbors(); len(silent) > 0 {
		problems += len(silent)
		fmt.Fprintln(c.out, "configured neighbours that never answered a hello:")
		for _, id := range silent {
			fmt.Fprintf(c.out, "  %-16s check that its address is current and that it is running\n", id)
		}
	}

	if lsr, ok := c.node.Algorithm().(*routing.LSRAlgorithm); ok {
		found := lsr.Asymmetries()
		problems += len(found)
		if len(found) > 0 {
			fmt.Fprintln(c.out, "links declared by only one endpoint:")
			for _, a := range found {
				fmt.Fprintf(c.out, "  %s -> %s (cost %.2f): %s does not declare it back\n",
					a.Declared, a.Missing, a.Cost, a.Missing)
			}
		}
	}

	if problems == 0 {
		fmt.Fprintln(c.out, "no inconsistencies found")
	}
}

// printDatabase dumps the link-state database, which only LSR maintains.
func (c *Console) printDatabase() {
	lsr, ok := c.node.Algorithm().(*routing.LSRAlgorithm)
	if !ok {
		fmt.Fprintf(c.out, "no link-state database in %s mode\n", c.node.Mode())
		return
	}

	db := lsr.Database()
	if len(db) == 0 {
		fmt.Fprintln(c.out, "(link-state database is empty)")
		return
	}

	origins := make([]string, 0, len(db))
	for origin := range db {
		origins = append(origins, origin)
	}
	sort.Strings(origins)

	for _, origin := range origins {
		lsp := db[origin]
		links := make([]string, 0, len(lsp.Neighbors))
		for neighbor, cost := range lsp.Neighbors {
			links = append(links, fmt.Sprintf("%s(%.2f)", neighbor, cost))
		}
		sort.Strings(links)
		fmt.Fprintf(c.out, "%-16s seq=%-4d %s\n", origin, lsp.Seq, strings.Join(links, " "))
	}
}

// printTopology dumps every link the algorithm currently knows about, with
// its cost: the whole network graph for dijkstra and lsr, only our own direct
// links for flooding.
func (c *Console) printTopology() {
	edges := c.node.Algorithm().Topology()
	if len(edges) == 0 {
		fmt.Fprintln(c.out, "(no topology known yet)")
		return
	}

	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})

	fmt.Fprintf(c.out, "%-16s %-16s %s\n", "ORIGEN", "DESTINO", "PESO")
	for _, e := range edges {
		fmt.Fprintf(c.out, "%-16s %-16s %.2f\n", e.From, e.To, e.Cost)
	}
}

// printDijkstra runs Dijkstra explicitly over the topology this node
// currently knows (the same edges "topology" shows) and prints the resulting
// routes: destination, next hop and total cost. In lsr and dijkstra modes
// this matches "table" exactly, since that is how those tables are built; the
// point of a separate command is to make that computation visible on demand
// and to give flooding a routes view too, limited to what it actually knows.
func (c *Console) printDijkstra() {
	graph := routing.NewGraph()
	for _, e := range c.node.Algorithm().Topology() {
		graph.AddEdge(e.From, e.To, e.Cost)
	}

	table := routing.Dijkstra(graph, c.node.ID())
	if len(table) == 0 {
		fmt.Fprintln(c.out, "(sin rutas: la topología conocida todavía no alcanza a nadie)")
		return
	}

	fmt.Fprintf(c.out, "%-16s %-16s %s\n", "DESTINO", "NEXT HOP", "COSTO")
	for _, r := range table.Sorted() {
		fmt.Fprintf(c.out, "%-16s %-16s %.2f\n", r.Dest, r.NextHop, r.Cost)
	}
}

func (c *Console) printHelp() {
	fmt.Fprint(c.out, `commands:
  send <dest> <text>   send a user message through the network
  table                show the routing table
  neighbors            show direct links and their measured cost
  lsdb                 show the link-state database (lsr mode only)
  topology             show every known link and its weight (alias: graph)
  dijkstra             recompute and show routes: destino, next hop, costo
  check                diagnose links that are configured but never came up,
                       and links only one endpoint declares
  info                 show this node's id and mode
  help                 show this text
  quit                 stop the node
`)
}
