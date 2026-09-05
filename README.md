# CC3067 — Laboratory 3: Routing Algorithms

A distributed routing simulator. Each node is an independent process that talks
to its neighbours over TCP sockets and builds its routing table with one of
three interchangeable algorithms: **Dijkstra**, **Flooding** or **Link State
Routing (LSR)**.

## Requirements

- Go 1.22 or newer (developed on 1.27)
- `make` (optional)
- `tmux` (optional, only for the scripted demo)

No third-party dependencies: the whole project builds on the standard library.

## Build

```bash
make build          # produces bin/node
# or
go build -o bin/node ./cmd/node
```

The result is a single static binary, so the same source runs on Linux, macOS
and Windows without any per-platform setup.

## Run one node

```bash
bin/node --id A --mode lsr \
         --topo  configs/topo-default.json \
         --names configs/names-default.json
```

| Flag | Meaning |
| --- | --- |
| `--id` | this node's identifier in the topology (required) |
| `--mode` | `dijkstra`, `flooding` or `lsr` (default `lsr`) |
| `--topo` | topology file: who is connected to whom |
| `--names` | name table: which address each node listens on |
| `--listen` | override the listening address from the name table |
| `-v` | log every packet the node handles |

## Run the whole network

```bash
make demo            # LSR, nine nodes, one tmux pane each
make demo MODE=flooding
```

Or start the nine nodes by hand in nine terminals, one `--id` each.

## Run the whole network in Docker

Nine containers on a private bridge network, one router each. This is the
closest thing to the real class setup: every node has its own network stack and
its own address, so nothing works by accident through shared loopback.

```bash
make docker-up                  # LSR by default
make docker-up MODE=flooding
docker attach lab3-node-a       # open a node's console
docker compose logs -f          # follow every node at once
make docker-down
```

Detach from an attached console with **Ctrl-P Ctrl-Q**. Ctrl-C would signal the
node instead; closing the terminal is harmless, since the node reopens its
console for the next attach.

Inside the containers the topology is resolved by service name
(`configs/names-docker.json` maps `A` to `node-a:5000`), so there is no host
port bookkeeping and adjacency is declared in exactly one place.

### Simulating a failure

```bash
docker compose stop node-f      # a router goes down
docker attach lab3-node-a       # then: table
docker compose start node-f     # and back up
```

Under LSR, node A withdraws the links to F, ages F's announcement out of its
database after `lspMaxAge`, and recomputes. Observed on this topology:

```
before   A>C>F>H       traffic to I crosses F
after    A>B>D>E>G     F is gone, the network routed around it
```

## Console

Every node opens an interactive console:

```
send <dest> <text>   send a user message through the network
table                show the routing table
neighbors            show direct links and their measured cost
lsdb                 show the link-state database (lsr mode only)
topology             show every known link and its weight (alias: graph)
dijkstra             recompute and show routes: destination, next hop, cost
check                diagnose links that are configured but never came up,
                     and links only one endpoint declares
info                 show this node's id and mode
help                 show this text
quit                 stop the node
```

### Diagnosing a network that does not converge

`check` answers the question a routing table cannot: not "what did I compute"
but "what did I fail to learn". It runs two independent checks.

The first lists **configured neighbours that never answered a hello**. A
link-state node only announces links it measured, so a neighbour with a stale
address is dropped from the announcement silently — the rest of the network is
told the link does not exist, and nothing errors anywhere. This is the check
that catches a wrong entry in the name table.

The second lists **links only one endpoint declares**. Those are usable in one
direction only, and make the two nodes compute different routes while both
remain internally consistent.

Both also report themselves on the console without being asked, once per
finding: silent neighbours after a few probe intervals, asymmetries after a
grace period that avoids flagging the transient asymmetry of every boot.

## Configuration files

Both files follow the `{"type": ..., "config": ...}` shape agreed for the
course.

`configs/topo-default.json` — adjacency, as plain neighbour lists:

```json
{ "type": "topo", "config": { "A": ["B", "C"], "B": ["A", "C", "D"] } }
```

`configs/topo-weighted.json` — the nine-node class graph with explicit link
weights, which the loader accepts interchangeably with the list form:

```json
{ "type": "topo", "config": { "A": { "B": 4, "C": 2, "D": 7 } } }
```

`configs/names-default.json` — where each node listens:

```json
{ "type": "names", "config": { "A": "127.0.0.1:5001" } }
```

For the class session, replace the loopback addresses with the IP each team
announces. A node id that already looks like `host:port` resolves to itself, so
another team can address this node by raw IP without appearing in our table.

## Wire protocol

One JSON object per line over TCP (NDJSON), UTF-8, max 65536 bytes per line.
Newline framing means a node can be inspected with nothing more than `nc`.
This is the format the class agreed on, so it interoperates with every other
team's implementation regardless of language.

```json
{
  "version": 1,
  "proto": "lsr",
  "type": "message",
  "from": "10.0.0.1:5000",
  "to": "10.0.0.7:5000",
  "ttl": 16,
  "headers": [{ "msg_id": "3f2a..." }, { "checksum": "0bded535" }, { "trace": ["10.0.0.1:5000"] }],
  "payload": "hola"
}
```

