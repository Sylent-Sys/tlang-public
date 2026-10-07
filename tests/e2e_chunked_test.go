//go:build e2e

// E2E chunked request-body scenario (chunked-bodies design §10.3). Go's
// net/http client sends a known-length body as Content-Length, so these
// scenarios drive the socket directly via dialServer and write hand-framed
// RFC 7230 §4.1 chunked requests, asserting the handler received the fully
// assembled body (201 echo) and that malformed framing is rejected (400). It
// runs on every compiler leg via forEachLeg/withEchoServer; the clang-asan-ubsan
// leg is the important one — the real decoder runs against a real socket under
// the sanitizer. Behind //go:build e2e so it runs only in the tlang-dev
// container. No change to the locked e2e harness: dialServer, startServer,
// withEchoServer, and forEachLeg already exist.

package tests

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// writeChunked frames body as a sequence of chunks of at most chunkSize bytes
// each, followed by the terminating zero-chunk and (empty) trailer. It returns
// the chunk-encoded bytes (data only, not the request head).
func writeChunked(body string, chunkSize int) string {
	if chunkSize < 1 {
		chunkSize = 1
	}
	var b strings.Builder
	for i := 0; i < len(body); i += chunkSize {
		end := i + chunkSize
		if end > len(body) {
			end = len(body)
		}
		part := body[i:end]
		fmt.Fprintf(&b, "%x\r\n%s\r\n", len(part), part)
	}
	b.WriteString("0\r\n\r\n")
	return b.String()
}

// readRawResponse reads a full HTTP/1.1 response from a raw connection using
// net/http's response parser, returning the status code and the body.
func readRawResponse(t *testing.T, r *bufio.Reader, req *http.Request) (int, []byte) {
	t.Helper()
	resp, err := http.ReadResponse(r, req)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 0, 256)
	tmp := make([]byte, 256)
	for {
		n, rerr := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if rerr != nil {
			break
		}
	}
	return resp.StatusCode, buf
}

// TestE2E_Chunked_Echo_201 sends a chunked POST /echo with a JSON body over a
// raw socket and asserts the handler assembled the full body (201 echo of
// id/name), on every leg. Splitting the JSON across several small chunks
// exercises the multi-chunk assembly path over a real connection.
func TestE2E_Chunked_Echo_201(t *testing.T) {
	forEachLeg(t, func(t *testing.T, lg leg) {
		withEchoServer(t, lg, func(t *testing.T, d *httpDriver, h *serverHandle) {
			const wantID = int64(7777)
			const wantName = "chunk-me"
			jsonBody := fmt.Sprintf(`{"id":%d,"name":%q}`, wantID, wantName)

			conn, err := dialServer(d.dialAddr())
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(requestTimeout))

			req := "POST /echo HTTP/1.1\r\n" +
				"Host: " + d.dialAddr() + "\r\n" +
				"Authorization: Bearer test\r\n" +
				"Content-Type: application/json\r\n" +
				"Transfer-Encoding: chunked\r\n" +
				"Connection: close\r\n" +
				"\r\n" +
				writeChunked(jsonBody, 4) // small chunks -> multiple chunks

			if _, err := conn.Write([]byte(req)); err != nil {
				t.Fatalf("write chunked request: %v", err)
			}

			// Parse the response for the assertion only (method POST, no body).
			parseReq, _ := http.NewRequest(http.MethodPost, "http://"+d.dialAddr()+"/echo", nil)
			status, body := readRawResponse(t, bufio.NewReader(conn), parseReq)
			if status != 201 {
				t.Fatalf("status = %d, want 201 (body %q)", status, body)
			}
			var got struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("decode response %q: %v", body, err)
			}
			if got.ID != wantID || got.Name != wantName {
				t.Fatalf("response = {id:%d name:%q}, want {id:%d name:%q}", got.ID, got.Name, wantID, wantName)
			}
		})
	})
}

// TestE2E_Chunked_Malformed_400 sends a chunked POST with a malformed chunk
// (a bad hex chunk-size) over a raw socket and asserts the runtime rejects it
// with a 400 status line, confirming the error path over a real connection.
func TestE2E_Chunked_Malformed_400(t *testing.T) {
	forEachLeg(t, func(t *testing.T, lg leg) {
		withEchoServer(t, lg, func(t *testing.T, d *httpDriver, h *serverHandle) {
			conn, err := dialServer(d.dialAddr())
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(requestTimeout))

			req := "POST /echo HTTP/1.1\r\n" +
				"Host: " + d.dialAddr() + "\r\n" +
				"Authorization: Bearer test\r\n" +
				"Content-Type: application/json\r\n" +
				"Transfer-Encoding: chunked\r\n" +
				"Connection: close\r\n" +
				"\r\n" +
				"zz\r\n{}\r\n0\r\n\r\n" // "zz" is not a valid hex chunk-size

			if _, err := conn.Write([]byte(req)); err != nil {
				t.Fatalf("write malformed chunked request: %v", err)
			}

			parseReq, _ := http.NewRequest(http.MethodPost, "http://"+d.dialAddr()+"/echo", nil)
			status, body := readRawResponse(t, bufio.NewReader(conn), parseReq)
			if status != 400 {
				t.Fatalf("status = %d, want 400 (body %q)", status, body)
			}
		})
	})
}
