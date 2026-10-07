package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"tlang/diag"
)

// session frames a list of JSON messages, runs the server over them, and
// returns the decoded response/notification stream.
type session struct {
	in  bytes.Buffer
	out bytes.Buffer
	log bytes.Buffer
}

func (s *session) send(method string, id any, params any) {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		msg["id"] = id
	}
	if params != nil {
		msg["params"] = params
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(&s.in, "Content-Length: %d\r\n\r\n%s", len(payload), payload)
}

// sendRaw appends an already-encoded body with framing (for malformed-JSON
// cases).
func (s *session) sendRaw(body string) {
	fmt.Fprintf(&s.in, "Content-Length: %d\r\n\r\n%s", len(body), body)
}

func (s *session) run() int {
	return run(&s.in, &s.out, &s.log)
}

// messages decodes every framed message the server wrote.
func (s *session) messages(t *testing.T) []map[string]json.RawMessage {
	t.Helper()
	var out []map[string]json.RawMessage
	r := bufio.NewReader(&s.out)
	for {
		body, err := readMessage(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decoding server output: %v", err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("unmarshal server message: %v", err)
		}
		out = append(out, m)
	}
	return out
}

func initialize(s *session) {
	s.send("initialize", 1, InitializeParams{})
	s.send("initialized", nil, map[string]any{})
}

func TestServerInitializeCapabilities(t *testing.T) {
	var s session
	initialize(&s)
	s.send("exit", nil, nil)
	s.run()

	msgs := s.messages(t)
	if len(msgs) == 0 {
		t.Fatal("no response to initialize")
	}
	var res InitializeResult
	if err := json.Unmarshal(msgs[0]["result"], &res); err != nil {
		t.Fatalf("initialize result: %v", err)
	}
	caps := res.Capabilities
	if caps.TextDocumentSync != textDocumentSyncFull {
		t.Errorf("textDocumentSync = %d, want %d", caps.TextDocumentSync, textDocumentSyncFull)
	}
	if caps.CompletionProvider == nil {
		t.Error("completionProvider not advertised")
	}
	if !caps.HoverProvider {
		t.Error("hoverProvider not advertised")
	}
}

func TestServerInitializeServerInfo(t *testing.T) {
	saved := version
	version = "9.8.7-test"
	defer func() { version = saved }()

	var s session
	initialize(&s)
	s.send("exit", nil, nil)
	s.run()

	msgs := s.messages(t)
	if len(msgs) == 0 {
		t.Fatal("no response to initialize")
	}
	var res InitializeResult
	if err := json.Unmarshal(msgs[0]["result"], &res); err != nil {
		t.Fatalf("initialize result: %v", err)
	}
	if res.ServerInfo == nil {
		t.Fatal("serverInfo missing from initialize result")
	}
	if res.ServerInfo.Name != "tlang-lsp" || res.ServerInfo.Version != "9.8.7-test" {
		t.Errorf("serverInfo = %+v, want {tlang-lsp 9.8.7-test}", *res.ServerInfo)
	}
}

// firstDiagnostics returns the diagnostics of the first publishDiagnostics
// notification for uri.
func firstDiagnostics(t *testing.T, msgs []map[string]json.RawMessage, uri string) ([]Diagnostic, bool) {
	t.Helper()
	for _, m := range msgs {
		var method string
		if err := json.Unmarshal(m["method"], &method); err != nil || method != "textDocument/publishDiagnostics" {
			continue
		}
		var p PublishDiagnosticsParams
		if err := json.Unmarshal(m["params"], &p); err != nil {
			t.Fatalf("publishDiagnostics params: %v", err)
		}
		if p.URI == uri {
			return p.Diagnostics, true
		}
	}
	return nil, false
}

func TestServerDidOpenSyntaxError(t *testing.T) {
	var s session
	const uri = "file:///app.tlang"
	initialize(&s)
	s.send("textDocument/didOpen", nil, DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, Text: "let x = ;"},
	})
	s.send("exit", nil, nil)
	s.run()

	diags, ok := firstDiagnostics(t, s.messages(t), uri)
	if !ok || len(diags) == 0 {
		t.Fatal("no diagnostics published for syntax error")
	}
	found := false
	for _, d := range diags {
		if d.Code == "E-PARSE" {
			found = true
			if d.Range.Start.Line != 0 {
				t.Errorf("diagnostic start line = %d, want 0", d.Range.Start.Line)
			}
		}
	}
	if !found {
		t.Fatalf("no E-PARSE diagnostic, got %+v", diags)
	}
}

