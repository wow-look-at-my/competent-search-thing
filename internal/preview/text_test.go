package preview

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsBinary(t *testing.T) {
	cases := []struct {
		name string
		head []byte
		want bool
	}{
		{"empty", nil, false},
		{"plain ascii", []byte("package preview\n\nfunc main() {}\n"), false},
		{"ascii with tabs and crlf", []byte("a\tb\r\nc\f\v"), false},
		{"nul byte anywhere", []byte("text\x00text"), true},
		{"elf header", append([]byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0}, make([]byte, 8)...), true},
		{"valid multibyte utf-8", []byte("caf\xc3\xa9 \xe6\x97\xa5\xe6\x9c\xac\xe8\xaa\x9e"), false},
		{"mostly control bytes", bytes.Repeat([]byte{0x01, 0x02, 0x03, 'a'}, 8), true},
		{"a stray bad byte in text", append([]byte("mostly perfectly fine text"), 0xff), false},
		{"mostly invalid utf-8", bytes.Repeat([]byte{0xff, 0xfe, 'a'}, 8), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, IsBinary(tc.head))
		})
	}
}

func TestReadCapped(t *testing.T) {
	dir := t.TempDir()

	small := filepath.Join(dir, "small.txt")
	require.NoError(t, os.WriteFile(small, []byte("hello preview"), 0o644))
	content, truncated, size, err := ReadCapped(small, 4)
	require.NoError(t, err)
	require.Equal(t, "hello preview", content)
	require.False(t, truncated)
	require.Equal(t, int64(len("hello preview")), size)

	big := filepath.Join(dir, "big.txt")
	require.NoError(t, os.WriteFile(big, bytes.Repeat([]byte("0123456789abcdef"), 1024), 0o644)) // 16 KiB
	content, truncated, size, err = ReadCapped(big, 4)
	require.NoError(t, err)
	require.Len(t, content, 4*1024)
	require.True(t, truncated)
	require.Equal(t, int64(16*1024), size)

	invalid := filepath.Join(dir, "invalid.txt")
	require.NoError(t, os.WriteFile(invalid, []byte("ok\xffbad"), 0o644))
	content, truncated, _, err = ReadCapped(invalid, 4)
	require.NoError(t, err)
	require.False(t, truncated)
	require.Equal(t, "ok\uFFFDbad", content, "invalid bytes are replaced, output is valid UTF-8")

	_, _, _, err = ReadCapped(filepath.Join(dir, "missing.txt"), 4)
	require.Error(t, err)
}

func TestLangHint(t *testing.T) {
	cases := map[string]string{
		"/src/main.go":        "go",
		"app.js":              "javascript",
		"widget.tsx":          "typescript",
		"script.PY":           "python",
		"lib.rs":              "rust",
		"port.c":              "c",
		"port.h":              "c",
		"engine.cpp":          "cpp",
		"engine.hpp":          "cpp",
		"Main.java":           "java",
		"App.kt":              "kotlin",
		"App.swift":           "swift",
		"tool.rb":             "ruby",
		"index.php":           "php",
		"run.sh":              "bash",
		"run.zsh":             "bash",
		"data.json":           "json",
		"ci.yaml":             "yaml",
		"ci.yml":              "yaml",
		"Cargo.toml":          "ini",
		"setup.ini":           "ini",
		"doc.xml":             "xml",
		"page.html":           "xml",
		"style.css":           "css",
		"style.scss":          "scss",
		"README.md":           "markdown",
		"schema.sql":          "sql",
		"fix.diff":            "diff",
		"fix.patch":           "diff",
		"Program.cs":          "csharp",
		"build.zig":           "zig",
		"init.lua":            "lua",
		"config.vim":          "vim",
		"/app/Dockerfile":     "dockerfile",
		"Makefile":            "makefile",
		"GNUmakefile":         "makefile",
		"photo.png":           "",
		"noext":               "",
		"archive.tar.unknown": "",
	}
	for name, want := range cases {
		require.Equal(t, want, LangHint(name), "LangHint(%q)", name)
	}
	require.GreaterOrEqual(t, len(langByExt), 30, "the extension map stays comprehensive")
}

func TestLangHintCaseInsensitive(t *testing.T) {
	require.Equal(t, "makefile", LangHint("/x/makefile"))
	require.Equal(t, "dockerfile", LangHint("DOCKERFILE"))
	require.Equal(t, "go", LangHint("WEIRD.GO"))
}

func TestReadCappedExactCapNotTruncated(t *testing.T) {
	dir := t.TempDir()
	exact := filepath.Join(dir, "exact.txt")
	require.NoError(t, os.WriteFile(exact, []byte(strings.Repeat("a", 1024)), 0o644))
	content, truncated, size, err := ReadCapped(exact, 1)
	require.NoError(t, err)
	require.Len(t, content, 1024)
	require.False(t, truncated, "a file exactly at the cap is not truncated")
	require.Equal(t, int64(1024), size)
}
