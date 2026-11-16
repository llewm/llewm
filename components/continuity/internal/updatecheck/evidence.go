package updatecheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
)

// VerificationReceipt contains publisher claims, not executable instructions.
// Acquisition and publisher authentication are deliberately separate from this
// offline comparison. A matching receipt never qualifies an interactive client.
type VerificationReceipt struct {
	SchemaVersion int                 `json:"schema_version"`
	Repository    string              `json:"repository"`
	Commit        string              `json:"commit"`
	Target        string              `json:"target"`
	GoVersion     string              `json:"go_version"`
	RunURL        string              `json:"run_url"`
	Status        string              `json:"status"`
	Checks        []VerificationCheck `json:"checks"`
	Artifact      *Asset              `json:"artifact,omitempty"`
}

type VerificationCheck struct {
	Component string `json:"component"`
	Name      string `json:"name"`
	Status    string `json:"status"`
}

type EvidenceReport struct {
	State                   string               `json:"state"`
	Reason                  string               `json:"reason"`
	Commit                  string               `json:"commit"`
	Target                  string               `json:"target"`
	Receipt                 *VerificationReceipt `json:"receipt,omitempty"`
	ArtifactVerified        bool                 `json:"artifact_verified"`
	PublisherAuthentication string               `json:"publisher_authentication"`
}

var platformPattern = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+$`)
var goVersionPattern = regexp.MustCompile(`^go[0-9]+\.[0-9]+(?:\.[0-9]+)?(?:rc[0-9]+|beta[0-9]+)?$`)

func httpsURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

// Evidence limits receipt input to 1 MiB and performs no network, process or database work.
// Non-passing observations are data; invalid selection arguments are errors.
func Evidence(path, commit, target, repository, artifact string) (EvidenceReport, error) {
	r := EvidenceReport{Commit: commit, Target: target, PublisherAuthentication: "not_verified"}
	if !commitPattern.MatchString(commit) || !platformPattern.MatchString(target) || !httpsURL(repository) {
		return r, fmt.Errorf("select a full lowercase commit, OS/architecture and HTTPS repository URL")
	}
	finish := func(state, reason string) (EvidenceReport, error) {
		r.State, r.Reason = state, reason
		return r, nil
	}
	if path == "" {
		return finish("missing", "No receipt supplied; upstream test results are unknown. Do not run local tests implicitly.")
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return finish("missing", "The selected receipt does not exist.")
	}
	if err != nil {
		return finish("unavailable", "The selected receipt cannot be read.")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return finish("unavailable", "Reading the selected receipt failed.")
	}
	var receipt VerificationReceipt
	if len(b) > 1<<20 || json.Unmarshal(b, &receipt) != nil {
		return finish("invalid", "Receipt must be valid JSON no larger than 1 MiB.")
	}
	if receipt.SchemaVersion != 1 || !commitPattern.MatchString(receipt.Commit) ||
		!platformPattern.MatchString(receipt.Target) || !goVersionPattern.MatchString(receipt.GoVersion) ||
		!httpsURL(receipt.Repository) || !httpsURL(receipt.RunURL) || len(receipt.Checks) > 64 {
		return finish("invalid", "Unsupported or incomplete receipt identity.")
	}
	if receipt.Commit != commit || receipt.Target != target || receipt.Repository != repository {
		return finish("mismatch", "Receipt repository, exact commit or native test platform differs from the selected source.")
	}
	r.Receipt = &receipt
	required := map[string]bool{}
	for _, component := range []string{"continuity", "action-control", "orchestration", "privatestorage"} {
		for _, name := range []string{"test", "vet", "fmt", "build", "standalone-build"} {
			required["components/"+component+":"+name] = true
		}
	}
	required["components/continuity:python-tests"] = true
	seen := map[string]bool{}
	failed, pending := false, false
	for _, check := range receipt.Checks {
		key := check.Component + ":" + check.Name
		if seen[key] || !required[key] {
			return finish("invalid", "Duplicate or unknown verification check.")
		}
		seen[key] = true
		switch check.Status {
		case "passed":
		case "failed":
			failed = true
		case "pending", "skipped":
			pending = true
		default:
			return finish("invalid", "Unknown check result.")
		}
	}
	if receipt.Status != "passed" && receipt.Status != "failed" && receipt.Status != "pending" {
		return finish("invalid", "Unknown receipt result.")
	}
	// Do not hide a known failed/pending run behind a missing bundle digest.
	if failed || receipt.Status == "failed" {
		return finish("failed", "Upstream receipt reports failed development checks.")
	}
	if pending || receipt.Status == "pending" {
		return finish("pending", "Upstream checks are pending or skipped; no complete pass is established.")
	}
	if len(seen) != len(required) {
		return finish("incomplete", "Receipt lacks required development checks.")
	}
	if a := receipt.Artifact; a != nil {
		if !hashPattern.MatchString(a.SHA256) || a.Size <= 0 || a.Target != target || a.Kind != "binary-bundle" {
			return finish("invalid", "Invalid binary bundle identity in receipt.")
		}
	}
	if artifact != "" {
		a := receipt.Artifact
		if a == nil {
			return finish("missing", "Receipt has no binary bundle digest; source tests do not verify this bundle.")
		}
		bundle, err := os.Open(artifact)
		if err != nil {
			return finish("unavailable", "Selected binary bundle cannot be read.")
		}
		defer bundle.Close()
		info, err := bundle.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return finish("unavailable", "Selected binary bundle must be a readable regular file.")
		}
		if info.Size() != a.Size {
			return finish("mismatch", "Binary bundle size differs from the receipt.")
		}
		h := sha256.New()
		// Bound reads without adding to an untrusted int64 size.
		size, err := io.Copy(h, io.LimitReader(bundle, a.Size))
		if err != nil {
			return finish("unavailable", "Reading the selected binary bundle failed.")
		}
		if size != a.Size || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
			return finish("mismatch", "Binary bundle size or SHA-256 differs from the receipt.")
		}
		r.ArtifactVerified = true
	}
	return finish("passed", "Receipt reports all development checks passed for this exact commit and platform; authenticate its publisher separately and retain local installation checks.")
}
