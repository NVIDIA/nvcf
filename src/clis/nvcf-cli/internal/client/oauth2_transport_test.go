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

package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewClientOAuth2ReusesToken(t *testing.T) {
	var tokenRequests, apiRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokenRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`)
			return
		}
		apiRequests.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = fmt.Fprint(w, `{"function":{"status":"ACTIVE"}}`)
	}))
	defer server.Close()

	c, err := NewClient(&Config{
		AuthType:            AuthTypeOAuth2,
		OAuth2ClientID:      "client-id",
		OAuth2ClientSecret:  "client-secret",
		OAuth2TokenEndpoint: server.URL + "/token",
		BaseHTTPURL:         server.URL,
		BaseGRPCURL:         "127.0.0.1:0",
		DefaultTimeout:      time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for range 2 {
		if _, err := c.GetFunction(context.Background(), "function", "version"); err != nil {
			t.Fatal(err)
		}
	}
	if tokenRequests.Load() != 1 || apiRequests.Load() != 2 {
		t.Fatalf("got token requests=%d API requests=%d, want 1 and 2", tokenRequests.Load(), apiRequests.Load())
	}
}

func TestNewClientOAuth2WaitingForTokenRespectsDeadline(t *testing.T) {
	var tokenRequests, apiRequests atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokenRequests.Add(1)
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`)
			return
		}
		apiRequests.Add(1)
		_, _ = fmt.Fprint(w, `{"function":{"status":"ACTIVE"}}`)
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	c, err := NewClient(&Config{
		AuthType:            AuthTypeOAuth2,
		OAuth2ClientID:      "client-id",
		OAuth2ClientSecret:  "client-secret",
		OAuth2TokenEndpoint: server.URL + "/token",
		BaseHTTPURL:         server.URL,
		BaseGRPCURL:         "127.0.0.1:0",
		DefaultTimeout:      2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	firstResult := make(chan error, 1)
	go func() {
		_, err := c.GetFunction(context.Background(), "function", "version")
		firstResult <- err
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = c.GetFunction(ctx, "function", "version")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting request got %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waiting request outlived its deadline: %s", elapsed)
	}
	close(release)
	if err := <-firstResult; err != nil {
		t.Fatalf("first request: %v", err)
	}
	if tokenRequests.Load() != 1 || apiRequests.Load() != 1 {
		t.Fatalf("got token requests=%d API requests=%d, want 1 and 1", tokenRequests.Load(), apiRequests.Load())
	}
}

func TestNewClientOAuth2TokenRequestCancellation(t *testing.T) {
	for _, phase := range []string{"acquisition", "refresh"} {
		for _, stop := range []string{"deadline", "cancel"} {
			t.Run(phase+"/"+stop, func(t *testing.T) {
				var blockToken atomic.Bool
				blockToken.Store(phase == "acquisition")
				var tokenRequests, apiRequests atomic.Int32
				blocked := make(chan struct{}, 1)
				release := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/token" {
						tokenRequests.Add(1)
						if blockToken.Load() {
							blocked <- struct{}{}
							select {
							case <-r.Context().Done():
							case <-release:
							}
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = fmt.Fprint(w, `{"access_token":"test-token","token_type":"Bearer","expires_in":1}`)
						return
					}
					apiRequests.Add(1)
					_, _ = fmt.Fprint(w, `{"function":{"status":"ACTIVE"}}`)
				}))
				defer server.Close()
				defer close(release)

				c, err := NewClient(&Config{
					AuthType:            AuthTypeOAuth2,
					OAuth2ClientID:      "client-id",
					OAuth2ClientSecret:  "client-secret",
					OAuth2TokenEndpoint: server.URL + "/token",
					BaseHTTPURL:         server.URL,
					BaseGRPCURL:         "127.0.0.1:0",
					DefaultTimeout:      2 * time.Second,
				})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				if phase == "refresh" {
					if _, err := c.GetFunction(context.Background(), "function", "version"); err != nil {
						t.Fatalf("prime token: %v", err)
					}
					blockToken.Store(true)
				}

				ctx := context.Background()
				if stop == "cancel" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					defer cancel()
					go func() {
						<-blocked
						cancel()
					}()
				}
				start := time.Now()
				err = c.waitForFunctionDeployment(ctx, "function", "version", 40*time.Millisecond, time.Hour, time.Hour)
				want := error(context.DeadlineExceeded)
				if stop == "cancel" {
					want = context.Canceled
				}
				if !errors.Is(err, want) {
					t.Fatalf("got %v, want %v", err, want)
				}
				if elapsed := time.Since(start); elapsed > time.Second {
					t.Fatalf("token request outlived the operation: %s", elapsed)
				}
				wantTokens, wantAPI := int32(1), int32(0)
				if phase == "refresh" {
					wantTokens, wantAPI = 2, 1
				}
				if tokenRequests.Load() != wantTokens || apiRequests.Load() != wantAPI {
					t.Fatalf("got token requests=%d API requests=%d, want %d and %d", tokenRequests.Load(), apiRequests.Load(), wantTokens, wantAPI)
				}
			})
		}
	}
}
