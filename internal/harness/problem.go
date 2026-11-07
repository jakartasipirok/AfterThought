// Package harness defines dataset-first admission independently of demo capabilities.
package harness

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/jakartasipirok/AfterThought/internal/graph"
)

type Problem struct {
	Version        string `json:"version"`
	DatasetURL     string `json:"dataset_url"`
	Objective      string `json:"objective"`
	DatasetVersion string `json:"dataset_version"`
	Status         string `json:"status"`
}

func NewProblem(dataset, objective string) (Problem, error) {
	u, err := url.Parse(dataset)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return Problem{}, fmt.Errorf("dataset must be a credential-free HTTPS URL without query parameters")
	}
	if u.Hostname() == "www.kaggle.com" || u.Hostname() == "kaggle.com" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) == 4 && parts[3] == "data" {
			parts = parts[:3]
		}
		if len(parts) != 3 || parts[0] != "datasets" || parts[1] == "" || parts[2] == "" {
			return Problem{}, fmt.Errorf("expected a Kaggle dataset URL with owner and slug")
		}
		u.Host = "www.kaggle.com"
		u.Path = "/" + strings.Join(parts, "/")
	}
	if len(objective) > 4000 {
		return Problem{}, fmt.Errorf("objective is too long")
	}
	return Problem{Version: graph.Version, DatasetURL: u.String(), Objective: strings.TrimSpace(objective), DatasetVersion: "unresolved", Status: "awaiting_discovery"}, nil
}
