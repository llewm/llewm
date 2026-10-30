package recordsync

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

const manifestHeader = "llewm-history-manifest-v1\n"
const MaxManifestEntries = 128
const MaxManifestBytes = 16 << 10

// Manifest catalogs immutable block identities, not semantic import receipts.
// A radix branch has at most sixteen children; a leaf has at most 128 blocks.
// Hashes and prefixes bind each child to exactly one position in the tree.
type Manifest struct {
	Hash, Prefix string
	Bytes        []byte
	Blocks       []string
	Children     map[string]string
}

// BuildManifest returns children before parents. Hash partitioning preserves
// unaffected subtrees when late arrivals change the inventory. Neither event
// timestamps nor canonical record ordering determine the tree shape.
func BuildManifest(lineage string, hashes []string) ([]Manifest, error) {
	if !lineagePattern.MatchString(lineage) || len(hashes) > portable.MaxRecordCount {
		return nil, fmt.Errorf("invalid manifest lineage or block count")
	}
	values := append([]string(nil), hashes...)
	sort.Strings(values)
	for i, hash := range values {
		if !hex64.MatchString(hash) || (i > 0 && hash == values[i-1]) {
			return nil, fmt.Errorf("invalid or duplicate manifest block")
		}
	}
	var nodes []Manifest
	var build func(string, []string) error
	build = func(prefix string, values []string) error {
		m := Manifest{Prefix: prefix}
		if len(values) <= MaxManifestEntries {
			m.Blocks = values
		} else {
			m.Children = make(map[string]string)
			for start := 0; start < len(values); {
				end := start + 1
				child := values[start][:len(prefix)+1]
				for end < len(values) && strings.HasPrefix(values[end], child) {
					end++
				}
				if err := build(child, values[start:end]); err != nil {
					return err
				}
				m.Children[child] = nodes[len(nodes)-1].Hash
				start = end
			}
		}
		var b strings.Builder
		b.WriteString(manifestHeader + lineage + "\n" + prefix + "\n")
		if m.Children == nil {
			b.WriteString("blocks\n")
			for _, hash := range values {
				b.WriteString(hash + "\n")
			}
		} else {
			b.WriteString("children\n")
			for _, digit := range "0123456789abcdef" {
				child := prefix + string(digit)
				if hash, ok := m.Children[child]; ok {
					b.WriteString(child + " " + hash + "\n")
				}
			}
		}
		m.Bytes = []byte(b.String())
		m.Hash = Hash(b.String())
		if len(nodes) >= MaxScanEntries {
			return fmt.Errorf("manifest node count exceeds bounds")
		}
		nodes = append(nodes, m)
		return nil
	}
	if err := build("", values); err != nil {
		return nil, err
	}
	return nodes, nil
}

// DecodeManifest checks canonical framing, lineage, content identity and the
// expected radix position before any references may be followed.
func DecodeManifest(lineage, hash, prefix string, body []byte) (Manifest, error) {
	var m Manifest
	if !lineagePattern.MatchString(lineage) || !hex64.MatchString(hash) || len(body) > MaxManifestBytes || len(prefix) > 64 || strings.Trim(prefix, "0123456789abcdef") != "" || Hash(string(body)) != hash {
		return m, fmt.Errorf("invalid manifest identity")
	}
	header := manifestHeader + lineage + "\n" + prefix + "\n"
	if !bytes.HasPrefix(body, []byte(header)) || len(body) <= len(header) || body[len(body)-1] != '\n' {
		return m, fmt.Errorf("invalid manifest framing")
	}
	lines := strings.Split(string(body[len(header):len(body)-1]), "\n")
	m = Manifest{Hash: hash, Prefix: prefix, Bytes: append([]byte(nil), body...)}
	switch lines[0] {
	case "blocks":
		if len(lines)-1 > MaxManifestEntries {
			return Manifest{}, fmt.Errorf("manifest leaf exceeds bounds")
		}
		for i, value := range lines[1:] {
			if !hex64.MatchString(value) || !strings.HasPrefix(value, prefix) || (i > 0 && value <= m.Blocks[i-1]) {
				return Manifest{}, fmt.Errorf("invalid manifest block")
			}
			m.Blocks = append(m.Blocks, value)
		}
	case "children":
		if len(prefix) >= 64 || len(lines) < 2 || len(lines) > 17 {
			return Manifest{}, fmt.Errorf("manifest branch exceeds bounds")
		}
		m.Children = make(map[string]string)
		prior := ""
		for _, value := range lines[1:] {
			key, hash, ok := strings.Cut(value, " ")
			if !ok || len(key) != len(prefix)+1 || !strings.HasPrefix(key, prefix) || strings.Trim(key, "0123456789abcdef") != "" || !hex64.MatchString(hash) || key <= prior {
				return Manifest{}, fmt.Errorf("invalid manifest child")
			}
			m.Children[key] = hash
			prior = key
		}
	default:
		return Manifest{}, fmt.Errorf("unknown manifest kind")
	}
	return m, nil
}

