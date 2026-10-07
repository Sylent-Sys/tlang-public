package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// errBadHeader marks a message whose header block is malformed: a missing,
// non-numeric, or negative Content-Length. It is recoverable — the run loop
// logs it and keeps reading rather than terminating.
var errBadHeader = errors.New("tlang-lsp: malformed message header")

// readMessage reads one Content-Length-framed message body from r. It reads
// header lines until a blank line, parses the (case-insensitive)
// Content-Length field, then reads exactly that many body bytes. Other header
// fields are read and ignored. A missing, non-numeric, or negative length
// returns errBadHeader. A clean io.EOF before any header byte signals
// shutdown; EOF in the middle of a header or body is reported as the
// underlying read error (io.ErrUnexpectedEOF for a truncated body).
func readMessage(r *bufio.Reader) ([]byte, error) {
	contentLength := -1
	sawHeader := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF && !sawHeader && line == "" {
				return nil, io.EOF
			}
			if err == io.EOF {
				return nil, errBadHeader
			}
			return nil, err
		}
		sawHeader = true
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			break // end of headers
		}
		name, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			return nil, errBadHeader
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n < 0 {
				return nil, errBadHeader
			}
			contentLength = n
		}
	}
	if contentLength < 0 {
		return nil, errBadHeader
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// writeMessage writes payload as one Content-Length-framed message. The length
// is the byte length of payload, not its rune count.
func writeMessage(w io.Writer, payload []byte) error {
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}
