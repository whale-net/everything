// Package common provides common utilities for Go applications.
package common

import "fmt"

// FormatGreeting formats a greeting message.
func FormatGreeting(name string) string {
	return fmt.Sprintf("Hello, %s from Go!", name)
}

// GetVersion returns the application version.
func GetVersion() string {
	return "1.0.0"
}
