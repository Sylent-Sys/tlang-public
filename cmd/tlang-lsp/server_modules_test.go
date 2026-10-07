package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// publishesFor returns every publishDiagnostics payload sent for uri, in
// order.
func publishesFor(t *testing.T, msgs []map[string]json.RawMessage, uri string) [][]Diagnostic {
	t.Helper()
	var out [][]Diagnostic
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
			out = append(out, p.Diagnostics)
		}
	}
	return out
}

// resultOf returns the raw result of the response with the given id.
func resultOf(t *testing.T, msgs []map[string]json.RawMessage, id string) json.RawMessage {
	t.Helper()
	for _, m := range msgs {
		if idOf(m) == id {
			return m["result"]
		}
	}
	t.Fatalf("no response with id %s", id)
	return nil
}

func initializeWorkspace(s *session, root string) {
	s.send("initialize", 1, InitializeParams{WorkspaceFolders: []WorkspaceFolder{{URI: pathToURI(root), Name: "ws"}}})
	s.send("initialized", nil, map[string]any{})
}

func positionParams(uri string, line, character int) TextDocumentPositionParams {
	return TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: line, Character: character},
	}
}

// lineCol returns the 0-based line and byte column of offset in src.
func lineCol(src string, offset int) (int, int) {
	line := strings.Count(src[:offset], "\n")
	return line, offset - (strings.LastIndex(src[:offset], "\n") + 1)
}

func TestServerAdvertisesDefinition(t *testing.T) {
	var s session
	initialize(&s)
	s.send("exit", nil, nil)
	s.run()
	var res InitializeResult
	if err := json.Unmarshal(s.messages(t)[0]["result"], &res); err != nil {
		t.Fatalf("initialize result: %v", err)
	}
	if !res.Capabilities.DefinitionProvider {
		t.Error("definitionProvider not advertised")
	}
	if ws := res.Capabilities.Workspace; ws == nil || ws.WorkspaceFolders == nil ||
		!ws.WorkspaceFolders.Supported || !ws.WorkspaceFolders.ChangeNotifications {
		t.Error("workspace folder change notifications not requested")
	}
	if res.Capabilities.CompletionProvider == nil || !hasString(res.Capabilities.CompletionProvider.TriggerCharacters, "@") {
		t.Error("completion should trigger on @ for decorators")
	}
}

// TestServerModuleWorkspace drives the server over the fixture project from
// a workspace folder: imports resolve (no diagnostics), and definitions land
// in other files, using the open document's own URI when the target is open.
func TestServerModuleWorkspace(t *testing.T) {
	root := writeProject(t, fixture)
	mainPath := filepath.Join(root, "app", "main.ts")
	modelsPath := filepath.Join(root, "lib", "models.ts")
	mainURI := pathToURI(mainPath)
	src := fixture["app/main.ts"]

	// The models document is opened under a different spelling of its URI
	// than pathToURI produces (an explicit localhost authority; on Windows
	// also an upper-case drive and a literal colon, as clients vary there);
	// results must echo the client's spelling on every platform.
	modelsURI := "file://localhost" + strings.TrimPrefix(pathToURI(modelsPath), "file://")
	if isWindows {
		modelsURI = strings.Replace(modelsURI, "localhost/"+modelsURI[17:18]+"%3A", "localhost/"+strings.ToUpper(modelsURI[17:18])+":", 1)
	}
	if modelsURI == pathToURI(modelsPath) {
		t.Fatal("test setup: the open URI must differ textually from pathToURI")
	}

	var s session
	initializeWorkspace(&s, root)
	s.send("textDocument/didOpen", nil, DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: mainURI, Text: src},
	})
	line, col := lineCol(src, strings.Index(src, "hi(u)"))
	s.send("textDocument/definition", 10, DefinitionParams{positionParams(mainURI, line, col)})
	s.send("textDocument/didOpen", nil, DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: modelsURI, Text: fixture["lib/models.ts"]},
	})
	s.send("textDocument/definition", 11, DefinitionParams{positionParams(mainURI, line, col)})
	line, col = lineCol(src, strings.Index(src, "\"../lib/hooks\""))
	s.send("textDocument/definition", 12, DefinitionParams{positionParams(mainURI, line, col+1)})
	// Whitespace has no definition.
	s.send("textDocument/definition", 13, DefinitionParams{positionParams(mainURI, 4, 0)})
	s.send("exit", nil, nil)
	s.run()
	msgs := s.messages(t)

	pubs := publishesFor(t, msgs, mainURI)
	if len(pubs) == 0 || len(pubs[0]) != 0 {
		t.Fatalf("main.ts publishes = %+v, want an empty first publish (imports resolve)", pubs)
	}

	wantGreet := Range{Start: Position{Line: 5, Character: 10}, End: Position{Line: 5, Character: 15}}
	var loc Location
	if err := json.Unmarshal(resultOf(t, msgs, "10"), &loc); err != nil {
		t.Fatalf("definition result: %v", err)
	}
	if loc.URI != pathToURI(modelsPath) || loc.Range != wantGreet {
		t.Errorf("definition (models closed) = %+v, want %s %+v", loc, pathToURI(modelsPath), wantGreet)
	}
	if err := json.Unmarshal(resultOf(t, msgs, "11"), &loc); err != nil {
		t.Fatalf("definition result: %v", err)
	}
	if loc.URI != modelsURI || loc.Range != wantGreet {
		t.Errorf("definition (models open) = %+v, want %s %+v", loc, modelsURI, wantGreet)
	}
	if err := json.Unmarshal(resultOf(t, msgs, "12"), &loc); err != nil {
		t.Fatalf("definition result: %v", err)
	}
	if loc.URI != pathToURI(filepath.Join(root, "lib", "hooks.ts")) || loc.Range != (Range{}) {
		t.Errorf("specifier definition = %+v, want the start of lib/hooks.ts", loc)
	}
	if raw := resultOf(t, msgs, "13"); string(raw) != "null" {
		t.Errorf("definition on whitespace = %s, want null", raw)
	}
}

