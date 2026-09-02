BINARY := bin/node
PKG    := ./cmd/node

.PHONY: all build test race vet fmt clean run demo docker-up docker-down docker-logs

all: fmt vet test build

## build: compile the node binary for the current platform
build:
	@mkdir -p bin
	go build -o $(BINARY) $(PKG)

## test: run the unit and integration test suites
test:
	go test ./...

## race: run the suite under the race detector (the concurrency proof)
race:
	go test -race -count=1 ./...

vet:
	go vet ./...

fmt:
	gofmt -w ./cmd ./internal

## run: start one node, e.g. make run ID=A MODE=lsr
ID   ?= A
MODE ?= lsr
run: build
	$(BINARY) --id $(ID) --mode $(MODE) \
		--topo configs/topo-default.json \
		--names configs/names-default.json

## demo: bring up the whole nine-node network in a tmux session
demo: build
	./scripts/demo.sh $(MODE)

## docker-up: build the image and start the nine-node network in containers
docker-up:
	MODE=$(MODE) docker compose up -d --build
	@echo "attach to a node with: docker attach lab3-node-a  (detach: Ctrl-P Ctrl-Q)"

## docker-down: stop and remove the containers and their network
docker-down:
	docker compose down

## docker-logs: follow every node's console output at once
docker-logs:
	docker compose logs -f

clean:
	rm -rf bin
