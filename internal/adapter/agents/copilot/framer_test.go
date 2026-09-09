package copilot

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
)

// TestFramerReadSingleMessage covers the happy path: one framed LSP
// message with Content-Length, the mandatory blank line and a small
// JSON payload. This is what every real Copilot request looks like.
func TestFramerReadSingleMessage(t *testing.T) {
	payload := `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	in := io.MultiReader(
		strings.NewReader("Content-Length: "+strconv.Itoa(len(payload))+"\r\n\r\n"),
		bytes.NewReader([]byte(payload)),
	)
	f := newFramer(in)
	got, err := f.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("payload = %q, want %q", got, payload)
	}
}

// TestFramerSkipsUnknownHeaders mirrors the LSP rule that headers other
// than Content-Length (e.g. Content-Type) are optional and must be
// ignored. The framer only stops scanning once Content-Length is
// found AND the blank line follows.
func TestFramerSkipsUnknownHeaders(t *testing.T) {
	payload := `{"jsonrpc":"2.0","id":1}`
	wire := "Content-Type: application/vscode-jsonrpc; charset=utf-8\r\n" +
		"Content-Length: " + strconv.Itoa(len(payload)) + "\r\n\r\n" +
		payload
	f := newFramer(strings.NewReader(wire))
	got, err := f.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("payload = %q, want %q", got, payload)
	}
}

// TestFramerBackToBackMessages proves the framer correctly resets
// between consecutive messages. The second read must return the second
// payload even though no extra blank lines separate them.
func TestFramerBackToBackMessages(t *testing.T) {
	first := `{"id":1,"method":"a"}`
	second := `{"id":2,"method":"b"}`
	wire := "Content-Length: " + strconv.Itoa(len(first)) + "\r\n\r\n" + first +
		"Content-Length: " + strconv.Itoa(len(second)) + "\r\n\r\n" + second
	f := newFramer(strings.NewReader(wire))
	got1, err := f.ReadMessage()
	if err != nil {
		t.Fatalf("first ReadMessage: %v", err)
	}
	if string(got1) != first {
		t.Fatalf("first payload = %q, want %q", got1, first)
	}
	got2, err := f.ReadMessage()
	if err != nil {
		t.Fatalf("second ReadMessage: %v", err)
	}
	if string(got2) != second {
		t.Fatalf("second payload = %q, want %q", got2, second)
	}
}

// TestFramerHandlesChunkedInput feeds the header and body as two
// separate Read calls to exercise the buffered-path branches in
// readLine and ReadMessage.
func TestFramerHandlesChunkedInput(t *testing.T) {
	payload := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{}}}`
	header := "Content-Length: " + strconv.Itoa(len(payload)) + "\r\n\r\n"
	// Split the wire so the header arrives alone, then the payload
	// arrives one byte at a time. The framer must reconstruct the
	// body regardless of how it is split.
	cr := &chunkedReader{chunks: [][]byte{[]byte(header), []byte(payload)}, perChunk: 1}
	f := newFramer(cr)
	got, err := f.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("payload = %q, want %q", got, payload)
	}
}

// TestFramerRejectsMissingContentLength feeds a header block with no
// Content-Length and lets the stream end mid-header. The framer must
// surface EOF so callers can distinguish a truncated wire from a
// successful read.
func TestFramerRejectsMissingContentLength(t *testing.T) {
	wire := "Content-Type: application/json\r\n" // no Content-Length, stream ends
	f := newFramer(strings.NewReader(wire))
	_, err := f.ReadMessage()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF for truncated header block", err)
	}
}

// TestFramerEOFAfterPartialHeader exercises the readLine EOF branch:
// the stream ends mid-header. The returned error must be io.EOF so
// callers can detect a server that closed the connection cleanly.
func TestFramerEOFAfterPartialHeader(t *testing.T) {
	wire := "Content-Length: 100\r\n" // no blank line, no body
	f := newFramer(strings.NewReader(wire))
	_, err := f.ReadMessage()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

// TestFramerHandlesLargeBody ensures the framer does not bound the
// body to its internal buffer size. We send 64 KiB which is the
// chunk size we use in the readLine reader.
func TestFramerHandlesLargeBody(t *testing.T) {
	body := strings.Repeat("x", 64*1024)
	wire := "Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
	f := newFramer(strings.NewReader(wire))
	got, err := f.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if len(got) != len(body) {
		t.Fatalf("body length = %d, want %d", len(got), len(body))
	}
}

// TestWriterPrependsContentLengthHeader verifies the matching write
// side prepends the Content-Length framing. This is the contract the
// client relies on when sending requests to the LSP server.
func TestWriterPrependsContentLengthHeader(t *testing.T) {
	payload := []byte(`{"jsonrpc":"2.0","method":"initialized","params":{}}`)
	var buf bytes.Buffer
	w := newWriter(&buf)
	if err := w.WriteMessage(payload); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	wire := buf.String()
	expectedPrefix := "Content-Length: " + strconv.Itoa(len(payload)) + "\r\n\r\n"
	if !strings.HasPrefix(wire, expectedPrefix) {
		t.Fatalf("wire prefix = %q, want %q", wire[:len(expectedPrefix)], expectedPrefix)
	}
	if !bytes.Equal([]byte(wire[len(expectedPrefix):]), payload) {
		t.Fatal("body after header does not match payload")
	}
}

// chunkedReader returns the configured chunks one byte at a time so
// tests can exercise the buffered paths in framer.readLine /
// ReadMessage without depending on socket behaviour.
type chunkedReader struct {
	chunks   [][]byte
	perChunk int
	idx      int
	pos      int
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	if c.idx >= len(c.chunks) {
		return 0, io.EOF
	}
	chunk := c.chunks[c.idx]
	if c.pos >= len(chunk) {
		c.idx++
		c.pos = 0
		return c.Read(p)
	}
	step := c.perChunk
	if step <= 0 || step > len(p) {
		step = len(p)
	}
	remaining := len(chunk) - c.pos
	if step > remaining {
		step = remaining
	}
	copy(p, chunk[c.pos:c.pos+step])
	c.pos += step
	return step, nil
}
