package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/bootstrap"
)

type apiResponse struct {
	Status int
	Body   map[string]any
	Raw    string
}

type harness struct {
	baseURL string
	client  *http.Client
	server  *httptest.Server
}

func main() {
	workflow := flag.String("workflow", "", "workflow id to verify")
	flag.Parse()

	checks := map[string]func() error{
		"register-device":          checkRegisterDevice,
		"open-verification-case":   checkOpenVerificationCase,
		"record-measurement":       checkRecordMeasurement,
		"reopen-verification-case": checkReopenVerificationCase,
		"seal-verification":        checkSealVerification,
		"case-state-timeline":      checkCaseStateTimeline,
	}
	run, ok := checks[*workflow]
	if !ok {
		fmt.Fprintf(flag.CommandLine.Output(), "unknown workflow %q\n", *workflow)
		flag.Usage()
		os.Exit(2)
	}
	if err := run(); err != nil {
		fmt.Printf("workflow %s: failed: %v\n", *workflow, err)
		os.Exit(1)
	}
	fmt.Printf("workflow %s: ok\n", *workflow)
}

func checkRegisterDevice() error {
	h := newHarness()
	defer h.close()

	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": " met-1001 ",
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return err
	}
	id, err := nestedString(response.Body, "device", "id")
	if err != nil {
		return err
	}
	tag, err := nestedString(response.Body, "device", "asset_tag")
	if err != nil {
		return err
	}
	if tag != "MET-1001" {
		return fmt.Errorf("asset tag was not normalized: %q", tag)
	}

	read, err := h.request(http.MethodGet, "/devices/"+id, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	readID, err := nestedString(read.Body, "device", "id")
	if err != nil {
		return err
	}
	if readID != id {
		return fmt.Errorf("read returned device %q, expected %q", readID, id)
	}

	duplicate, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": "MET-1001",
		"model":     "Other Meter",
		"room":      "Room B",
		"steward":   "Mira",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(duplicate, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(duplicate, "duplicate_asset_tag")
}

func checkOpenVerificationCase() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1101")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	state, err := nestedString(opened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("opened case state = %q", state)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}

	second, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(second, "active_case_exists")
}

