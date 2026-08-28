package activity

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/whale-net/everything/leaflab/api/audit"
)

// TestRender_UnknownKey_ReturnsNotOK proves an (action, entity_kind,
// actor_kind) triple with no Registry entry is reported to the caller as
// "not found" (ok=false), never as a rendered string -- callers (activity.go)
// treat that as a bug to fix by registering a Template, per Render's doc
// comment.
func TestRender_UnknownKey_ReturnsNotOK(t *testing.T) {
	sentence, ok := Render("SomeFutureAction", "some_future_entity", audit.ActorKindHuman, RenderInput{})
	if ok {
		t.Fatalf("Render on an unregistered key returned ok=true, sentence=%q, want ok=false", sentence)
	}
	if sentence != "" {
		t.Errorf("Render on an unregistered key returned sentence=%q, want empty", sentence)
	}
}

// TestRender_KnownKey_UsesActorAndEntityLabels proves a representative
// registered Template actually consumes RenderInput's fields (rather than,
// say, silently returning a static string) and capitalizes the actor label
// at the start of the sentence, as every Template in Registry does.
func TestRender_KnownKey_UsesActorAndEntityLabels(t *testing.T) {
	sentence, ok := Render("InviteMember", "household_membership", audit.ActorKindHuman, RenderInput{
		ActorLabel:  "you",
		EntityLabel: "a new member",
	})
	if !ok {
		t.Fatal("Render(InviteMember/household_membership) = not ok, want a registered Template")
	}
	want := "You invited a new member to the household."
	if sentence != want {
		t.Errorf("sentence = %q, want %q", sentence, want)
	}
}

// TestClaimAttemptTemplate_NeverReferencesActorLabel is the load-bearing
// A29/FR76.7 guard: the ClaimAttempt/board Template must never surface
// RenderInput.ActorLabel, for every outcome it switches on (including the
// defensive default), since the attempting principal belongs to another
// household and is never identified. A marker ActorLabel value is supplied
// deliberately -- if any branch of the Template were ever changed to
// interpolate it, this test would catch the leak regardless of what the
// marker's contents happen to be.
func TestClaimAttemptTemplate_NeverReferencesActorLabel(t *testing.T) {
	const marker = "THE-ATTEMPTING-PRINCIPAL-MUST-NEVER-APPEAR"
	outcomes := []string{
		ClaimAttemptNotDischarged,
		ClaimAttemptDischargedRetained,
		ClaimAttemptDischargedDeparted,
		"", // the defensive default branch
		"some-unrecognized-outcome",
	}
	for _, outcome := range outcomes {
		t.Run(outcome, func(t *testing.T) {
			sentence, ok := Render(ClaimAttemptAction, ClaimAttemptEntityKind, audit.ActorKindHuman, RenderInput{
				ActorLabel:  marker,
				EntityLabel: "Backyard Board",
				Outcome:     outcome,
			})
			if !ok {
				t.Fatal("Render(ClaimAttempt/board) = not ok, want a registered Template")
			}
			if strings.Contains(sentence, marker) {
				t.Errorf("sentence %q references ActorLabel -- A29 forbids identifying the attempting principal", sentence)
			}
		})
	}
}

// TestClaimAttemptTemplate_OutcomesRenderDistinctSentences proves the three
// named outcomes actually produce the three distinct sentences the issue's
// Implementation section specifies -- not just "no panic", but the right
// words for each case a household actually reads.
func TestClaimAttemptTemplate_OutcomesRenderDistinctSentences(t *testing.T) {
	cases := []struct {
		outcome string
		want    string
	}{
		{ClaimAttemptNotDischarged, "Someone tried to prove they were at Backyard Board. They couldn't."},
		{ClaimAttemptDischargedDeparted, "Someone tried to prove they were at Backyard Board. They did, and the board left your household."},
		{ClaimAttemptDischargedRetained, "Someone tried to prove they were at Backyard Board. They did, but the board is still yours."},
	}
	for _, c := range cases {
		t.Run(c.outcome, func(t *testing.T) {
			sentence, ok := Render(ClaimAttemptAction, ClaimAttemptEntityKind, audit.ActorKindHuman, RenderInput{
				EntityLabel: "Backyard Board",
				Outcome:     c.outcome,
			})
			if !ok {
				t.Fatal("Render(ClaimAttempt/board) = not ok, want a registered Template")
			}
			if sentence != c.want {
				t.Errorf("outcome %q rendered %q, want %q", c.outcome, sentence, c.want)
			}
		})
	}
}

// TestMustBeRenderSafe_PanicsOnForbiddenSubstring and
// TestMustBeRenderSafe_PanicsOnStatusCodeLikeToken prove the render-safety
// boundary itself (FR59.2) actually rejects the two shapes it claims to:
// an internal identifier and a bare three-digit status-code-shaped token.
// Exercised directly against mustBeRenderSafe (this package's own seam)
// rather than indirectly through some Template, so the assertion is about
// the boundary's behavior, not any one Template's current wording.
func TestMustBeRenderSafe_PanicsOnForbiddenSubstring(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("mustBeRenderSafe did not panic on a sentence containing household_id, want a panic (FR59.2)")
		}
	}()
	mustBeRenderSafe("This sentence leaks household_id somewhere in it.")
}

func TestMustBeRenderSafe_PanicsOnStatusCodeLikeToken(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("mustBeRenderSafe did not panic on a sentence containing a 404-shaped token, want a panic (FR59.2)")
		}
	}()
	mustBeRenderSafe("The request failed with 404.")
}