// TestServerRefreshesImporters: opening, editing and closing a document
// re-publishes the documents that import it. The imported file exists only
// as an unsaved buffer, so main.ts resolves its import only while it is open.
func TestServerRefreshesImporters(t *testing.T) {
	root := writeProject(t, map[string]string{
		"main.ts": "import { fresh } from \"./unsaved\";\nfn main(): void { fresh(); }\n",
	})
	mainURI := pathToURI(filepath.Join(root, "main.ts"))
	unsavedURI := pathToURI(filepath.Join(root, "unsaved.ts"))

	var s session
	initializeWorkspace(&s, root)
	s.send("textDocument/didOpen", nil, DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: mainURI, Text: "import { fresh } from \"./unsaved\";\nfn main(): void { fresh(); }\n"},
	})
	s.send("textDocument/didOpen", nil, DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: unsavedURI, Text: "export fn fresh(): void {}\n"},
	})
	s.send("textDocument/didChange", nil, DidChangeTextDocumentParams{
		TextDocument:   VersionedTextDocumentIdentifier{URI: unsavedURI, Version: 2},
		ContentChanges: []TextDocumentContentChangeEvent{{Text: "export fn fresh(): void {}\nexport fn more(): void {}\n"}},
	})
	s.send("textDocument/didClose", nil, DidCloseTextDocumentParams{
		TextDocument: TextDocumentIdentifier{URI: unsavedURI},
	})
	s.send("exit", nil, nil)
	s.run()
	msgs := s.messages(t)

	pubs := publishesFor(t, msgs, mainURI)
	if len(pubs) != 4 {
		t.Fatalf("main.ts published %d times, want 4 (own open, importee open, change, close)", len(pubs))
	}
	unresolved := func(ds []Diagnostic) bool {
		return len(ds) == 1 && ds[0].Code == "E-IMPORT" && strings.Contains(ds[0].Message, "cannot resolve import")
	}
	if !unresolved(pubs[0]) || len(pubs[1]) != 0 || len(pubs[2]) != 0 || !unresolved(pubs[3]) {
		t.Fatalf("main.ts publishes = %+v, want [unresolved, clean, clean, unresolved]", pubs)
	}
	upubs := publishesFor(t, msgs, unsavedURI)
	if len(upubs) == 0 || len(upubs[len(upubs)-1]) != 0 {
		t.Fatalf("unsaved.ts publishes = %+v, want a final empty publish on close", upubs)
	}
}

// TestServerUntitledDocument: a document without a file: URI is analyzed
// alone, and its definitions stay in the document.
func TestServerUntitledDocument(t *testing.T) {
	const uri = "untitled:Untitled-1"
	src := "fn greet(): void {}\nfn main(): void { greet(); let x = missing; }\n"
	var s session
	initialize(&s)
	s.send("textDocument/didOpen", nil, DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, Text: src},
	})
	line, col := lineCol(src, strings.Index(src, "greet();"))
	s.send("textDocument/definition", 20, DefinitionParams{positionParams(uri, line, col+len("greet"))})
	s.send("exit", nil, nil)
	s.run()
	msgs := s.messages(t)

	diags, ok := firstDiagnostics(t, msgs, uri)
	if !ok || len(diags) != 1 || diags[0].Code != "E-NAME" {
		t.Fatalf("untitled diagnostics = %+v, want one E-NAME", diags)
	}
	var loc Location
	if err := json.Unmarshal(resultOf(t, msgs, "20"), &loc); err != nil {
		t.Fatalf("definition result: %v", err)
	}
	want := Range{Start: Position{Line: 0, Character: 3}, End: Position{Line: 0, Character: 8}}
	if loc.URI != uri || loc.Range != want {
		t.Fatalf("definition = %+v, want %s %+v (cursor just past the name)", loc, uri, want)
	}
}

