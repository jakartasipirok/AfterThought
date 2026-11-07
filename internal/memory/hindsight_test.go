package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHindsightStableRetentionAndScopedRecall(t *testing.T) {
	var operations []string
	var documents []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing auth")
		}
		if strings.HasSuffix(r.URL.Path, "/memories/recall") {
			var request struct {
				Tags      []string `json:"tags"`
				Match     string   `json:"tags_match"`
				MaxTokens int      `json:"max_tokens"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			if len(request.Tags) != 1 || request.Match != "all_strict" || request.MaxTokens > 1500 {
				t.Error("recall is not scoped/bounded")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"id": "fact", "text": "A verified human correction", "type": "experience", "metadata": map[string]string{"source": "human:reviewer"}, "document_id": "doc"}}})
			return
		}
		var request struct {
			Async     bool   `json:"async"`
			Operation string `json:"operation_id"`
			Items     []struct {
				Document string            `json:"document_id"`
				Metadata map[string]string `json:"metadata"`
			} `json:"items"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if !request.Async || len(request.Items) != 1 || request.Items[0].Metadata["source"] != "human:reviewer" {
			t.Error("retain lost provenance")
		}
		operations = append(operations, request.Operation)
		documents = append(documents, request.Items[0].Document)
		_ = json.NewEncoder(w).Encode(Receipt{Success: true, Async: true, OperationID: request.Operation})
	}))
	defer server.Close()
	h := &Hindsight{BaseURL: server.URL, APIKey: "test-key"}
	event := Event{Key: "immutable-evidence-1", Source: "human:reviewer", Content: "A verified correction", Task: "task-a"}
	for i := 0; i < 2; i++ {
		if _, err := h.Retain(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if operations[0] == "" || operations[0] != operations[1] || documents[0] != documents[1] {
		t.Fatal("retention retries create duplicate identities")
	}
	facts, err := h.Recall(context.Background(), event.Task, "what worked?")
	if err != nil || len(facts) != 1 || facts[0].Metadata["source"] != "human:reviewer" {
		t.Fatalf("lost recalled provenance: %+v %v", facts, err)
	}
	if h.Bank("task-a") == h.Bank("task-b") {
		t.Fatal("task banks collide")
	}
}

func TestDerivedObservationRetainsSourcesWithoutInheritingTruth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&request)
		if _, ok := request["include"]; !ok {
			t.Error("source facts were not requested")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"id": "derived", "text": "A synthesized lesson", "type": "observation", "metadata": map[string]string{}, "source_fact_ids": []string{"original"}}}, "source_facts": map[string]any{"original": map[string]any{"id": "original", "text": "An unverified model observation", "metadata": map[string]string{"source": "model_observation"}, "document_id": "journal-event"}}})
	}))
	defer server.Close()
	h := &Hindsight{BaseURL: server.URL}
	facts, err := h.Recall(context.Background(), "task", "lessons")
	if err != nil || len(facts) != 1 {
		t.Fatal(err)
	}
	if facts[0].Provenance != "derived_observation_verify_sources" || len(facts[0].Sources) != 1 || facts[0].Sources[0].Metadata["source"] != "model_observation" || facts[0].Metadata["source"] != "" {
		t.Fatalf("derived memory lost provenance or inherited truth: %+v", facts[0])
	}
}
func TestHindsightFailureIsVisibleAndEmptyBankIsAllowed(t *testing.T) {
	status := http.StatusNotFound
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte("sensitive-provider-detail"))
	}))
	defer server.Close()
	h := &Hindsight{BaseURL: server.URL}
	if facts, err := h.Recall(context.Background(), "new-task", "query"); err != nil || len(facts) != 0 {
		t.Fatal("new bank did not start empty")
	}
	status = http.StatusInternalServerError
	_, err := h.Recall(context.Background(), "task", "query")
	if err == nil || strings.Contains(err.Error(), "sensitive-provider-detail") {
		t.Fatal("memory outage hidden or response leaked")
	}
}
