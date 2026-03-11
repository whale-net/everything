package types

import (
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

type ConfigDetailPageData struct {
	Layout      LayoutData
	Config      *manmanpb.GameConfig
	Deployments []*manmanpb.ServerGameConfig
}

type ConfigFormData struct {
	Layout LayoutData
	Config *manmanpb.GameConfig
	Games  []*manmanpb.Game
}
