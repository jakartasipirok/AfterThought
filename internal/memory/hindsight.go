// Package memory integrates Hindsight without adding model-visible tools.
package memory

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

	"github.com/google/uuid"
	"github.com/jakartasipirok/AfterThought/internal/graph"
)

// REST fields follow Hindsight v1.2.0's published OpenAPI contract.
type Hindsight struct {
	BaseURL, APIKey, BankPrefix string
	HTTP                        *http.Client
}
type Event struct {
	Key     string `json:"key"`
	Source  string `json:"source"`
	Content string `json:"content"`
	Task    string `json:"task"`
}
type Fact struct {
	ID            string            `json:"id"`
	Text          string            `json:"text"`
	Type          string            `json:"type"`
	Metadata      map[string]string `json:"metadata"`
	DocumentID    string            `json:"document_id"`
	SourceFactIDs []string          `json:"source_fact_ids,omitempty"`
	Sources       []SourceFact      `json:"sources,omitempty"`
	Provenance    string            `json:"provenance"`
}
type SourceFact struct {
	ID         string            `json:"id"`
	Text       string            `json:"text"`
	Metadata   map[string]string `json:"metadata"`
	DocumentID string            `json:"document_id"`
}
type Receipt struct {
	Success     bool   `json:"success"`
	OperationID string `json:"operation_id"`
	Async       bool   `json:"async"`
}

func (h *Hindsight) Bank(task string) string {
	prefix := h.BankPrefix
	if prefix == "" {
		prefix = "afterthought"
	}
	return prefix + "-" + strings.TrimPrefix(graph.Hash([]byte(task)), "sha256:")[:24]
}
func (h *Hindsight) request(ctx context.Context, path string, input, out any, emptyOn404 bool) error {
	u, err := url.Parse(h.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
		return fmt.Errorf("Hindsight requires HTTPS or loopback HTTP")
	}
	b, err := json.Marshal(input)
	if err != nil {
		return err
	}
	if len(b) > 64<<10 {
		return fmt.Errorf("memory event exceeds 64 KiB")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(h.BaseURL, "/")+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.APIKey)
	}
	client := h.HTTP
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("Hindsight redirects are disabled") }}
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Hindsight unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode == 404 && emptyOn404 {
		return nil
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("Hindsight HTTP status %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return fmt.Errorf("Hindsight response too large")
	}
	if err = json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("invalid Hindsight response")
	}
	return nil
}
func (h *Hindsight) Retain(ctx context.Context, e Event) (Receipt, error) {
	var receipt Receipt
	bank := h.Bank(e.Task)
	operation := uuid.NewSHA1(uuid.NameSpaceURL, []byte(bank+"/"+e.Key)).String()
	document := strings.TrimPrefix(graph.Hash([]byte(e.Key)), "sha256:")
	input := map[string]any{"async": true, "operation_id": operation, "items": []any{map[string]any{"content": e.Content, "context": "AfterThought learning evidence; preserve source attribution and uncertainty", "document_id": document, "metadata": map[string]string{"source": e.Source, "task": e.Task, "event_key": e.Key}, "tags": []string{"task:" + bank}}}}
	err := h.request(ctx, "/v1/default/banks/"+url.PathEscape(bank)+"/memories", input, &receipt, false)
	if err != nil {
		return receipt, err
	}
	if !receipt.Success || !receipt.Async || receipt.OperationID == "" {
		return receipt, fmt.Errorf("Hindsight did not acknowledge async retention")
	}
	return receipt, nil
}
func (h *Hindsight) Recall(ctx context.Context, task, query string) ([]Fact, error) {
	var response struct {
		Results     []Fact          `json:"results"`
		SourceFacts map[string]Fact `json:"source_facts"`
	}
	bank := h.Bank(task)
	err := h.request(ctx, "/v1/default/banks/"+url.PathEscape(bank)+"/memories/recall", map[string]any{"query": query, "budget": "low", "max_tokens": 1500, "include": map[string]any{"source_facts": map[string]int{"max_tokens": 1000, "max_tokens_per_observation": 500}}, "tags": []string{"task:" + bank}, "tags_match": "all_strict"}, &response, true)
	if err != nil {
		return nil, err
	}
	if response.Results == nil {
		response.Results = []Fact{}
	}
	if len(response.Results) > 20 {
		response.Results = response.Results[:20]
	}
	for i := range response.Results {
		fact := &response.Results[i]
		fact.Provenance = "unattributed_verify_journal"
		if fact.Metadata["source"] != "" {
			fact.Provenance = "attributed_fact"
		}
		if fact.Type == "observation" {
			fact.Provenance = "derived_observation_verify_sources"
		}
		for _, id := range fact.SourceFactIDs {
			if len(fact.Sources) >= 4 {
				break
			}
			if source, ok := response.SourceFacts[id]; ok {
				if len(source.Text) > 2000 {
					source.Text = source.Text[:2000]
				}
				fact.Sources = append(fact.Sources, SourceFact{ID: id, Text: source.Text, Metadata: source.Metadata, DocumentID: source.DocumentID})
			}
		}
		if len(response.Results[i].Text) > 4000 {
			response.Results[i].Text = response.Results[i].Text[:4000]
		}
	}
	return response.Results, nil
}
func (h *Hindsight) Reflect(ctx context.Context, task, query string) (json.RawMessage, error) {
	var out json.RawMessage
	bank := h.Bank(task)
	err := h.request(ctx, "/v1/default/banks/"+url.PathEscape(bank)+"/reflect", map[string]any{"query": query, "budget": "low", "max_tokens": 1000, "tags": []string{"task:" + bank}, "tags_match": "all_strict"}, &out, false)
	return out, err
}
