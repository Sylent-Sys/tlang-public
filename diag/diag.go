// Package diag holds compiler diagnostics: errors, warnings and notes with a
// source position, a stable code, and a message.
//
// Every stage (lexer via parser, parser, checker, driver) appends to a List.
// Output is deterministic: Sorted orders diagnostics by file, line and
// column, keeping insertion order for equal positions.
package diag

import (
	"bytes"
	"cmp"
	"fmt"
	"slices"
	"strings"

	"tlang/token"
)

// Severity classifies a diagnostic.
type Severity int

const (
	// Error makes compilation fail.
	Error Severity = iota
	// Warning is reported but does not make compilation fail.
	Warning
	// Note adds information to the preceding diagnostic.
	Note
)

// String returns "error", "warning" or "note".
func (s Severity) String() string {
	switch s {
	case Error:
		return "error"
	case Warning:
		return "warning"
	case Note:
		return "note"
	}
	return fmt.Sprintf("severity(%d)", int(s))
}

// Stable diagnostic codes. Tests and golden files match on them, so a code
// never changes meaning. Errors use the "E-" prefix; warnings use "W-".
// The checker uses CodeType when no more specific code applies.
const (
	CodeLex         = "E-LEX"         // lexical error (an ILLEGAL token), reported by the parser
	CodeParse       = "E-PARSE"       // syntax error, including '&' and arrow functions outside db.transaction
	CodeName        = "E-NAME"        // undefined, redeclared or reserved name
	CodeType        = "E-TYPE"        // type mismatch, invalid operation, wrong argument count, misuse of null/optional
	CodeConst       = "E-CONST"       // constant does not fit its type, constant division by zero, literal overflow
	CodeInit        = "E-INIT"        // missing initializer, cycle of required interface fields, missing/unknown object literal field
	CodeEscape      = "E-ESCAPE"      // request-region value stored into a global (DESIGN.md §2.10, spec §6.3)
	CodeTx          = "E-TX"          // transaction rules: return/break/continue leaving the block, nesting, tx outside the block
	CodeDecorator   = "E-DECORATOR"   // unknown decorator or invalid @Use target/guard
	CodeRoute       = "E-ROUTE"       // invalid ctx.match method or pattern, too many route parameters
	CodeDB          = "E-DB"          // non-literal SQL, unsupported argument or row type
	CodeJSON        = "E-JSON"        // type cannot be (de)serialized as JSON
	CodeEntry       = "E-ENTRY"       // main/route_dispatcher missing, duplicated, or with a wrong signature
	CodeGeneric     = "E-GENERIC"     // type argument count/inference failure, invalid type argument, instantiation cycle
	CodeUnsupported = "E-UNSUPPORTED" // construct reserved for a later version
	CodeInternal    = "E-INTERNAL"    // compiler bug
)

// Diagnostic is one message about a source location.
type Diagnostic struct {
	// File is the source file name; "" when unknown.
	File string
	// Pos is the location the message refers to; the zero Position when the
	// message is about the whole file.
	Pos token.Position
	// Severity is Error, Warning or Note.
	Severity Severity
	// Code is a stable short code such as CodeParse ("E-PARSE").
	Code string
	// Message is a lower-case sentence without a trailing period.
	Message string
}

// Error formats d as "file:line:col: severity: message". The "file:" part is
// omitted when File is empty and the "line:col: " part when Pos is not valid.
func (d Diagnostic) Error() string {
	var b strings.Builder
	if d.File != "" {
		b.WriteString(d.File)
		b.WriteString(":")
	}
	if d.Pos.IsValid() {
		b.WriteString(d.Pos.String())
		b.WriteString(":")
	}
	if b.Len() > 0 {
		b.WriteString(" ")
	}
	b.WriteString(d.Severity.String())
	b.WriteString(": ")
	b.WriteString(d.Message)
	return b.String()
}

// Render returns d.Error() followed, when d.Pos.Line exists in source, by
// the source line and a caret line pointing at d.Pos.Column. The caret line
// copies tabs from the source line and skips UTF-8 continuation bytes, so
// the caret lines up in a terminal. A trailing "\r" is removed from the
// source line. A nil source renders no snippet. The result ends with "\n".
//
// Example:
//
//	app.ts:3:13: error: undefined: foo
//	    let x = foo;
//	            ^
func (d Diagnostic) Render(source []byte) string {
	var b strings.Builder
	b.WriteString(d.Error())
	b.WriteString("\n")
	line, ok := sourceLine(source, d.Pos.Line)
	if !ok {
		return b.String()
	}
	b.WriteString(line)
	b.WriteString("\n")
	col := d.Pos.Column
	if col < 1 {
		col = 1
	}
	for i := 0; i < col-1 && i < len(line); i++ {
		switch c := line[i]; {
		case c == '\t':
			b.WriteByte('\t')
		case c&0xC0 == 0x80: // UTF-8 continuation byte: same display cell
		default:
			b.WriteByte(' ')
		}
	}
	b.WriteString("^\n")
	return b.String()
}

