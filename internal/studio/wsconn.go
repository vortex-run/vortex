package studio

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// wsConn is a minimal RFC 6455 server-side WebSocket connection supporting text
// (and binary) data frames. It is intentionally small — just enough to carry
// terminal I/O — and handles client-masked frames as the spec requires.
type wsConn struct {
	conn net.Conn
	rw   *bufio.ReadWriter
}

// WebSocket opcodes.
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// SetReadDeadline bounds the next ReadText.
func (c *wsConn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

// Close closes the underlying connection.
func (c *wsConn) Close() error { return c.conn.Close() }

// WriteText sends data as a single unfragmented text frame (server→client
// frames are never masked).
func (c *wsConn) WriteText(data []byte) error { return c.writeFrame(opText, data) }

// writeFrame writes one frame with the given opcode.
func (c *wsConn) writeFrame(opcode byte, data []byte) error {
	var header []byte
	b0 := byte(0x80) | opcode // FIN + opcode
	header = append(header, b0)

	n := len(data)
	switch {
	case n < 126:
		header = append(header, byte(n))
	case n < 65536:
		header = append(header, 126)
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], uint16(n))
		header = append(header, ext[:]...)
	default:
		header = append(header, 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		header = append(header, ext[:]...)
	}
	if _, err := c.rw.Write(header); err != nil {
		return err
	}
	if _, err := c.rw.Write(data); err != nil {
		return err
	}
	return c.rw.Flush()
}

// ReadText reads the next data frame's payload, transparently responding to
// ping and close control frames. It returns io.EOF on a close frame.
func (c *wsConn) ReadText() ([]byte, error) {
	for {
		opcode, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case opText, opBinary, opContinuation:
			return payload, nil
		case opPing:
			_ = c.writeFrame(opPong, payload)
		case opClose:
			return nil, io.EOF
		default:
			// ignore unknown control frames
		}
	}
}

// readFrame reads a single (client-masked) frame.
func (c *wsConn) readFrame() (opcode byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(c.rw, h[:]); err != nil {
		return 0, nil, err
	}
	opcode = h[0] & 0x0f
	masked := h[1]&0x80 != 0
	length := uint64(h[1] & 0x7f)

	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.rw, ext[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.rw, ext[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}

	// Bound the frame size to avoid unbounded allocation from a hostile client.
	const maxFrame = 1 << 20
	if length > maxFrame {
		return 0, nil, fmt.Errorf("studio: websocket frame too large: %d", length)
	}

	var maskKey [4]byte
	if masked {
		if _, err = io.ReadFull(c.rw, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.rw, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return opcode, payload, nil
}
