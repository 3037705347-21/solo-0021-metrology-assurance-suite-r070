package domain

import (
	"errors"
	"testing"
	"time"
)

func testClock() time.Time {
	return time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
}

func TestStateMachineLegalEdges(t *testing.T) {
	c, err := NewVerificationCase("case-1", "device-1", 25, 0.5, "Qiao", testClock())
	if err != nil {
		t.Fatalf("new case: %v", err)
	}
	if c.Version != 1 || c.State != CaseAwaitingMeasurement {
		t.Fatalf("new case = %s v%d", c.State, c.Version)
	}

	steps := []struct {
		name    string
		apply   func(at time.Time) error
		want    CaseState
		version int
	}{
		{"reject", func(at time.Time) error { return c.RecordMeasurement(false, at) }, CaseRecheckRequired, 2},
		{"reopen", c.Reopen, CaseAwaitingMeasurement, 3},
		{"accept", func(at time.Time) error { return c.RecordMeasurement(true, at) }, CaseReviewReady, 4},
		{"seal", c.Seal, CaseSealed, 5},
	}
	for _, step := range steps {
		at := testClock().Add(time.Duration(step.version) * time.Minute)
		if err := step.apply(at); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if c.State != step.want || c.Version != step.version {
			t.Fatalf("%s: got %s v%d, want %s v%d", step.name, c.State, c.Version, step.want, step.version)
		}
	}
}

func TestStateMachineRejectsIllegalEdges(t *testing.T) {
	c, err := NewVerificationCase("case-1", "device-1", 25, 0.5, "Qiao", testClock())
	if err != nil {
		t.Fatalf("new case: %v", err)
	}
	// seal and reopen are illegal directly after opening.
	if err := c.Seal(testClock()); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("seal from awaiting = %v, want state conflict", err)
	}
	if err := c.Reopen(testClock()); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("reopen from awaiting = %v, want state conflict", err)
	}
	if c.Version != 1 {
		t.Fatalf("rejected edge changed version to %d", c.Version)
	}
}

func TestCanTransitionTable(t *testing.T) {
	awaiting := CaseAwaitingMeasurement
	review := CaseReviewReady
	recheck := CaseRecheckRequired
	cases := []struct {
		action CaseAction
		from   *CaseState
		want   bool
	}{
		{CaseActionOpened, nil, true},
		{CaseActionOpened, &awaiting, false},
		{CaseActionAccepted, &awaiting, true},
		{CaseActionRejected, &awaiting, true},
		{CaseActionReopened, &recheck, true},
		{CaseActionReopened, &review, false},
		{CaseActionSealed, &review, true},
		{CaseActionSealed, &awaiting, false},
		{CaseAction("unknown"), nil, false},
	}
	for _, tc := range cases {
		if got := CanTransition(tc.action, tc.from); got != tc.want {
			t.Errorf("CanTransition(%s, %v) = %v, want %v", tc.action, tc.from, got, tc.want)
		}
	}
}

func ptrFloat(v float64) *float64 { return &v }
func ptrBool(v bool) *bool        { return &v }

