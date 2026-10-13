package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimalPM = `{"schema_version":1,"manifest":{"id":"adapter","version":"1","provider":"example","contract_versions":[1],"capabilities":["read_snapshot"],"resource_types":["work_item"],"mapping_profile":"basic","mapping_version":"1"},"project":{"provider":"example","instance":"tenant","project":"project"},"snapshot":{"complete":true,"resources":[],"relationships":[]},"proposals":[]}`

func TestPMInspectWithoutDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "packet.json")
	if err := os.WriteFile(path, []byte(minimalPM), 0600); err != nil {
		t.Fatal(err)
	}
	var previous string
	for i := 0; i < 2; i++ {
		cmd := newRootCommand("llewm")
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"--project-root", filepath.Join(dir, "absent"), "pm", "inspect", "--file", path, "--provider", "example", "--instance", "tenant", "--project", "project"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), `"state": "valid"`) {
			t.Fatal(out.String())
		}
		if i > 0 && out.String() != previous {
			t.Fatal("nondeterministic output")
		}
		previous = out.String()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "packet.json" {
		t.Fatalf("unexpected writes: %v", entries)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != minimalPM {
		t.Fatal("modified input")
	}
}

func TestPMInspectRejectsFilesWithoutEcho(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{"invalid": "secret=DO_NOT_PRINT", "large": strings.Repeat("x", (1<<20)+1)} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := newRootCommand("llewm")
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"pm", "inspect", "--file", path, "--provider", "example", "--instance", "tenant", "--project", "project"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("accepted invalid")
		}
		if strings.Contains(err.Error()+out.String(), "DO_NOT_PRINT") || out.Len() != 0 {
			t.Fatal("input echoed")
		}
	}
	for _, path := range []string{dir, filepath.Join(dir, "token=DO_NOT_PRINT")} {
		if _, err := readPMFile(path); err == nil || strings.Contains(err.Error(), "DO_NOT_PRINT") {
			t.Fatal("bad file error")
		}
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "invalid"), link); err == nil {
		if _, err := readPMFile(link); err == nil {
			t.Fatal("accepted symlink")
		}
	}
}
