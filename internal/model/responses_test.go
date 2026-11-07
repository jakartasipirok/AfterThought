package model_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jakartasipirok/AfterThought/internal/graph"
	"github.com/jakartasipirok/AfterThought/internal/harness"
	"github.com/jakartasipirok/AfterThought/internal/model"
)

func TestSolResponsesToolRoundTripPreservesReasoningAcrossPersistence(t *testing.T) {
	reasoning := json.RawMessage(`{"id":"reasoning-item","type":"reasoning","summary":[],"encrypted_content":"opaque-encrypted-state"}`)
	function := json.RawMessage(`{"id":"output-item-id","type":"function_call","status":"completed","call_id":"stable-call-id","name":"write","arguments":"{\"path\":\"check.txt\",\"content\":\"ok\"}"}`)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/responses" {
			t.Errorf("Sol used %s", r.URL.Path)
		}
		var request struct {
			Model              string            `json:"model"`
			Store              bool              `json:"store"`
			Input              []json.RawMessage `json:"input"`
			Tools              []map[string]any  `json:"tools"`
			Include            []string          `json:"include"`
			PreviousResponseID string            `json:"previous_response_id"`
			MaxTokens          int               `json:"max_output_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "gpt-6.1-sol" || request.Store || request.PreviousResponseID != "" || request.MaxTokens != 4096 {
			t.Error("model identity, storage, or stateless limits changed")
		}
		if !reflect.DeepEqual(request.Include, []string{"reasoning.encrypted_content"}) {
			t.Error("encrypted reasoning not requested")
		}
		var names []string
		for _, tool := range request.Tools {
			names = append(names, tool["name"].(string))
			if tool["strict"] != false || tool["function"] != nil || tool["parameters"] == nil {
				t.Error("Responses tool shape invalid")
			}
		}
		if !reflect.DeepEqual(names, []string{"read", "bash", "edit", "write"}) {
			t.Error("visible tool surface changed")
		}
		if requests == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []json.RawMessage{reasoning, function}, "usage": map[string]int{"input_tokens": 20, "output_tokens": 8}})
			return
		}
		if len(request.Input) != 4 {
			t.Fatalf("unexpected replay input: %s", request.Input)
		}
		for i, want := range []json.RawMessage{reasoning, function} {
			var got, expected any
			_ = json.Unmarshal(request.Input[i+1], &got)
			_ = json.Unmarshal(want, &expected)
			if !reflect.DeepEqual(got, expected) {
				t.Error("opaque response item was lost or reconstructed")
			}
		}
		var output struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		}
		_ = json.Unmarshal(request.Input[3], &output)
		if output.Type != "function_call_output" || output.CallID != "stable-call-id" || output.Output != "written" {
			t.Errorf("tool output did not match call_id: %+v", output)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []any{map[string]any{"id": "final-message", "type": "message", "role": "assistant", "status": "completed", "phase": "final", "content": []any{map[string]string{"type": "output_text", "text": "Verified."}}}}, "usage": map[string]int{"input_tokens": 30, "output_tokens": 3}})
	}))
	defer server.Close()
	c := model.Client{BaseURL: server.URL, Model: "gpt-6.1-sol"}
	messages := []model.Message{{Role: "user", Content: "Write a check file."}}
	first, err := c.Complete(context.Background(), messages, harness.Definitions())
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Message.ToolCalls) != 1 || first.Message.ToolCalls[0].ID != "stable-call-id" || first.Usage.Input != 20 || first.Usage.Output != 8 {
		t.Fatalf("tool identity or usage mapping failed: %+v", first)
	}
	messages = append(messages, first.Message, model.Message{Role: "tool", ToolCallID: first.Message.ToolCalls[0].ID, Content: "written"})
	encoded, _ := json.Marshal(messages)
	var persisted []model.Message
	if err = graph.Decode(encoded, &persisted); err != nil {
		t.Fatal(err)
	}
	last, err := c.Complete(context.Background(), persisted, harness.Definitions())
	if err != nil {
		t.Fatal(err)
	}
	if last.Message.Content != "Verified." || len(last.Message.ToolCalls) != 0 || last.Usage.Output != 3 {
		t.Fatalf("final response failed: %+v", last)
	}
}

func TestResponsesRejectsInvalidResultsAndPreservesUsage(t *testing.T) {
	tests := []struct {
		Name, Status string
		Output       any
	}{
		{"incomplete", "incomplete", []any{}},
		{"empty", "completed", []any{}},
		{"refusal", "completed", []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "refusal", "refusal": "Cannot comply"}}}}},
		{"unknown tool", "completed", []any{map[string]string{"type": "function_call", "call_id": "c", "name": "unavailable", "arguments": "{}"}}},
		{"bad arguments", "completed", []any{map[string]string{"type": "function_call", "call_id": "c", "name": "read", "arguments": "{"}}},
	}
	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": test.Status, "output": test.Output, "usage": map[string]int{"input_tokens": 11, "output_tokens": 2}})
			}))
			defer server.Close()
			c := model.Client{BaseURL: server.URL, Model: "gpt-6.1-sol"}
			out, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "test"}}, harness.Definitions())
			if err == nil || out.Usage.Input != 11 {
				t.Fatalf("invalid result accepted or usage lost: %v %+v", err, out)
			}
		})
	}
}

func TestChatRequestsDoNotLeakInternalResponseItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Error("legacy adapter changed")
		}
		var request struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		for _, message := range request.Messages {
			if _, ok := message["response_items"]; ok {
				t.Error("internal Responses state leaked into Chat wire format")
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": model.Message{Role: "assistant", Content: "ok"}}}})
	}))
	defer server.Close()
	c := model.Client{BaseURL: server.URL, Model: "legacy-model"}
	_, err := c.Complete(context.Background(), []model.Message{{Role: "assistant", Content: "prior", ResponseItems: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)}}}, harness.Definitions())
	if err != nil {
		t.Fatal(err)
	}
}

func TestResponsesCanConvertLegacyToolTranscripts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []map[string]any `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if len(request.Input) != 2 || request.Input[0]["type"] != "function_call" || request.Input[0]["call_id"] != "legacy-call" || request.Input[1]["type"] != "function_call_output" || request.Input[1]["call_id"] != "legacy-call" {
			t.Error("legacy transcript conversion failed")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": "ok"}}}}})
	}))
	defer server.Close()
	c := model.Client{BaseURL: server.URL, Model: "gpt-6.1-sol"}
	_, err := c.Complete(context.Background(), []model.Message{{Role: "assistant", ToolCalls: []model.Call{{ID: "legacy-call", Type: "function", Function: model.FunctionCall{Name: "read", Arguments: `{"path":"a"}`}}}}, {Role: "tool", ToolCallID: "legacy-call", Content: "data"}}, harness.Definitions())
	if err != nil {
		t.Fatal(err)
	}
}
