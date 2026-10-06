package domain

import (
	"fmt"
	"strings"
	"time"
)

// Reference kinds recorded on case transition events so reviewers can trace
// the evidence that drove a transition.
const (
	RefMeasurement  = "measurement"
	RefEvidenceSeal = "evidence_seal"
)

// Case audit event kinds. Every case event kind is also one valid case state
// transition; see caseTransitionShapes.
const (
	EventCaseOpened          = "case_opened"
	EventMeasurementAccepted = "measurement_accepted"
	EventMeasurementRejected = "measurement_rejected"
	EventCaseReopened        = "case_reopened"
	EventCaseSealed          = "case_sealed"
)

type caseTransitionShape struct {
	kind    string
	from    CaseState
	to      CaseState
	refType string // empty means the transition must not carry a reference
}

// caseTransitionShapes is the single source of truth for which event kind may
// move a case between which states, and which evidence reference it requires.
var caseTransitionShapes = map[string]caseTransitionShape{
	EventCaseOpened:          {kind: EventCaseOpened, from: "", to: CaseAwaitingMeasurement},
	EventMeasurementAccepted: {kind: EventMeasurementAccepted, from: CaseAwaitingMeasurement, to: CaseReviewReady, refType: RefMeasurement},
	EventMeasurementRejected: {kind: EventMeasurementRejected, from: CaseAwaitingMeasurement, to: CaseRecheckRequired, refType: RefMeasurement},
	EventCaseReopened:        {kind: EventCaseReopened, from: CaseRecheckRequired, to: CaseAwaitingMeasurement},
	EventCaseSealed:          {kind: EventCaseSealed, from: CaseReviewReady, to: CaseSealed, refType: RefEvidenceSeal},
}

func IsCaseTransitionKind(kind string) bool {
	_, ok := caseTransitionShapes[kind]
	return ok
}

type AuditEvent struct {
	ID string `json:"id"`
	// Sequence is the repository-wide, monotonically increasing append order.
	// Timelines order by Sequence (never by CreatedAt), so later events cannot
	// reorder history.
	Sequence    int64     `json:"sequence"`
	SubjectType string    `json:"subject_type"`
	SubjectID   string    `json:"subject_id"`
	CaseID      string    `json:"case_id,omitempty"`
	Kind        string    `json:"kind"`
	Actor       string    `json:"actor"`
	Detail      string    `json:"detail"`
	FromState   CaseState `json:"from_state,omitempty"`
	ToState     CaseState `json:"to_state,omitempty"`
	FromVersion int       `json:"from_version,omitempty"`
	ToVersion   int       `json:"to_version,omitempty"`
	// RefType/RefID point at the concrete evidence the transition is based on
	// (the recorded measurement or the evidence seal). Reopen carries its
	// reason in Detail instead.
	RefType   string    `json:"ref_type,omitempty"`
	RefID     string    `json:"ref_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// NewAuditEvent builds a subject audit event without transition fields. It is
// used for non-case subjects (for example device enrollment). Case state
// changes must go through NewCaseTransitionEvent.
func NewAuditEvent(id, subjectType, subjectID, caseID, kind, actor, detail string, at time.Time) (AuditEvent, error) {
	item := AuditEvent{
		ID:          strings.TrimSpace(id),
		SubjectType: strings.TrimSpace(subjectType),
		SubjectID:   strings.TrimSpace(subjectID),
		CaseID:      strings.TrimSpace(caseID),
		Kind:        strings.TrimSpace(kind),
		Actor:       strings.TrimSpace(actor),
		Detail:      strings.TrimSpace(detail),
		CreatedAt:   at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return AuditEvent{}, err
	}
	return item, nil
}

// NewCaseTransitionEvent builds the auditable certificate of one effective
// case state transition. It rejects any kind/state/version/reference
// combination that is not a legal transition, so an event can only be created
// for a transition that really moved the case forward by exactly one version.
func NewCaseTransitionEvent(id, caseID, kind, actor, basis string, fromState, toState CaseState, fromVersion, toVersion int, refType, refID string, at time.Time) (AuditEvent, error) {
	shape, ok := caseTransitionShapes[strings.TrimSpace(kind)]
	if !ok {
		return AuditEvent{}, fmt.Errorf("%w: unsupported case transition event %q", ErrInvalidInput, kind)
	}
	item := AuditEvent{
		ID:          strings.TrimSpace(id),
		SubjectType: "case",
		SubjectID:   strings.TrimSpace(caseID),
		CaseID:      strings.TrimSpace(caseID),
		Kind:        shape.kind,
		Actor:       strings.TrimSpace(actor),
		Detail:      strings.TrimSpace(basis),
		FromState:   fromState,
		ToState:     toState,
		FromVersion: fromVersion,
		ToVersion:   toVersion,
		RefType:     strings.TrimSpace(refType),
		RefID:       strings.TrimSpace(refID),
		CreatedAt:   at.UTC(),
	}
	if item.ID == "" || item.SubjectID == "" || item.Actor == "" || item.Detail == "" {
		return AuditEvent{}, fmt.Errorf("%w: transition event id, case id, actor, and basis are required", ErrInvalidInput)
	}
	if item.CreatedAt.IsZero() {
		return AuditEvent{}, fmt.Errorf("%w: event time is required", ErrInvalidInput)
	}
	if fromState != shape.from || toState != shape.to {
		return AuditEvent{}, fmt.Errorf("%w: event %q requires transition %s -> %s, got %s -> %s", ErrInvalidInput, kind, displayState(shape.from), displayState(shape.to), displayState(fromState), displayState(toState))
	}
	if toVersion != fromVersion+1 || toVersion < 1 {
		return AuditEvent{}, fmt.Errorf("%w: event %q must advance one version, got %d -> %d", ErrInvalidInput, kind, fromVersion, toVersion)
	}
	switch {
	case shape.refType == "" && (item.RefType != "" || item.RefID != ""):
		return AuditEvent{}, fmt.Errorf("%w: event %q cannot carry an evidence reference", ErrInvalidInput, kind)
	case shape.refType != "":
		if item.RefType != shape.refType || item.RefID == "" {
			return AuditEvent{}, fmt.Errorf("%w: event %q requires a %s reference", ErrInvalidInput, kind, shape.refType)
		}
	}
	return item, nil
}

func displayState(state CaseState) CaseState {
	if state == "" {
		return "(none)"
	}
	return state
}

func (e AuditEvent) Validate() error {
	if e.ID == "" || e.SubjectType == "" || e.SubjectID == "" || e.Kind == "" || e.Actor == "" || e.Detail == "" {
		return fmt.Errorf("%w: audit event fields are required", ErrInvalidInput)
	}
	if e.CreatedAt.IsZero() {
		return fmt.Errorf("%w: event time is required", ErrInvalidInput)
	}
	return nil
}
