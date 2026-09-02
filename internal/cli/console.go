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

func (c *Console) printHelp() {
	fmt.Fprint(c.out, `commands:
  send <dest> <text>   send a user message through the network
  table                show the routing table
  neighbors            show direct links and their measured cost
  lsdb                 show the link-state database (lsr mode only)
  info                 show this node's id and mode
  help                 show this text
  quit                 stop the node
`)
}
