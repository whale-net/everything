package cmd

import "os"

// BazelRunner runs bazel commands and returns stdout.
type BazelRunner interface {
	Run(args ...string) (string, error)
}

// GitRunner runs git commands and returns stdout.
type GitRunner interface {
	Run(args ...string) (string, error)
}

// FileSystem provides file system operations. Replaced in tests with a fake.
type FileSystem interface {
	Stat(path string) (os.FileInfo, error)
	ReadFile(path string) ([]byte, error)
}

// EnvLookup reads environment variables. Replaced in tests with a fake.
type EnvLookup func(key string) string

// Package-level defaults — replaced in tests via with* helpers.
var defaultFS FileSystem = realFS{}
var defaultEnv EnvLookup = os.Getenv
var defaultBazel BazelRunner   // set in init() after realBazelRunner is defined
var defaultGit GitRunner       // set in init() after realGitRunner is defined
var defaultWorkspaceRoot func() (string, error) = findWorkspaceRoot
