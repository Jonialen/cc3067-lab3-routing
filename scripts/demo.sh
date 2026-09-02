#!/usr/bin/env bash
# Brings up the nine-node reference topology, one node per tmux pane.
#
# Usage: ./scripts/demo.sh [mode]     mode = lsr (default) | flooding | dijkstra
set -euo pipefail

MODE="${1:-lsr}"
SESSION="lab3-${MODE}"
NODES=(A B C D E F G H I)
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

command -v tmux >/dev/null || { echo "tmux is required for the demo"; exit 1; }
[[ -x "${ROOT}/bin/node" ]] || { echo "run 'make build' first"; exit 1; }

tmux kill-session -t "${SESSION}" 2>/dev/null || true

node_cmd() {
  printf '%s --id %s --mode %s --topo %s --names %s' \
    "${ROOT}/bin/node" "$1" "${MODE}" \
    "${ROOT}/configs/topo-default.json" \
    "${ROOT}/configs/names-default.json"
}

tmux new-session -d -s "${SESSION}" -c "${ROOT}" "$(node_cmd "${NODES[0]}")"
for id in "${NODES[@]:1}"; do
  tmux split-window -t "${SESSION}" -c "${ROOT}" "$(node_cmd "${id}")"
  tmux select-layout -t "${SESSION}" tiled >/dev/null
done

tmux select-layout -t "${SESSION}" tiled >/dev/null
echo "session '${SESSION}' is up — attach with: tmux attach -t ${SESSION}"
echo "inside any pane try: table | neighbors | lsdb | send I hola"
tmux attach -t "${SESSION}"
