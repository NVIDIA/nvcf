/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package eventledger is a read-only client for the Event Ledger v3 query API.
package eventledger

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxErrorBody = 1024

// Config holds Event Ledger client settings.
type Config struct {
	ServiceURL string
	// HostHeader overrides the Host header for hostname-based gateway routing.
	HostHeader string
	// APIKey is the bearer credential; it needs the fnds:getEvents read scope.
	APIKey  string
	Timeout time.Duration
}

// Client queries the Event Ledger.
type Client struct {
	config     Config
	httpClient *http.Client
}

// NewClient validates cfg and returns a Client.
func NewClient(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.ServiceURL) == "" {
		return nil, fmt.Errorf("event ledger URL is not set (use --event-ledger-url, EVENT_LEDGER_URL, or event_ledger_url)")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("an API key is required (run 'nvcf-cli api-key generate' or set NVCF_API_KEY)")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{config: cfg, httpClient: &http.Client{Timeout: timeout}}, nil
}

// StatsResponse is the latest event per context in a namespace.
type StatsResponse struct {
	Namespace string         `json:"namespace"`
	Summary   StatsSummary   `json:"summary"`
	Contexts  []StatsContext `json:"contexts"`
}

// StatsSummary counts contexts in total and by their latest event name.
type StatsSummary struct {
	TotalContexts int            `json:"total_contexts"`
	ByEvent       map[string]int `json:"by_event"`
}

// StatsContext is the latest event of one context.
type StatsContext struct {
	Context   string `json:"context"`
	EventName string `json:"event_name"`
	Timestamp string `json:"timestamp"`
	FirstSeen string `json:"first_seen"`
}

// EventsResponse is the event history of one context.
type EventsResponse struct {
	Namespace string  `json:"namespace"`
	Context   string  `json:"context"`
	Events    []Event `json:"events"`
}

// Event is one ledger entry.
type Event struct {
	EventName string       `json:"event_name"`
	Source    string       `json:"source,omitempty"`
	Timestamp string       `json:"timestamp"`
	Details   EventDetails `json:"details"`
	CreatedAt string       `json:"created_at,omitempty"`
	UpdatedAt string       `json:"updated_at,omitempty"`
}

// EventDetails carries the event payload attributes.
type EventDetails struct {
	Severity       string            `json:"severity,omitempty"`
	SeverityNumber int               `json:"severity_number,omitempty"`
	Body           string            `json:"body,omitempty"`
	Attributes     map[string]string `json:"attributes,omitempty"`
}

// StatsOptions filters a stats query.
type StatsOptions struct {
	// EventFilter limits results to these event names.
	EventFilter []string
}

// EventsContext identifies the context whose history is read. The values must
// match how the events were ingested.
type EventsContext struct {
	InstanceID       string
	DeploymentID     string
	GPUSpecification string
	ClusterID        string
}

// GetStats returns the latest event per context for a function version.
func (c *Client) GetStats(ctx context.Context, namespace string, opts StatsOptions) (*StatsResponse, error) {
	query := url.Values{}
	if len(opts.EventFilter) > 0 {
		query.Set("eventFilter", strings.Join(opts.EventFilter, ","))
	}
	var out StatsResponse
	if err := c.get(ctx, namespacePath(namespace, "stats"), query, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListEvents returns the event history of one context in a function version.
func (c *Client) ListEvents(ctx context.Context, namespace string, ec EventsContext) (*EventsResponse, error) {
	query := url.Values{}
	set := func(key, value string) {
		if value != "" {
			query.Set(key, value)
		}
	}
	set("instance_id", ec.InstanceID)
	set("deployment_id", ec.DeploymentID)
	set("gpu_specification_id", ec.GPUSpecification)
	set("cluster_id", ec.ClusterID)
	var out EventsResponse
	if err := c.get(ctx, namespacePath(namespace, "events"), query, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func namespacePath(namespace, leaf string) string {
	return "/v3/ledger/namespace/" + url.PathEscape(namespace) + "/" + leaf
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	target := strings.TrimRight(c.config.ServiceURL, "/") + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	req.Header.Set("Accept", "application/json")
	if c.config.HostHeader != "" {
		req.Host = c.config.HostHeader
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("event ledger request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return fmt.Errorf("event ledger returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding event ledger response: %w", err)
	}
	return nil
}
