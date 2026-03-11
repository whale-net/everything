# Handler Migration Guide

## Status

**Templ components: ✅ Complete**  
**Handler migration: ⚠️ Not started**

All templ pages and components are built and ready to use. The handlers still use the old html/template system.

## Migration Pattern

### Old (html/template)
```go
data := GamesPageData{
    Title:  "Games",
    Active: "games",
    User:   user,
    Games:  games,
}

layoutData := LayoutData{
    Title:  data.Title,
    Active: data.Active,
    User:   data.User,
}

renderPage(w, "games_content", data, layoutData)
```

### New (templ)
```go
import (
    gamepages "github.com/whale-net/everything/manmanv2/ui/pages/games"
    "github.com/whale-net/everything/manmanv2/ui/types"
)

data := types.GamesPageData{
    Layout: types.LayoutData{
        Title:  "Games",
        Active: "games",
        User:   user,
    },
    Games: games,
}

gamepages.List(data).Render(r.Context(), w)
```

## Handlers to Migrate

### handlers_games.go
- `handleGames` → `gamepages.List`
- `handleGameNew` → `gamepages.Form`
- `handleGameDetail` → `gamepages.Detail`
- `handleGameConfigDetail` → `configpages.Detail`
- `handleGameConfigNew` → `configpages.Form`

### handlers_servers.go
- `handleServers` → `serverpages.List`
- `handleServerDetail` → `serverpages.Detail`

### handlers_sessions.go
- `handleSessions` → `sessionpages.List`
- `handleSessionDetail` → `sessionpages.Detail`

### handlers_workshop.go
- `handleWorkshopLibrary` → `workshoppages.Library`
- `handleWorkshopSearch` → `workshoppages.Search`
- `handleWorkshopAddonDetail` → `workshoppages.AddonDetail`
- `handleWorkshopInstallations` → `workshoppages.Installations`
- `handleWorkshopLibraryDetail` → `workshoppages.LibraryDetail`

## After Migration

Once all handlers are migrated:
1. Delete `templates/` directory
2. Delete `templates.go`
3. Remove `embedsrcs` from BUILD.bazel
4. Remove `renderPage` and `renderTemplate` functions
