package messaging

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"strings"
)

// This file adds native token streaming for AWS Bedrock (AGUI audit item C).
// Bedrock's invoke-with-response-stream endpoint does not speak SSE: it frames
// responses in AWS's binary event-stream encoding
// (application/vnd.amazon.eventstream). Each "chunk" event frame carries a
// JSON payload {"bytes": base64(<anthropic streaming event>)} — the decoded
// events are the same message_start / content_block_delta / message_delta
// sequence the Claude SSE stream produces, with Bedrock adding an
// amazon-bedrock-invocationMetrics object to the final event.

// streamBedrock invokes an Anthropic model on Bedrock with a streamed
// response, signing the request with SigV4 like the buffered callBedrock.
func (g *AIGateway) streamBedrock(ctx context.Context, p AIProvider, prompt, systemPrompt string, onDelta func(string)) (string, int, error) {
	region := p.Endpoint // Endpoint carries the region for bedrock
	if region == "" {
		region = "us-east-1"
	}
	accessKey, secretKey, ok := splitBedrockKey(p.APIKey)
	if !ok {
		return "", 0, fmt.Errorf("bedrock: APIKey must be \"<accessKey>:<secretKey>\"")
	}
	model := g.modelOf(p)
	if model == "" {
		model = "anthropic.claude-3-5-sonnet-20240620-v1:0"
	}
	host := fmt.Sprintf("bedrock-runtime.%s.amazonaws.com", region)
	path := "/model/" + model + "/invoke-with-response-stream"

	body, err := json.Marshal(map[string]any{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens":        g.maxTokensFor(ctx),
		"system":            systemPrompt,
		"messages":          []map[string]any{{"role": "user", "content": prompt}},
	})
	if err != nil {
		return "", 0, err
	}
	headers := map[string]string{"Content-Type": "application/json"}
	bedrockSignV4(headers, body, "POST", host, path, accessKey, secretKey, region, "bedrock", g.now())

	callCtx, cancel := context.WithTimeout(ctx, maxStreamDuration)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, "https://"+host+path, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := g.streamClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("bedrock: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", 0, fmt.Errorf("bedrock: status %d: %s", resp.StatusCode, data)
	}

	var (
		b      strings.Builder
		inTok  int
		outTok int
	)
	err = readEventStream(resp.Body, func(eventType string, payload []byte) error {
		if eventType != "chunk" {
			return nil
		}
		// encoding/json decodes the base64 "bytes" field into []byte.
		var wrap struct {
			Bytes []byte `json:"bytes"`
		}
		if json.Unmarshal(payload, &wrap) != nil || len(wrap.Bytes) == 0 {
			return nil
		}
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
			Message struct {
				Usage struct {
					InputTokens int `json:"input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
			Metrics struct {
				InputTokenCount  int `json:"inputTokenCount"`
				OutputTokenCount int `json:"outputTokenCount"`
			} `json:"amazon-bedrock-invocationMetrics"`
		}
		if json.Unmarshal(wrap.Bytes, &ev) != nil {
			return nil // tolerate unknown frames
		}
		switch ev.Type {
		case "message_start":
			inTok = ev.Message.Usage.InputTokens
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				b.WriteString(ev.Delta.Text)
				onDelta(ev.Delta.Text)
			}
		case "message_delta":
			outTok = ev.Usage.OutputTokens
		}
		// The final event carries authoritative Bedrock-side metrics.
		if ev.Metrics.InputTokenCount > 0 || ev.Metrics.OutputTokenCount > 0 {
			inTok, outTok = ev.Metrics.InputTokenCount, ev.Metrics.OutputTokenCount
		}
		return nil
	})
	if err != nil {
		return "", 0, fmt.Errorf("bedrock: %w", err)
	}
	return b.String(), inTok + outTok, nil
}

// maxEventFrameLen bounds one event-stream frame (16 MB, matching AWS's own
// limit) so a corrupt length prefix cannot trigger a huge allocation.
const maxEventFrameLen = 16 << 20

// readEventStream decodes AWS binary event-stream framing from r, invoking on
// for every event frame with its :event-type header value and payload.
// Exception and error frames terminate the stream with an error. Frame layout:
// 4-byte total length, 4-byte header length, 4-byte prelude CRC32, headers,
// payload, 4-byte message CRC32 (all big-endian, CRCs are IEEE).
func readEventStream(r io.Reader, on func(eventType string, payload []byte) error) error {
	br := bufio.NewReader(r)
	for {
		var prelude [12]byte
		if _, err := io.ReadFull(br, prelude[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("eventstream: read prelude: %w", err)
		}
		total := binary.BigEndian.Uint32(prelude[0:4])
		headerLen := binary.BigEndian.Uint32(prelude[4:8])
		preludeCRC := binary.BigEndian.Uint32(prelude[8:12])
		if crc32.ChecksumIEEE(prelude[0:8]) != preludeCRC {
			return fmt.Errorf("eventstream: prelude CRC mismatch")
		}
		if total < 16 || total > maxEventFrameLen || headerLen > total-16 {
			return fmt.Errorf("eventstream: invalid frame lengths (total=%d headers=%d)", total, headerLen)
		}
		rest := make([]byte, total-12)
		if _, err := io.ReadFull(br, rest); err != nil {
			return fmt.Errorf("eventstream: read frame: %w", err)
		}
		msgCRC := binary.BigEndian.Uint32(rest[len(rest)-4:])
		framed := make([]byte, 0, total-4)
		framed = append(framed, prelude[:]...)
		framed = append(framed, rest[:len(rest)-4]...)
		if crc32.ChecksumIEEE(framed) != msgCRC {
			return fmt.Errorf("eventstream: message CRC mismatch")
		}

		headers := rest[:headerLen]
		payload := rest[headerLen : len(rest)-4]
		msgType, eventType, excType := parseEventHeaders(headers)
		switch msgType {
		case "exception":
			return fmt.Errorf("eventstream: %s: %s", excType, payload)
		case "error":
			return fmt.Errorf("eventstream: error: %s", payload)
		}
		if err := on(eventType, payload); err != nil {
			return err
		}
	}
}

// parseEventHeaders extracts :message-type, :event-type, and :exception-type
// from an event-stream header block. Each header is a 1-byte name length, the
// name, a 1-byte value type, and a type-dependent value; Bedrock uses type 7
// (string) for the headers we care about, the rest are skipped by size.
func parseEventHeaders(h []byte) (msgType, eventType, excType string) {
	for len(h) >= 2 {
		nameLen := int(h[0])
		if len(h) < 1+nameLen+1 {
			return
		}
		name := string(h[1 : 1+nameLen])
		h = h[1+nameLen:]
		vtype := h[0]
		h = h[1:]
		var val string
		switch vtype {
		case 0, 1: // bool true/false: no value bytes
		case 2: // byte
			if len(h) < 1 {
				return
			}
			h = h[1:]
		case 3: // int16
			if len(h) < 2 {
				return
			}
			h = h[2:]
		case 4: // int32
			if len(h) < 4 {
				return
			}
			h = h[4:]
		case 5, 8: // int64, timestamp
			if len(h) < 8 {
				return
			}
			h = h[8:]
		case 6, 7: // byte array, string: 2-byte length prefix
			if len(h) < 2 {
				return
			}
			vlen := int(binary.BigEndian.Uint16(h[:2]))
			if len(h) < 2+vlen {
				return
			}
			if vtype == 7 {
				val = string(h[2 : 2+vlen])
			}
			h = h[2+vlen:]
		case 9: // uuid
			if len(h) < 16 {
				return
			}
			h = h[16:]
		default:
			return
		}
		switch name {
		case ":message-type":
			msgType = val
		case ":event-type":
			eventType = val
		case ":exception-type":
			excType = val
		}
	}
	return
}
