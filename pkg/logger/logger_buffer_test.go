package logger

import (
	"io"
	"sync"
	"testing"
)

// captureSink implements BufferSink for testing the tee.
type captureSink struct {
	mu      sync.Mutex
	records []string
}

func (c *captureSink) Record(_, level, msg string, _ map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, level+":"+msg)
}

func TestBufferSink_TeesRecords(t *testing.T) {
	sink := &captureSink{}
	log := New(Config{Output: io.Discard, Format: FormatText, Buffer: sink})
	log.Info("hello", "k", "v")
	log.Warn("careful")

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.records) != 2 {
		t.Fatalf("sink got %d records, want 2", len(sink.records))
	}
	if sink.records[0] != "INFO:hello" || sink.records[1] != "WARN:careful" {
		t.Errorf("records = %v", sink.records)
	}
}
