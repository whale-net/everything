package utils

import "strings"

type ValidationErrors map[string]string

func (v ValidationErrors) Add(field, message string) {
	v[field] = message
}

func (v ValidationErrors) Has(field string) bool {
	_, ok := v[field]
	return ok
}

func (v ValidationErrors) Get(field string) string {
	return v[field]
}

func (v ValidationErrors) IsEmpty() bool {
	return len(v) == 0
}

func ValidateRequired(value, field string, errors ValidationErrors) {
	if strings.TrimSpace(value) == "" {
		errors.Add(field, "This field is required")
	}
}

func ValidateEmail(value, field string, errors ValidationErrors) {
	if value != "" && !strings.Contains(value, "@") {
		errors.Add(field, "Invalid email address")
	}
}
