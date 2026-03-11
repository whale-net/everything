package types

import (
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// GamesPageData holds data for the games list page
type GamesPageData struct {
	Layout LayoutData
	Games  []*manmanpb.Game
}

// GameDetailPageData holds data for game detail page
type GameDetailPageData struct {
	Layout      LayoutData
	Game        *manmanpb.Game
	Configs     []*manmanpb.GameConfig
	SgcCounts   map[int64]int
	PathPresets []*manmanpb.GameAddonPathPreset
	Volumes     map[int64]*manmanpb.GameConfigVolume
}

// GameFormData holds data for create/edit game form
type GameFormData struct {
	Layout LayoutData
	Game   *manmanpb.Game
	Edit   bool
}
