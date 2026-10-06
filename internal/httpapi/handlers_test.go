package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"example.com/solo-0021-metrology-assurance-suite/internal/assurance"
	"example.com/solo-0021-metrology-assurance-suite/internal/repository"
)

type testClient struct {
	t       *testing.T
	handler http.Handler
}

func newClient(t *testing.T) *testClient {
	t.Helper()
	service := assurance.NewService(repository.New())
	return &testClient{t: t, handler: NewHandler(service)}
}

func (c *testClient) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	c.handler.ServeHTTP(recorder, request)
	var decoded map[string]any
	if recorder.Body.Len() > 0 {
		if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
			c.t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
		}
	}
	return recorder.Code, decoded
}

func (c *testClient) createReadyCase(tag string) string {
	c.t.Helper()
	status, body := c.do(http.MethodPost, "/devices", map[string]any{
		"asset_tag": tag, "model": "m", "room": "r", "steward": "Lin",
	})
	if status != http.StatusCreated {
		c.t.Fatalf("device status = %d", status)
	}
	deviceID := body["device"].(map[string]any)["id"].(string)
	status, body = c.do(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID, "nominal": 25.0, "tolerance": 0.5, "steward": "Qiao",
	})
	if status != http.StatusCreated {
		c.t.Fatalf("case status = %d", status)
	}
	caseID := body["case"].(map[string]any)["id"].(string)
	status, _ = c.do(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"observed": 25.2, "technician": "Rui",
	})
	if status != http.StatusCreated {
		c.t.Fatalf("measurement status = %d", status)
	}
	return caseID
}

func TestEventsAndCaseEndpointsReportSameTimeline(t *testing.T) {
	client := newClient(t)
	caseID := client.createReadyCase("HTTP-1001")
	if status, _ := client.do(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer": "V", "summary": "sealed",
	}); status != http.StatusCreated {
		t.Fatalf("seal status = %d", status)
	}

	caseStatus, caseBody := client.do(http.MethodGet, "/cases/"+caseID, nil)
	eventsStatus, eventsBody := client.do(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if caseStatus != http.StatusOK || eventsStatus != http.StatusOK {
		t.Fatalf("statuses = %d, %d", caseStatus, eventsStatus)
	}
	caseTimeline, _ := json.Marshal(caseBody["timeline"])
	eventsTimeline, _ := json.Marshal(eventsBody["timeline"])
	if !bytes.Equal(caseTimeline, eventsTimeline) {
		t.Fatalf("timelines differ:\ncase=%s\nevents=%s", caseTimeline, eventsTimeline)
	}
	if len(caseBody["events"].([]any)) != len(eventsBody["events"].([]any)) {
		t.Fatal("event arrays differ across entry points")
	}
}

func TestEventsEndpointTimelineShapesHistory(t *testing.T) {
	client := newClient(t)
	// Build reject -> reopen boundary on a second case.
	status, body := client.do(http.MethodPost, "/devices", map[string]any{
		"asset_tag": "HTTP-1002", "model": "m", "room": "r", "steward": "Lin",
	})
	if status != http.StatusCreated {
		t.Fatalf("device status = %d", status)
	}
	deviceID := body["device"].(map[string]any)["id"].(string)
	status, body = client.do(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID, "nominal": 25.0, "tolerance": 0.5, "steward": "Qiao",
	})
	if status != http.StatusCreated {
		t.Fatalf("case status = %d", status)
	}
	caseID := body["case"].(map[string]any)["id"].(string)
	if status, _ = client.do(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"observed": 26.5, "technician": "Rui",
	}); status != http.StatusCreated {
		t.Fatalf("reject status = %d", status)
	}
	if status, _ = client.do(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor": "Lin", "reason": "setup replaced",
	}); status != http.StatusOK {
		t.Fatalf("reopen status = %d", status)
	}

	status, body = client.do(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if status != http.StatusOK {
		t.Fatalf("events status = %d", status)
	}
	timeline := body["timeline"].(map[string]any)
	steps := timeline["steps"].([]any)
	if len(steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(steps))
	}
	middle := steps[1].(map[string]any)
	if middle["action"] != "measurement_rejected" || middle["from_state"] != "awaiting_measurement" || middle["to_state"] != "recheck_required" {
		t.Fatalf("reject step wrong: %v", middle)
	}
	basis := middle["basis"].(map[string]any)
	if basis["accepted"] != false || basis["measurement_id"] == "" {
		t.Fatalf("reject basis wrong: %v", basis)
	}
	reopen := steps[2].(map[string]any)
	if reopen["action"] != "case_reopened" || reopen["to_state"] != "awaiting_measurement" {
		t.Fatalf("reopen step wrong: %v", reopen)
	}
}

func TestFailedReopenLeavesTimelineUntouched(t *testing.T) {
	client := newClient(t)
	caseID := client.createReadyCase("HTTP-1003")
	_, before := client.do(http.MethodGet, "/cases/"+caseID, nil)

	if status, body := client.do(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor": "Lin",
	}); status != http.StatusBadRequest || body["error"].(map[string]any)["code"] != "invalid_input" {
		t.Fatalf("blank reopen = %d %v", status, body)
	}
	if status, _ := client.do(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor": "Lin", "reason": "illegal from review_ready",
	}); status != http.StatusConflict {
		t.Fatalf("illegal reopen status = %d", status)
	}

	_, after := client.do(http.MethodGet, "/cases/"+caseID+"/events", nil)
	beforeTimeline, _ := json.Marshal(before["timeline"])
	afterTimeline, _ := json.Marshal(after["timeline"])
	if !bytes.Equal(beforeTimeline, afterTimeline) {
		t.Fatalf("failed reopen changed timeline:\nbefore=%s\nafter=%s", beforeTimeline, afterTimeline)
	}
}

func TestConcurrentSealHTTPEndpoint(t *testing.T) {
	client := newClient(t)
	caseID := client.createReadyCase("HTTP-1004")

	var wg sync.WaitGroup
	statuses := make(chan int, 8)
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			status, _ := client.do(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
				"reviewer": "V", "summary": "race",
			})
			statuses <- status
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)

	created, conflict := 0, 0
	for status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflict++
		default:
			t.Fatalf("unexpected seal status %d", status)
		}
	}
	if created != 1 || conflict != 7 {
		t.Fatalf("created=%d conflict=%d", created, conflict)
	}
}

func TestUnknownCaseTimelineIsNotFound(t *testing.T) {
	client := newClient(t)
	for _, path := range []string{"/cases/missing", "/cases/missing/events"} {
		status, body := client.do(http.MethodGet, path, nil)
		if status != http.StatusNotFound {
			t.Fatalf("%s status = %d", path, status)
		}
		if body["error"].(map[string]any)["code"] != "not_found" {
			t.Fatalf("%s code = %v", path, body["error"])
		}
	}
}
