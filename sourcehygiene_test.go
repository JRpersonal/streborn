package streborn_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// No source file in this repository may carry a stray control character.
//
// Three of them shipped. A writer that reads a CSS escape as a C escape turns
// "\203A" into U+0083 followed by "A", and "\25B8" into U+0015 followed by
// "B8", because \203 is octal 0x83 and \25 is octal 0x15. The stylesheet then
// says to draw a C1 control where a chevron belongs, and a browser draws
// nothing, or a box, and prints the leftover letters beside it.
//
// The damage is quiet in every tool a person would use to look for it. A
// console shows U+0083 as a plain A. A diff shows one changed line that reads
// correctly. A linter has no opinion. So the help expanders printed "B8" and
// "BE" at users from 2026-08-16 (v0.9.48) until 2026-10-02, and the button that
// puts the remote on a phone's home screen grew a stray character the same way
// on the morning of the day it was found, by the same mistake in the same hour.
//
// The fix for the bug is three escapes. The fix for the CLASS is this test.
//
// Tab, newline and carriage return are the three controls that belong in text.
// Everything else below 0x20, plus DEL and the C1 block 0x80 to 0x9F, is either
// an accident or something that wanted to be a byte in a binary file.
func TestNoStrayControlCharactersInSource(t *testing.T) {
	// Deliberate separators, verified by reading them: both build a signature
	// string out of fields and need a byte that cannot occur inside a field.
	// Named with their line content so moving them does not silently widen the
	// exemption, and so a future reader can tell a decision from an accident.
	allowed := map[string]string{
		"desktop-app/frontend/src/main.js":         "preset signature joined on U+001F and U+001E",
		"internal/webui/assets/index.html":         "preset signature joined on U+0000 and U+0001",
		".claude/skills/str-gh-replies/partial.py": "scores on a separator byte",
	}

	skipDir := map[string]bool{
		".git": true, "node_modules": true, "dist": true, "build": true,
		"bin": true, "__pycache__": true, "vendor": true, "coverage": true,
		".idea": true, ".vscode": true,
	}
	// Binaries, archives and anything else that holds arbitrary bytes on
	// purpose. A diagnostic .msg from a reporter is in here too: it is somebody
	// else's file and not ours to judge.
	skipExt := map[string]bool{
		".exe": true, ".dll": true, ".so": true, ".dylib": true, ".png": true,
		".jpg": true, ".jpeg": true, ".gif": true, ".ico": true, ".zip": true,
		".gz": true, ".tgz": true, ".pdf": true, ".woff": true, ".woff2": true,
		".ttf": true, ".otf": true, ".msg": true, ".bin": true, ".img": true,
		".stu": true, ".webp": true, ".mp3": true, ".ogg": true, ".wav": true,
		".p12": true, ".armv7l": true,
	}

	root := "."
	var bad []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if skipExt[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() > 8<<20 {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		// A file that is not text is not this test's business, and the embedded
		// ARM binaries are tracked as zero-byte stubs locally and as megabytes
		// in CI.
		if !utf8.Valid(data) {
			return nil
		}
		rel := filepath.ToSlash(path)
		rel = strings.TrimPrefix(rel, "./")
		if _, ok := allowed[rel]; ok {
			return nil
		}
		for i, r := range string(data) {
			if r == '\t' || r == '\n' || r == '\r' {
				continue
			}
			if r < 0x20 || r == 0x7F || (r >= 0x80 && r <= 0x9F) {
				line := 1 + strings.Count(string(data[:i]), "\n")
				bad = append(bad, rel+":"+itoa(line)+" carries U+"+hex4(r))
				break // one per file is enough to send somebody looking
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
	if len(bad) > 0 {
		t.Errorf("stray control characters, most likely a backslash escape written as a byte:\n  %s",
			strings.Join(bad, "\n  "))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func hex4(r rune) string {
	const d = "0123456789ABCDEF"
	return string([]byte{d[(r>>12)&0xF], d[(r>>8)&0xF], d[(r>>4)&0xF], d[r&0xF]})
}
