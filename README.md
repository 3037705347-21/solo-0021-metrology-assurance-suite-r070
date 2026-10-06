# Metrology Assurance Suite

Metrology Assurance Suite is a Go HTTP service for bounded laboratory
measurement assurance work. It registers devices, opens verification cases,
records measurements, handles failed tolerance decisions, and seals traceable
evidence records.

## Run

```bash
go run ./cmd/metrology-service --addr 127.0.0.1:18121
```

The service listens on `127.0.0.1:18121` by default.

## Build And Verify

```bash
go build ./...
go test ./...
go run ./cmd/workflowcheck --workflow register-device
go run ./cmd/workflowcheck --workflow open-verification-case
go run ./cmd/workflowcheck --workflow record-measurement
go run ./cmd/workflowcheck --workflow reopen-verification-case
go run ./cmd/workflowcheck --workflow seal-verification
go run ./cmd/workflowcheck --workflow case-state-timeline
```

The workflow runner starts a fresh in-process HTTP service and exercises the
public API. It is a production smoke command, not a unit test.

## Case State Timeline

`GET /cases/{id}/events` returns a stable, reviewable timeline of the case's
effective state transitions:

```json
{
  "case_id": "case-0001",
  "state": "sealed",
  "version": 5,
  "events": [ { "id": "event-0001", "kind": "case_opened", "sequence": 2 } ],
  "timeline": [
    {
      "step": 1,
      "sequence": 2,
      "event_id": "event-0001",
      "case_id": "case-0001",
      "action": "case_opened",
      "actor": "Qiao",
      "to_state": "awaiting_measurement",
      "to_version": 1,
      "basis": "verification case opened",
      "created_at": "2026-10-06T00:00:00Z"
    }
  ]
}
```

- Each step carries the actor, basis (reason or decision), before/after state
  and version, and the referenced measurement or evidence seal when applicable.
- Ordering uses the append `sequence`, never timestamps: new events never
  reorder history, and `step` numbers are dense and stable within a case.
- Failed and rolled-back requests commit nothing, so they cannot create steps.
- `GET /cases/{id}` embeds the identical `timeline`; both entries read the
  same snapshot and refuse to disagree with the case's current state/version.

## API

- `GET /healthz`
- `POST /devices`
- `GET /devices/{id}`
- `POST /cases`
- `GET /cases/{id}`
- `POST /cases/{id}/measurements`
- `POST /cases/{id}/reopen`
- `POST /cases/{id}/seal`
- `GET /cases/{id}/events`

## Layout

- `cmd/metrology-service`: executable HTTP server.
- `cmd/workflowcheck`: bounded workflow verification commands.
- `internal/httpapi`: route and JSON boundary.
- `internal/assurance`: workflow orchestration.
- `internal/domain`: state rules and invariants.
- `internal/repository`: copy-on-write in-memory persistence.
- `internal/contracts`: API request and response models.
- `internal/bootstrap`: concrete dependency wiring.

## Inputs And Outputs

Inputs are JSON HTTP requests. Outputs are JSON response envelopes with either a
resource view or a stable error object:

```json
{
  "error": {
    "code": "state_conflict",
    "message": "case is not awaiting measurement"
  }
}
```

The service has no environment variables and no external dependencies.
