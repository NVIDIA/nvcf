/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package cloudevents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	cloudevent "github.com/cloudevents/sdk-go/v2/event"
	"gopkg.in/yaml.v2"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
)

const SendSyncTimeout = 30 * time.Second
const cloudEventsBatchContentType = "application/cloudevents-batch+json"

type CloudEventsStorageClient struct {
	endpoint   string
	httpClient *http.Client
	tokenAuth  *clientCredentialsTokenSource
}

// NewCloudEventsStorageClient creates a new CloudEvents storage client.
func NewCloudEventsStorageClient(cfg config.CloudEventsConfig) (*CloudEventsStorageClient, error) {
	if err := config.ValidateCloudEventsConfig(cfg); err != nil {
		return nil, err
	}

	var tokenAuth *clientCredentialsTokenSource
	if cfg.TokenEndpoint != "" {
		tokenAuth = &clientCredentialsTokenSource{
			tokenEndpoint:   cfg.TokenEndpoint,
			credentialsFile: cfg.CredentialsFile,
			clientID:        cfg.ClientID,
			clientSecret:    cfg.ClientSecret,
		}
	}

	return &CloudEventsStorageClient{
		endpoint: cfg.Endpoint,
		httpClient: &http.Client{
			Timeout: SendSyncTimeout,
		},
		tokenAuth: tokenAuth,
	}, nil
}

func (c *CloudEventsStorageClient) StoreBatch(ctx context.Context, batch []types.StageTransitionEvent) (err error) {
	var eventsList []cloudevent.Event
	for _, event := range batch {
		newEvent := cloudevent.New(cloudevent.CloudEventsVersionV1)
		newEvent.SetID(fmt.Sprintf("%s-%s", event.FunctionVersionId.String(), event.Event))
		newEvent.SetType(event.Event + event.EventType)
		newEvent.SetSource("event-ledger")
		newEvent.SetTime(cloudEventTime(event.Timestamp))
		err := newEvent.SetData(cloudevent.ApplicationJSON, event)
		if err != nil {
			return err
		}
		eventsList = append(eventsList, newEvent)
	}

	return c.sendEvents(ctx, "v1", eventsList)
}

func (c *CloudEventsStorageClient) StoreBatchV2(ctx context.Context, batch []types.DeploymentStageTransitionEvent) error {
	var eventsList []cloudevent.Event
	for _, event := range batch {
		newEvent := cloudevent.New(cloudevent.CloudEventsVersionV1)
		newEvent.SetID(fmt.Sprintf("%s-%s", event.FunctionVersionId.String(), event.Event))
		newEvent.SetType(event.Event + event.EventType)
		newEvent.SetSource("event-ledger")
		newEvent.SetTime(cloudEventTime(event.Timestamp))
		err := newEvent.SetData(cloudevent.ApplicationJSON, event)
		if err != nil {
			return err
		}
		eventsList = append(eventsList, newEvent)
	}

	return c.sendEvents(ctx, "v2", eventsList)
}

func (c *CloudEventsStorageClient) sendEvents(ctx context.Context, version string, events []cloudevent.Event) error {
	if len(events) == 0 {
		return nil
	}

	body, err := json.Marshal(events)
	if err != nil {
		return fmt.Errorf("failed to marshal CloudEvents %s batch: %w", version, err)
	}

	sendCtx, cancel := context.WithTimeout(ctx, SendSyncTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(sendCtx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create CloudEvents %s request: %w", version, err)
	}
	req.Header.Set("Content-Type", cloudEventsBatchContentType)
	req.Header.Set("Accept", "application/json")

	if c.tokenAuth != nil {
		token, err := c.tokenAuth.accessToken(sendCtx, c.httpClient)
		if err != nil {
			return fmt.Errorf("failed to fetch CloudEvents auth token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send CloudEvents %s batch: %w", version, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		message := strings.TrimSpace(string(respBody))
		if message == "" {
			message = resp.Status
		}
		return fmt.Errorf("failed to send CloudEvents %s batch: status %d: %s", version, resp.StatusCode, message)
	}

	return nil
}

func cloudEventTime(timestamp time.Time) time.Time {
	if timestamp.IsZero() {
		return time.Now()
	}
	return timestamp
}

type clientCredentialsTokenSource struct {
	tokenEndpoint   string
	credentialsFile string
	clientID        string
	clientSecret    string

	mu          sync.Mutex
	cachedToken string
	expiresAt   time.Time
}

func (s *clientCredentialsTokenSource) accessToken(ctx context.Context, httpClient *http.Client) (string, error) {
	s.mu.Lock()
	if s.cachedToken != "" && time.Now().Before(s.expiresAt.Add(-30*time.Second)) {
		token := s.cachedToken
		s.mu.Unlock()
		return token, nil
	}
	s.mu.Unlock()

	clientID, clientSecret, err := s.credentials()
	if err != nil {
		return "", err
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		message := strings.TrimSpace(string(respBody))
		if message == "" {
			message = resp.Status
		}
		return "", fmt.Errorf("token request failed with status %d: %s", resp.StatusCode, message)
	}

	var tokenResponse struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResponse); err != nil {
		return "", fmt.Errorf("failed to decode token response: %w", err)
	}
	if tokenResponse.AccessToken == "" {
		return "", fmt.Errorf("token response missing access_token")
	}

	expiresIn := time.Duration(tokenResponse.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = time.Hour
	}

	s.mu.Lock()
	s.cachedToken = tokenResponse.AccessToken
	s.expiresAt = time.Now().Add(expiresIn)
	s.mu.Unlock()

	return tokenResponse.AccessToken, nil
}

func (s *clientCredentialsTokenSource) credentials() (string, string, error) {
	if s.credentialsFile == "" {
		return s.clientID, s.clientSecret, nil
	}

	contents, err := os.ReadFile(s.credentialsFile)
	if err != nil {
		return "", "", fmt.Errorf("failed to read credentials file: %w", err)
	}

	var creds struct {
		ID           string `yaml:"id"`
		Secret       string `yaml:"secret"`
		ClientID     string `yaml:"client_id"`
		ClientSecret string `yaml:"client_secret"`
	}
	if err := yaml.Unmarshal(contents, &creds); err != nil {
		return "", "", fmt.Errorf("failed to parse credentials file: %w", err)
	}

	clientID := firstNonEmpty(creds.ClientID, creds.ID)
	clientSecret := firstNonEmpty(creds.ClientSecret, creds.Secret)
	if clientID == "" || clientSecret == "" {
		return "", "", fmt.Errorf("credentials file must contain client_id/client_secret or id/secret")
	}

	return clientID, clientSecret, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
