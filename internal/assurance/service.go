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
		item, err := domain.NewVerificationCase(
			m.NextID("case"),
			device.ID,
			input.Nominal,
			input.Tolerance,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutCase(item); err != nil {
			return err
		}
		at := s.now()
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			string(domain.CaseActionOpened),
			item.Steward,
			"verification case opened",
			at,
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		opening, err := domain.NewCaseTransition(
			m.NextTransitionSequence(item.ID),
			item.ID,
			domain.CaseActionOpened,
			item.Steward,
			nil,
			item.State,
			0,
			item.Version,
			domain.TransitionBasis{AuditEventID: event.ID},
			at,
		)
		if err != nil {
			return err
		}
		if err := m.AppendTransition(opening); err != nil {
			return err
		}
		result, err = caseResponse(m, item)
		return err
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
		view, err := caseResponseFromSnapshot(snapshot, item)
		if err != nil {
			return err
		}
		result = view
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
		fromState := item.State
		fromVersion := item.Version
		if err := item.RecordMeasurement(measurement.Accepted, at); err != nil {
			return err
		}
		if err := m.AppendMeasurement(measurement); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		action := domain.CaseActionRejected
		detail := "measurement exceeded tolerance and requires another measurement"
		if measurement.Accepted {
			action = domain.CaseActionAccepted
			detail = "measurement is within tolerance and ready for review"
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			string(action),
			measurement.Technician,
			detail,
			at,
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		accepted := measurement.Accepted
		transition, err := domain.NewCaseTransition(
			m.NextTransitionSequence(item.ID),
			item.ID,
			action,
			measurement.Technician,
			&fromState,
			item.State,
			fromVersion,
			item.Version,
			domain.TransitionBasis{
				MeasurementID: measurement.ID,
				Observed:      &measurement.Observed,
				Deviation:     &measurement.Deviation,
				Accepted:      &accepted,
				AuditEventID:  event.ID,
			},
			at,
		)
		if err != nil {
			return err
		}
		if err := m.AppendTransition(transition); err != nil {
			return err
		}
		result, err = caseResponse(m, item)
		return err
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
		actor := strings.TrimSpace(input.Actor)
		reason := strings.TrimSpace(input.Reason)
		if actor == "" || reason == "" {
			return fmt.Errorf("%w: actor and reason are required", domain.ErrInvalidInput)
		}
		at := s.now()
		fromState := item.State
		fromVersion := item.Version
		if err := item.Reopen(at); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			string(domain.CaseActionReopened),
			actor,
			reason,
			at,
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		transition, err := domain.NewCaseTransition(
			m.NextTransitionSequence(item.ID),
			item.ID,
			domain.CaseActionReopened,
			actor,
			&fromState,
			item.State,
			fromVersion,
			item.Version,
			domain.TransitionBasis{
				Reason:       reason,
				AuditEventID: event.ID,
			},
			at,
		)
		if err != nil {
			return err
		}
		if err := m.AppendTransition(transition); err != nil {
			return err
		}
		result, err = caseResponse(m, item)
		return err
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
		fromState := item.State
		fromVersion := item.Version
		if err := item.Seal(at); err != nil {
			return err
		}
		if err := m.PutSeal(seal); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			string(domain.CaseActionSealed),
			seal.Reviewer,
			seal.Summary,
			at,
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		accepted := true
		transition, err := domain.NewCaseTransition(
			m.NextTransitionSequence(item.ID),
			item.ID,
			domain.CaseActionSealed,
			seal.Reviewer,
			&fromState,
			item.State,
			fromVersion,
			item.Version,
			domain.TransitionBasis{
				MeasurementID: seal.MeasurementID,
				Observed:      &measurement.Observed,
				Deviation:     &measurement.Deviation,
				Accepted:      &accepted,
				SealID:        seal.ID,
				Summary:       seal.Summary,
				AuditEventID:  event.ID,
			},
			at,
		)
		if err != nil {
			return err
		}
		if err := m.AppendTransition(transition); err != nil {
			return err
		}
		result, err = caseResponse(m, item)
		return err
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

// timelineFor is the single place that assures the review-facing timeline, so
// every HTTP entry point reports identical state, version, and linkage facts.
func timelineFor(transitions []domain.CaseTransition, item domain.VerificationCase) (domain.CaseTimeline, error) {
	return domain.BuildCaseTimeline(item, transitions)
}

func caseResponse(m *repository.Mutation, item domain.VerificationCase) (contracts.CaseResponse, error) {
	timeline, err := timelineFor(m.Transitions(item.ID), item)
	if err != nil {
		return contracts.CaseResponse{}, err
	}
	result := contracts.CaseResponse{
		Case:         item,
		Measurements: m.Measurements(item.ID),
		Events:       m.Events(item.ID),
		Timeline:     timeline,
	}
	if seal, exists := m.Seal(item.ID); exists {
		result.Seal = &seal
	}
	return result, nil
}

func caseResponseFromSnapshot(snapshot repository.Snapshot, item domain.VerificationCase) (contracts.CaseResponse, error) {
	timeline, err := timelineFor(snapshot.Transitions(item.ID), item)
	if err != nil {
		return contracts.CaseResponse{}, err
	}
	result := contracts.CaseResponse{
		Case:         item,
		Measurements: snapshot.Measurements(item.ID),
		Events:       snapshot.Events(item.ID),
		Timeline:     timeline,
	}
	if seal, exists := snapshot.Seal(item.ID); exists {
		result.Seal = &seal
	}
	return result, nil
}
