package messaging

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// encodeEventFrame builds one AWS event-stream frame with string headers, the
// mirror of readEventStream's decoder.
func encodeEventFrame(headers map[string]string, payload []byte) []byte {
	var hb []byte
	for name, val := range headers {
		hb = append(hb, byte(len(name)))
		hb = append(hb, name...)
		hb = append(hb, 7) // string value type
		hb = binary.BigEndian.AppendUint16(hb, uint16(len(val)))
		hb = append(hb, val...)
	}
	total := uint32(12 + len(hb) + len(payload) + 4)
	frame := binary.BigEndian.AppendUint32(nil, total)
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(hb)))
	frame = binary.BigEndian.AppendUint32(frame, crc32.ChecksumIEEE(frame[:8]))
	frame = append(frame, hb...)
	frame = append(frame, payload...)
	frame = binary.BigEndian.AppendUint32(frame, crc32.ChecksumIEEE(frame))
	return frame
}

// chunkFrame wraps an anthropic streaming event as a Bedrock chunk frame.
func chunkFrame(event string) []byte {
	payload, _ := json.Marshal(map[string]string{
		"bytes": base64.StdEncoding.EncodeToString([]byte(event)),
	})
	return encodeEventFrame(map[string]string{
		":message-type": "event",
		":event-type":   "chunk",
	}, payload)
}

func TestStreamBedrock_DecodesEventStream(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(chunkFrame(`{"type":"message_start","message":{"usage":{"input_tokens":4}}}`))
		_, _ = w.Write(chunkFrame(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hel"}}`))
		_, _ = w.Write(chunkFrame(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}}`))
		_, _ = w.Write(chunkFrame(`{"type":"message_stop","amazon-bedrock-invocationMetrics":{"inputTokenCount":4,"outputTokenCount":6}}`))
	}))
	defer srv.Close()

	g, err := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{{
			Name: ProviderBedrock, APIKey: "AKIA:secret", Endpoint: "us-east-1",
			Models: []string{"anthropic.claude-3-5-sonnet-20240620-v1:0"},
		}},
		Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	g.streamClient = &http.Client{Transport: rewriteTransport{to: srv.URL, base: srv.Client().Transport}}

	var deltas []string
	text, tokens, serr := g.CompleteStreamForModel(context.Background(),
		"anthropic.claude-3-5-sonnet-20240620-v1:0", "hi", "sys",
		func(d string) { deltas = append(deltas, d) })
	if serr != nil {
		t.Fatal(serr)
	}
	if text != "Hello" {
		t.Errorf("text = %q, want Hello", text)
	}
	if tokens != 10 {
		t.Errorf("tokens = %d, want 10 (4 in + 6 out from invocationMetrics)", tokens)
	}
	if len(deltas) != 2 || deltas[0] != "Hel" || deltas[1] != "lo" {
		t.Errorf("deltas = %q", deltas)
	}
	if !strings.HasSuffix(gotPath, "/invoke-with-response-stream") {
		t.Errorf("path = %q, want invoke-with-response-stream", gotPath)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 ") {
		t.Errorf("Authorization = %q, want SigV4", gotAuth)
	}
}

func TestStreamBedrock_ExceptionFrameErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(encodeEventFrame(map[string]string{
			":message-type":   "exception",
			":exception-type": "throttlingException",
		}, []byte(`{"message":"slow down"}`)))
	}))
	defer srv.Close()

	g, err := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{{
			Name: ProviderBedrock, APIKey: "AKIA:secret", Endpoint: "us-east-1",
			Models: []string{"m"},
		}},
		Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	g.streamClient = &http.Client{Transport: rewriteTransport{to: srv.URL, base: srv.Client().Transport}}

	_, _, serr := g.CompleteStreamForModel(context.Background(), "m", "hi", "", nil)
	if serr == nil || !strings.Contains(serr.Error(), "throttlingException") {
		t.Errorf("err = %v, want throttlingException surfaced", serr)
	}
}

func TestReadEventStream_CorruptCRCRejected(t *testing.T) {
	frame := chunkFrame(`{"type":"message_stop"}`)
	frame[len(frame)-1] ^= 0xFF // corrupt the message CRC
	err := readEventStream(strings.NewReader(string(frame)), func(string, []byte) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "CRC") {
		t.Errorf("err = %v, want CRC mismatch", err)
	}
}

func TestReadEventStream_OversizedFrameRejected(t *testing.T) {
	var frame []byte
	frame = binary.BigEndian.AppendUint32(frame, 64<<20) // 64MB claimed length
	frame = binary.BigEndian.AppendUint32(frame, 0)
	frame = binary.BigEndian.AppendUint32(frame, crc32.ChecksumIEEE(frame[:8]))
	err := readEventStream(strings.NewReader(string(frame)), func(string, []byte) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "invalid frame") {
		t.Errorf("err = %v, want invalid frame lengths", err)
	}
}

func TestParseEventHeaders_SkipsNonStringTypes(t *testing.T) {
	// A frame mixing a bool, an int32, and the string headers we care about.
	var h []byte
	h = append(h, byte(len(":ok")))
	h = append(h, ":ok"...)
	h = append(h, 0) // bool true, no value bytes
	h = append(h, byte(len(":n")))
	h = append(h, ":n"...)
	h = append(h, 4) // int32
	h = binary.BigEndian.AppendUint32(h, 42)
	h = append(h, byte(len(":event-type")))
	h = append(h, ":event-type"...)
	h = append(h, 7)
	h = binary.BigEndian.AppendUint16(h, uint16(len("chunk")))
	h = append(h, "chunk"...)

	_, eventType, _ := parseEventHeaders(h)
	if eventType != "chunk" {
		t.Errorf("eventType = %q, want chunk", eventType)
	}
}
