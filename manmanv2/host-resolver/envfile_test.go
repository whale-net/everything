package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteEnvValue_NewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")

	if err := writeEnvValue(path, "HOST_MANAGER_VERSION", "v1.0.0"); err != nil {
		t.Fatalf("writeEnvValue: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got, want := string(data), "HOST_MANAGER_VERSION=v1.0.0\n"; got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestWriteEnvValue_ReplacesExistingAndPreservesRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	initial := "# comment\nSERVER_NAME=my-host\nHOST_MANAGER_VERSION=v1.0.0\nRABBITMQ_URL=amqp://x\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeEnvValue(path, "HOST_MANAGER_VERSION", "v1.1.0"); err != nil {
		t.Fatalf("writeEnvValue: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := "# comment\nSERVER_NAME=my-host\nHOST_MANAGER_VERSION=v1.1.0\nRABBITMQ_URL=amqp://x\n"
	if got := string(data); got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestWriteEnvValue_AppendsWhenKeyAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("SERVER_NAME=my-host\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeEnvValue(path, "HOST_MANAGER_VERSION", "v1.0.0"); err != nil {
		t.Fatalf("writeEnvValue: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := "SERVER_NAME=my-host\nHOST_MANAGER_VERSION=v1.0.0\n"
	if got := string(data); got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}
