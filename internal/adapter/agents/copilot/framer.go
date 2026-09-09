// Package copilot implements the AgentAdapter for GitHub Copilot over
// its Language Server Protocol transport. The transport is JSON-RPC
// 2.0 over stdio (Content-Length framed headers) — the same protocol
// VS Code uses to talk to the Copilot extension.
//
// The adapter ships a hand-rolled LSP client (no third-party
// dependency) because the integration only needs initialize +
// initialized + a single request method to drive prompts. The
// request method used today is textDocument/inlineCompletion; if
// GitHub ships a richer chat API in the future the sendPrompt
// implementation can switch over without touching the rest of the
// adapter.
package copilot

import (
	"errors"
	"fmt"
	"io"
)

// framer reads Content-Length framed JSON-RPC messages from an LSP
// connection. The framing is documented in the LSP spec:
//
//	Content-Length: <N>\r\n
//	\r\n
//	<N bytes of JSON>
type framer struct {
	rd     io.Reader
	buf    []byte
	header []byte
}

// newFramer builds a framer that reads from rd.
func newFramer(rd io.Reader) *framer {
	return &framer{rd: rd, header: make([]byte, 0, 256)}
}

// ReadMessage returns the next complete JSON-RPC payload or an
// error. EOF on a clean stream returns io.EOF.
func (f *framer) ReadMessage() ([]byte, error) {
	contentLength := -1
	for contentLength < 0 {
		line, err := f.readLine()
		if err != nil {
			return nil, err
		}
		if len(line) == 0 {
			continue // blank line, keep scanning
		}
		var got int
		if n, err := fmt.Sscanf(string(line), "Content-Length: %d", &got); err == nil && n == 1 {
			contentLength = got
		}
		// Other headers (Content-Type, etc.) are ignored; Copilot
		// does not require them.
	}
	if contentLength < 0 {
		return nil, errors.New("lsp: missing Content-Length header")
	}
	// Skip the blank line that separates the header block from the
	// JSON body. readLine may have already pulled the leading CRLF
	// into f.buf; if not we drain it from the raw reader.
	if n := stripBlankLineCRLF(f.buf); n > 0 {
		f.buf = f.buf[n:]
	} else {
		var crlf [2]byte
		if _, err := io.ReadFull(f.rd, crlf[:]); err != nil {
			return nil, fmt.Errorf("lsp: read blank line: %w", err)
		}
	}
	// The JSON body that follows the header block is one contiguous
	// chunk and never contains newlines, but readLine may have
	// already pulled a few body bytes ahead. Copy whatever is
	// buffered first, then ReadFull the remainder from the raw
	// reader.
	payload := make([]byte, contentLength)
	copied := copy(payload, f.buf)
	f.buf = f.buf[copied:]
	if copied < contentLength {
		if _, err := io.ReadFull(f.rd, payload[copied:]); err != nil {
			return nil, err
		}
	}
	return payload, nil
}

// readLine returns the next \r\n-terminated line as a byte slice
// without the terminator.
func (f *framer) readLine() ([]byte, error) {
	for {
		idx := indexByte(f.buf, '\n')
		if idx >= 0 {
			line := f.buf[:idx]
			f.buf = f.buf[idx+1:]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			return line, nil
		}
		var chunk [512]byte
		n, err := f.rd.Read(chunk[:])
		if n > 0 {
			f.buf = append(f.buf, chunk[:n]...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(f.buf) > 0 {
				line := f.buf
				f.buf = nil
				return line, nil
			}
			return nil, err
		}
	}
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// stripBlankLineCRLF returns the number of leading bytes that form a
// CRLF blank line separator: 0, 1 (for bare \n), or 2 (for \r\n).
// Anything else returns 0 so the caller does not strip real body bytes.
func stripBlankLineCRLF(b []byte) int {
	if len(b) >= 2 && b[0] == '\r' && b[1] == '\n' {
		return 2
	}
	if len(b) >= 1 && b[0] == '\n' {
		return 1
	}
	return 0
}

// writer is the matching write side: it prepends the Content-Length
// header to every payload.
type writer struct {
	w io.Writer
}

func newWriter(w io.Writer) *writer { return &writer{w: w} }

func (w *writer) WriteMessage(payload []byte) error {
	header := []byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(payload)))
	if _, err := w.w.Write(header); err != nil {
		return err
	}
	if _, err := w.w.Write(payload); err != nil {
		return err
	}
	return nil
}
