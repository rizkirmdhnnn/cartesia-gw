package gateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A handler that sets a header after writing the status used to reach the wire
// twice, which wrote "HTTP/1.1 200 OK" into the middle of a chunked body and
// produced a response no client could parse. The recorder must forward only
// the first WriteHeader and keep logging the request.
func TestSecondWriteHeaderNeverReachesTheWire(t *testing.T) {
	var (
		mu   sync.Mutex
		logs int
	)
	logger := slog.New(slog.NewTextHandler(&logSink{count: &logs, mu: &mu}, nil))
	s := &Server{logger: logger}

	handler := s.logMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Gateway-Provider", "cartesia")
		w.WriteHeader(http.StatusOK)
		w.Header().Set("X-Gateway-Adjusted", "speed:clamped")
		w.WriteHeader(http.StatusOK) // the bug: a second status line
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want one 200 on the wire", rec.Code)
	}
	if got := rec.Header().Get("X-Gateway-Provider"); got != "cartesia" {
		t.Errorf("headers were lost: X-Gateway-Provider = %q", got)
	}
	var body map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not one JSON document: %q (%v)", rec.Body.String(), err)
	}
	if !body["ok"] {
		t.Errorf("body = %q", rec.Body.String())
	}
	mu.Lock()
	logged := logs
	mu.Unlock()
	if logged == 0 {
		t.Error("the request was not logged")
	}
}

// A real HTTP client is the only way to see what the connection received, so
// this pins the contract end to end: one response per request.
func TestOneDocumentPerResponseOverHTTP(t *testing.T) {
	upstream := &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		return http.StatusOK, "AUDIO", nil
	}}
	_, st, server := newServer(t, upstream, "")
	if _, err := st.CreateKey(context.Background(), "main", "sk_car_main"); err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}

	resp, err := http.Get(server.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET /v1/models error = %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var page struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("body is not one JSON document: %v (%s)", err, body)
	}
	seen := map[string]bool{}
	for _, model := range page.Data {
		seen[model.ID] = true
	}
	if !seen["cartesia/sonic-3"] {
		t.Errorf("catalogue = %v, want the default provider\\x27s models", seen)
	}
}

type logSink struct {
	count *int
	mu    *sync.Mutex
}

func (c *logSink) Write(p []byte) (int, error) {
	c.mu.Lock()
	*c.count += strings.Count(string(p), "msg=request")
	c.mu.Unlock()
	return len(p), nil
}
