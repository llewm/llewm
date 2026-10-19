package buildinfo

import (
	"crypto/sha256"
	debugbuildinfo "debug/buildinfo"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"

	"github.com/llewm/llewm/components/continuity/internal/pluginidentity"
)

const (
	MCPAPIVersion = 131
	// WriterAPIVersion is the newest MCP API revision that introduced a
	// write-incompatible semantic change. Ordinary MCP surface changes must not
	// advance it: doing so would strand checkpoints in already-running clients.
	WriterAPIVersion = 125
)

// These values may be set with -ldflags for release builds. Development builds
// fall back to Go's embedded VCS settings when available.
var (
	Version   = "0.1.0-dev"
	Commit    = ""
	BuildTime = ""
)

type Info struct {
	ApplicationVersion string `json:"application_version"`
	GitCommit          string `json:"git_commit"`
	BuildDirty         bool   `json:"build_dirty"`
	BuildTime          string `json:"build_time,omitempty"`
	GoVersion          string `json:"go_version"`
	MCPAPIVersion      int    `json:"mcp_api_version"`
	WriterAPIVersion   int    `json:"writer_api_version"`
	Executable         string `json:"executable"`
}

type ExecutableInfo struct {
	Path          string `json:"path"`
	SHA256        string `json:"sha256,omitempty"`
	GitCommit     string `json:"git_commit"`
	BuildDirty    bool   `json:"build_dirty"`
	GoVersion     string `json:"go_version,omitempty"`
	Error         string `json:"error,omitempty"`
	MetadataError string `json:"metadata_error,omitempty"`
}

type RuntimeState struct {
	Plugin           pluginidentity.Snapshot `json:"plugin"`
	Build            Info                    `json:"build"`
	ExecutableSHA256 string                  `json:"executable_sha256,omitempty"`
}

type Health struct {
	Status          string         `json:"status"`
	RestartRequired bool           `json:"restart_required"`
	Reasons         []string       `json:"reasons,omitempty"`
	Runtime         RuntimeState   `json:"runtime"`
	Installed       ExecutableInfo `json:"installed"`
	PluginStatus    string         `json:"plugin_status"`
	PluginReason    string         `json:"plugin_reason"`
}

func Current() Info {
	result := Info{
		ApplicationVersion: Version,
		GitCommit:          Commit,
		BuildTime:          BuildTime,
		GoVersion:          runtime.Version(),
		MCPAPIVersion:      MCPAPIVersion,
		WriterAPIVersion:   WriterAPIVersion,
	}
	if executable, err := os.Executable(); err == nil {
		result.Executable = executable
	}
	if settings, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range settings.Settings {
			switch setting.Key {
			case "vcs.revision":
				if result.GitCommit == "" {
					result.GitCommit = setting.Value
				}
			case "vcs.modified":
				result.BuildDirty, _ = strconv.ParseBool(setting.Value)
			}
		}
	}
	if result.GitCommit == "" {
		result.GitCommit = "unknown"
	}
	return result
}

func CaptureRuntime() RuntimeState {
	result := RuntimeState{Build: Current(), Plugin: pluginidentity.Capture()}
	installed := InspectExecutable(result.Build.Executable)
	if installed.Error == "" {
		result.ExecutableSHA256 = installed.SHA256
	}
	return result
}

func (runtimeState RuntimeState) CheckInstalled() Health {
	health := Health{
		Status:       "current",
		Runtime:      runtimeState,
		Installed:    InspectExecutable(runtimeState.Build.Executable),
		PluginStatus: "unknown",
		PluginReason: "the MCP server is not given client plugin package identity",
	}
	if runtimeState.Plugin.Status != "" {
		health.PluginStatus, health.PluginReason = runtimeState.Plugin.Status, runtimeState.Plugin.Reason
		if runtimeState.Plugin.Status == "current" {
			expected, err := pluginidentity.Expected(runtimeState.Plugin.Client)
			if err == nil {
				observed := pluginidentity.Inspect(runtimeState.Plugin.Client, runtimeState.Plugin.Root, expected)
				if observed.Status != "current" || observed.SHA256 != runtimeState.Plugin.SHA256 || observed.Version != runtimeState.Plugin.Version {
					health.PluginStatus, health.PluginReason = "stale", "plugin files changed or became unavailable since MCP startup"
				}
			}
		}
		if health.PluginStatus == "stale" {
			health.Status = "stale"
			health.RestartRequired = true
			health.Reasons = append(health.Reasons, "refresh the matching plugin and restart the parent client")
		}
	}
	if health.Installed.Error != "" || runtimeState.ExecutableSHA256 == "" {
		health.Status = "unknown"
		health.Reasons = append(health.Reasons, "installed executable identity could not be compared")
		return health
	}
	if runtimeState.ExecutableSHA256 != health.Installed.SHA256 {
		health.Status = "stale"
		health.RestartRequired = true
		health.Reasons = append(health.Reasons, "running MCP executable differs from the installed executable")
	}
	if knownCommit(runtimeState.Build.GitCommit) && knownCommit(health.Installed.GitCommit) && runtimeState.Build.GitCommit != health.Installed.GitCommit {
		health.Status = "stale"
		health.RestartRequired = true
		health.Reasons = append(health.Reasons, "running MCP commit differs from the installed executable")
	}
	return health
}

func InspectExecutable(path string) ExecutableInfo {
	result := ExecutableInfo{Path: path, GitCommit: "unknown"}
	file, err := os.Open(path)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		file.Close()
		result.Error = err.Error()
		return result
	}
	if err := file.Close(); err != nil {
		result.Error = err.Error()
		return result
	}
	result.SHA256 = hex.EncodeToString(hash.Sum(nil))
	metadata, err := debugbuildinfo.ReadFile(path)
	if err != nil {
		result.MetadataError = fmt.Sprintf("read Go build metadata: %v", err)
		return result
	}
	result.GoVersion = metadata.GoVersion
	for _, setting := range metadata.Settings {
		switch setting.Key {
		case "vcs.revision":
			result.GitCommit = setting.Value
		case "vcs.modified":
			result.BuildDirty, _ = strconv.ParseBool(setting.Value)
		}
	}
	return result
}

func knownCommit(value string) bool { return value != "" && value != "unknown" }
