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

func TestDecodeGivesTheSameFallbackIDToTwoCopiesOfTheSamePacket(t *testing.T) {
	// Two nodes on different paths receive the same header-less packet; they
	// must derive the same id, or duplicate suppression never triggers.
	raw := []byte(`{"proto":"flooding","type":"message","from":"A","to":"D","ttl":9,"payload":"hola"}`)

	first, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode returned %v", err)
	}
	second, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode returned %v", err)
	}
	if first.HeaderString(HeaderMsgID) != second.HeaderString(HeaderMsgID) {
		t.Errorf("fallback ids differ: %q vs %q", first.HeaderString(HeaderMsgID), second.HeaderString(HeaderMsgID))
	}
}

func TestDecodeNeverUsesVersionOrChecksumToReject(t *testing.T) {
	raw := []byte(`{"version":99,"proto":"lsr","type":"message","from":"A","to":"D","ttl":9,` +
		`"headers":[{"checksum":"deadbeef"}],"payload":"hola"}`)

	pkt, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode returned %v, want no error even for a wrong version or checksum", err)
	}
	if pkt.ChecksumValid() {
		t.Error("ChecksumValid = true for a deliberately wrong checksum")
	}
}

func TestDecodeRejectsMalformedJSON(t *testing.T) {
	if _, err := Decode([]byte(`{"proto":`)); err == nil {
		t.Fatal("expected an error for truncated JSON")
	}
}

func TestSetHeaderReplacesInsteadOfAppending(t *testing.T) {
	pkt := New(ProtoLSR, TypeInfo, "A", "*")
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
	original := NewText(ProtoFlooding, TypeMessage, "A", "D", "hola")
	original.SetHeader(HeaderVia, "A")

	copyA := original.Clone()
	copyA.SetHeader(HeaderVia, "B")

	if got := original.HeaderString(HeaderVia); got != "A" {
		t.Errorf("original via = %q, want %q; the clone shared its headers", got, "A")
	}
}

func TestDecrementTTLStopsAtZero(t *testing.T) {
	pkt := NewText(ProtoFlooding, TypeMessage, "A", "D", "hola")
	pkt.TTL = 2

	if err := pkt.DecrementTTL(); err != nil {
		t.Fatalf("first hop returned %v, want nil", err)
	}
	if err := pkt.DecrementTTL(); err != ErrExpired {
		t.Fatalf("second hop returned %v, want ErrExpired", err)
	}
}

func TestAppendTraceBuildsTheTraversedRoute(t *testing.T) {
	pkt := NewText(ProtoLSR, TypeMessage, "A", "D", "hola")
	pkt.AppendTrace("A")
	pkt.AppendTrace("B")
	pkt.AppendTrace("C")

	got := pkt.Trace()
	want := []string{"A", "B", "C"}
	if len(got) != len(want) {
		t.Fatalf("trace = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("trace = %v, want %v", got, want)
		}
	}
}

func TestEncodedPacketMatchesTheAgreedWireFormat(t *testing.T) {
	pkt := NewText(ProtoLSR, TypeMessage, "A", "D", "hola")

	data, err := pkt.Encode()
	if err != nil {
		t.Fatalf("Encode returned %v", err)
	}

	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("encoded packet is not valid JSON: %v", err)
	}
	for _, field := range []string{"version", "proto", "type", "from", "to", "ttl", "headers", "payload"} {
		if _, ok := wire[field]; !ok {
			t.Errorf("encoded packet is missing the agreed field %q", field)
		}
	}
	if _, ok := wire["headers"].([]any); !ok {
		t.Errorf("headers = %T, want a JSON array as agreed between teams", wire["headers"])
	}
	if _, ok := wire["payload"].(string); !ok {
		t.Errorf("payload = %T, want a JSON string for a message packet", wire["payload"])
	}
}

