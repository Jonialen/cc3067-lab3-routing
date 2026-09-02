// Package protocol defines the on-the-wire message format shared by every node
// in the network, regardless of the routing algorithm it runs.
//
// The envelope follows the format agreed upon by the course, so that nodes
// implemented by different teams (and in different languages) interoperate.
package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// Proto identifies the routing algorithm that produced a packet.
type Proto string

const (
	ProtoDijkstra Proto = "dijkstra"
	ProtoFlooding Proto = "flooding"
	ProtoLSR      Proto = "lsr"
)

// Type identifies the purpose of a packet.
type Type string

const (
	// TypeMessage carries user data and must be forwarded to its destination.
	TypeMessage Type = "message"
	// TypeHello probes a neighbour to discover it and measure the link cost.
	TypeHello Type = "hello"
	// TypeEcho answers a hello and closes the round-trip measurement.
	TypeEcho Type = "echo"
	// TypeInfo carries routing state: link-state packets, tables, neighbours.
	TypeInfo Type = "info"
)

// DefaultTTL bounds how many hops a packet may travel before being dropped.
const DefaultTTL = 8

// Packet is the JSON envelope exchanged between nodes.
//
// Headers is a list of single-entry objects rather than a map because the
// agreed inter-team format specifies an array. Use Header and SetHeader to
// read and write entries instead of touching the slice directly.
type Packet struct {
	Proto   Proto            `json:"proto"`
	Type    Type             `json:"type"`
	From    string           `json:"from"`
	To      string           `json:"to"`
	TTL     int              `json:"ttl"`
	Headers []map[string]any `json:"headers"`
	Payload string           `json:"payload"`
}

// Reserved header keys used by our implementation. Unknown headers from other
// teams are preserved untouched when a packet is forwarded.
const (
	// HeaderMsgID uniquely identifies a packet so duplicates can be discarded.
	HeaderMsgID = "msg_id"
	// HeaderHop records the neighbour that handed us the packet, so flooding
	// never bounces a packet straight back to its sender.
	HeaderHop = "hop"
	// HeaderSentAt carries the sender's timestamp in nanoseconds, used by
	// hello/echo exchanges to measure the round-trip time of a link.
	HeaderSentAt = "sent_at"
	// HeaderPath accumulates the nodes a packet has traversed, for tracing.
	HeaderPath = "path"
)

// New builds a packet with a fresh identifier and the default TTL.
func New(proto Proto, typ Type, from, to, payload string) *Packet {
	p := &Packet{
		Proto:   proto,
		Type:    typ,
		From:    from,
		To:      to,
		TTL:     DefaultTTL,
		Headers: []map[string]any{},
		Payload: payload,
	}
	p.SetHeader(HeaderMsgID, NewID())
	return p
}

// NewID returns a random identifier for duplicate suppression.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read only fails if the system entropy source is broken; a
		// degraded identifier is still better than dropping the packet.
		return fmt.Sprintf("%p", &b)
	}
	return hex.EncodeToString(b[:])
}

// Header returns the value stored under key, if present.
func (p *Packet) Header(key string) (any, bool) {
	for _, h := range p.Headers {
		if v, ok := h[key]; ok {
			return v, true
		}
	}
	return nil, false
}

// HeaderString returns the value stored under key as a string, or "".
func (p *Packet) HeaderString(key string) string {
	v, ok := p.Header(key)
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Sprint(v)
	}
	return s
}

// HeaderFloat returns the value stored under key as a number, or 0.
// JSON numbers decode as float64, so integers arrive here too.
func (p *Packet) HeaderFloat(key string) (float64, bool) {
	v, ok := p.Header(key)
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// SetHeader stores key, replacing any previous entry with the same key.
func (p *Packet) SetHeader(key string, value any) {
	for _, h := range p.Headers {
		if _, ok := h[key]; ok {
			h[key] = value
			return
		}
	}
	p.Headers = append(p.Headers, map[string]any{key: value})
}

// AppendPath records that the packet traversed node id.
func (p *Packet) AppendPath(id string) {
	current := p.HeaderString(HeaderPath)
	if current == "" {
		p.SetHeader(HeaderPath, id)
		return
	}
	p.SetHeader(HeaderPath, current+">"+id)
}

// Clone returns a deep copy, so that forwarding the same packet to several
// neighbours never lets one branch mutate another's headers.
func (p *Packet) Clone() *Packet {
	cp := *p
	cp.Headers = make([]map[string]any, 0, len(p.Headers))
	for _, h := range p.Headers {
		entry := make(map[string]any, len(h))
		for k, v := range h {
			entry[k] = v
		}
		cp.Headers = append(cp.Headers, entry)
	}
	return &cp
}

// ErrExpired reports a packet whose TTL ran out.
var ErrExpired = errors.New("packet ttl expired")

// DecrementTTL consumes one hop and reports whether the packet may travel on.
func (p *Packet) DecrementTTL() error {
	p.TTL--
	if p.TTL <= 0 {
		return ErrExpired
	}
	return nil
}

// Encode serialises the packet as a single JSON line.
func (p *Packet) Encode() ([]byte, error) {
	return json.Marshal(p)
}

// Decode parses a JSON line into a packet and fills in safe defaults for
// fields other implementations may omit.
func Decode(data []byte) (*Packet, error) {
	var p Packet
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("decode packet: %w", err)
	}
	if p.TTL <= 0 {
		p.TTL = DefaultTTL
	}
	if p.Headers == nil {
		p.Headers = []map[string]any{}
	}
	if p.HeaderString(HeaderMsgID) == "" {
		p.SetHeader(HeaderMsgID, NewID())
	}
	return &p, nil
}

// String renders a compact one-line summary for logs.
func (p *Packet) String() string {
	return fmt.Sprintf("%s/%s %s->%s ttl=%d id=%s",
		p.Proto, p.Type, p.From, p.To, p.TTL, p.HeaderString(HeaderMsgID))
}
