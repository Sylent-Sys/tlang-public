package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestWriteReadRoundTrip(t *testing.T) {
	payload := []byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`)
	var buf bytes.Buffer
	if err := writeMessage(&buf, payload); err != nil {
		t.Fatalf("writeMessage: %v", err)
	}

	wire := buf.String()
	wantHeader := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(payload))
	if !strings.HasPrefix(wire, wantHeader) {
		t.Fatalf("header = %q, want prefix %q", wire, wantHeader)
	}

	got, err := readMessage(bufio.NewReader(&buf))
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("round-trip body = %q, want %q", got, payload)
	}
}

func TestReadMessageCaseInsensitiveAndExtraHeaders(t *testing.T) {
	payload := []byte(`{"x":1}`)
	raw := fmt.Sprintf("content-length: %d\r\nContent-Type: application/vscode-jsonrpc; charset=utf-8\r\n\r\n%s", len(payload), payload)
	got, err := readMessage(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("body = %q, want %q", got, payload)
	}
}

func TestReadMessageEOFBeforeHeader(t *testing.T) {
	_, err := readMessage(bufio.NewReader(strings.NewReader("")))
	if err != io.EOF {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestReadMessageErrors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want error
	}{
		{"garbage header", "not-a-header-line\r\n\r\n", errBadHeader},
		{"missing length", "Content-Type: x\r\n\r\n", errBadHeader},
		{"non-numeric length", "Content-Length: abc\r\n\r\n", errBadHeader},
		{"negative length", "Content-Length: -5\r\n\r\n", errBadHeader},
		{"truncated body", "Content-Length: 10\r\n\r\n{}", io.ErrUnexpectedEOF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("readMessage panicked: %v", r)
				}
			}()
			_, err := readMessage(bufio.NewReader(strings.NewReader(tt.raw)))
			if err != tt.want {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}
