package main

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"runtime/debug"
)

// Server holds the state for one LSP session.
type Server struct {
	out   *bufio.Writer
	log   *log.Logger
	store *documentStore

	// roots are the workspace folders (absolute paths) from initialize; the
	// longest one containing a document bounds its import resolution.
	roots []string
	// cache holds the analysis of each open document. Any document event
	// can change any analysis (an edited file may be imported by the others),
	// so every event clears it and re-analyzes every open document.
	cache map[string]analysis
	// overlay serves the open documents' text to module resolution; rebuilt
	// with the cache.
	overlay *overlayFS

	initialized bool
	shutdown    bool
}

// run drives one LSP session over the injected streams: it reads
// Content-Length-framed messages from in, writes framed responses and
// notifications to out, and logs to logw. It returns the process exit code: 0
// after a clean shutdown-then-exit, 1 if exit arrives without shutdown. All
// real work lives here so tests can drive it with byte buffers.
func run(in io.Reader, out io.Writer, logw io.Writer) int {
	s := newServer(out, logw)
	reader := bufio.NewReader(in)
	for {
		body, err := readMessage(reader)
		if err != nil {
			switch err {
			case io.EOF:
				return s.exitCode()
			case errBadHeader:
				s.log.Printf("skipping malformed message header: %v", err)
				continue
			default:
				s.log.Printf("read error, ending session: %v", err)
				return s.exitCode()
			}
		}
		if code, done := s.handle(body); done {
			return code
		}
	}
}

// newServer returns a session writing framed messages to out and logs to
// logw. run drives it from a stream; tests may call handle directly.
func newServer(out io.Writer, logw io.Writer) *Server {
	return &Server{
		out:   bufio.NewWriter(out),
		log:   log.New(logw, "tlang-lsp: ", 0),
		store: newDocumentStore(),
		cache: map[string]analysis{},
	}
}

// exitCode reports the code to return when the stream ends without an explicit
// exit notification: a clean shutdown still yields 0.
func (s *Server) exitCode() int {
	if s.shutdown {
		return 0
	}
	return 1
}

// handle dispatches one raw message. It returns done=true (with an exit code)
// only for the exit notification. Each message runs under a recover so one bad
// message never crashes the process.
func (s *Server) handle(body []byte) (code int, done bool) {
	var req requestMessage
	if err := json.Unmarshal(body, &req); err != nil {
		// A salvageable id lets the peer correlate the ParseError; otherwise
		// log and skip.
		if id := salvageID(body); id != nil {
			s.respondError(id, codeParseError, "parse error: invalid JSON")
		} else {
			s.log.Printf("skipping unparseable message: %v", err)
		}
		return 0, false
	}

	isRequest := req.ID != nil
	defer func() {
		if r := recover(); r != nil {
			s.log.Printf("recovered panic handling %q: %v\n%s", req.Method, r, debug.Stack())
			if isRequest {
				s.respondError(req.ID, codeInternalError, "internal error")
			}
		}
	}()

	// exit is honored in any state and ends the loop.
	if req.Method == "exit" {
		return s.exitCode(), true
	}

	// Lifecycle gating.
	if !s.initialized && req.Method != "initialize" {
		if isRequest {
			s.respondError(req.ID, codeInvalidRequest, "server not initialized")
		}
		return 0, false
	}
	if s.shutdown {
		if isRequest {
			s.respondError(req.ID, codeInvalidRequest, "server is shutting down")
		}
		return 0, false
	}

	s.dispatch(req, isRequest)
	return 0, false
}

// panicHook, when non-nil, is invoked by dispatch for a request so a test can
// force a handler panic and exercise the recovery path (AC10). It is nil in
// production.
var panicHook func(method string)

// dispatch routes one well-formed, lifecycle-gated message to its handler.
func (s *Server) dispatch(req requestMessage, isRequest bool) {
	if panicHook != nil && isRequest {
		panicHook(req.Method)
	}
	switch req.Method {
	case "initialize":
		s.handleInitialize(req.ID, req.Params)
	case "initialized":
		// Notification acknowledging initialize; nothing to do.
	case "shutdown":
		s.shutdown = true
		s.respondResult(req.ID, nil)
	case "textDocument/didOpen":
		s.handleDidOpen(req.Params)
	case "textDocument/didChange":
		s.handleDidChange(req.Params)
	case "textDocument/didSave":
		// The saved text is already the open buffer, but files that are not
		// open are read from disk and may have changed meanwhile.
		s.refresh()
	case "textDocument/didClose":
		s.handleDidClose(req.Params)
	case "textDocument/completion":
		s.handleCompletion(req.ID, req.Params)
	case "textDocument/hover":
		s.handleHover(req.ID, req.Params)
	case "textDocument/definition":
		s.handleDefinition(req.ID, req.Params)
	case "workspace/didChangeWorkspaceFolders":
		s.handleDidChangeWorkspaceFolders(req.Params)
	default:
		if isRequest {
			s.respondError(req.ID, codeMethodNotFound, "method not found: "+req.Method)
		}
		// Unknown notifications are ignored.
	}
}

