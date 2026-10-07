package main

import "sort"

// documentStore holds the current full text of every open document, keyed by
// URI. An open document is always analyzed from this text, never from disk;
// only files that are not open are read from disk (see overlayFS). There is
// no concurrency guard because the run loop is single-threaded.
type documentStore struct {
	docs map[string]string
}

// newDocumentStore returns an empty store.
func newDocumentStore() *documentStore {
	return &documentStore{docs: map[string]string{}}
}

// open records a newly opened document's text.
func (s *documentStore) open(uri, text string) {
	s.docs[uri] = text
}

// change replaces a document's text wholesale (full-text sync).
func (s *documentStore) change(uri, text string) {
	s.docs[uri] = text
}

// close forgets a document.
func (s *documentStore) close(uri string) {
	delete(s.docs, uri)
}

// get returns the current text of uri and whether it is open.
func (s *documentStore) get(uri string) (string, bool) {
	text, ok := s.docs[uri]
	return text, ok
}

// uris returns the URIs of the open documents in sorted order.
func (s *documentStore) uris() []string {
	out := make([]string, 0, len(s.docs))
	for uri := range s.docs {
		out = append(out, uri)
	}
	sort.Strings(out)
	return out
}
