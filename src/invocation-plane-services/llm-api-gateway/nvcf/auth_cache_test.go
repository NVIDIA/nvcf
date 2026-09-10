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

package nvcf

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeAuthClient struct {
	calls   atomic.Int64
	respond func(token, routingKey string) (*InvocationAuthResponse, error)
	// block, when non-nil, is closed by the test to release in-flight calls.
	block chan struct{}
}

func (f *fakeAuthClient) AuthorizeInvocation(
	_ context.Context,
	token string,
	routingKey string,
) (*InvocationAuthResponse, error) {
	f.calls.Add(1)
	if f.block != nil {
		<-f.block
	}
	if f.respond != nil {
		return f.respond(token, routingKey)
	}
	return authResponseFor(routingKey), nil
}

func (f *fakeAuthClient) Close() error { return nil }

func authResponseFor(routingKey string) *InvocationAuthResponse {
	priority := uint32(2)
	return &InvocationAuthResponse{
		RoutingKey:   routingKey,
		ClientAuthID: "client-auth-id",
		ProjectID:    "project-id",
		AuthContext:  map[string]string{"ncaId": "nca-" + routingKey},
		RateLimitKey: "nca-" + routingKey,
		ModelSpecs: map[string]ModelSpec{
			"model": {URIs: []string{"uri-1"}, TokenRateLimit: "10", RoutingMethod: "rr"},
		},
		Priority: &priority,
	}
}