func (s *Server) handleInitialize(id *json.RawMessage, params json.RawMessage) {
	var p InitializeParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			s.log.Printf("initialize: ignoring unreadable params: %v", err)
		}
	}
	s.roots = workspaceRoots(p)
	s.initialized = true
	s.respondResult(id, InitializeResult{
		Capabilities: ServerCapabilities{
			TextDocumentSync: textDocumentSyncFull,
			CompletionProvider: &CompletionOptions{
				TriggerCharacters: []string{".", "@"},
			},
			HoverProvider:      true,
			DefinitionProvider: true,
			Workspace: &WorkspaceServerCapabilities{
				WorkspaceFolders: &WorkspaceFoldersServerCapabilities{Supported: true, ChangeNotifications: true},
			},
		},
		ServerInfo: &ServerInfo{Name: serverName, Version: version},
	})
}

// handleDidChangeWorkspaceFolders applies folders added to or removed from a
// multi-root workspace, then re-analyzes every open document: a folder is the
// project root of the documents inside it.
func (s *Server) handleDidChangeWorkspaceFolders(params json.RawMessage) {
	var p DidChangeWorkspaceFoldersParams
	if err := json.Unmarshal(params, &p); err != nil {
		s.log.Printf("didChangeWorkspaceFolders: bad params: %v", err)
		return
	}
	for _, f := range p.Event.Removed {
		dir, ok := docPath(f.URI)
		if !ok {
			continue
		}
		kept := s.roots[:0]
		for _, r := range s.roots {
			if pathKey(r) != pathKey(dir) {
				kept = append(kept, r)
			}
		}
		s.roots = kept
	}
	for _, f := range p.Event.Added {
		dir, ok := docPath(f.URI)
		if !ok {
			continue
		}
		known := false
		for _, r := range s.roots {
			known = known || pathKey(r) == pathKey(dir)
		}
		if !known {
			s.roots = append(s.roots, dir)
		}
	}
	s.refresh()
}

func (s *Server) handleDidOpen(params json.RawMessage) {
	var p DidOpenTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		s.log.Printf("didOpen: bad params: %v", err)
		return
	}
	s.store.open(p.TextDocument.URI, p.TextDocument.Text)
	s.refresh()
}

func (s *Server) handleDidChange(params json.RawMessage) {
	var p DidChangeTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		s.log.Printf("didChange: bad params: %v", err)
		return
	}
	if len(p.ContentChanges) == 0 {
		return
	}
	// Full-text sync: the last change carries the whole new document.
	text := p.ContentChanges[len(p.ContentChanges)-1].Text
	s.store.change(p.TextDocument.URI, text)
	s.refresh()
}

func (s *Server) handleDidClose(params json.RawMessage) {
	var p DidCloseTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		s.log.Printf("didClose: bad params: %v", err)
		return
	}
	s.store.close(p.TextDocument.URI)
	// Clear diagnostics for the closed document.
	s.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{
		URI:         p.TextDocument.URI,
		Diagnostics: []Diagnostic{},
	})
	// Its importers now see the file's disk content instead of the buffer.
	s.refresh()
}

// refresh drops every cached analysis and re-analyzes and re-publishes every
// open document. This is how an edit reaches the documents that import the
// edited one, directly or transitively: simple, and cheap at this scale.
func (s *Server) refresh() {
	s.cache = map[string]analysis{}
	s.overlay = nil
	for _, uri := range s.store.uris() {
		s.publishDiagnostics(uri)
	}
}

// publishDiagnostics sends the diagnostics of the open document uri. A panic
// while analyzing one document is logged and skips only that document, so the
// others are still published.
func (s *Server) publishDiagnostics(uri string) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Printf("recovered panic analyzing %s: %v\n%s", uri, r, debug.Stack())
		}
	}()
	a, ok := s.analysisOf(uri)
	if !ok {
		return
	}
	items := make([]Diagnostic, 0, len(a.diags))
	for _, d := range a.diags {
		items = append(items, convertDiagnostic(a.src, d))
	}
	s.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: items,
	})
}

// analysisOf returns the analysis of the open document uri, analyzing it on a
// cache miss. ok is false when the document is not open.
func (s *Server) analysisOf(uri string) (analysis, bool) {
	if a, ok := s.cache[uri]; ok {
		return a, true
	}
	text, ok := s.store.get(uri)
	if !ok {
		return analysis{}, false
	}
	var a analysis
	if p, isFile := docPath(uri); isFile {
		if s.overlay == nil {
			s.overlay = newOverlay(s.store)
		}
		a = analyzeModule(p, projectRoot(s.roots, p), s.overlay, text)
	} else {
		a = analyze(uri, text)
	}
	s.cache[uri] = a
	return a, true
}