// sourceLine returns line n (1-based) of src without its line terminator.
// A nil src has no lines; the empty line after a final "\n" exists (EOF
// diagnostics point there).
func sourceLine(src []byte, n int) (string, bool) {
	if src == nil || n < 1 {
		return "", false
	}
	start := 0
	for i := 1; i < n; i++ {
		j := bytes.IndexByte(src[start:], '\n')
		if j < 0 {
			return "", false
		}
		start += j + 1
	}
	end := len(src)
	if j := bytes.IndexByte(src[start:], '\n'); j >= 0 {
		end = start + j
	}
	return strings.TrimSuffix(string(src[start:end]), "\r"), true
}

// List collects diagnostics. The zero value is ready to use.
type List struct {
	// File is stamped on diagnostics added through Errorf, Warnf and Notef,
	// and on diagnostics passed to Add whose File is empty.
	File string
	// Items holds the diagnostics in insertion order.
	Items []Diagnostic
}

// NewList returns an empty list whose diagnostics default to file.
func NewList(file string) *List { return &List{File: file} }

// Add appends d, filling d.File from l.File when d.File is empty.
func (l *List) Add(d Diagnostic) {
	if d.File == "" {
		d.File = l.File
	}
	l.Items = append(l.Items, d)
}

// Errorf appends an Error with the given code at pos.
func (l *List) Errorf(pos token.Position, code, format string, args ...any) {
	l.Add(Diagnostic{Pos: pos, Severity: Error, Code: code, Message: fmt.Sprintf(format, args...)})
}

// Warnf appends a Warning with the given code at pos.
func (l *List) Warnf(pos token.Position, code, format string, args ...any) {
	l.Add(Diagnostic{Pos: pos, Severity: Warning, Code: code, Message: fmt.Sprintf(format, args...)})
}

// Notef appends a Note at pos. Pass the code of the diagnostic the note
// explains (for example the position of a previous declaration after an
// E-NAME "redeclared" error).
func (l *List) Notef(pos token.Position, code, format string, args ...any) {
	l.Add(Diagnostic{Pos: pos, Severity: Note, Code: code, Message: fmt.Sprintf(format, args...)})
}

// Len returns the number of diagnostics of any severity.
func (l *List) Len() int { return len(l.Items) }

// ErrorCount returns the number of diagnostics with Severity Error.
func (l *List) ErrorCount() int {
	n := 0
	for _, d := range l.Items {
		if d.Severity == Error {
			n++
		}
	}
	return n
}

// HasErrors reports whether any diagnostic has Severity Error.
func (l *List) HasErrors() bool { return l.ErrorCount() > 0 }

// Sorted returns a copy of the diagnostics stably sorted by File, then
// Pos.Line, then Pos.Column. Diagnostics at the same position keep their
// insertion order. Duplicates are kept.
func (l *List) Sorted() []Diagnostic {
	out := slices.Clone(l.Items)
	slices.SortStableFunc(out, func(a, b Diagnostic) int {
		if c := cmp.Compare(a.File, b.File); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Pos.Line, b.Pos.Line); c != 0 {
			return c
		}
		return cmp.Compare(a.Pos.Column, b.Pos.Column)
	})
	return out
}

// Error returns the sorted diagnostics, one Diagnostic.Error() per line,
// joined by "\n" (no trailing newline), or "no errors" for an empty list.
func (l *List) Error() string {
	if len(l.Items) == 0 {
		return "no errors"
	}
	sorted := l.Sorted()
	lines := make([]string, len(sorted))
	for i, d := range sorted {
		lines[i] = d.Error()
	}
	return strings.Join(lines, "\n")
}

// Err returns l as an error if it contains at least one Error, else nil.
func (l *List) Err() error {
	if l.HasErrors() {
		return l
	}
	return nil
}

// Render returns Diagnostic.Render(source) for every diagnostic in Sorted
// order, concatenated. source is the text of l.File; diagnostics of other
// files are rendered without a source line.
func (l *List) Render(source []byte) string {
	var b strings.Builder
	for _, d := range l.Sorted() {
		if d.File != l.File {
			b.WriteString(d.Render(nil))
			continue
		}
		b.WriteString(d.Render(source))
	}
	return b.String()
}