func checkRecordMeasurement() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1201")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	accepted, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(accepted, http.StatusCreated); err != nil {
		return err
	}
	state, err := nestedString(accepted.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "review_ready" {
		return fmt.Errorf("accepted measurement state = %q", state)
	}
	acceptedValue, err := nestedBool(accepted.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if !acceptedValue {
		return errors.New("in-tolerance measurement was not accepted")
	}

	second, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	rejectDeviceID, err := h.createDevice("MET-1202")
	if err != nil {
		return err
	}
	rejectCase, err := h.openCase(rejectDeviceID)
	if err != nil {
		return err
	}
	rejectCaseID, err := nestedString(rejectCase.Body, "case", "id")
	if err != nil {
		return err
	}
	rejected, err := h.recordMeasurement(rejectCaseID, 26.5, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(rejected, http.StatusCreated); err != nil {
		return err
	}
	rejectedState, err := nestedString(rejected.Body, "case", "state")
	if err != nil {
		return err
	}
	if rejectedState != "recheck_required" {
		return fmt.Errorf("out-of-tolerance state = %q", rejectedState)
	}
	rejectedValue, err := nestedBool(rejected.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if rejectedValue {
		return errors.New("out-of-tolerance measurement was accepted")
	}
	return nil
}

func checkReopenVerificationCase() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.rejectedCase("MET-1301")
	if err != nil {
		return err
	}
	reopened, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "measurement setup was replaced",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopened, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(reopened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("reopened case state = %q", state)
	}
	measurements, err := nestedSlice(reopened.Body, "measurements")
	if err != nil {
		return err
	}
	if len(measurements) != 1 {
		return fmt.Errorf("reopen changed measurement history: %d entries", len(measurements))
	}

	second, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "duplicate request",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	events, err := h.request(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(events, http.StatusOK); err != nil {
		return err
	}
	if !hasEvent(events.Body, "case_reopened") {
		return errors.New("reopen audit event is missing")
	}
	return nil
}

func checkSealVerification() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.readyCase("MET-1401")
	if err != nil {
		return err
	}
	type outcome struct {
		response apiResponse
		err      error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			response, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
				"reviewer": fmt.Sprintf("Reviewer-%d", index+1),
				"summary":  "accepted measurement sealed for review",
			})
			results <- outcome{response: response, err: err}
		}(index)
	}
	wait.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for item := range results {
		if item.err != nil {
			return item.err
		}
		switch item.response.Status {
		case http.StatusCreated:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			return fmt.Errorf("unexpected concurrent seal status %d: %s", item.response.Status, item.response.Raw)
		}
	}
	if successes != 1 || conflicts != 1 {
		return fmt.Errorf("concurrent seal outcomes: %d success, %d conflict", successes, conflicts)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(read.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "sealed" {
		return fmt.Errorf("sealed case state = %q", state)
	}
	sealID, err := nestedString(read.Body, "seal", "id")
	if err != nil {
		return err
	}
	if sealID == "" {
		return errors.New("evidence seal is missing")
	}
	return nil
}

type expectedStep struct {
	action      string
	actor       string
	fromState   string
	toState     string
	fromVersion int
	toVersion   int
	refType     string
}

// checkCaseStateTimeline drives one case through rejection, reopen, acceptance
// and sealing, injecting failed requests and a concurrent seal. It then proves
// that the events endpoint presents a stable, chain-linked timeline that
// matches the case detail endpoint.
func checkCaseStateTimeline() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1501")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}

	// Capture the timeline right after opening: the first step is frozen and
	// must remain byte-for-byte the same after later transitions.
	openingEvents, err := h.request(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(openingEvents, http.StatusOK); err != nil {
		return err
	}
	openingTimeline, err := nestedSlice(openingEvents.Body, "timeline")
	if err != nil {
		return err
	}
	if len(openingTimeline) != 1 {
		return fmt.Errorf("opening timeline has %d steps, expected 1", len(openingTimeline))
	}
	firstSequence, err := nestedFloat(openingEvents.Body, "timeline", 0, "sequence")
	if err != nil {
		return err
	}

	// Out-of-tolerance measurement -> recheck_required, then a failed second
	// measurement attempt must not create a step.
	rejected, err := h.recordMeasurement(caseID, 26.5, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(rejected, http.StatusCreated); err != nil {
		return err
	}
	rejectedMeasurementID, err := nestedStringAt(rejected.Body, "measurements", 0, "id")
	if err != nil {
		return err
	}
	invalidMeasurement, err := h.recordMeasurement(caseID, 25.0, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(invalidMeasurement, http.StatusConflict); err != nil {
		return err
	}

	// Reopen, followed by a duplicate reopen and an invalid reopen, both of
	// which must be rejected without producing steps.
	reopened, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "measurement setup was replaced",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopened, http.StatusOK); err != nil {
		return err
	}
	duplicateReopen, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "duplicate request",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(duplicateReopen, http.StatusConflict); err != nil {
		return err
	}
	badReopen, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "",
		"reason": "",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(badReopen, http.StatusBadRequest); err != nil {
		return err
	}

	// In-tolerance re-measurement -> review_ready, then an illegal seal from a
	// measurement request surface: try sealing with wrong flow first by using
	// the correct seal but racing a duplicate.
	accepted, err := h.recordMeasurement(caseID, 25.1, "Ning")
	if err != nil {
		return err
	}
	if err := expectStatus(accepted, http.StatusCreated); err != nil {
		return err
	}
	acceptedMeasurementID, err := nestedStringAt(accepted.Body, "measurements", 1, "id")
	if err != nil {
		return err
	}

	type sealOutcome struct {
		response apiResponse
		err      error
	}
	sealResults := make(chan sealOutcome, 2)
	var sealWait sync.WaitGroup
	for index := 0; index < 2; index++ {
		sealWait.Add(1)
		go func(index int) {
			defer sealWait.Done()
			response, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
				"reviewer": fmt.Sprintf("Reviewer-%d", index+1),
				"summary":  "accepted measurement sealed for review",
			})
			sealResults <- sealOutcome{response: response, err: err}
		}(index)
	}
	sealWait.Wait()
	close(sealResults)
	sealSuccesses, sealConflicts := 0, 0
	for item := range sealResults {
		if item.err != nil {
			return item.err
		}
		switch item.response.Status {
		case http.StatusCreated:
			sealSuccesses++
		case http.StatusConflict:
			sealConflicts++
		default:
			return fmt.Errorf("unexpected concurrent seal status %d", item.response.Status)
		}
	}
	if sealSuccesses != 1 || sealConflicts != 1 {
		return fmt.Errorf("concurrent seal outcomes: %d success, %d conflict", sealSuccesses, sealConflicts)
	}

	// Further mutations on the sealed case must fail and leave no steps.
	lateMeasurement, err := h.recordMeasurement(caseID, 25.0, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(lateMeasurement, http.StatusConflict); err != nil {
		return err
	}
	lateReopen, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "late request after seal",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(lateReopen, http.StatusConflict); err != nil {
		return err
	}

	caseView, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(caseView, http.StatusOK); err != nil {
		return err
	}
	eventsView, err := h.request(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(eventsView, http.StatusOK); err != nil {
		return err
	}

	// Header and timeline of the events endpoint must match the case detail.
	headerState, err := nestedString(eventsView.Body, "state")
	if err != nil {
		return err
	}
	caseState, err := nestedString(caseView.Body, "case", "state")
	if err != nil {
		return err
	}
	if headerState != caseState || headerState != "sealed" {
		return fmt.Errorf("events header state %q does not match case state %q", headerState, caseState)
	}
	headerVersion, err := nestedFloat(eventsView.Body, "version")
	if err != nil {
		return err
	}
	caseVersion, err := nestedFloat(caseView.Body, "case", "version")
	if err != nil {
		return err
	}
	if headerVersion != caseVersion || headerVersion != 5 {
		return fmt.Errorf("events header version %v does not match case version %v", headerVersion, caseVersion)
	}
	if !reflect.DeepEqual(eventsView.Body["timeline"], caseView.Body["timeline"]) {
		return errors.New("case detail and events endpoints report different timelines")
	}

	timeline, err := nestedSlice(eventsView.Body, "timeline")
	if err != nil {
		return err
	}
	// Five effective transitions: open, reject, reopen, accept, seal. Every
	// failed request above must be absent.
	if len(timeline) != 5 {
		return fmt.Errorf("timeline has %d steps after full lifecycle, expected 5", len(timeline))
	}

	// History never reorders: the opening step is unchanged since first read.
	if !reflect.DeepEqual(timeline[0], openingTimeline[0]) {
		return errors.New("opening timeline step was rewritten by later events")
	}

	expected := []expectedStep{
		{action: "case_opened", actor: "Qiao", fromState: "", toState: "awaiting_measurement", fromVersion: 0, toVersion: 1},
		{action: "measurement_rejected", actor: "Rui", fromState: "awaiting_measurement", toState: "recheck_required", fromVersion: 1, toVersion: 2, refType: "measurement"},
		{action: "case_reopened", actor: "Lin", fromState: "recheck_required", toState: "awaiting_measurement", fromVersion: 2, toVersion: 3},
		{action: "measurement_accepted", actor: "Ning", fromState: "awaiting_measurement", toState: "review_ready", fromVersion: 3, toVersion: 4, refType: "measurement"},
		{action: "case_sealed", actor: "", fromState: "review_ready", toState: "sealed", fromVersion: 4, toVersion: 5, refType: "evidence_seal"},
	}
	var previousSequence float64
	for index, want := range expected {
		step, ok := timeline[index].(map[string]any)
		if !ok {
			return fmt.Errorf("step %d is not an object", index+1)
		}
		if got := stepNumber(step); got != index+1 {
			return fmt.Errorf("step %d has step number %d", index+1, got)
		}
		if got, _ := step["action"].(string); got != want.action {
			return fmt.Errorf("step %d action = %q, expected %q", index+1, got, want.action)
		}
		if want.actor != "" {
			if got, _ := step["actor"].(string); got != want.actor {
				return fmt.Errorf("step %d actor = %q, expected %q", index+1, got, want.actor)
			}
		} else if actor, _ := step["actor"].(string); actor == "" {
			return fmt.Errorf("step %d seal actor is empty", index+1)
		}
		if got, _ := step["from_state"].(string); got != want.fromState {
			return fmt.Errorf("step %d from_state = %q, expected %q", index+1, got, want.fromState)
		}
		if got, _ := step["to_state"].(string); got != want.toState {
			return fmt.Errorf("step %d to_state = %q, expected %q", index+1, got, want.toState)
		}
		if got := int(optionalNumber(step, "from_version")); got != want.fromVersion {
			return fmt.Errorf("step %d from_version = %d, expected %d", index+1, got, want.fromVersion)
		}
		if got := int(numberField(step, "to_version")); got != want.toVersion {
			return fmt.Errorf("step %d to_version = %d, expected %d", index+1, got, want.toVersion)
		}
		sequence, ok := step["sequence"].(float64)
		if !ok || sequence <= 0 {
			return fmt.Errorf("step %d has no commit sequence", index+1)
		}
		if index == 0 {
			if sequence != firstSequence {
				return fmt.Errorf("opening sequence changed: %v -> %v", firstSequence, sequence)
			}
		} else if sequence <= previousSequence {
			return fmt.Errorf("step %d sequence %v does not strictly follow %v", index+1, sequence, previousSequence)
		}
		previousSequence = sequence

		switch index {
		case 1:
			if refID, _ := step["ref_id"].(string); refID != rejectedMeasurementID {
				return fmt.Errorf("reject step references %q, expected measurement %s", refID, rejectedMeasurementID)
			}
		case 3:
			if refID, _ := step["ref_id"].(string); refID != acceptedMeasurementID {
				return fmt.Errorf("accept step references %q, expected measurement %s", refID, acceptedMeasurementID)
			}
		case 4:
			sealID, err := nestedString(caseView.Body, "seal", "id")
			if err != nil {
				return err
			}
			if refID, _ := step["ref_id"].(string); refID != sealID {
				return fmt.Errorf("seal step references %q, expected evidence seal %s", refID, sealID)
			}
		}
		if got, _ := step["ref_type"].(string); got != want.refType {
			return fmt.Errorf("step %d ref_type = %q, expected %q", index+1, got, want.refType)
		}
		if basis, _ := step["basis"].(string); basis == "" {
			return fmt.Errorf("step %d has no basis", index+1)
		}
	}

	// The raw events list stays append-ordered and has one event per step.
	rawEvents, err := nestedSlice(eventsView.Body, "events")
	if err != nil {
		return err
	}
	if len(rawEvents) != 5 {
		return fmt.Errorf("raw events length = %d, expected 5", len(rawEvents))
	}

	// A not-found case keeps the stable error contract on both read entries.
	missing, err := h.request(http.MethodGet, "/cases/case-9999/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(missing, http.StatusNotFound); err != nil {
		return err
	}
	return expectErrorCode(missing, "not_found")
}

func stepNumber(step map[string]any) int {
	return int(numberField(step, "step"))
}

func numberField(step map[string]any, key string) float64 {
	value, ok := step[key].(float64)
	if !ok {
		return -1
	}
	return value
}

// optionalNumber treats an omitted key as zero, matching omitempty on the
// opening boundary fields (step 1 has no predecessor version/state).
func optionalNumber(step map[string]any, key string) float64 {
	if value, ok := step[key].(float64); ok {
		return value
	}
	return 0
}

func nestedFloat(root map[string]any, path ...any) (float64, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return 0, err
	}
	number, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("%v is not a number", path)
	}
	return number, nil
}

