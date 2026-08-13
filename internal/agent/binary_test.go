package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsTemporaryBinary(t *testing.T) {
	tmp := os.TempDir()

	temporary := []string{
		filepath.Join(tmp, "go-build123", "b001", "exe", "swarm"),
		filepath.Join(tmp, "swarm"),
		"/tmp/go-build2915063361/b001/exe/swarm",
	}
	for _, path := range temporary {
		if !IsTemporaryBinary(path) {
			t.Errorf("IsTemporaryBinary(%q) = false, want true", path)
		}
	}

	stable := []string{
		"/home/dev/project/bin/swarm",
		"/usr/local/bin/swarm",
	}
	for _, path := range stable {
		if IsTemporaryBinary(path) {
			t.Errorf("IsTemporaryBinary(%q) = true, want false", path)
		}
	}
}

// writeBinary creates an executable stand-in.
func writeBinary(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestResolveBinaryPrefersRepoBin(t *testing.T) {
	t.Setenv(BinaryEnv, "")

	root := t.TempDir()
	want := filepath.Join(root, "bin", "swarm")
	writeBinary(t, want)

	got, err := ResolveBinary(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("ResolveBinary = %q, want %q", got, want)
	}
}

func TestResolveBinaryHonoursEnv(t *testing.T) {
	root := t.TempDir()
	writeBinary(t, filepath.Join(root, "bin", "swarm"))

	elsewhere := filepath.Join(t.TempDir(), "custom-swarm")
	writeBinary(t, elsewhere)
	t.Setenv(BinaryEnv, elsewhere)

	got, err := ResolveBinary(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != elsewhere {
		t.Errorf("ResolveBinary = %q, want the %s override %q", got, BinaryEnv, elsewhere)
	}
}

func TestResolveBinaryRejectsBadEnv(t *testing.T) {
	root := t.TempDir()

	t.Setenv(BinaryEnv, filepath.Join(root, "nonexistent"))
	if _, err := ResolveBinary(root); err == nil {
		t.Error("a nonexistent SWARM_BIN was accepted")
	}

	notExecutable := filepath.Join(root, "plain")
	if err := os.WriteFile(notExecutable, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(BinaryEnv, notExecutable)
	if _, err := ResolveBinary(root); err == nil {
		t.Error("a non-executable SWARM_BIN was accepted")
	}
}

// Under `go test` the running binary lives in a temp directory, so this
// exercises the refusal path that `go run` would hit.
func TestResolveBinaryRefusesTemporaryBuilds(t *testing.T) {
	t.Setenv(BinaryEnv, "")

	root := t.TempDir() // no bin/swarm here

	self, err := os.Executable()
	if err != nil {
		t.Skip("cannot determine the test executable")
	}
	if !IsTemporaryBinary(self) {
		t.Skipf("test binary %s is not temporary; nothing to assert", self)
	}

	_, err = ResolveBinary(root)
	if err == nil {
		t.Fatal("a temporary build was accepted as the agent binary")
	}
	if !strings.Contains(err.Error(), "go build -o ./bin/swarm") {
		t.Errorf("error does not tell the user how to fix it: %v", err)
	}
}
