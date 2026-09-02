package transport

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/uvg/cc3067-lab3/internal/protocol"
)

// dialTimeout bounds how long we wait for an unreachable neighbour before
// reporting the link as down.
const dialTimeout = 2 * time.Second

// writeTimeout bounds a single send, so one stalled peer cannot freeze the
// forwarding goroutine that is serving every other neighbour.
const writeTimeout = 2 * time.Second

// Client sends packets to remote nodes, reusing one connection per endpoint.
//
// Connections are cached because a node talks to the same handful of
// neighbours continuously; re-dialing on every hello would dominate the link
// cost we are trying to measure.
type Client struct {
	mu    sync.Mutex
	conns map[string]net.Conn
}

// NewClient returns an empty connection pool.
func NewClient() *Client {
	return &Client{conns: map[string]net.Conn{}}
}

// Send delivers one packet to addr. A cached connection that has been closed
// by the peer is transparently re-dialed once before the send is reported as
// failed.
func (c *Client) Send(addr string, pkt *protocol.Packet) error {
	data, err := pkt.Encode()
	if err != nil {
		return fmt.Errorf("encode packet: %w", err)
	}
	data = append(data, '\n')

	if err := c.write(addr, data, false); err == nil {
		return nil
	} else if writeErr := c.write(addr, data, true); writeErr != nil {
		return fmt.Errorf("send to %s: %w", addr, writeErr)
	}
	return nil
}

// write pushes data to addr, forcing a fresh connection when reconnect is set.
func (c *Client) write(addr string, data []byte, reconnect bool) error {
	conn, err := c.conn(addr, reconnect)
	if err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		c.drop(addr)
		return err
	}
	if _, err := conn.Write(data); err != nil {
		c.drop(addr)
		return err
	}
	return nil
}

func (c *Client) conn(addr string, reconnect bool) (net.Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if existing, ok := c.conns[addr]; ok {
		if !reconnect {
			return existing, nil
		}
		_ = existing.Close()
		delete(c.conns, addr)
	}

	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return nil, err
	}
	c.conns[addr] = conn
	return conn, nil
}

func (c *Client) drop(addr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if conn, ok := c.conns[addr]; ok {
		_ = conn.Close()
		delete(c.conns, addr)
	}
}

// Close releases every pooled connection.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for addr, conn := range c.conns {
		_ = conn.Close()
		delete(c.conns, addr)
	}
}