// TestMustBeRenderSafe_AllowsAnOrdinaryDigitInADate proves the status-code
// heuristic is narrow: an ordinary standalone number that is not
// three-digits-only (e.g. part of a longer numeral) does not false-positive.
// statusCodeLike is a word-boundary \d{3} match, so a bare three-digit
// number anywhere is intentionally still caught -- this test picks a
// four-digit number specifically to prove the boundary is \b\d{3}\b, not
// "contains three digits".
func TestMustBeRenderSafe_AllowsAnOrdinaryDigitInADate(t *testing.T) {
	if got := mustBeRenderSafe("Something happened in 2024."); got == "" {
		t.Error("mustBeRenderSafe rejected a sentence with a four-digit year, want it allowed")
	}
}

// knownInternalTableNames is this test's own independent list of internal
// table/column vocabulary a rendered sentence must never contain -- kept
// separate from render.go's forbiddenSubstrings (rather than importing it)
// so this test is a genuine second check on Registry's actual output, not a
// tautological re-assertion that mustBeRenderSafe agrees with itself.
var knownInternalTableNames = []string{
	"household_membership",
	"device_config",
	"audit_log",
	"claim_challenge",
	"claim_cooldown",
	"board_ownership",
	"admin_elevation",
	"board_uptime_watermark",
	"admin_resolution",
	"support_reference",
}

// knownProtoFieldNames is this test's own list of proto/column field
// vocabulary (distinct from the table names above) that a rendered sentence
// must never contain verbatim.
var knownProtoFieldNames = []string{
	"actor_subject",
	"principal_subject",
	"occurred_at",
	"entity_kind",
	"action",
	"reason",
	"correlation_id",
}

var testStatusCodeLike = regexp.MustCompile(`\b\d{3}\b`)

// representativeInputsFor returns one or more RenderInput values to exercise
// key with -- for the ClaimAttempt/board synthetic entry, one per named
// outcome plus the defensive-default branch (so every branch a Template can
// take is exercised, not just whichever one an arbitrary single RenderInput
// happens to hit); for every other entry, a single representative input
// with non-empty Actor/Entity labels.
func representativeInputsFor(key Key) []RenderInput {
	if key.Action == ClaimAttemptAction && key.EntityKind == ClaimAttemptEntityKind {
		return []RenderInput{
			{EntityLabel: "Greenhouse Sensor 3", Outcome: ClaimAttemptNotDischarged},
			{EntityLabel: "Greenhouse Sensor 3", Outcome: ClaimAttemptDischargedRetained},
			{EntityLabel: "Greenhouse Sensor 3", Outcome: ClaimAttemptDischargedDeparted},
			{EntityLabel: "Greenhouse Sensor 3", Outcome: ""},
		}
	}
	return []RenderInput{
		{ActorLabel: "you", EntityLabel: "a household member"},
		{ActorLabel: "an administrator", EntityLabel: "another household member"},
	}
}

// TestRegistry_Exhaustive_EveryEntryRendersSafely is this task's named
// Testing-section requirement: "Renderer test over every registered
// (action, entity_kind) pair asserting the sentence contains no _id, no
// table name from a known list, no proto field name, and no digit-only
// status code." Iterates Registry itself (not some separately maintained
// list of expected keys), so adding a new Key to Registry with a Template
// that leaks an internal identifier fails this test immediately -- and
// because it walks live map entries rather than a hard-coded expected
// count, it also fails closed (0 iterations silently "passing") if Registry
// were ever accidentally emptied: see the length assertion below.
func TestRegistry_Exhaustive_EveryEntryRendersSafely(t *testing.T) {
	if len(Registry) == 0 {
		t.Fatal("Registry is empty -- nothing was exercised by this test, which would silently pass on a broken build")
	}
	for key := range Registry {
		key := key
		for i, in := range representativeInputsFor(key) {
			name := fmt.Sprintf("%s/%s/%s#%d", key.Action, key.EntityKind, key.ActorKind, i)
			t.Run(name, func(t *testing.T) {
				sentence, ok := Render(key.Action, key.EntityKind, key.ActorKind, in)
				if !ok {
					t.Fatalf("Render(%s, %s, %s) = not ok for a key drawn from Registry itself", key.Action, key.EntityKind, key.ActorKind)
				}
				if sentence == "" {
					t.Fatal("rendered sentence is empty")
				}
				lower := strings.ToLower(sentence)
				if strings.Contains(lower, "_id") {
					t.Errorf("sentence %q contains the string \"_id\"", sentence)
				}
				for _, table := range knownInternalTableNames {
					if strings.Contains(lower, table) {
						t.Errorf("sentence %q contains internal table name %q", sentence, table)
					}
				}
				for _, field := range knownProtoFieldNames {
					if strings.Contains(lower, field) {
						t.Errorf("sentence %q contains proto field name %q", sentence, field)
					}
				}
				if testStatusCodeLike.MatchString(sentence) {
					t.Errorf("sentence %q looks like it carries a status code", sentence)
				}
			})
		}
	}
}

// TestPersonLabel proves the one branch this small helper has: the caller
// themselves renders as "you"; anyone else renders as the supplied
// third-person label, verbatim.
func TestPersonLabel(t *testing.T) {
	if got := PersonLabel(true, "another household member"); got != "you" {
		t.Errorf("PersonLabel(true, ...) = %q, want %q", got, "you")
	}
	if got := PersonLabel(false, "another household member"); got != "another household member" {
		t.Errorf("PersonLabel(false, %q) = %q, want it unchanged", "another household member", got)
	}
}
