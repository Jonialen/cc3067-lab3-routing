# Build stage: compile a fully static binary so the runtime image needs no
# toolchain and no C library.
FROM golang:1.24-alpine AS build

WORKDIR /src

# Copy the module definition first so dependency resolution is cached
# independently of the source, which keeps rebuilds fast.
COPY go.mod ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/node ./cmd/node

# Runtime stage: alpine rather than scratch, so a node can be inspected from
# inside the network with the usual tools while the demo is running.
FROM alpine:3.20

RUN apk add --no-cache ca-certificates iproute2 \
    && adduser -D -u 10001 router

WORKDIR /app
COPY --from=build /out/node /usr/local/bin/node
COPY configs/ /app/configs/

USER router

# Every node listens on the same port; inside the compose network each one has
# its own address, so there is no port bookkeeping to get wrong.
EXPOSE 5000

ENTRYPOINT ["node"]
CMD ["--help"]