| Field | Meaning |
| --- | --- |
| `version` | envelope version, currently `1`; a mismatch is logged, never a reason to drop a packet |
| `proto` | algorithm that produced the packet |
| `type` | `message`, `hello`, `echo` or `info` |
| `from` / `to` | `IP:puerto` of the originator and final destination (`*` broadcasts an LSP); a port-less address completes with the network's configured port |
| `ttl` | hop budget, starts at 16; the packet is dropped at zero |
| `headers` | array of single-entry objects, per the agreed format |
| `payload` | **string** for `message`; **object** for `hello`, `echo` and `info` |

Headers we rely on — unknown headers from other teams are preserved when a
packet is forwarded:

| Header | Purpose |
| --- | --- |
| `msg_id` | duplicate suppression; derived deterministically from `(from,to,type,payload)` when a peer omits it |
| `checksum` | CRC32 (hex, 8 digits) of the canonical payload; a mismatch is logged, never dropped |
| `via` | address of the previous hop, so flooding never bounces a packet back |
| `t0` | sender's timestamp, fractional Unix seconds, for `hello`/`echo` link measurement |
| `trace` | addresses the packet has traversed |

Packet types:

- `hello` — probe a neighbour, payload `{"listen_port": 5000}`; the receiver answers with `echo`.
- `echo` — carries the original `t0` back, so the prober measures the round
  trip without keeping per-probe state.
- `info` — a link-state packet, flooded across the network. Payload:
  `{"origin": "...", "seq": 7, "age_s": 0, "neighbors": [{"id": "...", "weight": 4.8}]}`.
  Identity is `(origin, seq)`: only a strictly higher `seq` is stored and
  re-flooded, except when it falls far enough below the one on file (16 or
  more) to signal a restarted origin rather than a stale duplicate.
- `message` — user data, forwarded to its destination or printed on arrival.

## Architecture

```
cmd/node            process entry point and flags
internal/protocol   the JSON envelope and the link-state payload
internal/transport  TCP server and pooled client, newline-framed JSON
internal/config     topology and name-table loading
internal/routing    the routing plane
  graph.go          Dijkstra — a pure function of (graph, source)
  flood.go          controlled flooding — a pure function of (neighbours, packet)
  seen.go           duplicate suppression
  algorithm.go      the strategy interface every algorithm implements
  alg_*.go          the three algorithms
internal/node       the forwarding plane, discovery, and the wiring
internal/cli        the interactive console
```

Two planes run concurrently as goroutines, as the assignment requires:

- **Forwarding** — one goroutine drains an inbox fed by the connection
  goroutines. Sockets never block on a routing recomputation.
- **Routing** — the neighbour monitor probes links on a timer, and the
  algorithm reacts to `OnLinkUp` / `OnLinkDown` events.

### Why Dijkstra and Flooding are functions, not programs

The assignment notes that LSR *uses* the other two. Both are therefore written
as pure functions with no knowledge of sockets:

```go
func Dijkstra(g Graph, source string) Table
func Flood(neighbors []string, previousHop string) []string
```

`LSRAlgorithm` calls `Flood` to disseminate link-state packets and `Dijkstra`
to turn the resulting database into a forwarding table. The standalone
`dijkstra` and `flooding` modes call exactly the same two functions. There is
one implementation of each algorithm in the project, not two.

### How each mode behaves

| | knows | table | reacts to failures |
| --- | --- | --- | --- |
| `dijkstra` | the whole topology from the file | computed once at boot | prunes dead neighbours and recomputes |
| `flooding` | its neighbours only | none | stops using a silent link |
| `lsr` | its neighbours, then the topology it learns | recomputed on every change | re-announces, ages out dead nodes, reroutes |

### How the flood terminates

Three independent mechanisms, because in a class network any one of them can be
defeated by another team's implementation:

1. **`msg_id` cache** — a packet already handled is dropped, so a cycle cannot
   multiply copies.
2. **Sequence numbers** — an LSP whose sequence is not higher than the stored
   one is never re-flooded.
3. **TTL** — a hard hop budget, the last resort against a malformed peer.

Split horizon (never forwarding back to `hop`) is not needed for correctness,
but it halves link traffic and keeps the traces readable.

### Keeping control traffic bounded

Link costs come from real round-trip measurements, and on a fast link those
swing by more than half from one probe to the next. Feeding that jitter
straight into the routing plane makes every node re-announce its links on every
hello — a control-traffic storm that grows with the size of the network.

Two mechanisms keep it bounded:

1. **Smoothing** — the discovery plane keeps an exponential moving average of
   each link's round trip, so a single noisy sample cannot move the cost much.
2. **Announcement policy** — a neighbour appearing or disappearing is a
   topology change and is flooded at once. A cost that merely drifted is
   announced only when it moved past a threshold, and never more often than
   the rate limit; the periodic refresh carries it otherwise.

Without these, sequence numbers on a nine-node network climb every few seconds
from jitter alone; with them, an origin's sequence number advances only on the
10-second periodic refresh (`lspInterval`) plus real topology changes — nothing
more.

## Tests

```bash
make test     # unit and integration suites
make race     # the same suites under the race detector
```

The integration suite in `internal/node/node_test.go` boots real nodes on real
loopback sockets and asserts end-to-end behaviour: LSR converging and choosing
the shortest path, flooding delivering exactly once across a cycle, Dijkstra
building its table from configuration alone, and an unreachable destination
being reported rather than silently dropped.
