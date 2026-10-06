package contracts

import "example.com/solo-0021-metrology-assurance-suite/internal/domain"

type RegisterDeviceRequest struct {
	AssetTag string `json:"asset_tag"`
	Model    string `json:"model"`
	Room     string `json:"room"`
	Steward  string `json:"steward"`
}

type OpenCaseRequest struct {
	DeviceID  string  `json:"device_id"`
	Nominal   float64 `json:"nominal"`
	Tolerance float64 `json:"tolerance"`
	Steward   string  `json:"steward"`
}

type RecordMeasurementRequest struct {
	Observed   float64 `json:"observed"`
	Technician string  `json:"technician"`
}

type ReopenCaseRequest struct {
	Actor  string `json:"actor"`
	Reason string `json:"reason"`
}

type SealCaseRequest struct {
	Reviewer string `json:"reviewer"`
	Summary  string `json:"summary"`
}

type DeviceResponse struct {
	Device domain.Device `json:"device"`
}

type CaseResponse struct {
	Case         domain.VerificationCase `json:"case"`
	Measurements []domain.Measurement    `json:"measurements"`
	Seal         *domain.EvidenceSeal    `json:"seal,omitempty"`
	Events       []domain.AuditEvent     `json:"events"`
	// Timeline is the deterministic, reviewable state-transition projection.
	// It is computed from the same snapshot as Case and Events, so the case
	// detail and the events endpoint can never describe different facts.
	Timeline []domain.TimelineStep `json:"timeline"`
}

// TimelineResponse is the events endpoint view. It keeps the raw append-order
// events for compatibility and adds the case header and the projected
// timeline, letting a reviewer anchor every step to the current case version.
type TimelineResponse struct {
	CaseID   string                `json:"case_id"`
	State    domain.CaseState      `json:"state"`
	Version  int                   `json:"version"`
	Events   []domain.AuditEvent   `json:"events"`
	Timeline []domain.TimelineStep `json:"timeline"`
}

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
