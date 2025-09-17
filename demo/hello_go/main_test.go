package main

import (
	"testing"
	"github.com/example/everything/libs/go"
)

func TestFormatGreeting(t *testing.T) {
	result := go_lib.FormatGreeting("test")
	expected := "Hello, test from Go!"
	if result != expected {
		t.Errorf("Expected %q, got %q", expected, result)
	}
}

func TestGetVersion(t *testing.T) {
	version := go_lib.GetVersion()
	if version == "" {
		t.Error("Version should not be empty")
	}
}
