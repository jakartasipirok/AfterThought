// Package model provides model transport; secrets stay in the worker process.
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Call struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type Message struct {
	Role       string `json:"role"`
	Content    string `json:"content,omitempty"`
	ToolCalls  []Call `json:"tool_calls,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Refusal    string `json:"refusal,omitempty"`
	// Persist opaque Responses items (including encrypted reasoning) in transcripts.
	// These are never sent as Chat Completions message fields.
	ResponseItems []json.RawMessage `json:"response_items,omitempty"`
}
type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}
type Function struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}
type Usage struct {
	Input  int64 `json:"prompt_tokens"`
	Output int64 `json:"completion_tokens"`
}
type Response struct {
	Message Message `json:"message"`
	Usage   Usage   `json:"usage"`
}
type Client struct {
	BaseURL, APIKey, Model string
	API                    string // auto (default), responses, or chat_completions
	HTTP                   *http.Client
}

func (c *Client) Complete(ctx context.Context, messages []Message, tools []Tool) (Response, error) {
	var result Response
	if c == nil || c.Model == "" {
		return result, fmt.Errorf("set AFTERTHOUGHT_MODEL and model credentials")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
		return result, fmt.Errorf("model endpoint requires HTTPS or loopback HTTP")
	}
	api := c.API
	if api == "" || api == "auto" {
		api = "chat_completions"
		if c.Model == "gpt-6.1-sol" || strings.HasPrefix(c.Model, "gpt-6.1-sol-") {
			api = "responses"
		}
	}
	switch api {
	case "responses":
		return c.completeResponses(ctx, messages, tools)
	case "chat_completions":
		return c.completeChat(ctx, messages, tools)
	default:
		return result, fmt.Errorf("model API must be auto, responses, or chat_completions")
	}
}

func (c *Client) post(ctx context.Context, path string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if len(body) > 256<<10 {
		return fmt.Errorf("context exceeds 256 KiB; start a new session with a concise memory summary")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("model redirects are disabled") }}
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("model request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("model HTTP status %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(b) > 1<<20 {
		return fmt.Errorf("model response too large")
	}
	if err = json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("invalid model response")
	}
	return nil
}

func (c *Client) completeChat(ctx context.Context, messages []Message, tools []Tool) (Response, error) {
	var result Response
	wireMessages := append([]Message(nil), messages...)
	for i := range wireMessages {
		wireMessages[i].ResponseItems = nil
	}
	var response struct {
		Choices []struct {
			Message Message `json:"message"`
			Finish  string  `json:"finish_reason"`
		} `json:"choices"`
		Usage Usage `json:"usage"`
	}
	if err := c.post(ctx, "/chat/completions", map[string]any{"model": c.Model, "store": false, "max_completion_tokens": 4096, "messages": wireMessages, "tools": tools, "parallel_tool_calls": false}, &response); err != nil {
		return result, err
	}
	result.Usage = response.Usage
	if len(response.Choices) != 1 {
		return result, fmt.Errorf("expected one model choice")
	}
	choice := response.Choices[0]
	if choice.Message.Refusal != "" {
		return result, fmt.Errorf("model refused the request")
	}
	if choice.Finish != "stop" && choice.Finish != "tool_calls" {
		return result, fmt.Errorf("model result incomplete or refused")
	}
	if choice.Finish == "tool_calls" && len(choice.Message.ToolCalls) == 0 {
		return result, fmt.Errorf("model omitted tool calls")
	}
	if choice.Finish == "stop" && (len(choice.Message.ToolCalls) != 0 || strings.TrimSpace(choice.Message.Content) == "") {
		return result, fmt.Errorf("model returned an empty or inconsistent terminal response")
	}
	if err := validateAssistant(choice.Message, tools); err != nil {
		return result, err
	}
	result.Message = choice.Message
	return result, nil
}

func validateAssistant(message Message, tools []Tool) error {
	if message.Role != "assistant" {
		return fmt.Errorf("invalid model message role")
	}
	if message.Refusal != "" {
		return fmt.Errorf("model refused the request")
	}
	if len(message.ToolCalls) > 8 {
		return fmt.Errorf("model exceeded per-turn tool budget")
	}
	if len(message.ToolCalls) == 0 && strings.TrimSpace(message.Content) == "" {
		return fmt.Errorf("model returned an empty terminal response")
	}
	known := map[string]bool{}
	for _, t := range tools {
		known[t.Function.Name] = true
	}
	seen := map[string]bool{}
	for _, call := range message.ToolCalls {
		if call.ID == "" || seen[call.ID] || call.Type != "function" || !known[call.Function.Name] || !json.Valid([]byte(call.Function.Arguments)) {
			return fmt.Errorf("model returned an invalid or unavailable tool")
		}
		seen[call.ID] = true
	}
	return nil
}
