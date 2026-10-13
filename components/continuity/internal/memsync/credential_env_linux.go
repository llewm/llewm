package memsync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// credentialSessionEnv lets a configured keyring helper reach the existing
// user bus when a client omitted desktop variables. It never starts a bus,
// unlocks a wallet itself, or enables askpass. Explicit addresses win.
func credentialSessionEnv(env []string) []string {
	runtime := ""
	for _, entry := range env {
		if strings.HasPrefix(entry, "DBUS_SESSION_BUS_ADDRESS=") {
			return env
		}
		if value, ok := strings.CutPrefix(entry, "XDG_RUNTIME_DIR="); ok {
			runtime = value
		}
	}
	if runtime == "" {
		runtime = fmt.Sprintf("/run/user/%d", os.Geteuid())
	}
	return withCredentialBus(env, runtime, uint32(os.Geteuid()))
}

func withCredentialBus(env []string, runtime string, uid uint32) []string {
	if !filepath.IsAbs(runtime) {
		return env
	}
	dir, err := os.Lstat(runtime)
	if err != nil || !dir.IsDir() || dir.Mode().Perm() != 0700 {
		return env
	}
	owner, ok := dir.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uid {
		return env
	}
	socket := filepath.Join(runtime, "bus")
	info, err := os.Lstat(socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return env
	}
	owner, ok = info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uid {
		return env
	}
	// D-Bus address values use percent escaping, not URL query escaping.
	var escaped strings.Builder
	for _, b := range []byte(socket) {
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("_-/\\.*", rune(b)) {
			escaped.WriteByte(b)
		} else {
			fmt.Fprintf(&escaped, "%%%02x", b)
		}
	}
	return append(env, "DBUS_SESSION_BUS_ADDRESS=unix:path="+escaped.String())
}