// TestServerWorkspaceFoldersChange: a folder added mid-session becomes the
// project root of its documents (an in-folder "../" import resolves), and
// once it is removed they fall back to their own directory (the same import
// escapes it).
func TestServerWorkspaceFoldersChange(t *testing.T) {
	folderA := writeProject(t, map[string]string{"a.ts": "fn main(): void {}\n"})
	folderB := writeProject(t, map[string]string{
		"lib/util.ts": "export fn util(): void {}\n",
	})
	mainURI := pathToURI(filepath.Join(folderB, "app", "main.ts"))
	src := "import { util } from \"../lib/util\";\nfn main(): void { util(); }\n"
	change := func(added, removed string) DidChangeWorkspaceFoldersParams {
		var p DidChangeWorkspaceFoldersParams
		if added != "" {
			p.Event.Added = []WorkspaceFolder{{URI: pathToURI(added), Name: "added"}}
		}
		if removed != "" {
			p.Event.Removed = []WorkspaceFolder{{URI: pathToURI(removed), Name: "removed"}}
		}
		return p
	}

	var s session
	initializeWorkspace(&s, folderA)
	s.send("workspace/didChangeWorkspaceFolders", nil, change(folderB, ""))
	s.send("textDocument/didOpen", nil, DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: mainURI, Text: src},
	})
	s.send("workspace/didChangeWorkspaceFolders", nil, change("", folderB))
	s.send("exit", nil, nil)
	s.run()

	pubs := publishesFor(t, s.messages(t), mainURI)
	if len(pubs) != 2 {
		t.Fatalf("main.ts published %d times, want 2 (open, folder removed)", len(pubs))
	}
	if len(pubs[0]) != 0 {
		t.Errorf("with folder B in the workspace: %+v, want no diagnostics", pubs[0])
	}
	if len(pubs[1]) != 1 || !strings.Contains(pubs[1][0].Message, "escapes the project root") {
		t.Errorf("after removing folder B: %+v, want the import to escape the document's directory", pubs[1])
	}
}

// encodeMessage marshals one JSON-RPC message body.
func encodeMessage(t *testing.T, method string, id any, params any) []byte {
	t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		msg["id"] = id
	}
	if params != nil {
		msg["params"] = params
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestServerDidSaveRereadsDisk: files that are not open are read from disk
// again on didSave, so a closed import edited on disk (here broken) shows up
// on the open importer after a save.
func TestServerDidSaveRereadsDisk(t *testing.T) {
	root := writeProject(t, map[string]string{
		"main.ts": "import { dep } from \"./dep\";\nfn main(): void { dep(); }\n",
		"dep.ts":  "export fn dep(): void {}\n",
	})
	mainURI := pathToURI(filepath.Join(root, "main.ts"))
	var sess session
	srv := newServer(&sess.out, &sess.log)
	srv.handle(encodeMessage(t, "initialize", 1, InitializeParams{WorkspaceFolders: []WorkspaceFolder{{URI: pathToURI(root)}}}))
	srv.handle(encodeMessage(t, "textDocument/didOpen", nil, DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: mainURI, Text: "import { dep } from \"./dep\";\nfn main(): void { dep(); }\n"},
	}))
	if err := os.WriteFile(filepath.Join(root, "dep.ts"), []byte("export fn dep(): void { let = ; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv.handle(encodeMessage(t, "textDocument/didSave", nil, DidSaveTextDocumentParams{
		TextDocument: TextDocumentIdentifier{URI: mainURI},
	}))

	pubs := publishesFor(t, sess.messages(t), mainURI)
	if len(pubs) != 2 {
		t.Fatalf("main.ts published %d times, want 2 (open, save)", len(pubs))
	}
	if len(pubs[0]) != 0 {
		t.Errorf("before the disk change: %+v, want no diagnostics", pubs[0])
	}
	if len(pubs[1]) != 1 || pubs[1][0].Message != `imported module "dep.ts" has errors` {
		t.Errorf("after didSave: %+v, want the broken import reported", pubs[1])
	}
}

func hasString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
