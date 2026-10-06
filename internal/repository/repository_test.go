package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
)

func at() time.Time { return time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC) }

func awaiting() *domain.CaseState {
	s := domain.CaseAwaitingMeasurement
	return &s
}

func TestTransitionAppendIsCommittedAtomicallyWithCase(t *testing.T) {
	repo := New()
	ctx := context.Background()

	// Seed a case with its opening transition.
	if err := repo.Mutate(ctx, func(m *Mutation) error {
		item, err := domain.NewVerificationCase("case-1", "device-1", 25, 0.5, "Qiao", at())
		if err != nil {
			return err
		}
		if err := m.PutCase(item); err != nil {
			return err
		}
		opening, err := domain.NewCaseTransition(m.NextTransitionSequence(item.ID), item.ID,
			domain.CaseActionOpened, "Qiao", nil, item.State, 0, item.Version, domain.TransitionBasis{}, at())
		if err != nil {
			return err
		}
		return m.AppendTransition(opening)
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	boom := errors.New("boom")
	// A mutation that appends a step and then returns an error must discard
	// the step, its sequence number, and every other pending change.
	err := repo.Mutate(ctx, func(m *Mutation) error {
		if got := m.NextTransitionSequence("case-1"); got != 2 {
			t.Fatalf("next sequence inside mutation = %d, want 2", got)
		}
		step, err := domain.NewCaseTransition(2, "case-1", domain.CaseActionRejected, "Rui",
			awaiting(), domain.CaseRecheckRequired, 1, 2, domain.TransitionBasis{}, at())
		if err != nil {
			return err
		}
		if err := m.AppendTransition(step); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("rollback error = %v", err)
	}

	if err := repo.Read(ctx, func(snapshot Snapshot) error {
		transitions := snapshot.Transitions("case-1")
		if len(transitions) != 1 {
			t.Fatalf("rolled-back transition persisted: %d steps", len(transitions))
		}
		item, exists := snapshot.Case("case-1")
		if !exists {
			t.Fatal("case vanished")
		}
		if item.Version != 1 {
			t.Fatalf("case version changed after rollback: %d", item.Version)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// A later committed transition must take sequence 2 with no gap.
	if err := repo.Mutate(ctx, func(m *Mutation) error {
		item, _ := m.Case("case-1")
		item.State = domain.CaseReviewReady
		item.Version = 2
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		review := domain.CaseReviewReady
		step, err := domain.NewCaseTransition(m.NextTransitionSequence("case-1"), "case-1",
			domain.CaseActionAccepted, "Rui", awaiting(), review, 1, 2, domain.TransitionBasis{}, at())
		if err != nil {
			return err
		}
		return m.AppendTransition(step)
	}); err != nil {
		t.Fatalf("second commit: %v", err)
	}
}

func TestAppendTransitionRejectsOutOfOrderSequence(t *testing.T) {
	repo := New()
	ctx := context.Background()
	if err := repo.Mutate(ctx, func(m *Mutation) error {
		item, err := domain.NewVerificationCase("case-1", "device-1", 25, 0.5, "Qiao", at())
		if err != nil {
			return err
		}
		if err := m.PutCase(item); err != nil {
			return err
		}
		// Claim sequence 3 directly, skipping 1 and 2.
		step, err := domain.NewCaseTransition(3, "case-1", domain.CaseActionOpened, "Qiao",
			nil, domain.CaseAwaitingMeasurement, 0, 1, domain.TransitionBasis{}, at())
		if err != nil {
			return err
		}
		if err := m.AppendTransition(step); err == nil {
			t.Fatal("out-of-order sequence was accepted")
		}
		// Transition for a case that does not exist must fail.
		other, err := domain.NewCaseTransition(1, "missing", domain.CaseActionOpened, "Qiao",
			nil, domain.CaseAwaitingMeasurement, 0, 1, domain.TransitionBasis{}, at())
		if err != nil {
			return err
		}
		if err := m.AppendTransition(other); err == nil {
			t.Fatal("transition for unknown case was accepted")
		}
		return nil
	}); err != nil {
		t.Fatalf("mutation: %v", err)
	}
}

func TestTransitionsAreIsolatedBetweenCases(t *testing.T) {
	repo := New()
	ctx := context.Background()
	if err := repo.Mutate(ctx, func(m *Mutation) error {
		for _, id := range []string{"case-1", "case-2"} {
			item, err := domain.NewVerificationCase(id, "device-1", 25, 0.5, "Qiao", at())
			if err != nil {
				return err
			}
			if err := m.PutCase(item); err != nil {
				return err
			}
			opening, err := domain.NewCaseTransition(m.NextTransitionSequence(id), id,
				domain.CaseActionOpened, "Qiao", nil, item.State, 0, item.Version, domain.TransitionBasis{}, at())
			if err != nil {
				return err
			}
			if err := m.AppendTransition(opening); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := repo.Read(ctx, func(snapshot Snapshot) error {
		if got := len(snapshot.Transitions("case-1")); got != 1 {
			t.Fatalf("case-1 steps = %d, want 1", got)
		}
		if got := len(snapshot.Transitions("case-2")); got != 1 {
			t.Fatalf("case-2 steps = %d, want 1", got)
		}
		if got := len(snapshot.Transitions("case-3")); got != 0 {
			t.Fatalf("unknown case steps = %d, want 0", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
