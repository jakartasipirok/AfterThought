package webapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jakartasipirok/AfterThought/internal/model"
)

func TestLoopbackAndSameOriginBoundary(t *testing.T) {
	handler := (&Server{}).Handler()
	for _, test := range []struct{ name, host, origin, fetch string }{
		{"foreign host", "attacker.example", "", ""},
		{"foreign origin", "127.0.0.1:8080", "https://attacker.example", ""},
		{"different loopback port", "localhost:8080", "http://localhost:8081", ""},
		{"cross site fetch", "localhost:8080", "", "cross-site"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://"+test.host+"/api/sessions", strings.NewReader(`{}`))
			request.Host = test.host
			request.Header.Set("Content-Type", "application/json")
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.fetch != "" {
				request.Header.Set("Sec-Fetch-Site", test.fetch)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != 403 {
				t.Fatalf("boundary accepted: %d %s", response.Code, response.Body.String())
			}
		})
	}
}
func TestAdmissionValidationPrecedesSideEffects(t *testing.T) {
	handler := (&Server{}).Handler()
	for _, body := range []string{`{"id":"../escape","prompt":"hello"}`, `{"id":"valid","prompt":""}`, `{"id":"valid","prompt":"hello","max_turns":99}`, `{"id":"valid","prompt":"hello","api_key":"not-accepted"}`, `{} {}`} {
		request := httptest.NewRequest(http.MethodPost, "http://localhost:8080/api/sessions", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "http://localhost:8080")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 400 {
			t.Fatalf("invalid request reached backend: %d %s", response.Code, response.Body.String())
		}
	}
}
func TestPublicTranscriptDTOHasNoOpaqueProviderState(t *testing.T) {
	internal := model.Message{Role: "assistant", Content: "Visible summary", ResponseItems: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"private-reasoning"}`)}}
	public := Message{Role: internal.Role, Content: internal.Content, ToolCalls: internal.ToolCalls, ToolCallID: internal.ToolCallID}
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("private-reasoning")) || bytes.Contains(encoded, []byte("response_items")) {
		t.Fatal("opaque reasoning escaped into public DTO")
	}
}
func TestHealthDoesNotClaimReadinessWithoutDependencies(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://localhost/api/health", nil)
	response := httptest.NewRecorder()
	(&Server{}).Handler().ServeHTTP(response, request)
	if response.Code != 503 {
		t.Fatal("reported ready without dependencies")
	}
	var body struct {
		Ready  bool            `json:"ready"`
		Checks map[string]bool `json:"checks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Ready || body.Checks["worker"] || body.Checks["database"] || body.Checks["temporal"] {
		t.Fatal("invented readiness")
	}
}
