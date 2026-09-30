// The read-only per-milestone/milepebble task list, reached from the
// delivery page. Reads through app.tasks / app.spec.
package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

func milestoneTasksPath(pid, mid uuid.UUID) string {
	return productPath(pid) + "/milestones/" + mid.String() + "/tasks"
}

func milestoneBoardPath(pid, mid uuid.UUID) string {
	return productPath(pid) + "/milestones/" + mid.String() + "/board"
}

func taskDetailPath(pid, mid, tid uuid.UUID) string {
	return milestoneTasksPath(pid, mid) + "/" + tid.String()
}

// taskContainer is the milestone or milepebble a task view is scoped to,
// resolved from the product's own delivery listing so an id that does not
// belong to the URL's product can never resolve.
type taskContainer struct {
	ID          uuid.UUID
	Name        string
	Milepebbles []taskContainerChild // non-empty only for a cut milestone
}

type taskContainerChild struct {
	ID   uuid.UUID
	Name string
}

// resolveTaskContainer finds mid among the milestones and milepebbles of
// listing. ok is false when mid is not part of the product.
func resolveTaskContainer(listing slice.DeliveryListing, mid uuid.UUID) (taskContainer, bool) {
	for _, m := range listing.Milestones {
		if m.ID == mid {
			c := taskContainer{ID: m.ID, Name: m.Name}
			for _, mp := range m.Milepebbles {
				c.Milepebbles = append(c.Milepebbles, taskContainerChild{ID: mp.ID, Name: mp.Name})
			}
			return c, true
		}
		for _, mp := range m.Milepebbles {
			if mp.ID == mid {
				return taskContainer{ID: mp.ID, Name: mp.Name}, true
			}
		}
	}
	return taskContainer{}, false
}

// taskStateBadges derives a task's distinct state badges. A claim whose
// lease has lapsed is "lease expired", never "claimed".
func taskStateBadges(t store.TaskSummary, now time.Time) []pages.TaskBadge {
	var badges []pages.TaskBadge
	if t.CurrentClaimID != nil {
		if t.LeaseExpiresAt != nil && !t.LeaseExpiresAt.After(now) {
			badges = append(badges, pages.TaskBadge{Key: "lease-expired", Label: "Lease expired"})
		} else {
			badges = append(badges, pages.TaskBadge{Key: "live", Label: "Claimed"})
		}
	}
	if t.AttemptCount >= store.DefaultAttemptCap {
		badges = append(badges, pages.TaskBadge{Key: "capped", Label: "Capped"})
	}
	if t.CurrentEscalationID != nil {
		badges = append(badges, pages.TaskBadge{Key: "escalated", Label: "Escalated"})
	}
	if t.CancelledAt != nil {
		badges = append(badges, pages.TaskBadge{Key: "cancelled", Label: "Cancelled"})
	}
	return badges
}

// taskAttemptsLabel is the attempt count against the cap, e.g. "2 of 3".
func taskAttemptsLabel(n int) string {
	return fmt.Sprintf("%d of %d", n, store.DefaultAttemptCap)
}

// taskRowOf builds one list row, carrying the observed claim identity and
// lease so a later write can be guarded against a changed claim.
func taskRowOf(pid, mid uuid.UUID, t store.TaskSummary, now time.Time) pages.TaskRow {
	row := pages.TaskRow{
		ID:         t.ID.String(),
		Title:      t.Title,
		Lane:       string(t.CurrentLane),
		Attempts:   taskAttemptsLabel(t.AttemptCount),
		DetailPath: taskDetailPath(pid, mid, t.ID),
		Badges:     taskStateBadges(t, now),
	}
	if t.CurrentClaimID != nil {
		row.ClaimID = t.CurrentClaimID.String()
	}
	if t.LeaseExpiresAt != nil {
		row.LeaseExpiresAt = t.LeaseExpiresAt.UTC().Format(time.RFC3339)
	}
	return row
}

// taskListPageOf assembles the list view model. A non-nil readErr yields
// an error view, never an empty one.
func taskListPageOf(pid uuid.UUID, c taskContainer, tasks []store.TaskSummary, readErr error, now time.Time) pages.TaskListPage {
	page := pages.TaskListPage{
		Path:         milestoneTasksPath(pid, c.ID),
		Name:         c.Name,
		LoadedAt:     now.UTC().Format(time.RFC3339),
		BoardPath:    milestoneBoardPath(pid, c.ID),
		DeliveryPath: deliveryPath(pid),
	}
	if readErr != nil {
		page.Error = "The tasks could not be read. See the logs."
		return page
	}
	if len(c.Milepebbles) > 0 {
		for _, mp := range c.Milepebbles {
			page.Milepebbles = append(page.Milepebbles, pages.TaskMilepebbleLink{
				Name:      mp.Name,
				TasksPath: milestoneTasksPath(pid, mp.ID),
				BoardPath: milestoneBoardPath(pid, mp.ID),
			})
		}
		return page
	}
	for _, t := range tasks {
		page.Tasks = append(page.Tasks, taskRowOf(pid, c.ID, t, now))
	}
	return page
}

// taskNotFound renders the in-shell 404 for a container id outside the product.
func taskNotFound(w http.ResponseWriter, r *http.Request, pid uuid.UUID) {
	renderSpecStatus(w, r, http.StatusNotFound, pages.StatusPage{
		Title:    "Not found",
		Detail:   "No milestone or milepebble with that id belongs to this product.",
		BackHref: deliveryPath(pid),
		BackText: "Back to delivery",
	})
}

// resolveTaskRoute validates {id}/{mid} and resolves the container within
// the product, writing the 400/404/500 page itself when it cannot.
func (app *App) resolveTaskRoute(w http.ResponseWriter, r *http.Request) (uuid.UUID, taskContainer, bool) {
	pid, ok := specProductID(w, r)
	if !ok {
		return uuid.Nil, taskContainer{}, false
	}
	mid, err := uuid.Parse(r.PathValue("mid"))
	if err != nil {
		renderSpecStatus(w, r, http.StatusBadRequest, pages.StatusPage{
			Title:    "Bad milestone id",
			Detail:   "The milestone id in the URL is not a UUID.",
			BackHref: deliveryPath(pid),
			BackText: "Back to delivery",
		})
		return uuid.Nil, taskContainer{}, false
	}
	listing, err := app.spec.Delivery(r.Context(), pid, nil)
	if err != nil {
		renderSpecError(w, r, pages.TasksAnchor, err)
		return uuid.Nil, taskContainer{}, false
	}
	c, found := resolveTaskContainer(listing, mid)
	if !found {
		taskNotFound(w, r, pid)
		return uuid.Nil, taskContainer{}, false
	}
	return pid, c, true
}

// handleTaskList renders a milestone's or milepebble's task list.
func (app *App) handleTaskList(w http.ResponseWriter, r *http.Request) {
	pid, c, ok := app.resolveTaskRoute(w, r)
	if !ok {
		return
	}
	var tasks []store.TaskSummary
	var readErr error
	// A cut milestone's tasks live on its milepebbles; never read or
	// aggregate them here.
	if len(c.Milepebbles) == 0 {
		tasks, readErr = app.tasks.ListTasksByMilestone(r.Context(), c.ID)
		if readErr != nil {
			logger.Error("task list read failed", "container", c.ID.String(), "error", readErr)
		}
	}
	body := pages.TaskList(taskListPageOf(pid, c, tasks, readErr, time.Now()))
	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, body)
		return
	}
	renderShell(w, r, "Tasks", specPath, body)
}
