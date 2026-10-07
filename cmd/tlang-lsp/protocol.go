package main

import "encoding/json"

// JSON-RPC 2.0 error codes used by the server (LSP reuses the base JSON-RPC
// set).
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// requestMessage is one incoming JSON-RPC message. ID is a *json.RawMessage so
// a notification (no id) is distinguished from an id of 0 or null, and the id
// is echoed back verbatim whether it was a number or a string.
type requestMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

// responseMessage is one outgoing JSON-RPC response. A success carries Result
// (a pre-marshaled value, "null" when the handler result is nil, as hover
// requires); an error carries Error instead.
type responseMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *responseError   `json:"error,omitempty"`
}

// responseError is a JSON-RPC error object.
type responseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// textDocumentSyncKind values. The server advertises full-text sync.
const textDocumentSyncFull = 1

// CompletionItemKind values (subset used by the server), from the LSP spec.
const (
	completionKindFunction      = 3
	completionKindField         = 5
	completionKindVariable      = 6
	completionKindInterface     = 8
	completionKindModule        = 9
	completionKindKeyword       = 14
	completionKindConstant      = 21
	completionKindTypeParameter = 25
)

// DiagnosticSeverity values from the LSP spec.
const (
	severityError       = 1
	severityWarning     = 2
	severityInformation = 3
)

// Position is a zero-based line/character pair. The character axis is counted
// in bytes in the first cut (ASCII-correct; see convert.go).
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a half-open [Start, End) span.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// InitializeParams is the client's initialize request payload. The server
// reads only the workspace description, which sets the project root that
// bounds import resolution (workspaceFolders, else rootUri, else the
// deprecated rootPath). A JSON null leaves a field empty.
type InitializeParams struct {
	RootPath         string            `json:"rootPath,omitempty"`
	RootURI          string            `json:"rootUri,omitempty"`
	WorkspaceFolders []WorkspaceFolder `json:"workspaceFolders,omitempty"`
}

// WorkspaceFolder is one folder open in the client.
type WorkspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

// InitializeResult is the server's initialize response.
type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   *ServerInfo        `json:"serverInfo,omitempty"`
}

// ServerInfo names the server and its version in the initialize result.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// ServerCapabilities advertises what the server supports.
type ServerCapabilities struct {
	TextDocumentSync   int                          `json:"textDocumentSync"`
	CompletionProvider *CompletionOptions           `json:"completionProvider,omitempty"`
	HoverProvider      bool                         `json:"hoverProvider"`
	DefinitionProvider bool                         `json:"definitionProvider"`
	Workspace          *WorkspaceServerCapabilities `json:"workspace,omitempty"`
}

// WorkspaceServerCapabilities advertises workspace-level support.
type WorkspaceServerCapabilities struct {
	WorkspaceFolders *WorkspaceFoldersServerCapabilities `json:"workspaceFolders,omitempty"`
}

// WorkspaceFoldersServerCapabilities asks the client to report folders being
// added to or removed from the workspace.
type WorkspaceFoldersServerCapabilities struct {
	Supported           bool `json:"supported"`
	ChangeNotifications bool `json:"changeNotifications"`
}

// DidChangeWorkspaceFoldersParams is the workspace/didChangeWorkspaceFolders
// payload.
type DidChangeWorkspaceFoldersParams struct {
	Event WorkspaceFoldersChangeEvent `json:"event"`
}

// WorkspaceFoldersChangeEvent lists the folders added and removed.
type WorkspaceFoldersChangeEvent struct {
	Added   []WorkspaceFolder `json:"added"`
	Removed []WorkspaceFolder `json:"removed"`
}

// CompletionOptions configures the completion provider.
type CompletionOptions struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
}

// TextDocumentItem is a document the client opened.
type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

// TextDocumentIdentifier names a document by URI.
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// VersionedTextDocumentIdentifier names a document and a version.
type VersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

// DidOpenTextDocumentParams is the textDocument/didOpen payload.
type DidOpenTextDocumentParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

// TextDocumentContentChangeEvent is one change. Under full-text sync only Text
// is read (it carries the whole new document).
type TextDocumentContentChangeEvent struct {
	Text string `json:"text"`
}

// DidChangeTextDocumentParams is the textDocument/didChange payload.
type DidChangeTextDocumentParams struct {
	TextDocument   VersionedTextDocumentIdentifier  `json:"textDocument"`
	ContentChanges []TextDocumentContentChangeEvent `json:"contentChanges"`
}

// DidSaveTextDocumentParams is the textDocument/didSave payload.
type DidSaveTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// DidCloseTextDocumentParams is the textDocument/didClose payload.
type DidCloseTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// PublishDiagnosticsParams is the textDocument/publishDiagnostics payload.
type PublishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// Diagnostic is one LSP diagnostic.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity"`
	Code     string `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// TextDocumentPositionParams carries a document and a cursor position; it is
// embedded by the completion and hover requests.
type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// CompletionParams is the textDocument/completion payload.
type CompletionParams struct {
	TextDocumentPositionParams
}

// CompletionItem is one completion suggestion.
type CompletionItem struct {
	Label  string `json:"label"`
	Kind   int    `json:"kind,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// CompletionList is the completion response. IsIncomplete is false: the server
// returns the full set each time.
type CompletionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []CompletionItem `json:"items"`
}

// HoverParams is the textDocument/hover payload.
type HoverParams struct {
	TextDocumentPositionParams
}

// Hover is the hover response.
type Hover struct {
	Contents MarkupContent `json:"contents"`
}

// MarkupContent is rendered content; the server emits markdown.
type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// DefinitionParams is the textDocument/definition payload.
type DefinitionParams struct {
	TextDocumentPositionParams
}

// Location is a range in a document, the textDocument/definition result.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}
