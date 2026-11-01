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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	client "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/fnds/client"
	types "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/fnds/common/core/types"
)

// MockTokenFetcher implements the TokenFetcher interface
type MockTokenFetcher struct {
	clientID     string
	clientSecret string
	token        string
}

// NewMockTokenFetcher creates a new MockTokenFetcher
func NewMockTokenFetcher(clientID, clientSecret string) *MockTokenFetcher {
	return &MockTokenFetcher{
		clientID:     clientID,
		clientSecret: clientSecret,
		token:        "mock-token", // Default mock token if no OAuth is used
	}
}

// FetchToken implements TokenFetcher interface
func (m *MockTokenFetcher) FetchToken(ctx context.Context) (string, error) {
	if m.clientID == "" || m.clientSecret == "" {
		return m.token, nil
	}

	// Create the form data for the token request
	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", m.clientID)
	data.Set("client_secret", m.clientSecret)

	// Create the request
	req, err := http.NewRequestWithContext(ctx, "POST", "https://your-oauth-server/oauth/token", strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	// Make the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	// Read and parse the response
	var tokenResponse struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oauth request failed with status %d", resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(&tokenResponse); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	return tokenResponse.AccessToken, nil
}

func prettyPrint(data interface{}) {
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		log.Fatalf("error marshaling JSON: %v", err)
	}
	log.Printf("%s", jsonData)
}

func main() {
	var (
		baseUrl           = "http://localhost:8080"
		ncaId             = "0b0125ee-43cf-4e3f-8f1a-f9933ba9ad6c"
		functionVersionId = uuid.MustParse("0b0125ee-43cf-4e3f-8f1a-f9933ba9ad6c")
		instanceId        = "93787d8a-0a60-4f44-b121-572d2fae61c6"
		timestamp         = time.Now()
	)

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	// Create a mock token fetcher
	// For real OAuth, replace empty strings with actual clientID and clientSecret
	tokenFetcher := NewMockTokenFetcher("", "")

	// Create new FnDS client with context and token fetcher
	fndsClient := client.NewFndsClient(baseUrl, ncaId, tokenFetcher)

	var result interface{}
	var err error

	// Create a new event
	eventData, err := types.NewStageTransitionEvent(
		fndsClient.NcaId,
		functionVersionId,
		functionVersionId,
		instanceId,
		"pending",
		"string",
		timestamp,
		json.RawMessage(`{"key": "value"}`),
	)
	if err != nil {
		cancel()
		log.Fatalf("error: %v", err)
	}

	if err := fndsClient.CreateEvent(ctx, eventData); err != nil {
		cancel()
		log.Fatalf("%s", err)
	} else {
		log.Printf("event created for function %v and instance %v", functionVersionId, instanceId)
	}

	// List all instances by function id
	result, err = fndsClient.ListInstances(ctx, functionVersionId)
	if err != nil {
		cancel()
		log.Fatalf("error: %v", err)
	} else {
		log.Printf("instances: \n")
		prettyPrint(result)
	}

	// Get all events by function id and instance id
	result, err = fndsClient.GetEvents(ctx, functionVersionId, instanceId)
	if err != nil {
		cancel()
		log.Fatalf("error: %v", err)
	} else {
		log.Printf("events: \n")
		prettyPrint(result)
	}

	// Get single event by function id, instance id, and event type
	result, err = fndsClient.GetEventByEventType(ctx, functionVersionId, instanceId, "pending")
	if err != nil {
		cancel()
		log.Fatalf("error: %v", err)
	} else {
		prettyPrint(result)
	}

	// Delete events by instance id
	err = fndsClient.DeleteInstanceEvents(ctx, functionVersionId, instanceId)
	if err != nil {
		cancel()
		log.Fatalf("error: %v", err)
	} else {
		log.Printf("events deleted for function %v, instance %v", functionVersionId, instanceId)
	}

	// Create another event
	if err := fndsClient.CreateEvent(ctx, eventData); err != nil {
		cancel()
		log.Fatalf("%s", err)
	} else {
		log.Printf("event created for function %v and instance %v", functionVersionId, instanceId)
	}

	// Delete events by function version id
	err = fndsClient.DeleteFunctionVersionEvents(ctx, functionVersionId)
	if err != nil {
		cancel()
		log.Fatalf("error: %v", err)
	} else {
		log.Printf("events deleted for function %v", functionVersionId)
	}

	// Get all stats
	result, err = fndsClient.GetStats(ctx, functionVersionId)
	if err != nil {
		cancel()
		log.Fatalf("error: %v", err)
	} else {
		prettyPrint(result)
	}

	cancel()
}
