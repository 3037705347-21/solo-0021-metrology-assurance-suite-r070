package assurance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
	"example.com/solo-0021-metrology-assurance-suite/internal/repository"
)

type Service struct {
	repository *repository.Repository
	now        func() time.Time
}

func NewService(repo *repository.Repository) *Service {
	return &Service{
		repository: repo,
		now:        time.Now,
	}
}

func (s *Service) RegisterDevice(ctx context.Context, input contracts.RegisterDeviceRequest) (domain.Device, error) {
	var created domain.Device
	tag := domain.NormalizeAssetTag(input.AssetTag)
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		if _, exists := m.DeviceByTag(tag); exists {
			return fmt.Errorf("%w: %s", domain.ErrDuplicateAssetTag, tag)
		}
		item, err := domain.NewDevice(
			m.NextID("device"),
			input.AssetTag,
			input.Model,
			input.Room,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutDevice(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"device",
			item.ID,
			"",
			"device_enrolled",
			item.Steward,
			"device enrolled for assurance work",
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		created = item
		return nil
	})
	return created, err
}

func (s *Service) Device(ctx context.Context, id string) (domain.Device, error) {
	var found domain.Device
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Device(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, id)
		}
		found = item
		return nil
	})
	return found, err
}

func (s *Service) OpenCase(ctx context.Context, input contracts.OpenCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		deviceID := strings.TrimSpace(input.DeviceID)
		device, exists := m.Device(deviceID)
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, deviceID)
		}
		if !device.IsActive() {
			return fmt.Errorf("%w: device %s is %s", domain.ErrStateConflict, device.ID, device.Status)
		}
		if active, exists := m.ActiveCaseForDevice(device.ID); exists {
			return fmt.Errorf("%w: case %s is still %s", domain.ErrActiveCaseExists, active.ID, active.State)
		}
		at := s.now()
		item, err := domain.NewVerificationCase(
			m.NextID("case"),
			device.ID,
			input.Nominal,
			input.Tolerance,
			input.Steward,
			at,
		)
		if err != nil {
			return err
		}
		if err := m.PutCase(item); err != nil {
			return err
		}
		event, err := domain.NewCaseTransitionEvent(
			m.NextID("event"),
			item.ID,
			domain.EventCaseOpened,
			item.Steward,
			"verification case opened",
			"",
			item.State,
			0,
			item.Version,
			"",
			"",
			at,
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		return buildCaseView(m, item, &result)
	})
	return result, err
}

func (s *Service) Case(ctx context.Context, id string) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Case(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		return buildCaseView(snapshot, item, &result)
	})
	return result, err
}

// CaseTimeline backs GET /cases/{id}/events. It reads the case and its events
// in one snapshot and projects the timeline there, so the response cannot
// diverge from GET /cases/{id}.
func (s *Service) CaseTimeline(ctx context.Context, id string) (contracts.TimelineResponse, error) {
	var result contracts.TimelineResponse
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		caseID := strings.TrimSpace(id)
		item, exists := snapshot.Case(caseID)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		events := snapshot.Events(item.ID)
		timeline, err := domain.BuildCaseTimeline(item, events)
		if err != nil {
			return err
		}
		result = contracts.TimelineResponse{
			CaseID:   item.ID,
			State:    item.State,
			Version:  item.Version,
			Events:   events,
			Timeline: timeline,
		}
		return nil
	})
	return result, err
}

