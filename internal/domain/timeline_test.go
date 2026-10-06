package domain

import (
	"errors"
	"testing"
	"time"
)

func sampleCase() VerificationCase {
	item, err := NewVerificationCase("case-0001", "device-0001", 25, 0.5, "Qiao", time.Unix(100, 0))
	if err != nil {
		panic(err)
	}
	return item
}

func transitionEvent(t *testing.T, id string, sequence int64, kind, actor, basis string, from, to CaseState, fromV, toV int, refType, refID string, at time.Time) AuditEvent {
	t.Helper()
	event, err := NewCaseTransitionEvent(id, "case-0001", kind, actor, basis, from, to, fromV, toV, refType, refID, at)
	if err != nil {
		t.Fatalf("NewCaseTransitionEvent(%s): %v", id, err)
	}
	event.Sequence = sequence
	return event
}

func fullLifecycleEvents(t *testing.T) []AuditEvent {
	open := transitionEvent(t, "event-1", 2, EventCaseOpened, "Qiao", "opened", "", CaseAwaitingMeasurement, 0, 1, "", "", time.Unix(100, 0))
	reject := transitionEvent(t, "event-2", 5, EventMeasurementRejected, "Rui", "too large", CaseAwaitingMeasurement, CaseRecheckRequired, 1, 2, RefMeasurement, "measurement-1", time.Unix(200, 0))
	reopen := transitionEvent(t, "event-3", 6, EventCaseReopened, "Lin", "setup replaced", CaseRecheckRequired, CaseAwaitingMeasurement, 2, 3, "", "", time.Unix(300, 0))
	accept := transitionEvent(t, "event-4", 9, EventMeasurementAccepted, "Ning", "within tolerance", CaseAwaitingMeasurement, CaseReviewReady, 3, 4, RefMeasurement, "measurement-2", time.Unix(400, 0))
	seal := transitionEvent(t, "event-5", 11, EventCaseSealed, "Vera", "reviewed", CaseReviewReady, CaseSealed, 4, 5, RefEvidenceSeal, "seal-1", time.Unix(500, 0))
	return []AuditEvent{open, reject, reopen, accept, seal}
}

func TestBuildCaseTimelineFullLifecycle(t *testing.T) {
	item := sampleCase()
	item.State = CaseSealed
	item.Version = 5
	item.SealedAt = nil

	steps, err := BuildCaseTimeline(item, fullLifecycleEvents(t))
	if err != nil {
		t.Fatalf("BuildCaseTimeline: %v", err)
	}
	if len(steps) != 5 {
		t.Fatalf("steps = %d, expected 5", len(steps))
	}
	expected := []struct {
		action   string
		from, to CaseState
		fromV, v int
		refType  string
	}{
		{EventCaseOpened, "", CaseAwaitingMeasurement, 0, 1, ""},
		{EventMeasurementRejected, CaseAwaitingMeasurement, CaseRecheckRequired, 1, 2, RefMeasurement},
		{EventCaseReopened, CaseRecheckRequired, CaseAwaitingMeasurement, 2, 3, ""},
		{EventMeasurementAccepted, CaseAwaitingMeasurement, CaseReviewReady, 3, 4, RefMeasurement},
		{EventCaseSealed, CaseReviewReady, CaseSealed, 4, 5, RefEvidenceSeal},
	}
	for index, want := range expected {
		step := steps[index]
		if step.Step != index+1 {
			t.Errorf("step %d: step number = %d", index+1, step.Step)
		}
		if step.Action != want.action || step.FromState != want.from || step.ToState != want.to ||
			step.FromVersion != want.fromV || step.ToVersion != want.v || step.RefType != want.refType {
			t.Errorf("step %d = %+v, expected %+v", index+1, step, want)
		}
	}
}

func TestBuildCaseTimelineIgnoresForeignAndNonTransitionEvents(t *testing.T) {
	item := sampleCase()
	events := []AuditEvent{
		{ID: "device-event", Sequence: 1, SubjectType: "device", SubjectID: "device-0001", Kind: "device_enrolled", Actor: "Qiao", Detail: "enrolled", CreatedAt: time.Unix(90, 0)},
		transitionEvent(t, "event-1", 2, EventCaseOpened, "Qiao", "opened", "", CaseAwaitingMeasurement, 0, 1, "", "", time.Unix(100, 0)),
		{ID: "other-case-event", Sequence: 3, SubjectType: "case", SubjectID: "case-9999", CaseID: "case-9999", Kind: EventCaseOpened, Actor: "Other", Detail: "x", CreatedAt: time.Unix(110, 0)},
	}
	steps, err := BuildCaseTimeline(item, events)
	if err != nil {
		t.Fatalf("BuildCaseTimeline: %v", err)
	}
	if len(steps) != 1 || steps[0].Action != EventCaseOpened {
		t.Fatalf("steps = %+v, expected only the opening step", steps)
	}
}