func TestCachedClient_AuthorizeInvocation_CacheBehavior(t *testing.T) {
	errUpstream := errors.New("upstream failure")
	errDenied := status.Error(codes.PermissionDenied, "permission denied")

	tests := []struct {
		name string
		run  func(t *testing.T, fake *fakeAuthClient, client Client)
		want int64 // expected upstream calls
	}{
		{
			name: "hit within ttl makes one upstream call",
			run: func(t *testing.T, _ *fakeAuthClient, client Client) {
				for range 3 {
					if _, err := client.AuthorizeInvocation(context.Background(), "token", "fn"); err != nil {
						t.Fatalf("unexpected error: %v", err)
					}
				}
			},
			want: 1,
		},
		{
			name: "different routing keys are separate entries",
			run: func(t *testing.T, _ *fakeAuthClient, client Client) {
				for _, fn := range []string{"fn-a", "fn-b", "fn-a"} {
					if _, err := client.AuthorizeInvocation(context.Background(), "token", fn); err != nil {
						t.Fatalf("unexpected error: %v", err)
					}
				}
			},
			want: 2,
		},
		{
			name: "different tokens are separate entries",
			run: func(t *testing.T, _ *fakeAuthClient, client Client) {
				for _, token := range []string{"token-a", "token-b", "token-a"} {
					if _, err := client.AuthorizeInvocation(context.Background(), token, "fn"); err != nil {
						t.Fatalf("unexpected error: %v", err)
					}
				}
			},
			want: 2,
		},
		{
			name: "errors are not cached",
			run: func(t *testing.T, fake *fakeAuthClient, client Client) {
				fake.respond = func(string, string) (*InvocationAuthResponse, error) {
					return nil, errUpstream
				}
				if _, err := client.AuthorizeInvocation(context.Background(), "token", "fn"); !errors.Is(err, errUpstream) {
					t.Fatalf("want upstream error, got %v", err)
				}
				fake.respond = nil
				if _, err := client.AuthorizeInvocation(context.Background(), "token", "fn"); err != nil {
					t.Fatalf("unexpected error after recovery: %v", err)
				}
			},
			want: 2,
		},
		{
			name: "denials are not cached",
			run: func(t *testing.T, fake *fakeAuthClient, client Client) {
				fake.respond = func(string, string) (*InvocationAuthResponse, error) {
					return nil, errDenied
				}
				if _, err := client.AuthorizeInvocation(context.Background(), "token", "fn"); status.Code(err) != codes.PermissionDenied {
					t.Fatalf("want PermissionDenied, got %v", err)
				}
				fake.respond = nil
				if _, err := client.AuthorizeInvocation(context.Background(), "token", "fn"); err != nil {
					t.Fatalf("unexpected error after authorization granted: %v", err)
				}
			},
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeAuthClient{}
			client := newCachedClient(fake, time.Minute, authCacheMaxEntries)
			tt.run(t, fake, client)
			if got := fake.calls.Load(); got != tt.want {
				t.Fatalf("upstream calls = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCachedClient_AuthorizeInvocation_ExpiryRefetches(t *testing.T) {
	fake := &fakeAuthClient{}
	client := newCachedClient(fake, 10*time.Millisecond, authCacheMaxEntries)

	if _, err := client.AuthorizeInvocation(context.Background(), "token", "fn"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := client.AuthorizeInvocation(context.Background(), "token", "fn"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := fake.calls.Load(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2", got)
	}
}

func TestCachedClient_AuthorizeInvocation_ConcurrentMissesCollapse(t *testing.T) {
	fake := &fakeAuthClient{block: make(chan struct{})}
	client := newCachedClient(fake, time.Minute, authCacheMaxEntries)

	const concurrency = 16
	var wg sync.WaitGroup
	errs := make(chan error, concurrency)
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := client.AuthorizeInvocation(context.Background(), "token", "fn")
			errs <- err
		}()
	}

	// Give the goroutines time to pile onto the in-flight load, then release.
	time.Sleep(50 * time.Millisecond)
	close(fake.block)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if got := fake.calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
}

func TestCachedClient_AuthorizeInvocation_HitsShareNoMutableState(t *testing.T) {
	fake := &fakeAuthClient{}
	client := newCachedClient(fake, time.Minute, authCacheMaxEntries)

	first, err := client.AuthorizeInvocation(context.Background(), "token", "fn")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	first.AuthContext["ncaId"] = "tampered"
	first.ModelSpecs["model"].URIs[0] = "tampered"
	*first.Priority = 99

	second, err := client.AuthorizeInvocation(context.Background(), "token", "fn")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if second.AuthContext["ncaId"] != "nca-fn" {
		t.Fatalf("AuthContext leaked mutation: %q", second.AuthContext["ncaId"])
	}
	if second.ModelSpecs["model"].URIs[0] != "uri-1" {
		t.Fatalf("ModelSpecs leaked mutation: %q", second.ModelSpecs["model"].URIs[0])
	}
	if *second.Priority != 2 {
		t.Fatalf("Priority leaked mutation: %d", *second.Priority)
	}
}

func TestCachedClient_AuthorizeInvocation_ZeroTTLIsPassThrough(t *testing.T) {
	fake := &fakeAuthClient{}
	client := newCachedClient(fake, 0, authCacheMaxEntries)

	for range 3 {
		if _, err := client.AuthorizeInvocation(context.Background(), "token", "fn"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if got := fake.calls.Load(); got != 3 {
		t.Fatalf("upstream calls = %d, want 3", got)
	}
}

func TestCachedClient_AuthorizeInvocation_EvictionKeepsCacheBounded(t *testing.T) {
	const maxEntries = 8
	fake := &fakeAuthClient{}
	client := newCachedClient(fake, time.Minute, maxEntries)

	for i := range maxEntries * 3 {
		routingKey := fmt.Sprintf("fn-%d", i)
		if _, err := client.AuthorizeInvocation(context.Background(), "token", routingKey); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	cached, ok := client.(*cachedClient)
	if !ok {
		t.Fatalf("expected *cachedClient, got %T", client)
	}
	// Run pending maintenance so the size estimate reflects eviction.
	cached.cache.CleanUp()
	if size := cached.cache.EstimatedSize(); size > maxEntries {
		t.Fatalf("cache size = %d, want <= %d", size, maxEntries)
	}
}