func (s *Service) RecordMeasurement(ctx context.Context, caseID string, input contracts.RecordMeasurementRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		at := s.now()
		measurement, err := domain.NewMeasurement(
			m.NextID("measurement"),
			item.ID,
			input.Observed,
			item.Nominal,
			item.Tolerance,
			input.Technician,
			at,
		)
		if err != nil {
			return err
		}
		before := item
		if err := item.RecordMeasurement(measurement.Accepted, at); err != nil {
			return err
		}
		if err := m.AppendMeasurement(measurement); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		kind := domain.EventMeasurementRejected
		basis := "measurement exceeded tolerance and requires another measurement"
		if measurement.Accepted {
			kind = domain.EventMeasurementAccepted
			basis = "measurement is within tolerance and ready for review"
		}
		event, err := domain.NewCaseTransitionEvent(
			m.NextID("event"),
			item.ID,
			kind,
			measurement.Technician,
			basis,
			before.State,
			item.State,
			before.Version,
			item.Version,
			domain.RefMeasurement,
			measurement.ID,
			at,
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		return buildCaseView(m, item, &result)
	})
	return result, err
}

func (s *Service) ReopenCase(ctx context.Context, caseID string, input contracts.ReopenCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		// Validate the requested action before touching state: an invalid or
		// illegal reopen returns its error and the copy-on-write commit is
		// discarded, so no event or timeline step is produced.
		actor := strings.TrimSpace(input.Actor)
		reason := strings.TrimSpace(input.Reason)
		if actor == "" || reason == "" {
			return fmt.Errorf("%w: actor and reason are required", domain.ErrInvalidInput)
		}
		before := item
		at := s.now()
		if err := item.Reopen(at); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		event, err := domain.NewCaseTransitionEvent(
			m.NextID("event"),
			item.ID,
			domain.EventCaseReopened,
			actor,
			reason,
			before.State,
			item.State,
			before.Version,
			item.Version,
			"",
			"",
			at,
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		return buildCaseView(m, item, &result)
	})
	return result, err
}

func (s *Service) SealCase(ctx context.Context, caseID string, input contracts.SealCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if _, exists := m.Seal(item.ID); exists {
			return fmt.Errorf("%w: case %s", domain.ErrSealAlreadyCreated, item.ID)
		}
		measurement, ok := bestAcceptedMeasurement(m.Measurements(item.ID))
		if !ok {
			return fmt.Errorf("%w: an accepted measurement is required before sealing", domain.ErrStateConflict)
		}
		at := s.now()
		before := item
		if err := item.Seal(at); err != nil {
			return err
		}
		seal, err := domain.NewEvidenceSeal(
			m.NextID("seal"),
			item.ID,
			item.DeviceID,
			measurement.ID,
			input.Reviewer,
			input.Summary,
			at,
		)
		if err != nil {
			return err
		}
		if err := m.PutSeal(seal); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		event, err := domain.NewCaseTransitionEvent(
			m.NextID("event"),
			item.ID,
			domain.EventCaseSealed,
			seal.Reviewer,
			seal.Summary,
			before.State,
			item.State,
			before.Version,
			item.Version,
			domain.RefEvidenceSeal,
			seal.ID,
			at,
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		return buildCaseView(m, item, &result)
	})
	return result, err
}

func bestAcceptedMeasurement(items []domain.Measurement) (domain.Measurement, bool) {
	var selected domain.Measurement
	found := false
	for _, item := range items {
		if !item.Accepted {
			continue
		}
		if !found || item.AbsoluteDeviation() < selected.AbsoluteDeviation() {
			selected = item
			found = true
		}
	}
	return selected, found
}

// caseReader exposes the read side shared by a mutation working copy and a
// read snapshot, so both command and query paths project identically.
type caseReader interface {
	Measurements(caseID string) []domain.Measurement
	Events(caseID string) []domain.AuditEvent
	Seal(caseID string) (domain.EvidenceSeal, bool)
}

func buildCaseView(reader caseReader, item domain.VerificationCase, result *contracts.CaseResponse) error {
	events := reader.Events(item.ID)
	timeline, err := domain.BuildCaseTimeline(item, events)
	if err != nil {
		return err
	}
	view := contracts.CaseResponse{
		Case:         item,
		Measurements: reader.Measurements(item.ID),
		Events:       events,
		Timeline:     timeline,
	}
	if seal, exists := reader.Seal(item.ID); exists {
		view.Seal = &seal
	}
	*result = view
	return nil
}