func TestBuildCaseTimelineOrdersBySequenceNotTimestamp(t *testing.T) {
	item := sampleCase()
	item.State = CaseRecheckRequired
	item.Version = 2
	open := transitionEvent(t, "event-1", 3, EventCaseOpened, "Qiao", "opened", "", CaseAwaitingMeasurement, 0, 1, "", "", time.Unix(500, 0))
	reject := transitionEvent(t, "event-2", 7, EventMeasurementRejected, "Rui", "too large", CaseAwaitingMeasurement, CaseRecheckRequired, 1, 2, RefMeasurement, "measurement-1", time.Unix(100, 0))
	// Hand the events in reverse and give the earlier sequence a later
	// timestamp: the projection must follow append sequence, not time.
	steps, err := BuildCaseTimeline(item, []AuditEvent{reject, open})
	if err != nil {
		t.Fatalf("BuildCaseTimeline: %v", err)
	}
	if steps[0].EventID != "event-1" || steps[1].EventID != "event-2" {
		t.Fatalf("timeline order = %s, %s; expected event-1, event-2", steps[0].EventID, steps[1].EventID)
	}
}

func TestBuildCaseTimelineRejectsBrokenChain(t *testing.T) {
	item := sampleCase()
	item.State = CaseReviewReady
	item.Version = 4
	open := transitionEvent(t, "event-1", 2, EventCaseOpened, "Qiao", "opened", "", CaseAwaitingMeasurement, 0, 1, "", "", time.Unix(100, 0))
	// Claims to jump straight to review_ready v4, skipping versions.
	jump := transitionEvent(t, "event-2", 5, EventMeasurementAccepted, "Rui", "x", CaseAwaitingMeasurement, CaseReviewReady, 1, 2, RefMeasurement, "measurement-1", time.Unix(200, 0))
	jump.FromVersion = 3
	jump.ToVersion = 4
	if _, err := BuildCaseTimeline(item, []AuditEvent{open, jump}); !errors.Is(err, ErrTimelineIntegrity) {
		t.Fatalf("expected ErrTimelineIntegrity for broken chain, got %v", err)
	}
}

func TestBuildCaseTimelineRejectsFinalMismatch(t *testing.T) {
	item := sampleCase() // case still claims v1 awaiting_measurement
	events := fullLifecycleEvents(t)
	events[4].Sequence = 11
	if _, err := BuildCaseTimeline(item, events); !errors.Is(err, ErrTimelineIntegrity) {
		t.Fatalf("expected ErrTimelineIntegrity when timeline outruns the case, got %v", err)
	}
}

func TestBuildCaseTimelineRejectsMissingOpening(t *testing.T) {
	item := sampleCase()
	item.State = CaseRecheckRequired
	item.Version = 2
	reject := transitionEvent(t, "event-9", 9, EventMeasurementRejected, "Rui", "x", CaseAwaitingMeasurement, CaseRecheckRequired, 1, 2, RefMeasurement, "measurement-1", time.Unix(200, 0))
	if _, err := BuildCaseTimeline(item, []AuditEvent{reject}); !errors.Is(err, ErrTimelineIntegrity) {
		t.Fatalf("expected ErrTimelineIntegrity without opening, got %v", err)
	}
}

func TestNewCaseTransitionEventRejectsIllegalShape(t *testing.T) {
	// reopening a review_ready case is not a legal transition.
	if _, err := NewCaseTransitionEvent("event-x", "case-0001", EventCaseReopened, "Lin", "reason",
		CaseReviewReady, CaseAwaitingMeasurement, 4, 5, "", "", time.Unix(100, 0)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for illegal reopen, got %v", err)
	}
	// accepted measurement event must reference a measurement.
	if _, err := NewCaseTransitionEvent("event-x", "case-0001", EventMeasurementAccepted, "Rui", "basis",
		CaseAwaitingMeasurement, CaseReviewReady, 1, 2, "", "", time.Unix(100, 0)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for missing reference, got %v", err)
	}
	// version must advance by exactly one.
	if _, err := NewCaseTransitionEvent("event-x", "case-0001", EventCaseOpened, "Qiao", "basis",
		"", CaseAwaitingMeasurement, 0, 2, "", "", time.Unix(100, 0)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for version skip, got %v", err)
	}
	// unknown event kind.
	if _, err := NewCaseTransitionEvent("event-x", "case-0001", "mystery", "Qiao", "basis",
		"", CaseAwaitingMeasurement, 0, 1, "", "", time.Unix(100, 0)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for unknown kind, got %v", err)
	}
}
