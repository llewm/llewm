package instructions

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Section struct {
	Source      string `json:"source"`
	HeadingPath string `json:"heading_path"`
	Content     string `json:"content"`
	Hash        string `json:"hash"`
	Precedence  int    `json:"precedence"`
}

// Discover reads only known instruction files. The line scanner is intentionally
// conservative; a Markdown AST implementation will replace it before v0.1.
func Discover(root string) ([]Section, error) {
	files := []struct {
		path       string
		precedence int
	}{
		{filepath.Join(root, "AGENTS.md"), 100},
		{filepath.Join(root, "AGENTS.override.md"), 200},
		{filepath.Join(root, "CLAUDE.md"), 100},
	}
	var sections []Section
	for _, candidate := range files {
		content, err := readSource(candidate.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sections = append(sections, split(candidate.path, string(content), candidate.precedence)...)
	}
	return sections, nil
}

// DiscoverGlobal reads only explicitly supported sources, never client settings,
// shell profiles, or arbitrary files in the engineer's home directory.
func DiscoverGlobal(root string) ([]Section, error) {
	if root == "" {
		return nil, nil
	}
	var sections []Section
	for _, name := range []string{"AGENTS.md", "CONTEXT.md", "PROMPTS.md"} {
		path := filepath.Join(root, name)
		content, err := readSource(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sections = append(sections, split(path, string(content), 50)...)
	}
	return sections, nil
}

const maxSourceBytes = 1 << 20

func readSource(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	content, err := io.ReadAll(io.LimitReader(f, maxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxSourceBytes {
		return nil, fmt.Errorf("instruction source %s exceeds 1 MiB limit", path)
	}
	return content, nil
}

func split(path, body string, precedence int) []Section {
	var result []Section
	heading := "document"
	var content strings.Builder
	flush := func() {
		text := strings.TrimSpace(content.String())
		if text == "" {
			return
		}
		sum := sha256.Sum256([]byte(text))
		result = append(result, Section{Source: path, HeadingPath: heading, Content: text, Hash: hex.EncodeToString(sum[:]), Precedence: precedence})
		content.Reset()
	}
	scanner := bufio.NewScanner(strings.NewReader(body))
	// readSource bounds inputs; accept long lines without silent scanner truncation.
	scanner.Buffer(make([]byte, 4096), maxSourceBytes+1)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			trimmed := strings.TrimLeft(line, "#")
			if strings.HasPrefix(trimmed, " ") {
				flush()
				heading = strings.TrimSpace(trimmed)
				continue
			}
		}
		content.WriteString(line)
		content.WriteByte('\n')
	}
	flush()
	return result
}
