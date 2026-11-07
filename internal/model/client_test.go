package model_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jakartasipirok/AfterThought/internal/harness"
	"github.com/jakartasipirok/AfterThought/internal/model"
)

func TestFourToolsAndProviderResultValidation(t *testing.T) {
	for _, finish := range []string{"tool_calls", "length"} {
		t.Run(finish, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Tools []model.Tool `json:"tools"`
					Store bool         `json:"store"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if len(request.Tools) != 4 || request.Store {
					t.Error("unexpected model visibility or storage")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": finish, "message": model.Message{Role: "assistant", ToolCalls: []model.Call{{ID: "c", Type: "function", Function: model.FunctionCall{Name: "write", Arguments: `{"path":"test.txt","content":"hello"}`}}}}}}, "usage": map[string]int{"prompt_tokens": 12, "completion_tokens": 4}})
			}))
			defer server.Close()
			c := model.Client{BaseURL: server.URL, Model: "test"}
			response, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "test"}}, harness.Definitions())
			if response.Usage.Input != 12 {
				t.Fatal("usage missing")
			}
			if finish == "length" && err == nil {
				t.Fatal("truncated response accepted")
			}
			if finish == "tool_calls" && (err != nil || len(response.Message.ToolCalls) != 1) {
				t.Fatalf("valid tool call failed: %v", err)
			}
		})
	}
}

func TestRefusalAndEmptyTerminalResultAreNotSuccess(t *testing.T) {
	for _, message := range []map[string]any{{"role": "assistant", "content": nil, "refusal": "Cannot comply"}, {"role": "assistant", "content": " "}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": message}}})
		}))
		c := model.Client{BaseURL: server.URL, Model: "test"}
		if _, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "test"}}, harness.Definitions()); err == nil {
			server.Close()
			t.Fatal("refusal or empty response was marked successful")
		}
		server.Close()
	}
}
