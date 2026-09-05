BINARY := bin/node
PKG    := ./cmd/node

.PHONY: all build test race vet fmt clean run demo docker-up docker-down docker-logs report

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

## report: render docs/reporte.md to docs/reporte.pdf via pandoc + xelatex
report:
	pandoc docs/reporte.md -o docs/reporte.pdf \
		--pdf-engine=xelatex \
		--from=markdown+smart \
		--resource-path=docs \
		--toc --toc-depth=2 \
		--syntax-highlighting=tango \
		-V documentclass=article -V papersize=letter \
		-V geometry:margin=2.5cm -V fontsize=11pt -V lang=es \
		-V colorlinks=true -V linkcolor=black -V urlcolor=blue \
		-V mainfont="Liberation Serif" \
		-V sansfont="Liberation Sans" \
		-V monofont="Liberation Mono"

clean:
	rm -rf bin
