package domain

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type CaseState string

const (
	CaseAwaitingMeasurement CaseState = "awaiting_measurement"
	CaseReviewReady         CaseState = "review_ready"
	CaseRecheckRequired     CaseState = "recheck_required"
	CaseSealed              CaseState = "sealed"
)

// CaseAction identifies the workflow action that drove one case transition.
type CaseAction string

const (
	CaseActionOpened   CaseAction = "case_opened"
	CaseActionAccepted CaseAction = "measurement_accepted"
	CaseActionRejected CaseAction = "measurement_rejected"
	CaseActionReopened CaseAction = "case_reopened"
	CaseActionSealed   CaseAction = "case_sealed"
)

// allowedCaseTransitions is the single source of truth for the legal edges of
// the verification case state graph. A nil source state marks the opening
// transition, which creates the case in awaiting_measurement at version 1.
var allowedCaseTransitions = map[CaseAction]struct {
	from *CaseState
	to   CaseState
}{
	CaseActionOpened:   {from: nil, to: CaseAwaitingMeasurement},
	CaseActionAccepted: {from: caseStatePointer(CaseAwaitingMeasurement), to: CaseReviewReady},
	CaseActionRejected: {from: caseStatePointer(CaseAwaitingMeasurement), to: CaseRecheckRequired},
	CaseActionReopened: {from: caseStatePointer(CaseRecheckRequired), to: CaseAwaitingMeasurement},
	CaseActionSealed:   {from: caseStatePointer(CaseReviewReady), to: CaseSealed},
}

func caseStatePointer(state CaseState) *CaseState {
	return &state
}

// CanTransition reports whether the action is legal from the given source
// state. A nil source state represents the implicit pre-opening state.
func CanTransition(action CaseAction, from *CaseState) bool {
	rule, ok := allowedCaseTransitions[action]
	if !ok {
		return false
	}
	if rule.from == nil {
		return from == nil
	}
	return from != nil && *from == *rule.from
}

type VerificationCase struct {
	ID        string     `json:"id"`
	DeviceID  string     `json:"device_id"`
	Nominal   float64    `json:"nominal"`
	Tolerance float64    `json:"tolerance"`
	Steward   string     `json:"steward"`
	State     CaseState  `json:"state"`
	Version   int        `json:"version"`
	OpenedAt  time.Time  `json:"opened_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	SealedAt  *time.Time `json:"sealed_at,omitempty"`
}

func NewVerificationCase(id, deviceID string, nominal, tolerance float64, steward string, at time.Time) (VerificationCase, error) {
	item := VerificationCase{
		ID:        strings.TrimSpace(id),
		DeviceID:  strings.TrimSpace(deviceID),
		Nominal:   nominal,
		Tolerance: tolerance,
		Steward:   strings.TrimSpace(steward),
		State:     CaseAwaitingMeasurement,
		Version:   1,
		OpenedAt:  at.UTC(),
		UpdatedAt: at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return VerificationCase{}, err
	}
	return item, nil
}

func (c VerificationCase) Validate() error {
	if c.ID == "" || c.DeviceID == "" || c.Steward == "" {
		return fmt.Errorf("%w: case id, device id, and steward are required", ErrInvalidInput)
	}
	if !isFinite(c.Nominal) || !isFinite(c.Tolerance) || c.Tolerance <= 0 {
		return fmt.Errorf("%w: nominal must be finite and tolerance must be positive", ErrInvalidInput)
	}
	if c.Version < 1 || c.OpenedAt.IsZero() || c.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: case version and timestamps are required", ErrInvalidInput)
	}
	switch c.State {
	case CaseAwaitingMeasurement, CaseReviewReady, CaseRecheckRequired, CaseSealed:
		return nil
	default:
		return fmt.Errorf("%w: unsupported case state %q", ErrInvalidInput, c.State)
	}
}

func (c VerificationCase) IsUnfinished() bool {
	return c.State != CaseSealed
}

func (c *VerificationCase) RecordMeasurement(accepted bool, at time.Time) error {
	action := CaseActionRejected
	if accepted {
		action = CaseActionAccepted
	}
	return c.apply(action, at)
}

func (c *VerificationCase) Reopen(at time.Time) error {
	return c.apply(CaseActionReopened, at)
}

func (c *VerificationCase) Seal(at time.Time) error {
	return c.apply(CaseActionSealed, at)
}

// apply performs a legal state-machine edge. It increments the case version
// exactly once per accepted transition, so every persisted transition has a
// distinct from/to version pair.
func (c *VerificationCase) apply(action CaseAction, at time.Time) error {
	from := c.State
	if !CanTransition(action, &from) {
		return fmt.Errorf("%w: %s", ErrStateConflict, transitionConflictMessage(action, from))
	}
	rule := allowedCaseTransitions[action]
	c.State = rule.to
	c.Version++
	c.UpdatedAt = at.UTC()
	if action == CaseActionSealed {
		stamp := at.UTC()
		c.SealedAt = &stamp
	}
	return nil
}

func transitionConflictMessage(action CaseAction, from CaseState) string {
	switch action {
	case CaseActionAccepted, CaseActionRejected:
		return "case is not awaiting measurement"
	case CaseActionReopened:
		return "only a recheck_required case can be reopened"
	case CaseActionSealed:
		return "case is not ready for review"
	default:
		return fmt.Sprintf("action %q is not allowed from state %q", action, from)
	}
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
