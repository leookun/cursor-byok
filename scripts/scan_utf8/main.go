package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// scan_utf8 reports lines containing invalid UTF-8 bytes in .go files.
// Usage: go run ./scripts/scan_utf8 <path> [<path>...]
// A path may be a file or a directory (recursively scans *.go).
func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: scan_utf8 <path> [...]")
		os.Exit(2)
	}
	total := 0
	for _, arg := range os.Args[1:] {
		info, err := os.Stat(arg)
		if err != nil {
			fmt.Printf("stat %s: %v\n", arg, err)
			continue
		}
		if info.IsDir() {
			filepath.WalkDir(arg, func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
					return nil
				}
				total += scanFile(p)
				return nil
			})
		} else {
			total += scanFile(arg)
		}
	}
	fmt.Printf("\n=== total lines with invalid UTF-8: %d ===\n", total)
}

func scanFile(path string) int {
	f, err := os.Open(path)
	if err != nil {
		fmt.Printf("open %s: %v\n", path, err)
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	line := 0
	count := 0
	for sc.Scan() {
		line++
		b := sc.Bytes()
		if utf8.Valid(b) {
			continue
		}
		count++
		fmt.Printf("%s:%d: %s\n", path, line, renderInvalid(b))
	}
	return count
}

// renderInvalid shows the line with invalid bytes as \xNN so the exact
// corrupted positions are visible while valid UTF-8 stays readable.
func renderInvalid(b []byte) string {
	var sb strings.Builder
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size == 1 {
			sb.WriteString(fmt.Sprintf("\\x%02X", b[i]))
			i++
			continue
		}
		sb.WriteString(string(b[i : i+size]))
		i += size
	}
	return sb.String()
}