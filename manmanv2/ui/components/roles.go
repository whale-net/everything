package components

import (
	"github.com/whale-net/everything/libs/go/htmxauth"
)

// HasAdminRole reports whether the user holds an administrative role:
// "admin", "server-manager", or the dev wildcard "*".
// If user is nil, or user.Roles is nil or empty, or contains only viewer/player
// roles, HasAdminRole returns false.
func HasAdminRole(user *htmxauth.UserInfo) bool {
	if user == nil || user.Roles == nil {
		return false
	}
	for _, r := range user.Roles {
		if r == "*" || r == "admin" || r == "server-manager" {
			return true
		}
	}
	return false
}
