package types

import manmanpb "github.com/whale-net/everything/manmanv2/protos"

// ServersPageData holds data for servers list page
type ServersPageData struct {
	Layout  LayoutData
	Servers []*manmanpb.Server
}

// ServerDetailPageData holds data for server detail page
type ServerDetailPageData struct {
	Layout LayoutData
	Server *manmanpb.Server
}