func manifestPath(lineage, hash string) (string, error) {
	base, err := BlockDirectory(lineage)
	if err != nil || !hex64.MatchString(hash) {
		return "", fmt.Errorf("invalid manifest path")
	}
	return filepath.Join(filepath.Dir(base), "manifests", hash[:2], hash+".manifest"), nil
}

func ManifestRootPath(lineage, hash string) (string, error) {
	base, err := BlockDirectory(lineage)
	if err != nil || !hex64.MatchString(hash) {
		return "", fmt.Errorf("invalid manifest root")
	}
	return filepath.Join(filepath.Dir(base), "manifest-roots", hash+".root"), nil
}

// PublishManifest durably writes all child nodes before its immutable root.
// The caller publishes and validates the referenced blocks first. A published
// root describes that snapshot only, never endpoint completeness or delivery.
// Concurrent snapshots have separate roots; this function removes no history.
func PublishManifest(ctx context.Context, root *os.Root, lineage string, hashes []string) (string, []string, error) {
	nodes, err := BuildManifest(lineage, hashes)
	if err != nil {
		return "", nil, err
	}
	var paths []string
	for _, node := range nodes {
		if err := ctx.Err(); err != nil {
			return "", paths, err
		}
		path, err := manifestPath(lineage, node.Hash)
		if err != nil {
			return "", paths, err
		}
		if _, err := WriteFile(root, path, node.Bytes, false); err != nil {
			return "", paths, err
		}
		paths = append(paths, path)
	}
	hash := nodes[len(nodes)-1].Hash
	path, err := ManifestRootPath(lineage, hash)
	if err != nil {
		return "", paths, err
	}
	if err := ctx.Err(); err != nil {
		return "", paths, err
	}
	if _, err := WriteFile(root, path, []byte(manifestHeader+lineage+"\n"+hash+"\n"), false); err != nil {
		return "", paths, err
	}
	return hash, append(paths, path), nil
}

// ManifestBlocks reads a complete snapshot, validating every manifest node.
// It deliberately does not trust a manifest as proof of block byte validity.
func ManifestBlocks(ctx context.Context, root *os.Root, lineage, hash string) ([]string, error) {
	if err := readManifestAnnouncement(root, lineage, hash); err != nil {
		return nil, err
	}
	var result []string
	visited := 0
	var walk func(string, string) error
	walk = func(hash, prefix string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		visited++
		if visited > MaxScanEntries {
			return fmt.Errorf("manifest traversal exceeds bounds")
		}
		path, err := manifestPath(lineage, hash)
		if err != nil {
			return err
		}
		body, err := ReadFile(root, path, MaxManifestBytes)
		if err != nil {
			return err
		}
		m, err := DecodeManifest(lineage, hash, prefix, body)
		if err != nil {
			return err
		}
		result = append(result, m.Blocks...)
		if len(result) > portable.MaxRecordCount {
			return fmt.Errorf("manifest blocks exceed bounds")
		}
		for _, digit := range "0123456789abcdef" {
			child := prefix + string(digit)
			if hash, ok := m.Children[child]; ok {
				if err := walk(hash, child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(hash, ""); err != nil {
		return nil, err
	}
	return result, nil
}
