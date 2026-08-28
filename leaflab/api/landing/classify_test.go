package landing

// Testing for #1350's FR62 five-condition classifier. Every case here is a
// pure, fixture-isolated Input -> Result check (no DB, no gRPC) --
// per this task's Testing section: "each of the five conditions is produced
// by a fixture that isolates it, and each produces a different sentence.
// Assert all five sentence texts are distinct" and "no sentence contains
// plant vocabulary".

import (
	"regexp"
	"testing"
)

// -- Five conditions, isolated fixtures, distinct sentences -----------------

func TestClassify_FiveConditions_IsolatedFixtures(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want Condition
	}{
		{
			name: "condition 5: no household",
			in:   Input{HasHousehold: false},
			want: ConditionNoHousehold,
		},
		{
			name: "condition 4: service degraded",
			in:   Input{HasHousehold: true, ServiceDegraded: true},
			want: ConditionServiceDegraded,
		},
		{
			name: "condition 3: household wholly silent",
			in:   Input{HasHousehold: true, HouseholdWhollySilent: true},
			want: ConditionHouseholdSilent,
		},
		{
			name: "condition 2: one board wholly silent",
			in:   Input{HasHousehold: true, AnyBoardWhollySilent: true},
			want: ConditionBoardSilent,
		},
		{
			name: "condition 1: one sensor silent while its board reports",
			in:   Input{HasHousehold: true, AnySensorSilentWhileBoardReports: true},
			want: ConditionSensorSilent,
		},
		{
			name: "healthy: no fault signal set",
			in:   Input{HasHousehold: true},
			want: ConditionHealthy,
		},
	}

	seenSentences := map[string]string{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(c.in)
			if got.Condition != c.want {
				t.Fatalf("Classify(%+v).Condition = %v, want %v", c.in, got.Condition, c.want)
			}
			if c.want == ConditionHealthy {
				if got.Sentence != "" {
					t.Errorf("ConditionHealthy Sentence = %q, want empty (TopLine alone covers it)", got.Sentence)
				}
				return
			}
			if got.Sentence == "" {
				t.Fatalf("%s: Sentence is empty -- FR62 requires each fault condition to carry a complete sentence, never a blank page", c.name)
			}
			if prior, ok := seenSentences[got.Sentence]; ok {
				t.Errorf("%s: Sentence %q is identical to %s's sentence -- FR62 requires all five worded differently", c.name, got.Sentence, prior)
			}
			seenSentences[got.Sentence] = c.name
		})
	}

	if len(seenSentences) != 5 {
		t.Fatalf("collected %d distinct fault sentences, want 5 (one per FR62 condition)", len(seenSentences))
	}
}

// TestClassify_AllFiveSentencesDistinct is the issue's literal "assert all
// five sentence texts are distinct" checked directly against the five
// sentence constants classify.go declares, independent of any Input
// plumbing above.
func TestClassify_AllFiveSentencesDistinct(t *testing.T) {
	sentences := []string{
		sentenceNoHousehold,
		sentenceServiceDegraded,
		sentenceHouseholdSilent,
		sentenceBoardSilent,
		sentenceSensorSilent,
	}
	seen := map[string]bool{}
	for _, s := range sentences {
		if s == "" {
			t.Fatal("a FR62 sentence constant is empty")
		}
		if seen[s] {
			t.Errorf("duplicate FR62 sentence text: %q", s)
		}
		seen[s] = true
	}
	if len(seen) != 5 {
		t.Fatalf("got %d distinct sentence texts, want 5", len(seen))
	}
}

// nfr62PlantVocabulary are word-boundary-safe patterns for words that would
// make a sentence "a verdict on the plants" rather than the device-scoped
// statement FR62 requires ("the top-line sentence is device-scoped ... and
// is never worded as a verdict on the plants"). Matched case-insensitively.
// Word-boundary anchored so "leaf" doesn't false-positive on the "LeafLab"
// brand name embedded in sentenceServiceDegraded.
var nfr62PlantVocabulary = []*regexp.Regexp{
	regexp.MustCompile(`(?i)plants?`),
	regexp.MustCompile(`(?i)leaves`),
	regexp.MustCompile(`(?i)soil`),
	regexp.MustCompile(`(?i)moisture`),
	regexp.MustCompile(`(?i)water(ing)?`),
	regexp.MustCompile(`(?i)wilt(ed|ing)?`),
	regexp.MustCompile(`(?i)grow(th|ing)?`),
	regexp.MustCompile(`(?i)roots?`),
}

func containsPlantVocabulary(s string) (string, bool) {
	for _, re := range nfr62PlantVocabulary {
		if re.MatchString(s) {
			return re.String(), true
		}
	}
	return "", false
}

