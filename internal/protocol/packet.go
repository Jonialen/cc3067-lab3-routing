// Package protocol defines the on-the-wire message format shared by every node
// in the network, regardless of the routing algorithm it runs.
//
// The envelope follows the format agreed upon by the course, so that nodes
// implemented by different teams (and in different languages) interoperate.
package protocol

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
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

// Version is the only envelope version this implementation speaks. A packet
// carrying a different value is still processed — see Decode — because
// rejecting it would fragment the class network over a field nobody expects
// to change.
const Version = 1

// DefaultTTL bounds how many hops a packet may travel before being dropped.
const DefaultTTL = 16

// Packet is the JSON envelope exchanged between nodes.
//
// Headers is a list of single-entry objects rather than a map because the
// agreed inter-team format specifies an array. Use Header and SetHeader to
// read and write entries instead of touching the slice directly.
//
// Payload is kept as raw JSON rather than a Go string because the protocol
// requires it to be a string for "message" packets but an object for
// "hello", "echo" and "info". Use SetPayloadText/SetPayloadObject to write it
// and PayloadText/PayloadObject to read it back typed.
type Packet struct {
	Version int              `json:"version"`
	Proto   Proto            `json:"proto"`
	Type    Type             `json:"type"`
	From    string           `json:"from"`
	To      string           `json:"to"`
	TTL     int              `json:"ttl"`
	Headers []map[string]any `json:"headers"`
	Payload json.RawMessage  `json:"payload"`
}

// Reserved header keys used by our implementation. Unknown headers from other
// teams are preserved untouched when a packet is forwarded.
const (
	// HeaderMsgID uniquely identifies a packet so duplicates can be discarded.
	HeaderMsgID = "msg_id"
	// HeaderChecksum carries the CRC32 of the canonical payload, see
	// ComputeChecksum. A mismatch is logged, never a reason to drop a packet.
	HeaderChecksum = "checksum"
	// HeaderVia records the address of the previous hop, so flooding never
	// bounces a packet straight back to its sender.
	HeaderVia = "via"
	// HeaderT0 carries the sender's timestamp, in fractional Unix seconds,
	// used by hello/echo exchanges to measure the round-trip time of a link.
	HeaderT0 = "t0"
	// HeaderTrace accumulates the addresses a packet has traversed.
	HeaderTrace = "trace"
)

// New builds a packet with a fresh identifier, the default TTL and no
// payload. Callers must set one with SetPayloadText or SetPayloadObject
// before sending it, which is also what fills in the checksum header.
func New(proto Proto, typ Type, from, to string) *Packet {
	p := &Packet{
		Version: Version,
		Proto:   proto,
		Type:    typ,
		From:    from,
		To:      to,
		TTL:     DefaultTTL,
		Headers: []map[string]any{},
	}
	p.SetHeader(HeaderMsgID, NewID())
	return p
}

// NewText builds a packet whose payload is plain text, as required for
// "message" packets.
func NewText(proto Proto, typ Type, from, to, text string) *Packet {
	p := New(proto, typ, from, to)
	p.SetPayloadText(text)
	return p
}

