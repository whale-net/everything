package types

import manmanpb "github.com/whale-net/everything/manmanv2/protos"

// WorkshopLibraryPageData holds data for workshop library list
type WorkshopLibraryPageData struct {
	Layout    LayoutData
	Libraries []*manmanpb.WorkshopLibrary
}

// WorkshopLibraryDetailPageData holds data for library detail
type WorkshopLibraryDetailPageData struct {
	Layout  LayoutData
	Library *manmanpb.WorkshopLibrary
	Addons  []*manmanpb.WorkshopAddon
}

// WorkshopAddonDetailPageData holds data for addon detail
type WorkshopAddonDetailPageData struct {
	Layout LayoutData
	Addon  *manmanpb.WorkshopAddon
}

// WorkshopSearchPageData holds data for workshop search
type WorkshopSearchPageData struct {
	Layout    LayoutData
	Query     string
	Libraries []*manmanpb.WorkshopLibrary
	Addons    []*manmanpb.WorkshopAddon
}

// WorkshopInstallationsPageData holds data for installations page
type WorkshopInstallationsPageData struct {
	Layout        LayoutData
	Installations []*manmanpb.WorkshopInstallation
}
