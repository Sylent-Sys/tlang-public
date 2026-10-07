package main

import (
	"tlang/diag"
	"tlang/token"
)

// Position mapping is ASCII-correct in the first cut: one byte counts as one
// LSP character unit, so the character axis equals the byte count from the
// start of the line. For source containing multi-byte UTF-8 on the cursor
// line the character axis is miscounted relative to the LSP UTF-16 contract;
// upgrading to UTF-16 code-unit counting is a localized change behind this
// same API with no caller changes. UTF-16 support is deferred (see docs/LSP.md).

// lspToOffset maps an LSP Position (0-based line, byte-counted character) to a
// 0-based byte offset into src. Inputs are clamped: the result is always in
// [0, len(src)], so a bad position never panics or indexes out of range.
func lspToOffset(src []byte, line, character int) int {
	if line < 0 {
		line = 0
	}
	if character < 0 {
		character = 0
	}
	offset := 0
	curLine := 0
	for curLine < line && offset < len(src) {
		if src[offset] == '\n' {
			curLine++
		}
		offset++
	}
	// Advance over the characters on the target line, stopping at a newline or
	// end of input.
	col := 0
	for col < character && offset < len(src) && src[offset] != '\n' {
		offset++
		col++
	}
	if offset > len(src) {
		offset = len(src)
	}
	return offset
}

// offsetToLSP maps a 0-based byte offset into src to a 0-based LSP Position.
// The offset is clamped to [0, len(src)] and the result coordinates are never
// negative.
func offsetToLSP(src []byte, offset int) (line, character int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(src) {
		offset = len(src)
	}
	for i := 0; i < offset; i++ {
		if src[i] == '\n' {
			line++
			character = 0
		} else {
			character++
		}
	}
	return line, character
}

// posToLSPRange maps a 1-based token.Position to an LSP Range. An invalid
// position (Line == 0) maps to the zero-width range at document start so the
// message is shown but never dropped or negative. Otherwise the start is
// (Line-1, Column-1) and the end is widened over the run of ASCII identifier
// characters at the start offset, giving a visible squiggle on a name or
// keyword; when the start is not on such a character the range is zero-width.
func posToLSPRange(src []byte, pos token.Position) Range {
	if !pos.IsValid() {
		return Range{}
	}
	line := pos.Line - 1
	character := pos.Column - 1
	if line < 0 {
		line = 0
	}
	if character < 0 {
		character = 0
	}
	start := Position{Line: line, Character: character}

	end := start
	off := pos.Offset
	for off >= 0 && off < len(src) && isIdentByte(src[off]) {
		off++
		end.Character++
	}
	return Range{Start: start, End: end}
}

// convertDiagnostic maps a diag.Diagnostic to an LSP Diagnostic. Severity maps
// Error(0)->1, Warning(1)->2, Note(2)->3 (Information). The source is tagged
// "tlang".
func convertDiagnostic(src []byte, d diag.Diagnostic) Diagnostic {
	return Diagnostic{
		Range:    posToLSPRange(src, d.Pos),
		Severity: convertSeverity(d.Severity),
		Code:     d.Code,
		Source:   "tlang",
		Message:  d.Message,
	}
}

func convertSeverity(s diag.Severity) int {
	switch s {
	case diag.Error:
		return severityError
	case diag.Warning:
		return severityWarning
	case diag.Note:
		return severityInformation
	}
	return severityError
}

// isIdentByte reports whether b is an ASCII identifier character [A-Za-z0-9_].
func isIdentByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}
