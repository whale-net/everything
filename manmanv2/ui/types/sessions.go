package types

import manmanpb "github.com/whale-net/everything/manmanv2/protos"

// SessionsPageData holds data for sessions list page
type SessionsPageData struct {
	Layout              LayoutData
	Sessions            []*manmanpb.Session
	LiveOnly            bool
	StatusFilter        string
	ServerGameConfigID  string
	SGCDisplayNames     map[int64]string
	LiveSessionByConfig map[int64]*manmanpb.Session
}

// SessionDetailPageData holds data for session detail page
type SessionDetailPageData struct {
	Layout  LayoutData
	Session *manmanpb.Session
	Logs    []string
}