func (s *Server) handleCompletion(id *json.RawMessage, params json.RawMessage) {
	var p CompletionParams
	if err := json.Unmarshal(params, &p); err != nil {
		s.respondError(id, codeInvalidParams, "completion: invalid params")
		return
	}
	a, ok := s.analysisOf(p.TextDocument.URI)
	if !ok {
		s.respondResult(id, CompletionList{Items: []CompletionItem{}})
		return
	}
	offset := lspToOffset(a.src, p.Position.Line, p.Position.Character)
	items := completeIn(a.mc, a.info, a.prog, a.src, offset)
	if items == nil {
		items = []CompletionItem{}
	}
	s.respondResult(id, CompletionList{Items: items})
}

func (s *Server) handleHover(id *json.RawMessage, params json.RawMessage) {
	var p HoverParams
	if err := json.Unmarshal(params, &p); err != nil {
		s.respondError(id, codeInvalidParams, "hover: invalid params")
		return
	}
	a, ok := s.analysisOf(p.TextDocument.URI)
	if !ok {
		s.respondResult(id, nil)
		return
	}
	offset := lspToOffset(a.src, p.Position.Line, p.Position.Character)
	h := hoverIn(a.mc, a.info, a.prog, a.src, offset)
	if h == nil {
		s.respondResult(id, nil)
		return
	}
	s.respondResult(id, h)
}

// handleDefinition answers textDocument/definition with one Location, or null
// when nothing at the position has a declaration in source.
func (s *Server) handleDefinition(id *json.RawMessage, params json.RawMessage) {
	var p DefinitionParams
	if err := json.Unmarshal(params, &p); err != nil {
		s.respondError(id, codeInvalidParams, "definition: invalid params")
		return
	}
	a, ok := s.analysisOf(p.TextDocument.URI)
	if !ok {
		s.respondResult(id, nil)
		return
	}
	offset := lspToOffset(a.src, p.Position.Line, p.Position.Character)
	t, found := definitionIn(a.mc, a.info, a.prog, a.src, offset)
	if !found && offset > 0 && isIdentByte(a.src[offset-1]) {
		// The cursor sits just past a name (as after typing it).
		t, found = definitionIn(a.mc, a.info, a.prog, a.src, offset-1)
	}
	if !found {
		s.respondResult(id, nil)
		return
	}
	uri := p.TextDocument.URI
	if t.mod != nil {
		uri = s.uriOf(t.mod.AbsPath)
	}
	s.respondResult(id, Location{URI: uri, Range: t.targetRange()})
}

// uriOf returns the URI for an absolute path: the open document's own URI
// when the path is open (so the editor matches it exactly), else a file: URI.
func (s *Server) uriOf(absPath string) string {
	key := pathKey(absPath)
	for _, uri := range s.store.uris() {
		if p, ok := docPath(uri); ok && pathKey(p) == key {
			return uri
		}
	}
	return pathToURI(absPath)
}

// respondResult writes a success response echoing id. A nil result serializes
// as JSON null (hover returns null when nothing resolves).
func (s *Server) respondResult(id *json.RawMessage, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		s.log.Printf("marshal result: %v", err)
		s.respondError(id, codeInternalError, "internal error")
		return
	}
	s.write(responseMessage{JSONRPC: "2.0", ID: id, Result: raw})
}

// respondError writes an error response echoing id.
func (s *Server) respondError(id *json.RawMessage, code int, message string) {
	s.write(responseMessage{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &responseError{Code: code, Message: message},
	})
}

// notify writes a server-initiated notification.
func (s *Server) notify(method string, params any) {
	raw, err := json.Marshal(params)
	if err != nil {
		s.log.Printf("marshal %s params: %v", method, err)
		return
	}
	p := json.RawMessage(raw)
	s.write(requestMessage{JSONRPC: "2.0", Method: method, Params: p})
}

// write marshals v and sends it as one framed message, flushing out.
func (s *Server) write(v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		s.log.Printf("marshal response: %v", err)
		return
	}
	if err := writeMessage(s.out, payload); err != nil {
		s.log.Printf("write message: %v", err)
		return
	}
	if err := s.out.Flush(); err != nil {
		s.log.Printf("flush: %v", err)
	}
}

// salvageID extracts the raw "id" field from a message whose body did not
// unmarshal into a requestMessage, so a ParseError can still echo it. It
// returns nil when no id is present.
func salvageID(body []byte) *json.RawMessage {
	var probe struct {
		ID *json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil
	}
	return probe.ID
}
