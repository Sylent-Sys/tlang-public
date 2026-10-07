package codegen

import (
	"bytes"
	"fmt"
	"strings"
)

// Output format (plan D2): LF line ends, four spaces per indentation level,
// no tabs, no trailing white space, and no comments. Labels are written as
// "name: ;" at the current indentation: the empty statement after the colon
// is mandatory, because C11 forbids a label directly before "}" or before a
// declaration (probes b1, c1).

// indentUnit is one level of indentation.
const indentUnit = "    "

// section identifies one region of the generated translation unit.
// generator.assemble concatenates the non-empty sections in this order
// (codegen design §3.2, plan D3), which is load-bearing: the row
// descriptors of region D use sizeof and offsetof of the struct bodies of
// region A.
type section int

const (
	secInclude section = iota // #include "tlang.h"
	secPragma                 // the guarded diagnostic pragma block
	secTypes                  // region A: typedefs, slice defines, struct bodies
	secGlobals                // region B: struct tl_globals
	secProtos                 // region C: prototypes
	secTables                 // region D: long strings, route tables, row descriptors
	secJSON                   // region E: JSON parsers and writers
	secFuncs                  // region F: user functions and instances
	secProgram                // region G: tl__init_globals, __tl_program, main
	numSections
)

// cwriter accumulates the C text of one output section, one line at a time
// at its current indentation.
type cwriter struct {
	buf bytes.Buffer
	// indent is the current indentation level.
	indent int
}

// line writes s on its own line at the current indentation; an empty s
// writes an empty line.
func (w *cwriter) line(s string) {
	checkLine(s)
	if s != "" {
		for range w.indent {
			w.buf.WriteString(indentUnit)
		}
		w.buf.WriteString(s)
	}
	w.buf.WriteByte('\n')
}

// linef writes a formatted line at the current indentation.
func (w *cwriter) linef(format string, args ...any) {
	w.line(fmt.Sprintf(format, args...))
}

// writef appends formatted text verbatim, without indentation or a line end:
// for text that is already laid out, such as a separately rendered entity.
// The text must consist of complete lines.
func (w *cwriter) writef(format string, args ...any) {
	fmt.Fprintf(&w.buf, format, args...)
}

// open writes head followed by " {" (or just "{" for an empty head) and
// indents the following lines one more level.
func (w *cwriter) open(head string) {
	w.line(braceOpen(head))
	w.indent++
}

// close ends the innermost open brace: one level less indentation, then "}"
// followed by tail ("" or ";"). reopen continues with another brace.
func (w *cwriter) close(tail string) {
	if w.indent == 0 {
		panic("cwriter: close without open")
	}
	w.indent--
	w.line("}" + tail)
}

// reopen ends the innermost open brace and opens the next one with the line
// s, as in "} else {" or "} else if (l_x > 0) {".
func (w *cwriter) reopen(s string) {
	if w.indent == 0 {
		panic("cwriter: reopen without open")
	}
	w.indent--
	w.line(s)
	w.indent++
}

// label writes the label name as "name: ;" at the current indentation.
func (w *cwriter) label(name string) {
	w.line(name + ": ;")
}

// gap separates the next entity from the previous one with an empty line,
// unless the section is empty or already ends with an empty line.
func (w *cwriter) gap() {
	b := w.buf.Bytes()
	if len(b) == 0 || bytes.HasSuffix(b, []byte("\n\n")) {
		return
	}
	w.buf.WriteByte('\n')
}

// writeBlock writes a finished block at the current indentation: its
// hoisted declarations, then its content, each nested block one level deeper
// than the line before it.
func (w *cwriter) writeBlock(b *block) {
	if !b.done {
		panic("cwriter: block written before it was finished")
	}
	for _, d := range b.decls {
		w.line(d)
	}
	for _, it := range b.items {
		w.indent += it.depth
		if it.sub != nil {
			w.indent++
			w.writeBlock(it.sub)
			w.indent--
		} else {
			w.line(it.text)
		}
		w.indent -= it.depth
	}
}

// bytes returns the text written so far.
func (w *cwriter) bytes() []byte { return w.buf.Bytes() }