func TestServerDidOpenTypeError(t *testing.T) {
	var s session
	const uri = "file:///type.tlang"
	initialize(&s)
	s.send("textDocument/didOpen", nil, DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, Text: "fn main(): void { let x = missing; }"},
	})
	s.send("exit", nil, nil)
	s.run()

	diags, ok := firstDiagnostics(t, s.messages(t), uri)
	if !ok || len(diags) == 0 {
		t.Fatal("no diagnostics published for type error")
	}
	found := false
	for _, d := range diags {
		if d.Code == "E-NAME" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no checker-coded (E-NAME) diagnostic, got %+v", diags)
	}
}

func TestServerDidChangeAndClose(t *testing.T) {
	var s session
	const uri = "file:///c.tlang"
	initialize(&s)
	s.send("textDocument/didOpen", nil, DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, Text: "fn main(): void {}"},
	})
	// Introduce an error via didChange.
	s.send("textDocument/didChange", nil, DidChangeTextDocumentParams{
		TextDocument:   VersionedTextDocumentIdentifier{URI: uri, Version: 2},
		ContentChanges: []TextDocumentContentChangeEvent{{Text: "fn main(): void { let x = missing; }"}},
	})
	s.send("textDocument/didClose", nil, DidCloseTextDocumentParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
	})
	s.send("exit", nil, nil)
	s.run()

	msgs := s.messages(t)
	// Collect every publishDiagnostics for uri in order.
	var published [][]Diagnostic
	for _, m := range msgs {
		var method string
		if err := json.Unmarshal(m["method"], &method); err != nil || method != "textDocument/publishDiagnostics" {
			continue
		}
		var p PublishDiagnosticsParams
		if err := json.Unmarshal(m["params"], &p); err != nil {
			t.Fatalf("params: %v", err)
		}
		if p.URI == uri {
			published = append(published, p.Diagnostics)
		}
	}
	if len(published) < 3 {
		t.Fatalf("want 3 publishes (open, change, close), got %d", len(published))
	}
	// The change publish must carry an error from the new text.
	if len(published[1]) == 0 {
		t.Fatal("didChange did not re-publish diagnostics from new text")
	}
	// The close publish must be an empty array.
	last := published[len(published)-1]
	if len(last) != 0 {
		t.Fatalf("didClose published %d diagnostics, want empty array", len(last))
	}
}

// TestServerDiagnosticNeverNegative drives a real erroring document through
// the server and asserts no published diagnostic has a negative coordinate
// (AC6: a position is never dropped or negative). The exact (0,0) mapping for
// an invalid token.Position is covered directly in convert_test.go, since the
// front end always attaches a real position to these diagnostics.
func TestServerDiagnosticNeverNegative(t *testing.T) {
	var s session
	const uri = "file:///neg.tlang"
	initialize(&s)
	s.send("textDocument/didOpen", nil, DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, Text: "fn main(): void { let x = missing; }"},
	})
	s.send("exit", nil, nil)
	s.run()

	diags, ok := firstDiagnostics(t, s.messages(t), uri)
	if !ok || len(diags) == 0 {
		t.Fatal("no diagnostics published")
	}
	for _, d := range diags {
		r := d.Range
		if r.Start.Line < 0 || r.Start.Character < 0 || r.End.Line < 0 || r.End.Character < 0 {
			t.Fatalf("diagnostic has a negative coordinate: %+v", r)
		}
	}
}

// TestPublishInvalidPositionAtZero proves the server's publish path maps an
// invalid token.Position to the whole-file (0,0)-(0,0) range (AC6). It builds
// the LSP diagnostic the same way publishDiagnostics does.
func TestPublishInvalidPositionAtZero(t *testing.T) {
	src := []byte("fn main(): void {}")
	d := convertDiagnostic(src, diagWholeFile())
	want := Range{}
	if d.Range != want {
		t.Fatalf("invalid-position diagnostic range = %+v, want (0,0)-(0,0)", d.Range)
	}
}