func newHarness() *harness {
	server := httptest.NewServer(bootstrap.NewHandler())
	return &harness{
		baseURL: server.URL,
		client:  server.Client(),
		server:  server,
	}
}

func (h *harness) close() {
	h.server.Close()
}

func (h *harness) request(method, path string, body any) (apiResponse, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return apiResponse{}, err
		}
		reader = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, reader)
	if err != nil {
		return apiResponse{}, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := h.client.Do(request)
	if err != nil {
		return apiResponse{}, err
	}
	defer response.Body.Close()
	rawBytes, err := io.ReadAll(response.Body)
	if err != nil {
		return apiResponse{}, err
	}
	result := apiResponse{Status: response.StatusCode, Raw: string(rawBytes)}
	if len(bytes.TrimSpace(rawBytes)) > 0 {
		if err := json.Unmarshal(rawBytes, &result.Body); err != nil {
			return apiResponse{}, fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return result, nil
}

func (h *harness) createDevice(tag string) (string, error) {
	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": tag,
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return nestedString(response.Body, "device", "id")
}

func (h *harness) openCase(deviceID string) (apiResponse, error) {
	response, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return apiResponse{}, err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return apiResponse{}, err
	}
	return response, nil
}

func (h *harness) recordMeasurement(caseID string, observed float64, technician string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"observed":   observed,
		"technician": technician,
	})
}