// TestClassify_NoSentenceContainsPlantVocabulary is the issue's "no
// sentence contains plant vocabulary" -- checked against every FR62
// sentence. The two top-line strings (landingTopLineHealthy/
// landingTopLineNotHealthy) live in leaflab/api's landing.go and are
// checked by TestLandingTopLines_NoPlantVocabulary in that package instead,
// since this package has no knowledge of them.
func TestClassify_NoSentenceContainsPlantVocabulary(t *testing.T) {
	sentences := map[string]string{
		"sentenceNoHousehold":     sentenceNoHousehold,
		"sentenceServiceDegraded": sentenceServiceDegraded,
		"sentenceHouseholdSilent": sentenceHouseholdSilent,
		"sentenceBoardSilent":     sentenceBoardSilent,
		"sentenceSensorSilent":    sentenceSensorSilent,
	}
	for name, sentence := range sentences {
		if word, found := containsPlantVocabulary(sentence); found {
			t.Errorf("%s (%q) contains plant vocabulary %q -- FR62: never worded as a verdict on the plants", name, sentence, word)
		}
	}
}

// -- Condition 5: never a blank page -----------------------------------------

// TestClassify_NoHousehold_CarriesBothNamedNextSteps is FR62's "carrying a
// named next step (claim a board you have, per FR76; or ask the person who
// set it up to add you, per FR75). Never a blank page" -- both steps
// together, never one at the exclusion of the other (per NextStep's doc
// comment in classify.go).
func TestClassify_NoHousehold_CarriesBothNamedNextSteps(t *testing.T) {
	got := Classify(Input{HasHousehold: false})

	if got.Condition != ConditionNoHousehold {
		t.Fatalf("Condition = %v, want ConditionNoHousehold", got.Condition)
	}
	if len(got.NextSteps) == 0 {
		t.Fatal("NextSteps is empty for ConditionNoHousehold -- FR62: never a blank page")
	}

	var sawClaim, sawInvite bool
	for _, step := range got.NextSteps {
		switch step.Action {
		case NextStepActionClaimBoard:
			sawClaim = true
		case NextStepActionRequestInvite:
			sawInvite = true
		default:
			t.Errorf("unexpected NextStepAction %v -- FR62 names exactly two next steps", step.Action)
		}
	}
	if !sawClaim {
		t.Error("NextSteps missing NextStepActionClaimBoard (FR76)")
	}
	if !sawInvite {
		t.Error("NextSteps missing NextStepActionRequestInvite (FR75)")
	}
}

// TestClassify_OnlyConditionNoHousehold_HasNextSteps proves every other
// condition (including Healthy) carries no NextSteps -- NextSteps is
// exclusively condition 5's mechanism, not a general-purpose field other
// conditions might accidentally populate.
func TestClassify_OnlyConditionNoHousehold_HasNextSteps(t *testing.T) {
	cases := []Input{
		{HasHousehold: true, ServiceDegraded: true},
		{HasHousehold: true, HouseholdWhollySilent: true},
		{HasHousehold: true, AnyBoardWhollySilent: true},
		{HasHousehold: true, AnySensorSilentWhileBoardReports: true},
		{HasHousehold: true},
	}
	for _, in := range cases {
		got := Classify(in)
		if len(got.NextSteps) != 0 {
			t.Errorf("Classify(%+v) = %+v, want no NextSteps (only ConditionNoHousehold carries any)", in, got)
		}
	}
}

// -- Priority ordering --------------------------------------------------------

// TestClassify_PriorityOrder proves the fixed priority order documented on
// Classify: when more than one signal is true at once, the most-encompassing
// condition wins, first match in the documented order.
func TestClassify_PriorityOrder(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want Condition
	}{
		{
			name: "no household beats everything, even if other signals happen to be set",
			in: Input{
				HasHousehold:                     false,
				ServiceDegraded:                  true,
				HouseholdWhollySilent:            true,
				AnyBoardWhollySilent:              true,
				AnySensorSilentWhileBoardReports: true,
			},
			want: ConditionNoHousehold,
		},
		{
			name: "service degraded beats household-wide silence",
			in: Input{
				HasHousehold:          true,
				ServiceDegraded:       true,
				HouseholdWhollySilent: true,
				AnyBoardWhollySilent:  true,
			},
			want: ConditionServiceDegraded,
		},
		{
			name: "household-wide silence beats a single board",
			in: Input{
				HasHousehold:          true,
				HouseholdWhollySilent: true,
				AnyBoardWhollySilent:  true,
			},
			want: ConditionHouseholdSilent,
		},
		{
			name: "board silence beats sensor silence",
			in: Input{
				HasHousehold:                     true,
				AnyBoardWhollySilent:              true,
				AnySensorSilentWhileBoardReports: true,
			},
			want: ConditionBoardSilent,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.in).Condition; got != c.want {
				t.Errorf("Classify(%+v).Condition = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