func TestServerHandlerPanicRecovers(t *testing.T) {
	panicHook = func(method string) {
		if method == "textDocument/hover" {
			panic("boom")
		}
	}
	defer func() { panicHook = nil }()

	var s session
	const uri = "file:///p.tlang"
	initialize(&s)
	s.send("textDocument/didOpen", nil, DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, Text: "fn main(): void {}"},
	})
	s.send("textDocument/hover", 42, HoverParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Position:     Position{Line: 0, Character: 0},
		},
	})
	// A follow-up request must still be answered, proving the loop continued.
	s.send("shutdown", 43, nil)
	s.send("exit", nil, nil)
	code := s.run()
	if code != 0 {
		t.Errorf("exit code = %d, want 0 after shutdown", code)
	}

	msgs := s.messages(t)
	gotInternalError := false
	gotShutdownReply := false
	for _, m := range msgs {
		id := idOf(m)
		if id == "42" {
			var e struct {
				Error *responseError `json:"error"`
			}
			json.Unmarshal(mustMarshal(m), &e)
			if e.Error != nil && e.Error.Code == codeInternalError {
				gotInternalError = true
			}
		}
		if id == "43" {
			gotShutdownReply = true
		}
	}
	if !gotInternalError {
		t.Error("panicking request did not produce an InternalError response")
	}
	if !gotShutdownReply {
		t.Error("loop did not continue after panic (no shutdown reply)")
	}
}

func TestServerMalformedFramingDoesNotCrash(t *testing.T) {
	var s session
	// A malformed header line (no Content-Length): the loop logs and resyncs.
	s.in.WriteString("Garbage-Header: x\r\n\r\n")
	// Valid framing but unparseable JSON body.
	s.sendRaw("this is not json")
	// Then a normal exchange that must still be answered.
	initialize(&s)
	s.send("exit", nil, nil)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("run panicked on malformed input: %v", r)
		}
	}()
	s.run()
	// The initialize that followed must still have been answered.
	msgs := s.messages(t)
	found := false
	for _, m := range msgs {
		if _, ok := m["result"]; ok {
			found = true
		}
	}
	if !found {
		t.Error("server produced no result after malformed message")
	}
}

func TestServerCompletionAndHover(t *testing.T) {
	var s session
	const uri = "file:///q.tlang"
	src := "fn greet(name: string): void {}\nlet counter = 1;\n"
	initialize(&s)
	s.send("textDocument/didOpen", nil, DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, Text: src},
	})
	// Completion at end of file (top level).
	line := strings.Count(src, "\n")
	s.send("textDocument/completion", 10, CompletionParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Position:     Position{Line: line, Character: 0},
		},
	})
	// Hover over "counter" (line 1, char 4).
	s.send("textDocument/hover", 11, HoverParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Position:     Position{Line: 1, Character: 4},
		},
	})
	// Hover over whitespace -> null result.
	s.send("textDocument/hover", 12, HoverParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Position:     Position{Line: 0, Character: 2},
		},
	})
	s.send("exit", nil, nil)
	s.run()

	msgs := s.messages(t)
	for _, m := range msgs {
		switch idOf(m) {
		case "10":
			var list CompletionList
			if err := json.Unmarshal(m["result"], &list); err != nil {
				t.Fatalf("completion result: %v", err)
			}
			if len(list.Items) == 0 {
				t.Error("completion returned no items")
			}
			if !hasLabel(list.Items, "let") {
				t.Error("completion missing keyword 'let'")
			}
			if !hasLabel(list.Items, "counter") && !hasLabel(list.Items, "greet") {
				t.Error("completion missing in-scope identifier")
			}
		case "11":
			var h Hover
			if err := json.Unmarshal(m["result"], &h); err != nil {
				t.Fatalf("hover result: %v", err)
			}
			if h.Contents.Value == "" {
				t.Error("hover over typed identifier returned empty content")
			}
		case "12":
			var raw any
			if err := json.Unmarshal(m["result"], &raw); err != nil {
				t.Fatalf("hover-null result: %v", err)
			}
			if raw != nil {
				t.Errorf("hover over whitespace = %v, want null", raw)
			}
		}
	}
}

// idOf returns the stringified id of a response, or "" for a notification.
func idOf(m map[string]json.RawMessage) string {
	raw, ok := m["id"]
	if !ok {
		return ""
	}
	return strings.Trim(string(raw), `"`)
}

func hasLabel(items []CompletionItem, label string) bool {
	for _, it := range items {
		if it.Label == label {
			return true
		}
	}
	return false
}

// diagWholeFile is a diagnostic with no position (the zero token.Position),
// as the front end produces for a whole-file message.
func diagWholeFile() diag.Diagnostic {
	return diag.Diagnostic{Severity: diag.Error, Code: "E-ENTRY", Message: "no entry point"}
}

func mustMarshal(m map[string]json.RawMessage) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}
