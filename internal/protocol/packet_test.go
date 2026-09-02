package protocol

import (
	"encoding/json"
	"testing"
)

func TestDecodeFillsDefaultsForFieldsOtherTeamsMayOmit(t *testing.T) {
	// A minimal packet from another implementation: no ttl, no headers.
	raw := []byte(`{"proto":"flooding","type":"message","from":"A","to":"D","payload":"hola"}`)

	pkt, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode returned %v", err)
	}
	if pkt.TTL != DefaultTTL {
		t.Errorf("ttl = %d, want the default %d", pkt.TTL, DefaultTTL)
	}
	if pkt.HeaderString(HeaderMsgID) == "" {
		t.Error("a packet without an id must be given one, or duplicates cannot be detected")
	}
}

func TestDecodeRejectsMalformedJSON(t *testing.T) {
	if _, err := Decode([]byte(`{"proto":`)); err == nil {
		t.Fatal("expected an error for truncated JSON")
	}
}

func TestSetHeaderReplacesInsteadOfAppending(t *testing.T) {
	pkt := New(ProtoLSR, TypeInfo, "A", "*", "")
	pkt.SetHeader("k", "first")
	pkt.SetHeader("k", "second")

	count := 0
	for _, h := range pkt.Headers {
		if _, ok := h["k"]; ok {
			count++
		}
	}
	if count != 1 {
		t.Errorf("header k appears %d times, want 1", count)
	}
	if got := pkt.HeaderString("k"); got != "second" {
		t.Errorf("header k = %q, want %q", got, "second")
	}
}

func TestCloneIsolatesHeadersBetweenForwardedCopies(t *testing.T) {
	// Flooding sends one packet to several neighbours; a mutation on one copy
	// must never be visible on another.
	original := New(ProtoFlooding, TypeMessage, "A", "D", "hola")
	original.SetHeader(HeaderHop, "A")

	copyA := original.Clone()
	copyA.SetHeader(HeaderHop, "B")

	if got := original.HeaderString(HeaderHop); got != "A" {
		t.Errorf("original hop = %q, want %q; the clone shared its headers", got, "A")
	}
}

func TestDecrementTTLStopsAtZero(t *testing.T) {
	pkt := New(ProtoFlooding, TypeMessage, "A", "D", "hola")
	pkt.TTL = 2

	if err := pkt.DecrementTTL(); err != nil {
		t.Fatalf("first hop returned %v, want nil", err)
	}
	if err := pkt.DecrementTTL(); err != ErrExpired {
		t.Fatalf("second hop returned %v, want ErrExpired", err)
	}
}

func TestAppendPathBuildsTheTraversedRoute(t *testing.T) {
	pkt := New(ProtoLSR, TypeMessage, "A", "D", "hola")
	pkt.AppendPath("A")
	pkt.AppendPath("B")
	pkt.AppendPath("C")

	if got := pkt.HeaderString(HeaderPath); got != "A>B>C" {
		t.Errorf("path = %q, want %q", got, "A>B>C")
	}
}

func TestEncodedPacketMatchesTheAgreedWireFormat(t *testing.T) {
	pkt := New(ProtoLSR, TypeMessage, "A", "D", "hola")

	data, err := pkt.Encode()
	if err != nil {
		t.Fatalf("Encode returned %v", err)
	}

	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("encoded packet is not valid JSON: %v", err)
	}
	for _, field := range []string{"proto", "type", "from", "to", "ttl", "headers", "payload"} {
		if _, ok := wire[field]; !ok {
			t.Errorf("encoded packet is missing the agreed field %q", field)
		}
	}
	if _, ok := wire["headers"].([]any); !ok {
		t.Errorf("headers = %T, want a JSON array as agreed between teams", wire["headers"])
	}
}

func TestLinkStatePacketSurvivesARoundTrip(t *testing.T) {
	original := LinkStatePacket{
		Origin:    "B",
		Seq:       7,
		Neighbors: map[string]float64{"A": 1.5, "C": 2},
	}

	payload, err := EncodeLSP(original)
	if err != nil {
		t.Fatalf("EncodeLSP returned %v", err)
	}
	decoded, err := DecodeLSP(payload)
	if err != nil {
		t.Fatalf("DecodeLSP returned %v", err)
	}

	if decoded.Origin != original.Origin || decoded.Seq != original.Seq {
		t.Errorf("decoded = %+v, want %+v", decoded, original)
	}
	if decoded.Neighbors["A"] != 1.5 || decoded.Neighbors["C"] != 2 {
		t.Errorf("neighbours = %v, want %v", decoded.Neighbors, original.Neighbors)
	}
}