// NewObject builds a packet whose payload is a JSON object, as required for
// "hello", "echo" and "info" packets.
func NewObject(proto Proto, typ Type, from, to string, obj any) (*Packet, error) {
	p := New(proto, typ, from, to)
	if err := p.SetPayloadObject(obj); err != nil {
		return nil, err
	}
	return p, nil
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

// Trace returns the addresses a packet has traversed, oldest first. It reads
// back cleanly whether the slice was set locally (as []string) or arrived
// over JSON (as []any).
func (p *Packet) Trace() []string {
	v, ok := p.Header(HeaderTrace)
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// AppendTrace records that the packet traversed addr.
func (p *Packet) AppendTrace(addr string) {
	p.SetHeader(HeaderTrace, append(p.Trace(), addr))
}

// SetPayloadText stores text as the payload, as required for "message"
// packets, and refreshes the checksum header to match.
func (p *Packet) SetPayloadText(text string) {
	encoded, err := json.Marshal(text)
	if err != nil {
		// json.Marshal on a string never fails.
		encoded = []byte(`""`)
	}
	p.Payload = encoded
	p.SetHeader(HeaderChecksum, checksumHex([]byte(text)))
}

// SetPayloadObject stores obj as the payload, as required for "hello",
// "echo" and "info" packets, and refreshes the checksum header to match.
func (p *Packet) SetPayloadObject(obj any) error {
	encoded, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}
	p.Payload = encoded
	canonical, err := canonicalJSON(encoded)
	if err != nil {
		return fmt.Errorf("canonicalize payload: %w", err)
	}
	p.SetHeader(HeaderChecksum, checksumHex(canonical))
	return nil
}

// PayloadText reads the payload back as text. It fails for a packet whose
// payload is an object rather than a string.
func (p *Packet) PayloadText() (string, error) {
	var s string
	if err := json.Unmarshal(p.Payload, &s); err != nil {
		return "", fmt.Errorf("payload is not text: %w", err)
	}
	return s, nil
}

// PayloadObject decodes the payload into v, which must be a pointer. It
// transparently unwraps a payload that another implementation sent as a
// JSON-encoded string instead of a raw object, which the protocol tolerates.
func (p *Packet) PayloadObject(v any) error {
	data := []byte(p.Payload)
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		data = []byte(asString)
	}
	return json.Unmarshal(data, v)
}

// canonicalPayloadBytes returns the bytes ComputeChecksum must hash: the raw
// UTF-8 text when the payload is a JSON string, or the canonical
// (alphabetically-keyed, compact, non-HTML-escaped) serialisation when it is
// an object or array.
func (p *Packet) canonicalPayloadBytes() []byte {
	if len(p.Payload) == 0 {
		return nil
	}
	trimmed := bytes.TrimSpace(p.Payload)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return []byte(s)
		}
	}
	canonical, err := canonicalJSON(p.Payload)
	if err != nil {
		return p.Payload
	}
	return canonical
}

// canonicalJSON re-serialises data with alphabetically sorted keys, compact
// separators and no HTML escaping, by round-tripping it through a generic
// interface{} — encoding/json sorts map keys at every level automatically.
func canonicalJSON(data []byte) ([]byte, error) {
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		return nil, err
	}
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// checksumHex formats the CRC32 of data as required: hexadecimal, 8 digits,
// lowercase.
func checksumHex(data []byte) string {
	return fmt.Sprintf("%08x", crc32.ChecksumIEEE(data))
}

// ChecksumValid reports whether the packet's checksum header matches its
// payload. A packet with no checksum header is considered valid: the field
// is mandatory for our own packets, but a receiver must not use its absence
// to drop someone else's.
func (p *Packet) ChecksumValid() bool {
	want := p.HeaderString(HeaderChecksum)
	if want == "" {
		return true
	}
	return want == checksumHex(p.canonicalPayloadBytes())
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
	cp.Payload = append(json.RawMessage(nil), p.Payload...)
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
// fields other implementations may omit. Per the protocol, a missing or
// unexpected version, or a checksum that does not verify, is never a reason
// to reject a packet — callers may log it, but must keep processing.
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
		p.SetHeader(HeaderMsgID, fallbackMsgID(&p))
	}
	return &p, nil
}

// fallbackMsgID derives a deterministic identifier for a packet that arrived
// without one, so that the same packet reaching two nodes by different paths
// still deduplicates. TTL is deliberately excluded: it changes on every hop,
// which would make every copy look like a new packet.
func fallbackMsgID(p *Packet) string {
	h := crc32.ChecksumIEEE([]byte(fmt.Sprintf("%s|%s|%s|%s", p.From, p.To, p.Type, p.Payload)))
	return fmt.Sprintf("fallback-%08x", h)
}

// String renders a compact one-line summary for logs.
func (p *Packet) String() string {
	return fmt.Sprintf("%s/%s %s->%s ttl=%d id=%s",
		p.Proto, p.Type, p.From, p.To, p.TTL, p.HeaderString(HeaderMsgID))
}
