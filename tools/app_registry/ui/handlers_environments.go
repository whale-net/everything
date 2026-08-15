package main

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/libs/go/htmxauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// grpcErrorMessage implements FR-45: distinguishing "you lack role X" from
// a transport or server failure, in a message specific enough to act on.
// Every handler that calls a mutating (admin-only) RPC in this file routes
// its error through this — never a bare err.Error() — so a denied caller
// sees the missing role, not an opaque 500-shaped message indistinguishable
// from app-registry-api being unreachable.
func grpcErrorMessage(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		return "Transport or server failure calling app-registry-api: " + err.Error()
	}
	switch st.Code() {
	case codes.PermissionDenied:
		return "Denied: your session lacks the required role. " + st.Message()
	case codes.Unauthenticated:
		return "Your session is not authenticated with app-registry-api — sign in again. " + st.Message()
	case codes.InvalidArgument:
		return "Rejected: " + st.Message()
	default:
		return "Transport or server failure calling app-registry-api (" + st.Code().String() + "): " + st.Message()
	}
}

// handleEnvironments is screen 09-environments — the "Environments" nav
// item (configuration), kept a fully distinct screen and data source from
// "Deployments" (state, screen 10, FR-2). Reads require only
// authentication (see server/handlers/environment.go), so this renders
// fully for every signed-in user regardless of role; only the Actions
// column's controls are admin-gated (FR-38), inside the template.
func (app *App) handleEnvironments(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	includeArchived := r.URL.Query().Get("include_archived") == "true"

	resp, err := app.registry.Environment.ListEnvironments(r.Context(), &pb.ListEnvironmentsRequest{
		IncludeArchived: includeArchived,
	})
	var loadErr error
	var envs []*pb.Environment
	if err != nil {
		log.Printf("ListEnvironments failed: %v", err)
		loadErr = err
	} else {
		envs = resp.GetEnvironments()
	}

	if err := RenderTempl(w, r, "Environments", pages.Environments(user, envs, includeArchived, loadErr)); err != nil {
		log.Printf("Failed to render environments page: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

// handleEnvironmentNew renders screen 53 in "new" mode — Key editable, no
// danger zone (per the wireframe's own note: same form as "edit", minus
// the archive section).
func (app *App) handleEnvironmentNew(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	input := pages.EnvironmentFormInput{}
	if err := RenderTempl(w, r, "Add environment", pages.EnvironmentForm(user, "new", input, false, "")); err != nil {
		log.Printf("Failed to render environment new form: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

// handleEnvironmentEdit renders screen 53 in "edit" mode for the ?key=
// environment — Key locked, danger zone shown (FR-37).
func (app *App) handleEnvironmentEdit(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "missing 'key' query parameter", http.StatusBadRequest)
		return
	}

	resp, err := app.registry.Environment.GetEnvironment(r.Context(), &pb.GetEnvironmentRequest{Key: key})
	if err != nil {
		log.Printf("GetEnvironment(%q) failed: %v", key, err)
		http.Error(w, grpcErrorMessage(err), http.StatusBadGateway)
		return
	}
	env := resp.GetEnvironment()
	input := environmentToFormInput(env)

	if err := RenderTempl(w, r, "Edit environment", pages.EnvironmentForm(user, "edit", input, env.GetArchived(), "")); err != nil {
		log.Printf("Failed to render environment edit form: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

func environmentToFormInput(env *pb.Environment) pages.EnvironmentFormInput {
	return pages.EnvironmentFormInput{
		Key:               env.GetKey(),
		DisplayName:       env.GetDisplayName(),
		Rank:              env.GetRank(),
		RequiresApproval:  env.GetRequiresApproval(),
		GitopsPath:        env.GetGitopsPath(),
		AllowedPrincipals: strings.Join(env.GetAllowedPrincipals(), "\n"),
	}
}

// parseEnvironmentForm reads the create/edit form's POST body into an
// EnvironmentFormInput, mirroring UpsertEnvironmentRequest's fields exactly
// (FR-35) — the same set pages.EnvironmentFormInput documents.
func parseEnvironmentForm(r *http.Request) (pages.EnvironmentFormInput, error) {
	if err := r.ParseForm(); err != nil {
		return pages.EnvironmentFormInput{}, err
	}
	rank, err := strconv.Atoi(strings.TrimSpace(r.FormValue("rank")))
	if err != nil {
		return pages.EnvironmentFormInput{}, err
	}
	principalsRaw := r.FormValue("allowed_principals")
	return pages.EnvironmentFormInput{
		Key:               strings.TrimSpace(r.FormValue("key")),
		DisplayName:       r.FormValue("display_name"),
		Rank:              int32(rank),
		RequiresApproval:  r.FormValue("requires_approval") == "true",
		GitopsPath:        r.FormValue("gitops_path"),
		AllowedPrincipals: principalsRaw,
	}, nil
}

// splitPrincipals turns the textarea's newline-separated value into the
// repeated string field UpsertEnvironmentRequest expects, dropping blank
// lines so stray whitespace never becomes a bogus empty-string principal.
func splitPrincipals(raw string) []string {
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// handleEnvironmentSave is the POST target for both "new" and "edit" mode
// (UpsertEnvironment is the same RPC either way — see
// api_messages_environment.proto). A PermissionDenied here (non-admin, or
// role missing/misconfigured) re-renders the form with the FR-45 message
// and the user's submitted values intact, rather than losing their input.
func (app *App) handleEnvironmentSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := htmxauth.GetUser(r.Context())

	input, err := parseEnvironmentForm(r)
	if err != nil {
		log.Printf("parseEnvironmentForm failed: %v", err)
		if renderErr := RenderTempl(w, r, "Environment", pages.EnvironmentForm(user, formMode(r), input, false, "Invalid form input: "+err.Error())); renderErr != nil {
			http.Error(w, "Failed to render page", http.StatusInternalServerError)
		}
		return
	}

	mode := "edit"
	// UpsertEnvironmentResponse.Created tells us whether the row already
	// existed; but the form itself already knows whether Key was editable
	// (new) or locked (edit) — derive the same distinction from whether the
	// environment already exists, via GetEnvironment, only when re-rendering
	// after an error, so the danger zone and Key-lock state don't flicker
	// incorrectly on a failed save. A brand-new key that fails to save has
	// no existing row, so it stays in "new" mode.
	if existing, getErr := app.registry.Environment.GetEnvironment(r.Context(), &pb.GetEnvironmentRequest{Key: input.Key}); getErr == nil && existing.GetEnvironment() != nil {
		mode = "edit"
	} else {
		mode = "new"
	}

	resp, err := app.registry.Environment.UpsertEnvironment(r.Context(), &pb.UpsertEnvironmentRequest{
		Key:               input.Key,
		DisplayName:       input.DisplayName,
		Rank:              input.Rank,
		RequiresApproval:  input.RequiresApproval,
		GitopsPath:        input.GitopsPath,
		AllowedPrincipals: splitPrincipals(input.AllowedPrincipals),
	})
	if err != nil {
		log.Printf("UpsertEnvironment(%q) failed: %v", input.Key, err)
		archived := false
		if existing, getErr := app.registry.Environment.GetEnvironment(r.Context(), &pb.GetEnvironmentRequest{Key: input.Key}); getErr == nil {
			archived = existing.GetEnvironment().GetArchived()
		}
		if renderErr := RenderTempl(w, r, "Environment", pages.EnvironmentForm(user, mode, input, archived, grpcErrorMessage(err))); renderErr != nil {
			log.Printf("Failed to render environment form after save error: %v", renderErr)
			http.Error(w, "Failed to render page", http.StatusInternalServerError)
		}
		return
	}

	http.Redirect(w, r, "/environments/edit?key="+resp.GetEnvironment().GetKey(), http.StatusSeeOther)
}

// formMode is a best-effort guess ("new" vs "edit") used only to re-render
// the form after a request-parsing failure, before we know whether the key
// even parsed. It defaults to "new" — the safer failure mode, since it
// merely hides the danger zone rather than risking it appearing for a
// nonexistent row.
func formMode(r *http.Request) string {
	if strings.TrimSpace(r.FormValue("key")) != "" {
		return "edit"
	}
	return "new"
}

// handleEnvironmentArchive is the danger-zone POST target (FR-37): requires
// a non-empty reason (also enforced server-side by ArchiveEnvironment) and
// never hard-deletes — promotion history is retained.
func (app *App) handleEnvironmentArchive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := htmxauth.GetUser(r.Context())

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form: "+err.Error(), http.StatusBadRequest)
		return
	}
	key := strings.TrimSpace(r.FormValue("key"))
	reason := strings.TrimSpace(r.FormValue("reason"))
	if key == "" {
		http.Error(w, "missing 'key'", http.StatusBadRequest)
		return
	}

	renderWithError := func(msg string) {
		resp, getErr := app.registry.Environment.GetEnvironment(r.Context(), &pb.GetEnvironmentRequest{Key: key})
		if getErr != nil {
			log.Printf("GetEnvironment(%q) failed while re-rendering after archive error: %v", key, getErr)
			http.Error(w, grpcErrorMessage(getErr), http.StatusBadGateway)
			return
		}
		env := resp.GetEnvironment()
		input := environmentToFormInput(env)
		if renderErr := RenderTempl(w, r, "Edit environment", pages.EnvironmentForm(user, "edit", input, env.GetArchived(), msg)); renderErr != nil {
			log.Printf("Failed to render environment form after archive error: %v", renderErr)
			http.Error(w, "Failed to render page", http.StatusInternalServerError)
		}
	}

	if reason == "" {
		// NFR-7: a real labelled field, refused without a value — mirrored
		// client-side by the textarea's required attribute, but enforced
		// here too since HTML validation is not a security or correctness
		// boundary.
		renderWithError("Archive reason is required — promotion history is retained, but the reason explains why this environment stopped being a target.")
		return
	}

	_, err := app.registry.Environment.ArchiveEnvironment(r.Context(), &pb.ArchiveEnvironmentRequest{Key: key, Reason: reason})
	if err != nil {
		log.Printf("ArchiveEnvironment(%q) failed: %v", key, err)
		renderWithError(grpcErrorMessage(err))
		return
	}

	http.Redirect(w, r, "/environments/edit?key="+key, http.StatusSeeOther)
}
