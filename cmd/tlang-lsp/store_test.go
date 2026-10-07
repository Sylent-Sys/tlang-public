package main

import "testing"

func TestDocumentStoreLifecycle(t *testing.T) {
	s := newDocumentStore()
	const uri = "file:///app.tlang"

	if _, ok := s.get(uri); ok {
		t.Fatal("get on empty store returned ok")
	}

	s.open(uri, "let x = 1;")
	text, ok := s.get(uri)
	if !ok || text != "let x = 1;" {
		t.Fatalf("after open: text=%q ok=%v", text, ok)
	}

	s.change(uri, "let x = 2;")
	text, ok = s.get(uri)
	if !ok || text != "let x = 2;" {
		t.Fatalf("after change: text=%q ok=%v, want last update", text, ok)
	}

	s.close(uri)
	if _, ok := s.get(uri); ok {
		t.Fatal("after close: entry still present")
	}
}
