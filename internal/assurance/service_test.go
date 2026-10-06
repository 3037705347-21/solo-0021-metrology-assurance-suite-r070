package assurance

import (
	"context"
	"errors"
	"sync"
	"testing"

	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
	"example.com/solo-0021-metrology-assurance-suite/internal/repository"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	return NewService(repository.New())
}

func mustOpenCase(t *testing.T, s *Service, deviceID string) contracts.CaseResponse {
	t.Helper()
	view, err := s.OpenCase(context.Background(), contracts.OpenCaseRequest{
		DeviceID:  deviceID,
		Nominal:   25.0,
		Tolerance: 0.5,
		Steward:   "Qiao",
	})
	if err != nil {
		t.Fatalf("open case: %v", err)
	}
	return view
}

func mustRegisterDevice(t *testing.T, s *Service, tag string) string {
	t.Helper()
	device, err := s.RegisterDevice(context.Background(), contracts.RegisterDeviceRequest{
		AssetTag: tag,
		Model:    "Bench Meter",
		Room:     "Room A",
		Steward:  "Lin",
	})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	return device.ID
}

func TestFullLifecycleTimeline(t *testing.T) {
	s := newTestService(t)
	deviceID := mustRegisterDevice(t, s, "TL-1001")
	opened := mustOpenCase(t, s, deviceID)
	caseID := opened.Case.ID

	reject, err := s.RecordMeasurement(context.Background(), caseID, contracts.RecordMeasurementRequest{Observed: 26.5, Technician: "Rui"})
	if err != nil {
		t.Fatalf("reject measurement: %v", err)
	}
	if reject.Case.State != domain.CaseRecheckRequired {
		t.Fatalf("state = %s", reject.Case.State)
	}
	if _, err := s.ReopenCase(context.Background(), caseID, contracts.ReopenCaseRequest{Actor: "Lin", Reason: "setup replaced"}); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := s.RecordMeasurement(context.Background(), caseID, contracts.RecordMeasurementRequest{Observed: 25.2, Technician: "Rui"}); err != nil {
		t.Fatalf("accept measurement: %v", err)
	}
	if _, err := s.SealCase(context.Background(), caseID, contracts.SealCaseRequest{Reviewer: "Verifier", Summary: "sealed for review"}); err != nil {
		t.Fatalf("seal: %v", err)
	}

	view, err := s.Case(context.Background(), caseID)
	if err != nil {
		t.Fatalf("read case: %v", err)
	}
	if got := len(view.Timeline.Steps); got != 5 {
		t.Fatalf("timeline steps = %d, want 5", got)
	}
	want := []struct {
		action    domain.CaseAction
		actor     string
		to        domain.CaseState
		toVersion int
	}{
		{domain.CaseActionOpened, "Qiao", domain.CaseAwaitingMeasurement, 1},
		{domain.CaseActionRejected, "Rui", domain.CaseRecheckRequired, 2},
		{domain.CaseActionReopened, "Lin", domain.CaseAwaitingMeasurement, 3},
		{domain.CaseActionAccepted, "Rui", domain.CaseReviewReady, 4},
		{domain.CaseActionSealed, "Verifier", domain.CaseSealed, 5},
	}
	for i, step := range view.Timeline.Steps {
		if step.Sequence != i+1 {
			t.Fatalf("step %d sequence = %d", i, step.Sequence)
		}
		if step.Action != want[i].action || step.Actor != want[i].actor || step.ToState != want[i].to || step.ToVersion != want[i].toVersion {
			t.Fatalf("step %d = %s by %s -> %s v%d, want %s by %s -> %s v%d",
				i, step.Action, step.Actor, step.ToState, step.ToVersion,
				want[i].action, want[i].actor, want[i].to, want[i].toVersion)
		}
		if step.Basis.AuditEventID == "" {
			t.Fatalf("step %d missing audit event link", i)
		}
		if i > 0 && step.FromVersion != want[i-1].toVersion {
			t.Fatalf("step %d version chain broken", i)
		}
	}
	if view.Timeline.CurrentState != domain.CaseSealed || view.Timeline.CurrentVersion != 5 {
		t.Fatalf("head = %s v%d", view.Timeline.CurrentState, view.Timeline.CurrentVersion)
	}
}

