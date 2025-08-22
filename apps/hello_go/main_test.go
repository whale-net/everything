package main

import (
	"testing"
	"github.com/example/everything/libs/common_go"
)

func TestFormatGreeting(t *testing.T) {
	result := common.FormatGreeting("test")
	expected := "Hello, test from Go!"
	if result != expected {
		t.Errorf("Expected %q, got %q", expected, result)
	}
}

func TestGetVersion(t *testing.T) {
	version := common.GetVersion()
	if version == "" {
		t.Error("Version should not be empty")
	}
}
