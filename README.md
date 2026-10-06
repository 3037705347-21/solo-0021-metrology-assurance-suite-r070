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

### State timeline

Both `GET /cases/{id}` and `GET /cases/{id}/events` return a verified
`timeline` for the case. Every committed transition appears as one ordered
step with:

- `sequence`: case-scoped, gap-free position (1, 2, 3, ...), the only ordering
  key, so new events never rearrange earlier history.
- `action` and `actor`: the workflow action (`case_opened`,
  `measurement_accepted`, `measurement_rejected`, `case_reopened`,
  `case_sealed`) and the operator who drove it.
- `from_state`/`to_state` and `from_version`/`to_version`: the business state
  and version before and after the transition; consecutive steps link together
  and the last step equals the stored case.
- `basis`: the action-specific evidence — measurement id/observed/deviation/
  accepted decision for measurement steps, reopen reason, or seal id/summary —
  plus the linked `audit_event_id`.
- `occurred_at`: the committed transition time.

Failed or rolled-back requests never append a step (and leave no sequence
gap); the read side rejects any timeline whose chain does not end at the
current case instead of serving a misleading history.

## Build And Verify

```bash
go build ./...
go test ./...
go run ./cmd/workflowcheck --workflow register-device
go run ./cmd/workflowcheck --workflow open-verification-case
go run ./cmd/workflowcheck --workflow record-measurement
go run ./cmd/workflowcheck --workflow reopen-verification-case
go run ./cmd/workflowcheck --workflow seal-verification
```

The workflow runner starts a fresh in-process HTTP service and exercises the
public API. It is a production smoke command, complementing the unit and
integration test suite in `go test ./...`.

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
