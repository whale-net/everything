package types

import "github.com/whale-net/everything/libs/go/htmxauth"

// LayoutData contains data for the base layout
type LayoutData struct {
	Title          string
	Active         string
	User           *htmxauth.UserInfo
	Servers        interface{} // Will be typed properly later
	SelectedServer interface{} // Will be typed properly later
}
