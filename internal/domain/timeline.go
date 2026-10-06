package domain

import (
	"fmt"
	"strings"
	"time"
)

// TransitionBasis captures the evidence a reviewer needs to explain why a
// transition happened. Only the fields relevant to the driving action are
// populated, so the basis stays self-describing across every state.
type TransitionBasis struct {
	// Reason carries the steward's justification for a reopen.
	Reason string `json:"reason,omitempty"`
	// MeasurementID identifies the measurement that drove an accept/reject
	// decision and, for sealing, the evidence measurement under seal.
	MeasurementID string `json:"measurement_id,omitempty"`
	// Observed, Deviation and Accepted mirror the decision inputs for
	// measurement-driven transitions.
	Observed  *float64 `json:"observed,omitempty"`
	Deviation *float64 `json:"deviation,omitempty"`
	Accepted  *bool    `json:"accepted,omitempty"`
	// SealID identifies the evidence seal produced by a sealing transition.
	SealID string `json:"seal_id,omitempty"`
	// Summary carries the reviewer's sealing summary.
	Summary string `json:"summary,omitempty"`
	// AuditEventID links the transition back to its raw audit event, so the
	// timeline and the append-only event log never disagree about a step.
	AuditEventID string `json:"audit_event_id,omitempty"`
}

// CaseTransition is one effective state transition of a verification case.
// Transitions are append-only: a failed or rolled-back request never reaches
// storage, so every stored transition corresponds to a committed case change.
type CaseTransition struct {
	// Sequence is the case-scoped, gap-free position of the transition,
	// starting at 1. It is the sole ordering key of the timeline, so later
	// events can never rearrange earlier steps.
	Sequence int        `json:"sequence"`
	CaseID   string     `json:"case_id"`
	Action   CaseAction `json:"action"`
	Actor    string     `json:"actor"`
	// FromState is nil only for the opening transition (no prior state).
	FromState   *CaseState      `json:"from_state,omitempty"`
	ToState     CaseState       `json:"to_state"`
	FromVersion int             `json:"from_version"`
	ToVersion   int             `json:"to_version"`
	Basis       TransitionBasis `json:"basis"`
	OccurredAt  time.Time       `json:"occurred_at"`
}

// NewCaseTransition assures the structural fields of a transition before it is
// committed. Cross-step chain integrity is verified by BuildCaseTimeline at
// read time rather than per record.
func NewCaseTransition(
	sequence int,
	caseID string,
	action CaseAction,
	actor string,
	fromState *CaseState,
	toState CaseState,
	fromVersion, toVersion int,
	basis TransitionBasis,
	at time.Time,
) (CaseTransition, error) {
	item := CaseTransition{
		Sequence:    sequence,
		CaseID:      strings.TrimSpace(caseID),
		Action:      action,
		Actor:       strings.TrimSpace(actor),
		FromState:   fromState,
		ToState:     toState,
		FromVersion: fromVersion,
		ToVersion:   toVersion,
		Basis:       basis,
		OccurredAt:  at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return CaseTransition{}, err
	}
	return item, nil
}

func (t CaseTransition) Validate() error {
	if t.Sequence < 1 {
		return fmt.Errorf("%w: transition sequence must start at 1", ErrInvalidInput)
	}
	if t.CaseID == "" || t.Actor == "" {
		return fmt.Errorf("%w: transition case id and actor are required", ErrInvalidInput)
	}
	rule, ok := allowedCaseTransitions[t.Action]
	if !ok {
		return fmt.Errorf("%w: unsupported case action %q", ErrInvalidInput, t.Action)
	}
	if rule.from == nil {
		if t.FromState != nil {
			return fmt.Errorf("%w: opening transition cannot have a source state", ErrInvalidInput)
		}
	} else if t.FromState == nil || *t.FromState != *rule.from {
		return fmt.Errorf("%w: action %s cannot start from %v", ErrInvalidInput, t.Action, t.FromState)
	}
	if t.ToState != rule.to {
		return fmt.Errorf("%w: action %s cannot end at %q", ErrInvalidInput, t.Action, t.ToState)
	}
	if t.FromVersion < 0 || t.ToVersion < 1 || t.ToVersion <= t.FromVersion {
		return fmt.Errorf("%w: transition versions must advance", ErrInvalidInput)
	}
	if t.OccurredAt.IsZero() {
		return fmt.Errorf("%w: transition time is required", ErrInvalidInput)
	}
	return nil
}

