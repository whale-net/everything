package main

import (
	"os"
	"path/filepath"
	"strings"
)

// writeEnvValue sets key=value in the .env file at path, replacing an
// existing line for key or appending a new one, and preserves every other
// line verbatim (comments, blank lines, unrelated vars). This file is not
// read back by the resolver -- current state is always read from the
// running container (see reconcile) -- it exists purely so an operator
// running `cat .env` or a manual `docker compose up -d` sees the version
// that's actually running.
//
// Written via a temp file + rename so a crash mid-write can never leave a
// truncated .env behind. The rewritten file keeps the original's
// permissions (falling back to 0o644 only if it didn't exist yet) rather
// than a hardcoded mode, since this file commonly holds secrets
// (GRPC_AUTH_CLIENT_SECRET and friends) that an operator may have locked
// down to 0600.
func writeEnvValue(path, key, value string) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}

	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		lines = strings.Split(string(data), "\n")
	} else if !os.IsNotExist(err) {
		return err
	}

	prefix := key + "="
	found := false
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			lines[i] = prefix + value
			found = true
			break
		}
	}
	if !found {
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, prefix+value)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".env-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed below

	content := strings.Join(lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close() //nolint:errcheck
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
