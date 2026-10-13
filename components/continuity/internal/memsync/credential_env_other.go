//go:build !linux

package memsync

func credentialSessionEnv(env []string) []string { return env }
