// Command node runs one router of the network.
//
// Example:
//
//	node --id A --mode lsr --topo configs/topo-default.json --names configs/names-default.json
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/uvg/cc3067-lab3/internal/cli"
	"github.com/uvg/cc3067-lab3/internal/config"
	"github.com/uvg/cc3067-lab3/internal/node"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		id       = flag.String("id", "", "identifier of this node in the topology (required)")
		mode     = flag.String("mode", "lsr", "routing algorithm: dijkstra, flooding or lsr")
		topoPath = flag.String("topo", "configs/topo-default.json", "path to the topology file")
		namePath = flag.String("names", "configs/names-default.json", "path to the name table")
		listen   = flag.String("listen", "", "override the listening address from the name table")
		verbose  = flag.Bool("v", false, "log every packet the node handles")
	)
	flag.Parse()

	if *id == "" {
		flag.Usage()
		return fmt.Errorf("--id is required")
	}
	selected, err := node.ParseMode(*mode)
	if err != nil {
		return err
	}
	topology, err := config.LoadTopology(*topoPath)
	if err != nil {
		return err
	}
	names, err := config.LoadNames(*namePath)
	if err != nil {
		return err
	}

	router, err := node.New(node.Options{
		ID:       *id,
		Mode:     selected,
		Topology: topology,
		Names:    names,
		Listen:   *listen,
		Verbose:  *verbose,
	})
	if err != nil {
		return err
	}

	// A cancelled context stops every plane, so Ctrl-C and the "quit" command
	// take exactly the same shutdown path.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := router.Start(ctx); err != nil {
		return err
	}

	serveConsole(ctx, router)

	stop()
	router.Wait()
	return nil
}

// reattachDelay throttles reattach attempts so a terminal that closes
// immediately cannot spin the loop.
const reattachDelay = 200 * time.Millisecond

// serveConsole runs the interactive console and returns when the node should
// stop: either the user asked to quit, or the process was signalled.
//
// Reaching the end of stdin is neither. A router must keep routing when nobody
// is watching its console, and when it runs inside a container a client
// detaching from `docker attach` closes stdin without the node being asked to
// do anything. In that case the terminal is reopened so a later attach gets a
// working prompt again; when stdin is an ordinary pipe there is nothing to
// reopen, so the node simply keeps running until it is signalled.
func serveConsole(ctx context.Context, router *node.Node) {
	stdin := os.Stdin

	for ctx.Err() == nil {
		if cli.New(router, stdin, os.Stdout).Run(ctx) {
			return
		}

		reopened, ok := reopenTerminal(stdin)
		if !ok {
			<-ctx.Done()
			return
		}
		if stdin != os.Stdin {
			_ = stdin.Close()
		}
		stdin = reopened

		select {
		case <-ctx.Done():
			return
		case <-time.After(reattachDelay):
		}
	}
}

// reopenTerminal reopens the controlling terminal behind current. It reports
// false when current is not a terminal, which is the case for a pipe or a file
// and means there is nothing to reattach to.
func reopenTerminal(current *os.File) (*os.File, bool) {
	info, err := current.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return nil, false
	}
	reopened, err := os.Open("/dev/stdin")
	if err != nil {
		return nil, false
	}
	return reopened, true
}
