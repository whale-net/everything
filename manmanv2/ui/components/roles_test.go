package components

import (
	"testing"

	"github.com/whale-net/everything/libs/go/htmxauth"
)

func TestHasAdminRole(t *testing.T) {
	tests := []struct {
		name string
		user *htmxauth.UserInfo
		want bool
	}{
		{
			name: "nil user",
			user: nil,
			want: false,
		},
		{
			name: "nil roles (claim absent)",
			user: &htmxauth.UserInfo{Roles: nil},
			want: false,
		},
		{
			name: "empty roles (read-only viewer)",
			user: &htmxauth.UserInfo{Roles: []string{}},
			want: false,
		},
		{
			name: "player/viewer role",
			user: &htmxauth.UserInfo{Roles: []string{"player", "viewer"}},
			want: false,
		},
		{
			name: "admin role",
			user: &htmxauth.UserInfo{Roles: []string{"admin"}},
			want: true,
		},
		{
			name: "server-manager role",
			user: &htmxauth.UserInfo{Roles: []string{"server-manager"}},
			want: true,
		},
		{
			name: "dev wildcard role",
			user: &htmxauth.UserInfo{Roles: htmxauth.AllRoles},
			want: true,
		},
		{
			name: "mixed roles containing admin",
			user: &htmxauth.UserInfo{Roles: []string{"player", "admin"}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasAdminRole(tt.user)
			if got != tt.want {
				t.Errorf("HasAdminRole() = %v, want %v", got, tt.want)
			}
		})
	}
}
