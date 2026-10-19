// package-preview is developer release tooling, not part of the installed
// Continuity runtime. It builds local archives; it never publishes or installs.
package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/gitenv"
)

var previewVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*$`)
var numericIdentifier = regexp.MustCompile(`^[0-9]+$`)
var supportedTargets = []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64"}

type asset struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type manifest struct {
	SchemaVersion        int     `json:"schema_version"`
	Version              string  `json:"version"`
	Channel              string  `json:"channel"`
	Commit               string  `json:"commit"`
	BuildTime            string  `json:"build_time"`
	MCPAPIVersion        int     `json:"mcp_api_version"`
	RuntimeQualification string  `json:"runtime_qualification"`
	Assets               []asset `json:"assets"`
}

func main() {
	var version, targets, output string
	flag.StringVar(&version, "version", "", "prerelease version, e.g. 0.1.0-preview.1 (required)")
	flag.StringVar(&targets, "targets", runtime.GOOS+"/"+runtime.GOARCH, "comma-separated OS/architecture targets, or all")
	flag.StringVar(&output, "output", "", "new output directory (default: ../../dist/previews/VERSION)")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(1)
	}
	if err := packagePreview(context.Background(), version, targets, output); err != nil {
		fmt.Fprintln(os.Stderr, "package-preview:", err)
		os.Exit(1)
	}
}

func parseTargets(value string) ([]string, error) {
	if value == "all" {
		return append([]string(nil), supportedTargets...), nil
	}
	seen := map[string]bool{}
	var result []string
	for _, target := range strings.Split(value, ",") {
		target = strings.TrimSpace(target)
		valid := false
		for _, candidate := range supportedTargets {
			valid = valid || target == candidate
		}
		if !valid || seen[target] {
			return nil, fmt.Errorf("unsupported or duplicate target %q", target)
		}
		seen[target] = true
		result = append(result, target)
	}
	sort.Strings(result)
	return result, nil
}

func packagePreview(ctx context.Context, version, targetList, output string) error {
	if !validPreviewVersion(version) {
		return fmt.Errorf("version must be a prerelease such as 0.1.0-preview.1")
	}
	targets, err := parseTargets(targetList)
	if err != nil {
		return err
	}
	module, err := os.Getwd()
	if err != nil {
		return err
	}
	goMod, err := os.ReadFile(filepath.Join(module, "go.mod"))
	if err != nil || !strings.HasPrefix(string(goMod), "module github.com/llewm/llewm/components/continuity\n") {
		return fmt.Errorf("run from components/continuity")
	}
	license, err := os.ReadFile(filepath.Join(module, "..", "..", "LICENSE"))
	if err != nil {
		return fmt.Errorf("read distribution license: %w", err)
	}
	notices, err := os.ReadFile(filepath.Join(module, "THIRD_PARTY_NOTICES.txt"))
	if err != nil || len(notices) == 0 {
		return fmt.Errorf("read nonempty dependency notices before packaging")
	}
	git := func(args ...string) (string, error) {
		body, err := gitenv.Command(ctx, append([]string{"-C", module}, args...)...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %w", args[0], err)
		}
		return strings.TrimSpace(string(body)), nil
	}
	status, err := git("status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("preview packages require a clean Git checkout; commit or remove outstanding changes first")
	}
	commit, err := git("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if output == "" {
		output = filepath.Join(module, "..", "..", "dist", "previews", version)
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		return fmt.Errorf("output must be a new directory: %s", output)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	// Stage beside the output on the home disk, never in the small /tmp tmpfs.
	stage, err := os.MkdirTemp(filepath.Dir(output), ".preview-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	builtAt := time.Now().UTC()
	m := manifest{SchemaVersion: 1, Version: version, Channel: "preview", Commit: commit, BuildTime: builtAt.Format(time.RFC3339), MCPAPIVersion: buildinfo.MCPAPIVersion, RuntimeQualification: "Cross-compilation is not runtime or client-integration qualification. See the coverage matrix and installation guide."}
	addAsset := func(name, kind, target string, files map[string][]byte, modes map[string]fs.FileMode) error {
		path := filepath.Join(stage, name)
		if err := writeArchive(path, files, modes, builtAt); err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(body)
		m.Assets = append(m.Assets, asset{Name: name, Kind: kind, Target: target, SHA256: hex.EncodeToString(hash[:]), Size: int64(len(body))})
		return nil
	}
	for _, target := range targets {
		osName, arch, _ := strings.Cut(target, "/")
		binaryName := "llewm"
		if osName == "windows" {
			binaryName += ".exe"
		}
		binaryPath := filepath.Join(stage, binaryName)
		ldflags := "-X github.com/llewm/llewm/components/continuity/internal/buildinfo.Version=" + version + " -X github.com/llewm/llewm/components/continuity/internal/buildinfo.Commit=" + commit + " -X github.com/llewm/llewm/components/continuity/internal/buildinfo.BuildTime=" + m.BuildTime
		command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=true", "-ldflags", ldflags, "-o", binaryPath, "./cmd/llewm")
		command.Dir = module
		for _, entry := range gitenv.Environ() {
			name, _, _ := strings.Cut(entry, "=")
			if name != "GOOS" && name != "GOARCH" && name != "CGO_ENABLED" && name != "GOFLAGS" && name != "GOWORK" {
				command.Env = append(command.Env, entry)
			}
		}
		command.Env = append(command.Env, "GOOS="+osName, "GOARCH="+arch, "CGO_ENABLED=0", "GOWORK=off")
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		fmt.Fprintln(os.Stderr, "Building", target)
		if err := command.Run(); err != nil {
			return fmt.Errorf("build %s: %w", target, err)
		}
		binary, err := os.ReadFile(binaryPath)
		if err != nil {
			return err
		}
		if err := addAsset("llewm-"+version+"-"+osName+"-"+arch+".zip", "binary-bundle", target, map[string][]byte{binaryName: binary, "INSTALL.txt": []byte(installText), "LICENSE": license, "THIRD_PARTY_NOTICES.txt": notices}, map[string]fs.FileMode{binaryName: 0o755}); err != nil {
			return err
		}
		if err := os.Remove(binaryPath); err != nil {
			return err
		}
	}
	for _, client := range []struct{ name, path, metadata string }{{"codex", "llewm", ".codex-plugin/plugin.json"}, {"claude-code", "claude-code", ".claude-plugin/plugin.json"}} {
		tracked, err := git("ls-files", "-z", "--", "plugins/"+client.path)
		if err != nil {
			return err
		}
		var paths []string
		for _, path := range strings.Split(strings.TrimSuffix(tracked, "\x00"), "\x00") {
			paths = append(paths, strings.TrimPrefix(path, "plugins/"+client.path+"/"))
		}
		files, err := pluginFiles(filepath.Join(module, "plugins", client.path), client.metadata, version, paths)
		if err != nil {
			return err
		}
		files["llewm/LICENSE"] = license
		if err := addAsset("llewm-"+version+"-"+client.name+".zip", "client-plugin", "", files, nil); err != nil {
			return err
		}
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err := os.WriteFile(filepath.Join(stage, "manifest.json"), body, 0o644); err != nil {
		return err
	}
	var sums strings.Builder
	for _, a := range m.Assets {
		fmt.Fprintf(&sums, "%s  %s\n", a.SHA256, a.Name)
	}
	hash := sha256.Sum256(body)
	fmt.Fprintf(&sums, "%x  manifest.json\n", hash)
	if err := os.WriteFile(filepath.Join(stage, "SHA256SUMS"), []byte(sums.String()), 0o644); err != nil {
		return err
	}
	finalStatus, err := git("status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	finalCommit, err := git("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if finalStatus != "" || finalCommit != commit {
		return fmt.Errorf("source changed while packaging; no preview was finalized (use an ignored or external output directory)")
	}
	if err := os.Rename(stage, output); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, output)
	return nil
}

func validPreviewVersion(value string) bool {
	if !previewVersion.MatchString(value) {
		return false
	}
	_, suffix, _ := strings.Cut(value, "-")
	for _, part := range strings.Split(suffix, ".") {
		if numericIdentifier.MatchString(part) && len(part) > 1 && part[0] == '0' {
			return false
		}
	}
	return true
}

func pluginFiles(root, metadata, version string, paths []string) (map[string][]byte, error) {
	files := map[string][]byte{}
	for _, rel := range paths {
		if !fs.ValidPath(rel) || strings.Contains(rel, "\\") {
			return nil, fmt.Errorf("unsafe plugin path %q", rel)
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		entry, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !entry.Mode().IsRegular() {
			return nil, fmt.Errorf("plugin contains a non-regular file: %s", path)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if filepath.ToSlash(rel) == metadata {
			var value map[string]any
			if err := json.Unmarshal(body, &value); err != nil {
				return nil, err
			}
			value["version"] = version
			body, err = json.MarshalIndent(value, "", "  ")
			if err != nil {
				return nil, err
			}
			body = append(body, '\n')
		}
		files["llewm/"+filepath.ToSlash(rel)] = body
	}
	if files["llewm/"+metadata] == nil {
		return nil, fmt.Errorf("plugin metadata missing: %s", metadata)
	}
	return files, nil
}

func writeArchive(path string, files map[string][]byte, modes map[string]fs.FileMode, at time.Time) (err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()
	archive := zip.NewWriter(file)
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !fs.ValidPath(name) || strings.Contains(name, "\\") {
			archive.Close()
			return fmt.Errorf("unsafe archive path %q", name)
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: at}
		mode := modes[name]
		if mode == 0 {
			mode = 0o644
		}
		header.SetMode(mode)
		writer, err := archive.CreateHeader(header)
		if err == nil {
			_, err = writer.Write(files[name])
		}
		if err != nil {
			archive.Close()
			return err
		}
	}
	return archive.Close()
}

const installText = `Llewm preview

Check SHA256SUMS against the downloaded archive before extracting. Checksums
detect corruption; they are not signatures or proof of publisher authenticity.
On Linux use sha256sum -c SHA256SUMS; on macOS use shasum -a 256 -c SHA256SUMS.
On Windows use Get-FileHash -Algorithm SHA256 and compare with SHA256SUMS.

Place llewm (llewm.exe on Windows) in a directory on your user PATH.
Restart terminals, coding-agent clients and parent IDEs after changing PATH.
Run llewm version, then llewm doctor in each initialized project.
For a new project, run llewm init at its Git root before doctor.
Back up and explicitly migrate an existing database if doctor requires it.

Use the matching Codex or Claude Code plugin archive from this same preview.
Extract it and follow the repository's client-integrations guide for the local
plugin registration method supported by your client. Review hooks in the client.
The plugin's llewm directory contains skills, hooks and .mcp.json.

This archive does not install plugins or migrate databases.
Cross-compilation does not establish platform or client runtime qualification.
`
