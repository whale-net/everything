package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRoleConfigResolvePersona(t *testing.T) {
	cfg := RoleConfig{OperatorRole: "OP", ReaderRole: "RD"}
	tests := []struct {
		name   string
		cfg    RoleConfig
		roles  []string
		want   Persona
		wantOK bool
	}{
		{"operator only", cfg, []string{"OP"}, PersonaSwarmOperator, true},
		{"reader only", cfg, []string{"RD"}, PersonaReader, true},
		{"both -> operator", cfg, []string{"RD", "OP"}, PersonaSwarmOperator, true},
		{"neither", cfg, []string{"other"}, "", false},
		{"no roles", cfg, nil, "", false},
		{"unset operator role, holds empty-string role", RoleConfig{ReaderRole: "RD"}, []string{""}, "", false},
		{"unset operator role, reader still works", RoleConfig{ReaderRole: "RD"}, []string{"RD"}, PersonaReader, true},
		{"unset both", RoleConfig{}, []string{"OP", "RD", ""}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.cfg.ResolvePersona(tc.roles)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantOK, ok)
		})
	}
}