func (h *harness) rejectedCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 26.5, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func (h *harness) readyCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func expectStatus(response apiResponse, expected int) error {
	if response.Status != expected {
		return fmt.Errorf("status = %d, expected %d: %s", response.Status, expected, response.Raw)
	}
	return nil
}

func expectErrorCode(response apiResponse, expected string) error {
	code, err := nestedString(response.Body, "error", "code")
	if err != nil {
		return err
	}
	if code != expected {
		return fmt.Errorf("error code = %q, expected %q", code, expected)
	}
	return nil
}

func nestedString(root map[string]any, path ...string) (string, error) {
	value, err := nestedValue(root, path...)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", strings.Join(path, "."))
	}
	return text, nil
}

func nestedStringAt(root map[string]any, path ...any) (string, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%v is not a string", path)
	}
	return text, nil
}

func nestedBool(root map[string]any, path ...any) (bool, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return false, err
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%v is not a boolean", path)
	}
	return flag, nil
}

func nestedSlice(root map[string]any, key string) ([]any, error) {
	value, ok := root[key]
	if !ok {
		return nil, fmt.Errorf("missing %s", key)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s is not an array", key)
	}
	return items, nil
}

func hasEvent(root map[string]any, kind string) bool {
	items, err := nestedSlice(root, "events")
	if err != nil {
		return false
	}
	for _, item := range items {
		event, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if event["kind"] == kind {
			return true
		}
	}
	return false
}

func nestedValue(root map[string]any, path ...string) (any, error) {
	parts := make([]any, 0, len(path))
	for _, part := range path {
		parts = append(parts, part)
	}
	return nestedAny(root, parts...)
}

func nestedAny(root map[string]any, path ...any) (any, error) {
	var current any = root
	for _, part := range path {
		switch typed := part.(type) {
		case string:
			object, ok := current.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s is not an object", typed)
			}
			value, ok := object[typed]
			if !ok {
				return nil, fmt.Errorf("missing %s", typed)
			}
			current = value
		case int:
			items, ok := current.([]any)
			if !ok {
				return nil, fmt.Errorf("index %d is not inside an array", typed)
			}
			if typed < 0 || typed >= len(items) {
				return nil, fmt.Errorf("index %d is out of range", typed)
			}
			current = items[typed]
		default:
			return nil, fmt.Errorf("unsupported path element %T", part)
		}
	}
	return current, nil
}
