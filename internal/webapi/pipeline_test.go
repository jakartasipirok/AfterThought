package webapi

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/jakartasipirok/AfterThought/internal/pipeline"
)

func TestPromotionRequiresExplicitHumanConfirmation(t *testing.T) {
	for _, body := range []string{
		`{"actor":"reviewer","approved":false}`,
		`{"actor":"reviewer"}`,
	} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/releases/candidate/approve", bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		// No database or key is configured: rejection must happen before either is accessed.
		(&Server{Pipeline: &pipeline.Service{}}).Handler().ServeHTTP(w, r)
		if w.Code != 403 || !bytes.Contains(w.Body.Bytes(), []byte("explicit human approval is required")) {
			t.Fatalf("unconfirmed promotion was not rejected: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestInternalAuthorSessionsAreReadableButCannotBeAdmittedDirectly(t *testing.T) {
	id := "internal/build/demo-job/author/0"
	if !readableSessionID(id) || identifier.MatchString(id) {
		t.Fatal("internal read/admission namespace is not separated")
	}
	for _, bad := range []string{"internal/build/../author/0", "internal/build/job/author/99", "internal/batch/job/serve"} {
		if readableSessionID(bad) {
			t.Fatal("unexpected internal session read path", bad)
		}
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/sessions", bytes.NewBufferString(`{"id":"internal/build/demo-job/author/0","prompt":"collide"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	(&Server{}).Handler().ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("public admission accepted a reserved child ID", w.Code)
	}
}

func TestPublicRequestsCannotClaimAutomaticReviewIdentity(t *testing.T) {
	for _, request := range []struct{ path, body string }{
		{"/api/bootstraps", `{"id":"public","task":"demo","auto_review_id":"internal"}`},
		{"/api/bootstraps", `{"id":"public","task":"demo","regression":{"batch_id":"private"}}`},
		{"/api/bootstraps", `{"id":"auto-review-reserved","task":"demo"}`},
		{"/api/sessions", `{"id":"auto-review-reserved","task":"demo","prompt":"collide"}`},
	} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8080"+request.path, bytes.NewBufferString(request.body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		(&Server{Pipeline: &pipeline.Service{}}).Handler().ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("reserved identity admitted: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestAutoReviewRequiresExplicitToggleAndSameOrigin(t *testing.T) {
	for _, body := range []string{`{"task":"demo"}`, `{"task":"demo","enabled":null}`, `{"task":"../demo","enabled":true}`} {
		r := httptest.NewRequest("PUT", "http://127.0.0.1:8080/api/auto-review", bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		(&Server{Pipeline: &pipeline.Service{}}).Handler().ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid toggle admitted: %d", w.Code)
		}
	}
	r := httptest.NewRequest("PUT", "http://127.0.0.1:8080/api/auto-review", bytes.NewBufferString(`{"task":"demo","enabled":true}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://untrusted.example")
	w := httptest.NewRecorder()
	(&Server{Pipeline: &pipeline.Service{}}).Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("cross-origin toggle admitted: %d", w.Code)
	}
}

func TestPolicyDecisionsRequireExplicitConfirmationAndSameOrigin(t *testing.T) {
	for _, tc := range []struct{ body, origin string }{
		{`{"decision":"approve","actor":"reviewer"}`, ""},
		{`{"decision":"dismiss","confirmed":false}`, ""},
		{`{"decision":"approve","confirmed":true}`, "https://untrusted.example"},
	} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/policy-proposals/test/decision", bytes.NewBufferString(tc.body))
		r.Header.Set("Content-Type", "application/json")
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		(&Server{Pipeline: &pipeline.Service{}}).Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("unconfirmed or cross-origin policy decision admitted: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestPolicyReviewRejectsUnrequestedDraftsAndInvalidScope(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/api/auto-reviews/review/draft", `{"requested":false}`},
		{"/api/auto-review/run", `{"task":"../invalid"}`},
	} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8080"+tc.path, bytes.NewBufferString(tc.body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		(&Server{Pipeline: &pipeline.Service{}}).Handler().ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid request accepted: %d", w.Code)
		}
	}
}
