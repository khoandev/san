// Package markdown provides shared utilities for parsing markdown files.
package markdown

import (
	"fmt"
	"os"
	"strings"
)

// ParseFrontmatterFile reads a markdown file and returns (frontmatter, body).
// Frontmatter is the YAML content between opening and closing --- delimiters.
func ParseFrontmatterFile(path string) (frontmatter, body string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	return ParseFrontmatter(data)
}

// ParseFrontmatter splits an in-memory markdown document into its YAML
// frontmatter and its body, with the same rules as ParseFrontmatterFile. It
// exists for documents that are not on disk — embedded agent definitions,
// generated prompts — so both paths cannot disagree about where the delimiter
// is or how a CRLF file is normalised.
func ParseFrontmatter(data []byte) (frontmatter, body string, err error) {
	src := strings.ReplaceAll(string(data), "\r\n", "\n")
	first, rest, _ := strings.Cut(src, "\n")
	if strings.TrimSpace(first) != "---" {
		return "", strings.TrimSpace(src), nil
	}
	var fm strings.Builder
	for rest != "" {
		line, next, _ := strings.Cut(rest, "\n")
		if strings.TrimSpace(line) == "---" {
			return fm.String(), strings.TrimSpace(next), nil
		}
		fm.WriteString(line + "\n")
		rest = next
	}
	return "", "", fmt.Errorf("unclosed frontmatter: missing closing '---' delimiter")
}
