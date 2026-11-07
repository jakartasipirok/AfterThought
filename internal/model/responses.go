package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// responsesInput preserves all opaque output items for stateless tool turns.
// Normalized messages remain available for journals and legacy Chat clients.
func responsesInput(messages []Message) ([]any, error) {
	input := []any{}
	for _, message := range messages {
		if len(message.ResponseItems) > 0 {
			if message.Role != "assistant" {
				return nil, fmt.Errorf("Responses items require an assistant message")
			}
			for _, item := range message.ResponseItems {
				if !json.Valid(item) {
					return nil, fmt.Errorf("invalid persisted Responses item")
				}
				input = append(input, item)
			}
			continue
		}
		switch message.Role {
		case "tool":
			if message.ToolCallID == "" {
				return nil, fmt.Errorf("tool result requires a call ID")
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": message.Content})
		case "system", "developer", "user", "assistant":
			if message.Content != "" {
				input = append(input, map[string]any{"role": message.Role, "content": message.Content})
			}
			for _, call := range message.ToolCalls {
				input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
			}
		default:
			return nil, fmt.Errorf("unsupported transcript role")
		}
	}
	return input, nil
}

func (c *Client) completeResponses(ctx context.Context, messages []Message, tools []Tool) (Response, error) {
	var result Response
	input, err := responsesInput(messages)
	if err != nil {
		return result, err
	}
	flatTools := make([]any, 0, len(tools))
	for _, tool := range tools {
		// Existing schemas have optional offset/timeout properties. Keep them
		// non-strict and validate tool arguments at the executor boundary.
		flatTools = append(flatTools, map[string]any{"type": "function", "name": tool.Function.Name, "description": tool.Function.Description, "parameters": tool.Function.Parameters, "strict": false})
	}
	var response struct {
		Status string            `json:"status"`
		Output []json.RawMessage `json:"output"`
		Usage  struct {
			Input  int64 `json:"input_tokens"`
			Output int64 `json:"output_tokens"`
		} `json:"usage"`
		Error json.RawMessage `json:"error"`
	}
	err = c.post(ctx, "/responses", map[string]any{"model": c.Model, "store": false, "max_output_tokens": 4096, "input": input, "tools": flatTools, "parallel_tool_calls": false, "include": []string{"reasoning.encrypted_content"}}, &response)
	if err != nil {
		return result, err
	}
	result.Usage = Usage{Input: response.Usage.Input, Output: response.Usage.Output}
	if response.Status != "completed" || len(response.Error) > 0 && string(response.Error) != "null" {
		return result, fmt.Errorf("Responses result is incomplete or failed")
	}
	message := Message{Role: "assistant", ResponseItems: response.Output}
	var texts []string
	for _, raw := range response.Output {
		var item struct {
			Type      string `json:"type"`
			Role      string `json:"role"`
			Status    string `json:"status"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err = json.Unmarshal(raw, &item); err != nil {
			return result, fmt.Errorf("invalid Responses output item")
		}
		if item.Status != "" && item.Status != "completed" {
			return result, fmt.Errorf("Responses output item is incomplete")
		}
		switch item.Type {
		case "reasoning": // retained opaquely, including encrypted_content
		case "function_call":
			message.ToolCalls = append(message.ToolCalls, Call{ID: item.CallID, Type: "function", Function: FunctionCall{Name: item.Name, Arguments: item.Arguments}})
		case "message":
			if item.Role != "assistant" {
				return result, fmt.Errorf("invalid Responses message role")
			}
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					texts = append(texts, part.Text)
				case "refusal":
					return result, fmt.Errorf("model refused the request")
				default:
					return result, fmt.Errorf("unsupported Responses content type")
				}
			}
		default:
			return result, fmt.Errorf("unsupported Responses output type")
		}
	}
	message.Content = strings.Join(texts, "\n")
	if err = validateAssistant(message, tools); err != nil {
		return result, err
	}
	result.Message = message
	return result, nil
}
