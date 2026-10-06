package domain

import (
	"fmt"
	"sort"
	"time"
)

// TimelineStep is one effective state transition, presented in a stable,
// reviewable order for a single verification case. Step numbers are dense and
// case-local (1 = case opening); Sequence is the repository-wide append order
// that anchors the ordering.
type TimelineStep struct {
	Step        int       `json:"step"`
	Sequence    int64     `json:"sequence"`
	EventID     string    `json:"event_id"`
	CaseID      string    `json:"case_id"`
	Action      string    `json:"action"`
	Actor       string    `json:"actor"`
	FromState   CaseState `json:"from_state,omitempty"`
	ToState     CaseState `json:"to_state"`
	FromVersion int       `json:"from_version,omitempty"`
	ToVersion   int       `json:"to_version"`
	RefType     string    `json:"ref_type,omitempty"`
	RefID       string    `json:"ref_id,omitempty"`
	Basis       string    `json:"basis"`
	CreatedAt   time.Time `json:"created_at"`
}

// BuildCaseTimeline projects the append-only audit events of one case into a
// deterministic timeline.
//
// Guarantees:
//   - Only events that certify a legal case state transition become steps, so
//     failed or rolled-back requests (which never committed an event) cannot
//     fabricate steps.
//   - Steps are ordered by their commit Sequence, never by timestamp: events
//     appended later cannot reorder earlier history.
//   - Consecutive steps must link (to-state/version of one equals from-state/
//     version of the next), the first step must be case_opened, and the final
//     step must land exactly on the case's current state and version. Any
//     divergence is reported as a timeline integrity error instead of a
//     silently rewritten history.
func BuildCaseTimeline(item VerificationCase, events []AuditEvent) ([]TimelineStep, error) {
	candidates := make([]AuditEvent, 0, len(events))
	for _, event := range events {
		if event.CaseID == item.ID && event.SubjectType == "case" && IsCaseTransitionKind(event.Kind) {
			candidates = append(candidates, event)
		}
	}
	// Stable sort on Sequence (tie broken by id) makes the projection
	// independent of slice order and immune to timestamp collisions.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Sequence != candidates[j].Sequence {
			return candidates[i].Sequence < candidates[j].Sequence
		}
		return candidates[i].ID < candidates[j].ID
	})

	steps := make([]TimelineStep, 0, len(candidates))
	var prevState CaseState
	var prevVersion int
	for index, event := range candidates {
		if event.Sequence <= 0 {
			return nil, fmt.Errorf("%w: event %s has no commit sequence", ErrTimelineIntegrity, event.ID)
		}
		if event.FromState != prevState || event.FromVersion != prevVersion {
			return nil, fmt.Errorf("%w: event %s breaks the chain: expected %s v%d, found %s v%d",
				ErrTimelineIntegrity, event.ID, displayState(prevState), prevVersion, displayState(event.FromState), event.FromVersion)
		}
		shape, ok := caseTransitionShapes[event.Kind]
		if !ok || event.ToState != shape.to || event.ToVersion != event.FromVersion+1 {
			return nil, fmt.Errorf("%w: event %s is not a valid %s transition", ErrTimelineIntegrity, event.ID, event.Kind)
		}
		if index > 0 && event.Sequence <= candidates[index-1].Sequence {
			return nil, fmt.Errorf("%w: event %s repeats an earlier append sequence", ErrTimelineIntegrity, event.ID)
		}
		steps = append(steps, TimelineStep{
			Step:        index + 1,
			Sequence:    event.Sequence,
			EventID:     event.ID,
			CaseID:      item.ID,
			Action:      event.Kind,
			Actor:       event.Actor,
			FromState:   event.FromState,
			ToState:     event.ToState,
			FromVersion: event.FromVersion,
			ToVersion:   event.ToVersion,
			RefType:     event.RefType,
			RefID:       event.RefID,
			Basis:       event.Detail,
			CreatedAt:   event.CreatedAt,
		})
		prevState = event.ToState
		prevVersion = event.ToVersion
	}

	if len(steps) == 0 {
		return nil, fmt.Errorf("%w: case %s has no opening transition", ErrTimelineIntegrity, item.ID)
	}
	if first := steps[0]; first.Action != EventCaseOpened || first.ToState != CaseAwaitingMeasurement || first.ToVersion != 1 {
		return nil, fmt.Errorf("%w: case %s does not start with an opening transition", ErrTimelineIntegrity, item.ID)
	}
	if prevState != item.State || prevVersion != item.Version {
		return nil, fmt.Errorf("%w: timeline of case %s ends at %s v%d but the case is %s v%d",
			ErrTimelineIntegrity, item.ID, displayState(prevState), prevVersion, displayState(item.State), item.Version)
	}
	return steps, nil
}
