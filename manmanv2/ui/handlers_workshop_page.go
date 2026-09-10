package main

import (
	"context"
	"log"
	"net/http"
	"sort"

	"github.com/whale-net/everything/libs/go/htmxauth"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
)

// handleWorkshopPage renders the redesigned Workshop top-level page
// (root plan #2359, task #2362 -- FR6/FR7), registered at "/workshop" in
// main.go alongside the other authenticated routes. It is additive: the
// pre-existing "/workshop/library" page and every "/workshop/*"
// sub-route (handlers_workshop.go) are untouched by this handler and
// stay reachable (NFR6).
//
// Data assembly mirrors handleWorkshopLibrary's shape (games, libraries,
// addons, a bounded per-game loop for recent batch jobs -- see its doc
// comment) plus one addition: a WorkshopLibraryPanelData per library
// (buildWorkshopLibraryPanels below), so every library's Manage Blade
// (FR6: addon/reference management) opens with zero further request, the
// same pre-rendered-<template> convention games.templ's per-deployment
// Customize blade uses (see gameRow's doc comment there).
func (app *App) handleWorkshopPage(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := r.Context()

	games, err := app.grpc.ListGames(ctx)
	if err != nil {
		log.Printf("Error fetching games: %v", err)
		http.Error(w, "Failed to fetch games", http.StatusInternalServerError)
		return
	}

	libraries, err := app.grpc.ListLibraries(ctx, 200, 0, 0)
	if err != nil {
		log.Printf("Error fetching libraries: %v", err)
		http.Error(w, "Failed to fetch libraries", http.StatusInternalServerError)
		return
	}

	addons, err := app.grpc.ListWorkshopAddons(ctx, 0, 200, 0)
	if err != nil {
		log.Printf("Error fetching addons: %v", err)
		http.Error(w, "Failed to fetch addons", http.StatusInternalServerError)
		return
	}

	panels := app.buildWorkshopLibraryPanels(ctx, games, libraries, addons)

	// Recent batch jobs (mirrors handleWorkshopLibrary's RecentBatchJobs,
	// see that doc comment for why this is a bounded per-game loop rather
	// than a single fleet-wide call): lets a Server Manager reach the
	// batch-status view from "/workshop" without holding on to a
	// batch_job_id, same as the pre-existing "/workshop/library" page.
	var recentBatchJobs []*manmanpb.WorkshopBatchJob
	for _, game := range games {
		jobs, err := app.grpc.ListBatchJobs(ctx, game.GameId, 5)
		if err != nil {
			log.Printf("Error fetching batch jobs for game %d: %v", game.GameId, err)
			continue
		}
		recentBatchJobs = append(recentBatchJobs, jobs...)
	}
	sort.Slice(recentBatchJobs, func(i, j int) bool {
		return recentBatchJobs[i].BatchJobId > recentBatchJobs[j].BatchJobId
	})
	if len(recentBatchJobs) > 8 {
		recentBatchJobs = recentBatchJobs[:8]
	}

	breadcrumbs := []components.Breadcrumb{
		{Label: "Workshop", URL: "/workshop"},
	}
	// "Workshop" matches navItems' nav.templ label exactly (see
	// components/layout.templ's navLink) so the nav entry highlights when
	// this page is active. The dependent navigation/disposition task
	// swaps that nav link's target from "/workshop/library" to
	// "/workshop" -- this handler does not touch layout.templ.
	layoutData, err := app.buildTemplLayoutData(r, "Workshop", "Workshop", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	pageData := pages.WorkshopPageData{
		Layout:          layoutData,
		Games:           games,
		Libraries:       libraries,
		Addons:          addons,
		LibraryPanels:   panels,
		RecentBatchJobs: recentBatchJobs,
	}

	if err := RenderTempl(w, r, "Workshop", pages.WorkshopPage(pageData)); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// buildWorkshopLibraryPanels assembles one WorkshopLibraryPanelData per
// library entirely from data already fetched at page load plus one
// GetLibraryAddons/GetChildLibraries pair per library -- the same bounded
// per-entity RPC loop shape handleWorkshopLibrary already uses for
// RecentBatchJobs (per game, not per library, but the same "loop the
// already-small fleet-wide list, not a single unbounded call" pattern).
// AvailableAddons/AvailableLibraries are derived in-process from the
// page's already-fetched addons/libraries lists rather than a further
// per-library RPC, so opening a library's Manage Blade (FR6) costs no
// request at all -- only building the panel data itself (this function)
// costs the two RPCs per library, at page load.
func (app *App) buildWorkshopLibraryPanels(ctx context.Context, games []*manmanpb.Game, libraries []*manmanpb.WorkshopLibrary, addons []*manmanpb.WorkshopAddon) []pages.WorkshopLibraryPanelData {
	gameNames := make(map[int64]string, len(games))
	for _, g := range games {
		gameNames[g.GameId] = g.Name
	}

	panels := make([]pages.WorkshopLibraryPanelData, 0, len(libraries))
	for _, lib := range libraries {
		libAddons, err := app.grpc.GetLibraryAddons(ctx, lib.LibraryId)
		if err != nil {
			log.Printf("Error fetching addons for library %d: %v", lib.LibraryId, err)
			libAddons = nil
		}
		inLibrary := make(map[int64]bool, len(libAddons))
		for _, a := range libAddons {
			inLibrary[a.AddonId] = true
		}
		var availableAddons []*manmanpb.WorkshopAddon
		for _, a := range addons {
			if a.GameId == lib.GameId && !inLibrary[a.AddonId] {
				availableAddons = append(availableAddons, a)
			}
		}

		children, err := app.grpc.GetChildLibraries(ctx, lib.LibraryId)
		if err != nil {
			log.Printf("Error fetching child libraries for library %d: %v", lib.LibraryId, err)
			children = nil
		}
		isChild := make(map[int64]bool, len(children))
		for _, c := range children {
			isChild[c.LibraryId] = true
		}
		var availableLibraries []*manmanpb.WorkshopLibrary
		for _, other := range libraries {
			if other.LibraryId == lib.LibraryId || other.GameId != lib.GameId || isChild[other.LibraryId] {
				continue
			}
			availableLibraries = append(availableLibraries, other)
		}

		panels = append(panels, pages.WorkshopLibraryPanelData{
			Library:            lib,
			GameName:           gameNames[lib.GameId],
			Addons:             libAddons,
			AvailableAddons:    availableAddons,
			ChildLibraries:     children,
			AvailableLibraries: availableLibraries,
		})
	}
	return panels
}