func TestFailedRequestsDoNotCreateSteps(t *testing.T) {
	s := newTestService(t)
	deviceID := mustRegisterDevice(t, s, "TL-1002")
	caseID := mustOpenCase(t, s, deviceID).Case.ID

	if _, err := s.RecordMeasurement(context.Background(), caseID, contracts.RecordMeasurementRequest{Observed: 25.2, Technician: "Rui"}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	// Every later action on a review_ready case must fail and roll back.
	if _, err := s.RecordMeasurement(context.Background(), caseID, contracts.RecordMeasurementRequest{Observed: 25.1, Technician: "Rui"}); err == nil {
		t.Fatal("second measurement should conflict")
	}
	if _, err := s.ReopenCase(context.Background(), caseID, contracts.ReopenCaseRequest{Actor: "Lin", Reason: "x"}); err == nil {
		t.Fatal("reopen should conflict")
	}
	if _, err := s.ReopenCase(context.Background(), caseID, contracts.ReopenCaseRequest{Actor: "", Reason: ""}); err == nil {
		t.Fatal("blank reopen should be invalid")
	}

	view, err := s.Case(context.Background(), caseID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := len(view.Timeline.Steps); got != 2 {
		t.Fatalf("steps after failures = %d, want 2", got)
	}
	if view.Timeline.Steps[1].Sequence != 2 {
		t.Fatalf("sequence gap: %v", view.Timeline.Steps)
	}
	if len(view.Events) != 2 {
		t.Fatalf("audit events = %d, want 2 (failed requests must not log)", len(view.Events))
	}
}

func TestLaterEventsDoNotRearrangeHistory(t *testing.T) {
	s := newTestService(t)
	deviceID := mustRegisterDevice(t, s, "TL-1003")
	caseID := mustOpenCase(t, s, deviceID).Case.ID
	if _, err := s.RecordMeasurement(context.Background(), caseID, contracts.RecordMeasurementRequest{Observed: 26.5, Technician: "Rui"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReopenCase(context.Background(), caseID, contracts.ReopenCaseRequest{Actor: "Lin", Reason: "rework"}); err != nil {
		t.Fatal(err)
	}

	before, err := s.Case(context.Background(), caseID)
	if err != nil {
		t.Fatal(err)
	}
	firstSteps := append([]domain.TimelineStep(nil), before.Timeline.Steps...)

	if _, err := s.RecordMeasurement(context.Background(), caseID, contracts.RecordMeasurementRequest{Observed: 25.2, Technician: "Rui"}); err != nil {
		t.Fatal(err)
	}
	after, err := s.Case(context.Background(), caseID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Timeline.Steps) != len(firstSteps)+1 {
		t.Fatalf("new event did not append exactly one step")
	}
	for i := range firstSteps {
		if !stepsEqual(firstSteps[i], after.Timeline.Steps[i]) {
			t.Fatalf("step %d changed after a later event:\nbefore=%+v\nafter =%+v", i, firstSteps[i], after.Timeline.Steps[i])
		}
	}
}

func TestConcurrentSealCreatesOneStep(t *testing.T) {
	s := newTestService(t)
	deviceID := mustRegisterDevice(t, s, "TL-1004")
	caseID := mustOpenCase(t, s, deviceID).Case.ID
	if _, err := s.RecordMeasurement(context.Background(), caseID, contracts.RecordMeasurementRequest{Observed: 25.2, Technician: "Rui"}); err != nil {
		t.Fatal(err)
	}

	const racers = 8
	var wg sync.WaitGroup
	successes, conflicts := 0, 0
	var mu sync.Mutex
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := s.SealCase(context.Background(), caseID, contracts.SealCaseRequest{
				Reviewer: "Reviewer",
				Summary:  "concurrent seal",
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case isStateOrSealConflict(err):
				conflicts++
			default:
				t.Errorf("unexpected seal error: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if successes != 1 || conflicts != racers-1 {
		t.Fatalf("seal outcomes = %d success, %d conflict, want 1/%d", successes, conflicts, racers-1)
	}
	view, err := s.Case(context.Background(), caseID)
	if err != nil {
		t.Fatal(err)
	}
	seals := 0
	for _, step := range view.Timeline.Steps {
		if step.Action == domain.CaseActionSealed {
			seals++
		}
	}
	if seals != 1 {
		t.Fatalf("seal steps = %d, want 1", seals)
	}
	if view.Case.State != domain.CaseSealed || view.Case.Version != 3 {
		t.Fatalf("case head = %s v%d", view.Case.State, view.Case.Version)
	}
	if view.Seal == nil {
		t.Fatal("evidence seal missing")
	}
}

func isStateOrSealConflict(err error) bool {
	return errors.Is(err, domain.ErrStateConflict) || errors.Is(err, domain.ErrSealAlreadyCreated)
}

func stepsEqual(a, b domain.TimelineStep) bool {
	if a.Sequence != b.Sequence || a.Action != b.Action || a.Actor != b.Actor || a.ToState != b.ToState ||
		a.FromVersion != b.FromVersion || a.ToVersion != b.ToVersion || !a.OccurredAt.Equal(b.OccurredAt) {
		return false
	}
	return a.Basis.AuditEventID == b.Basis.AuditEventID
}
