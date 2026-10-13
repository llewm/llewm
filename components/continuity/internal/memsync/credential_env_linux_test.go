package memsync

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCredentialBusDiscovery(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "bus")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	env := []string{"XDG_RUNTIME_DIR=" + dir, "GIT_ASKPASS="}
	got := credentialSessionEnv(env)
	if len(got) != len(env)+1 || !strings.HasPrefix(got[len(env)], "DBUS_SESSION_BUS_ADDRESS=unix:path=") {
		t.Fatalf("missing existing bus: %v", got)
	}
	for _, address := range []string{"", "unix:path=/explicit/bus"} {
		explicit := append(slices.Clone(env), "DBUS_SESSION_BUS_ADDRESS="+address)
		if !slices.Equal(credentialSessionEnv(explicit), explicit) {
			t.Fatal("replaced explicit bus setting")
		}
	}
	if got := withCredentialBus(env, dir, uint32(os.Geteuid())+1); !slices.Equal(got, env) {
		t.Fatal("accepted another owner's runtime")
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if got := credentialSessionEnv(env); !slices.Equal(got, env) {
		t.Fatal("accepted nonprivate runtime")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	listener.Close()
	if err := os.WriteFile(socket, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got := credentialSessionEnv(env); !slices.Equal(got, env) {
		t.Fatal("accepted regular file as bus")
	}
	os.Remove(socket)
	if err := os.Symlink("/missing", socket); err != nil {
		t.Fatal(err)
	}
	if got := credentialSessionEnv(env); !slices.Equal(got, env) {
		t.Fatal("accepted symlink as bus")
	}
}
