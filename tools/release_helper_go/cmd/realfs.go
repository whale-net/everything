package cmd

import "os"

type realFS struct{}

func (realFS) Stat(path string) (os.FileInfo, error)                        { return os.Stat(path) }
func (realFS) ReadFile(path string) ([]byte, error)                          { return os.ReadFile(path) }
func (realFS) WriteFile(path string, data []byte, perm os.FileMode) error   { return os.WriteFile(path, data, perm) }
