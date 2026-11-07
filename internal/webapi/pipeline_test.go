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
