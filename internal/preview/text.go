package preview

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// binaryBadRatio is the fraction of "bad" bytes (neither valid UTF-8
// text nor printable ASCII/whitespace) above which a head is treated
// as binary.
const binaryBadRatio = 0.30

// IsBinary reports whether head looks like binary data: any NUL byte,
// or more than 30% of the bytes being neither valid UTF-8 text nor
// printable ASCII/whitespace. An empty head is not binary.
func IsBinary(head []byte) bool {
	if len(head) == 0 {
		return false
	}
	bad := 0
	for i := 0; i < len(head); {
		b := head[i]
		if b == 0x00 {
			return true
		}
		if b < utf8.RuneSelf {
			// ASCII: printable and common whitespace are fine,
			// other control bytes are bad.
			if b >= 0x20 || b == '\n' || b == '\r' || b == '\t' || b == '\f' || b == '\v' {
				i++
				continue
			}
			bad++
			i++
			continue
		}
		r, size := utf8.DecodeRune(head[i:])
		if r == utf8.RuneError && size == 1 {
			bad++
			i++
			continue
		}
		i += size
	}
	return float64(bad)/float64(len(head)) > binaryBadRatio
}

// ReadCapped reads at most maxKB KiB of the file at path, sanitized to
// valid UTF-8. truncated reports that the file was longer than the
// cap; size is the file's full size on disk.
func ReadCapped(path string, maxKB int) (content string, truncated bool, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", false, 0, err
	}
	size = fi.Size()
	limit := int64(maxKB) * 1024
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return "", false, size, err
	}
	return strings.ToValidUTF8(string(data), "\uFFFD"), size > int64(len(data)), size, nil
}

// langByExt maps lowercased file extensions (no dot) to highlight.js
// language names.
var langByExt = map[string]string{
	"go":    "go",
	"js":    "javascript",
	"jsx":   "javascript",
	"ts":    "typescript",
	"tsx":   "typescript",
	"py":    "python",
	"rs":    "rust",
	"c":     "c",
	"h":     "c",
	"cpp":   "cpp",
	"hpp":   "cpp",
	"java":  "java",
	"kt":    "kotlin",
	"swift": "swift",
	"rb":    "ruby",
	"php":   "php",
	"sh":    "bash",
	"bash":  "bash",
	"zsh":   "bash",
	"json":  "json",
	"yaml":  "yaml",
	"yml":   "yaml",
	"toml":  "ini",
	"ini":   "ini",
	"xml":   "xml",
	"html":  "xml",
	"css":   "css",
	"scss":  "scss",
	"md":    "markdown",
	"sql":   "sql",
	"diff":  "diff",
	"patch": "diff",
	"cs":    "csharp",
	"zig":   "zig",
	"lua":   "lua",
	"vim":   "vim",
}

// langByName maps whole lowercased base names (extension-less build
// files) to highlight.js language names.
var langByName = map[string]string{
	"dockerfile":  "dockerfile",
	"makefile":    "makefile",
	"gnumakefile": "makefile",
}

// LangHint returns the highlight.js language name for a file name --
// by whole-name match first (Dockerfile, Makefile), then by extension
// -- or "" when unknown.
func LangHint(name string) string {
	base := strings.ToLower(filepath.Base(name))
	if lang, ok := langByName[base]; ok {
		return lang
	}
	ext := strings.TrimPrefix(filepath.Ext(base), ".")
	return langByExt[ext]
}