// block is one C block of a generated function under construction (codegen
// design §5.2 step 2, plan D5): the locals hoisted to its top, written first,
// then its content in order. Content is a line, a nested block, or a
// deferred fragment whose text is decided when the block is finished (plan
// D10: "(void)l_x;" only for a local that turned out to be never read).
// Blocks are written into a cwriter only once finished, so a block's
// declarations can be collected while its statements are lowered.
type block struct {
	// parent is the enclosing block; nil for a function body.
	parent *block
	// decls are the hoisted declarations ("<CType> <name>;") in declaration
	// order.
	decls []string
	// items is the content in order.
	items []blockItem
	// depth is the extra indentation of the next item (open and close).
	depth int
	// done reports that finish was called; a finished block is immutable.
	done bool
}

// blockItem is one piece of block content: a line of text, a nested block
// (sub), or a deferred fragment (resolve) until the block is finished.
type blockItem struct {
	depth   int
	text    string
	sub     *block
	resolve func() string
}

// newBlock returns an empty top-level block (a function body).
func newBlock() *block { return &block{} }

func (b *block) add(it blockItem) {
	if b.done {
		panic("block: content added after finish")
	}
	b.items = append(b.items, it)
}

// decl hoists a declaration line to the top of the block.
func (b *block) decl(s string) {
	if b.done {
		panic("block: declaration added after finish")
	}
	checkLine(s)
	b.decls = append(b.decls, s)
}

// line appends a line of content.
func (b *block) line(s string) {
	checkLine(s)
	b.add(blockItem{depth: b.depth, text: s})
}

// linef appends a formatted line of content.
func (b *block) linef(format string, args ...any) {
	b.line(fmt.Sprintf(format, args...))
}

// open appends head followed by " {" and indents the following content of
// this block one more level. It does not open a C scope for hoisting: a
// construct whose body declares locals uses child instead.
func (b *block) open(head string) {
	b.line(braceOpen(head))
	b.depth++
}

// close ends the innermost open of this block with "}" followed by tail.
func (b *block) close(tail string) {
	if b.depth == 0 {
		panic("block: close without open")
	}
	b.depth--
	b.line("}" + tail)
}

// reopen ends the innermost open of this block and opens the next one with
// the line s, as in "} else {" or "} else if (l_x > 0) {" (plan D12).
func (b *block) reopen(s string) {
	if b.depth == 0 {
		panic("block: reopen without open")
	}
	b.depth--
	b.line(s)
	b.depth++
}

// label appends the label name as "name: ;".
func (b *block) label(name string) {
	b.line(name + ": ;")
}

// child appends a nested block, rendered one level deeper than the current
// content, and returns it. The braces around it are the caller's lines.
func (b *block) child() *block {
	c := &block{parent: b}
	b.add(blockItem{depth: b.depth, sub: c})
	return c
}

// deferred appends a fragment whose line is computed by f when the block is
// finished; an empty result leaves no line.
func (b *block) deferred(f func() string) {
	b.add(blockItem{depth: b.depth, resolve: f})
}

// finish resolves the deferred fragments and makes the block immutable.
// Every nested block must be finished first, and every open closed.
func (b *block) finish() {
	if b.done {
		panic("block: finished twice")
	}
	if b.depth != 0 {
		panic("block: finished with an unclosed open")
	}
	items := b.items[:0]
	for _, it := range b.items {
		if it.sub != nil && !it.sub.done {
			panic("block: finished before a nested block")
		}
		if it.resolve != nil {
			it.text = it.resolve()
			it.resolve = nil
			if it.text == "" {
				continue
			}
			checkLine(it.text)
		}
		items = append(items, it)
	}
	b.items = items
	b.done = true
}

// braceOpen returns head + " {", or "{" for an empty head.
func braceOpen(head string) string {
	if head == "" {
		return "{"
	}
	return head + " {"
}

// checkLine enforces the line format of plan D2 on one line of C text: no
// line breaks or tabs inside, and no leading or trailing spaces (the writer
// owns indentation). A violation is a codegen bug.
func checkLine(s string) {
	if strings.ContainsAny(s, "\n\r\t") || strings.HasPrefix(s, " ") || strings.HasSuffix(s, " ") {
		panic(fmt.Sprintf("malformed C line %q", s))
	}
}