func sampleTransitions(t *testing.T) []CaseTransition {
	t.Helper()
	open, err := NewCaseTransition(1, "case-1", CaseActionOpened, "Qiao", nil,
		CaseAwaitingMeasurement, 0, 1, TransitionBasis{}, testClock())
	if err != nil {
		t.Fatal(err)
	}
	reject, err := NewCaseTransition(2, "case-1", CaseActionRejected, "Rui",
		ptr(CaseAwaitingMeasurement), CaseRecheckRequired, 1, 2,
		TransitionBasis{MeasurementID: "m-1", Observed: ptrFloat(26.5), Accepted: ptrBool(false)}, testClock().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	reopen, err := NewCaseTransition(3, "case-1", CaseActionReopened, "Lin",
		ptr(CaseRecheckRequired), CaseAwaitingMeasurement, 2, 3,
		TransitionBasis{Reason: "setup replaced"}, testClock().Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	accept, err := NewCaseTransition(4, "case-1", CaseActionAccepted, "Rui",
		ptr(CaseAwaitingMeasurement), CaseReviewReady, 3, 4,
		TransitionBasis{MeasurementID: "m-2", Observed: ptrFloat(25.2), Accepted: ptrBool(true)}, testClock().Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	seal, err := NewCaseTransition(5, "case-1", CaseActionSealed, "Verifier",
		ptr(CaseReviewReady), CaseSealed, 4, 5,
		TransitionBasis{SealID: "seal-1", MeasurementID: "m-2"}, testClock().Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return []CaseTransition{open, reject, reopen, accept, seal}
}

func ptr(s CaseState) *CaseState { return &s }

func TestBuildCaseTimelineAcceptsContinuousChain(t *testing.T) {
	transitions := sampleTransitions(t)
	c, err := NewVerificationCase("case-1", "device-1", 25, 0.5, "Qiao", testClock())
	if err != nil {
		t.Fatal(err)
	}
	c.State = CaseSealed
	c.Version = 5

	timeline, err := BuildCaseTimeline(c, transitions)
	if err != nil {
		t.Fatalf("build timeline: %v", err)
	}
	if len(timeline.Steps) != 5 {
		t.Fatalf("steps = %d, want 5", len(timeline.Steps))
	}
	if timeline.CurrentState != CaseSealed || timeline.CurrentVersion != 5 {
		t.Fatalf("head = %s v%d", timeline.CurrentState, timeline.CurrentVersion)
	}
	for i, step := range timeline.Steps {
		if step.Sequence != i+1 {
			t.Fatalf("step %d sequence = %d", i, step.Sequence)
		}
	}
}

func TestBuildCaseTimelineDetectsCorruption(t *testing.T) {
	good := sampleTransitions(t)
	c, err := NewVerificationCase("case-1", "device-1", 25, 0.5, "Qiao", testClock())
	if err != nil {
		t.Fatal(err)
	}
	c.State = CaseSealed
	c.Version = 5

	t.Run("no transitions", func(t *testing.T) {
		if _, err := BuildCaseTimeline(c, nil); !errors.Is(err, ErrTimelineCorrupt) {
			t.Fatalf("empty transitions = %v, want corrupt", err)
		}
	})

	t.Run("sequence gap", func(t *testing.T) {
		broken := append([]CaseTransition(nil), good...)
		broken[1].Sequence = 9
		if _, err := BuildCaseTimeline(c, broken); !errors.Is(err, ErrTimelineCorrupt) {
			t.Fatalf("gap = %v, want corrupt", err)
		}
	})

	t.Run("broken linkage", func(t *testing.T) {
		broken := append([]CaseTransition(nil), good...)
		broken[2].FromState = ptr(CaseReviewReady)
		if _, err := BuildCaseTimeline(c, broken); !errors.Is(err, ErrTimelineCorrupt) {
			t.Fatalf("broken link = %v, want corrupt", err)
		}
	})

	t.Run("head mismatch", func(t *testing.T) {
		head := c
		head.Version = 4
		head.State = CaseReviewReady
		if _, err := BuildCaseTimeline(head, good); !errors.Is(err, ErrTimelineCorrupt) {
			t.Fatalf("head mismatch = %v, want corrupt", err)
		}
	})

	t.Run("foreign case transition", func(t *testing.T) {
		broken := append([]CaseTransition(nil), good...)
		broken[0].CaseID = "other-case"
		if _, err := BuildCaseTimeline(c, broken); !errors.Is(err, ErrTimelineCorrupt) {
			t.Fatalf("foreign transition = %v, want corrupt", err)
		}
	})
}

func TestNewCaseTransitionRejectsStructuralErrors(t *testing.T) {
	if _, err := NewCaseTransition(0, "case-1", CaseActionOpened, "Qiao", nil,
		CaseAwaitingMeasurement, 0, 1, TransitionBasis{}, testClock()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("sequence 0 = %v", err)
	}
	if _, err := NewCaseTransition(1, "case-1", CaseActionSealed, "V",
		nil, CaseSealed, 0, 1, TransitionBasis{}, testClock()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("seal without source = %v", err)
	}
	if _, err := NewCaseTransition(1, "case-1", CaseActionAccepted, "Rui",
		ptr(CaseReviewReady), CaseReviewReady, 1, 2, TransitionBasis{}, testClock()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("illegal edge accepted = %v", err)
	}
}
