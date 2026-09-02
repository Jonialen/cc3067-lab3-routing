// Package transport carries protocol packets over TCP.
//
// Every packet travels as one JSON object terminated by a newline, which keeps
// framing trivial and lets a node be inspected with nothing more than netcat.
package transport

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/uvg/cc3067-lab3/internal/protocol"
)

// Handler processes one inbound packet. It runs on the connection's goroutine,
// so it must not block for long; the node hands work to its queues instead.
type Handler func(*protocol.Packet)

// maxLine bounds an inbound JSON line so a malformed peer cannot exhaust memory.
const maxLine = 1 << 20 // 1 MiB

// Server accepts inbound connections and feeds decoded packets to a handler.
type Server struct {
	addr     string
	handler  Handler
	logf     func(string, ...any)
	listener net.Listener
	wg       sync.WaitGroup
}

// NewServer prepares a server bound to addr ("host:port").
func NewServer(addr string, handler Handler, logf func(string, ...any)) *Server {
	return &Server{addr: addr, handler: handler, logf: logf}
}

// Start begins listening and serves connections until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.addr, err)
	}
	s.listener = ln

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	s.wg.Add(1)
	go s.acceptLoop(ctx, ln)
	return nil
}

// Addr reports the address actually bound, useful when port 0 was requested.
func (s *Server) Addr() string {
	if s.listener == nil {
		return s.addr
	}
	return s.listener.Addr().String()
}

// Wait blocks until every accepted connection has finished.
func (s *Server) Wait() { s.wg.Wait() }

func (s *Server) acceptLoop(ctx context.Context, ln net.Listener) {
	defer s.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			// A cancelled context closes the listener, which surfaces here as
			// an error; that is a clean shutdown, not a failure.
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			s.logf("accept error: %v", err)
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveConn(ctx, conn)
		}()
	}
}

func (s *Server) serveConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLine)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		pkt, err := protocol.Decode(line)
		if err != nil {
			s.logf("dropping malformed packet from %s: %v", conn.RemoteAddr(), err)
			continue
		}
		s.handler(pkt)
	}
}