func TestObjectPayloadIsEncodedAsAnObjectNotAString(t *testing.T) {
	pkt, err := NewObject(ProtoLSR, TypeHello, "A", "B", HelloPayload{ListenPort: 5000})
	if err != nil {
		t.Fatalf("NewObject returned %v", err)
	}

	data, err := pkt.Encode()
	if err != nil {
		t.Fatalf("Encode returned %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("encoded packet is not valid JSON: %v", err)
	}
	if _, ok := wire["payload"].(map[string]any); !ok {
		t.Errorf("payload = %T, want a JSON object for a hello packet", wire["payload"])
	}
}

func TestChecksumMatchesThePublishedTestVectors(t *testing.T) {
	text := NewText(ProtoLSR, TypeMessage, "10.0.0.1:5000", "10.0.0.7:5000", "hola G")
	if got := text.HeaderString(HeaderChecksum); got != "0bded535" {
		t.Errorf("checksum of a text payload = %q, want %q", got, "0bded535")
	}

	// Exactly the fields in the published vector, so the canonical
	// serialisation (sorted keys, compact, non-HTML-escaped) can be checked
	// independently of our own struct's extra age_s field.
	obj := &Packet{
		Payload: json.RawMessage(`{"origin":"10.0.0.1:5000","seq":7,"neighbors":[{"id":"10.0.0.2:5000","weight":4.8}]}`),
	}
	if got := checksumHex(obj.canonicalPayloadBytes()); got != "cbd08356" {
		t.Errorf("checksum of an object payload = %q, want %q", got, "cbd08356")
	}
}

func TestLinkStatePacketSurvivesARoundTrip(t *testing.T) {
	original := LinkStatePacket{
		Origin: "B",
		Seq:    7,
		Neighbors: []NeighborEntry{
			{ID: "A", Weight: 1.5},
			{ID: "C", Weight: 2},
		},
	}

	pkt, err := NewObject(ProtoLSR, TypeInfo, "B", "*", original)
	if err != nil {
		t.Fatalf("NewObject returned %v", err)
	}
	decoded, err := DecodeLSPFlexible(pkt.Payload)
	if err != nil {
		t.Fatalf("DecodeLSPFlexible returned %v", err)
	}

	if decoded.Origin != original.Origin || decoded.Seq != original.Seq {
		t.Errorf("decoded = %+v, want %+v", decoded, original)
	}
	byID := map[string]float64{}
	for _, n := range decoded.Neighbors {
		byID[n.ID] = n.Weight
	}
	if byID["A"] != 1.5 || byID["C"] != 2 {
		t.Errorf("neighbours = %v, want A=1.5 C=2", byID)
	}
}

func TestDecodeLSPFlexibleAcceptsTheAlternateNeighborShapes(t *testing.T) {
	cases := map[string]json.RawMessage{
		"dict":            json.RawMessage(`{"origin":"A","seq":1,"neighbors":{"B":2.5}}`),
		"links key":       json.RawMessage(`{"origin":"A","seq":1,"links":[{"id":"B","weight":2.5}]}`),
		"node/cost names": json.RawMessage(`{"origin":"A","seq":1,"neighbors":[{"node":"B","cost":2.5}]}`),
		"string payload":  json.RawMessage(`"{\"origin\":\"A\",\"seq\":1,\"neighbors\":[{\"id\":\"B\",\"weight\":2.5}]}"`),
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			lsp, err := DecodeLSPFlexible(raw)
			if err != nil {
				t.Fatalf("DecodeLSPFlexible returned %v", err)
			}
			if lsp.Origin != "A" || lsp.Seq != 1 {
				t.Fatalf("decoded = %+v, want origin=A seq=1", lsp)
			}
			if len(lsp.Neighbors) != 1 || lsp.Neighbors[0].ID != "B" || lsp.Neighbors[0].Weight != 2.5 {
				t.Errorf("neighbours = %v, want [{B 2.5}]", lsp.Neighbors)
			}
		})
	}
}