// TimelineStep is one entry of the review-facing state timeline. It mirrors a
// committed CaseTransition and adds the link to the previous step so reviewers
// can walk the chain in either direction.
type TimelineStep struct {
	Sequence    int             `json:"sequence"`
	CaseID      string          `json:"case_id"`
	Action      CaseAction      `json:"action"`
	Actor       string          `json:"actor"`
	FromState   *CaseState      `json:"from_state,omitempty"`
	ToState     CaseState       `json:"to_state"`
	FromVersion int             `json:"from_version"`
	ToVersion   int             `json:"to_version"`
	Basis       TransitionBasis `json:"basis"`
	OccurredAt  time.Time       `json:"occurred_at"`
}

// CaseTimeline is the stable, reviewable history of one case.
type CaseTimeline struct {
	CaseID string         `json:"case_id"`
	Steps  []TimelineStep `json:"steps"`
	// CurrentState and CurrentVersion restate the case head after the last
	// committed step, letting every entry point describe the same fact.
	CurrentState   CaseState `json:"current_state"`
	CurrentVersion int       `json:"current_version"`
}

// BuildCaseTimeline turns the committed, append-only transitions of one case
// into a verified timeline. It enforces that the sequence is gap-free, that
// each step's source state/version matches the previous step's target, and
// that the timeline head matches the case as currently stored. Any break is a
// repository integrity failure rather than a client error.
func BuildCaseTimeline(c VerificationCase, transitions []CaseTransition) (CaseTimeline, error) {
	timeline := CaseTimeline{
		CaseID:         c.ID,
		Steps:          make([]TimelineStep, 0, len(transitions)),
		CurrentState:   c.State,
		CurrentVersion: c.Version,
	}
	if len(transitions) == 0 {
		return CaseTimeline{}, fmt.Errorf("%w: case %s has no committed transitions", ErrTimelineCorrupt, c.ID)
	}

	var fromState *CaseState
	fromVersion := 0
	for index, transition := range transitions {
		if transition.CaseID != c.ID {
			return CaseTimeline{}, fmt.Errorf("%w: transition belongs to case %s, expected %s", ErrTimelineCorrupt, transition.CaseID, c.ID)
		}
		if transition.Sequence != index+1 {
			return CaseTimeline{}, fmt.Errorf("%w: case %s transition sequence gap at position %d (got %d)", ErrTimelineCorrupt, c.ID, index+1, transition.Sequence)
		}
		if !statesEqual(transition.FromState, fromState) || transition.FromVersion != fromVersion {
			return CaseTimeline{}, fmt.Errorf("%w: case %s step %d is not linked to the previous step", ErrTimelineCorrupt, c.ID, transition.Sequence)
		}
		if !CanTransition(transition.Action, transition.FromState) {
			return CaseTimeline{}, fmt.Errorf("%w: case %s step %d uses an illegal transition", ErrTimelineCorrupt, c.ID, transition.Sequence)
		}
		timeline.Steps = append(timeline.Steps, TimelineStep{
			Sequence:    transition.Sequence,
			CaseID:      transition.CaseID,
			Action:      transition.Action,
			Actor:       transition.Actor,
			FromState:   transition.FromState,
			ToState:     transition.ToState,
			FromVersion: transition.FromVersion,
			ToVersion:   transition.ToVersion,
			Basis:       transition.Basis,
			OccurredAt:  transition.OccurredAt,
		})
		fromState = caseStatePointer(transition.ToState)
		fromVersion = transition.ToVersion
	}

	last := transitions[len(transitions)-1]
	if last.ToState != c.State || last.ToVersion != c.Version {
		return CaseTimeline{}, fmt.Errorf("%w: case %s timeline head (%s v%d) does not match stored case (%s v%d)",
			ErrTimelineCorrupt, c.ID, last.ToState, last.ToVersion, c.State, c.Version)
	}
	return timeline, nil
}

func statesEqual(a, b *CaseState) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
