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

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/lastcluster"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
)

// lastClusterStore is a lastcluster.Store that counts calls, can fail them,
// and can block Get while ignoring the context.
type lastClusterStore struct {
	mu         sync.Mutex
	values     map[string]string
	gets, sets int
	err        error
	block      chan struct{}
}

func (s *lastClusterStore) Get(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	s.gets++
	value, ok := s.values[key]
	s.mu.Unlock()
	if s.block != nil {
		<-s.block
	}
	return value, ok, s.err
}

func (s *lastClusterStore) Set(_ context.Context, key, value string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sets++
	if s.err == nil {
		s.values[key] = value
	}
	return s.err
}

func (s *lastClusterStore) snapshot() (gets, sets int, values map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values = map[string]string{}
	for k, v := range s.values {
		values[k] = v
	}
	return s.gets, s.sets, values
}

var lastClusterPaths = []string{"Complete", "Stream", "Proxy"}

func lastClusterReqCtx(source string) *requestctx.RequestContext {
	return &requestctx.RequestContext{
		RoutingKey:       "fn-lc",
		Model:            "company/model",
		SessionSource:    source,
		CacheAffinityKey: "mt:v1:session:lc-hash",
	}
}

func lastClusterKey(reqCtx *requestctx.RequestContext) string {
	return lastcluster.Key(reqCtx.RoutingKey, reqCtx.Model, reqCtx.CacheAffinityKey)
}

// newLastClusterProvider returns a provider whose Stargate answers with
// status and x-stargate-cluster-id: cluster-b, or with transportErr, and
// records each last-cluster hint it receives. A nil store disables hints.
func newLastClusterProvider(
	t *testing.T,
	store *lastClusterStore,
	status int,
	transportErr error,
) (*StargateProvider, *lastcluster.Tracker, func() []string) {
	t.Helper()
	p, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)
	var tracker *lastcluster.Tracker
	if store != nil {
		tracker = lastcluster.NewTracker(store, lastcluster.Options{TTL: time.Minute, LookupTimeout: 20 * time.Millisecond})
	}
	p.SetLastClusterTracker(tracker)

	body := sseChatBody(t, models.ChatCompletionChunk{
		ID:     "chatcmpl-lc",
		Object: models.ObjectChatCompletionChunk,
		Choices: []models.ChatCompletionChunkChoice{{
			Delta:        models.ChatCompletionChunkDelta{Content: ptr.To("ok")},
			FinishReason: ptr.To(models.FinishReasonStop),
		}},
	})
	var mu sync.Mutex
	var hints []string
	p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		hints = append(hints, r.Header.Get(headerStargateLastClusterID))
		mu.Unlock()
		if transportErr != nil {
			return nil, transportErr
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{headerContentType: {contentTypeSSE}, headerStargateClusterID: {"cluster-b"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}
	return p, tracker, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), hints...)
	}
}

func lastClusterChat(reqCtx *requestctx.RequestContext) *NormalizedRequest {
	return &NormalizedRequest{ChatRequest: &models.ChatCompletionRequest{
		Model:    reqCtx.Model,
		Messages: &[]models.ChatMessage{{Role: models.ChatCompletionRoleUser, Content: models.SingleTextContent("hi")}},
	}}
}

// lastClusterCall runs one request through the named provider path and
// returns everything the client would see.
func lastClusterCall(
	t *testing.T,
	p *StargateProvider,
	path string,
	reqCtx *requestctx.RequestContext,
	inbound http.Header,
) (string, error) {
	t.Helper()
	ctx := context.Background()
	chat := lastClusterChat(reqCtx)
	switch path {
	case "Complete":
		response, err := p.Complete(ctx, reqCtx, chat)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(response)
		require.NoError(t, err)
		return string(raw), nil
	case "Stream":
		events, err := p.Stream(ctx, reqCtx, chat)
		if err != nil {
			return "", err
		}
		var out strings.Builder
		for event := range events {
			raw, err := json.Marshal(event.Chunk)
			require.NoError(t, err)
			fmt.Fprintf(&out, "%s err=%v\n", raw, event.Err)
		}
		return out.String(), nil
	default:
		resp, err := p.Proxy(ctx, reqCtx, &ProxyRequest{
			Method: http.MethodPost,
			Path:   "/v1/responses",
			Header: inbound,
			Body:   io.NopCloser(strings.NewReader(`{}`)),
		})
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return fmt.Sprintf("%d %s", resp.StatusCode, raw), nil
	}
}

// TestLastClusterRoundTrip covers every session source on every path: an
// eligible session's second request carries the hint, the same session with
// another model or routing key does not, and payload sessions never touch the
// store.
func TestLastClusterRoundTrip(t *testing.T) {
	for _, path := range lastClusterPaths {
		for _, source := range []string{
			requestctx.SessionSourcePromptCacheKey,
			requestctx.SessionSourceConversationID,
			requestctx.SessionSourceHeader,
			requestctx.SessionSourceClaudeCodeHeader,
			requestctx.SessionSourcePayload,
		} {
			t.Run(path+"/"+source, func(t *testing.T) {
				store := &lastClusterStore{values: map[string]string{}}
				p, tracker, hints := newLastClusterProvider(t, store, http.StatusOK, nil)
				reqCtx := lastClusterReqCtx(source)
				otherModel := lastClusterReqCtx(source)
				otherModel.Model = "company/other-model"
				otherRoutingKey := lastClusterReqCtx(source)
				otherRoutingKey.RoutingKey = "fn-other"

				for _, rc := range []*requestctx.RequestContext{reqCtx, reqCtx, otherModel, otherRoutingKey} {
					_, err := lastClusterCall(t, p, path, rc, nil)
					require.NoError(t, err)
					tracker.Drain(context.Background())
				}

				if source != requestctx.SessionSourcePayload {
					require.Equal(t, []string{"", "cluster-b", "", ""}, hints())
					return
				}
				require.Equal(t, []string{"", "", "", ""}, hints())
				gets, sets, _ := store.snapshot()
				require.Zero(t, gets)
				require.Zero(t, sets)
			})
		}
	}
}

func TestLastClusterLookupTimeoutProceeds(t *testing.T) {
	for _, path := range lastClusterPaths {
		t.Run(path, func(t *testing.T) {
			store := &lastClusterStore{values: map[string]string{}, block: make(chan struct{})}
			defer close(store.block)
			p, _, hints := newLastClusterProvider(t, store, http.StatusOK, nil)

			start := time.Now()
			_, err := lastClusterCall(t, p, path, lastClusterReqCtx(requestctx.SessionSourceHeader), nil)
			require.NoError(t, err)
			// The store blocks until cleanup; the slack over the 20ms lookup
			// timeout keeps loaded -race runs stable.
			require.Less(t, time.Since(start), 200*time.Millisecond)
			require.Equal(t, []string{""}, hints())
		})
	}
}

// Complete and Stream build outbound headers from scratch, so only Proxy can
// carry a client-supplied hint.
func TestLastClusterProxyStripsInboundHint(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		source  string
		want    string
	}{
		{name: "disabled", source: requestctx.SessionSourceHeader, want: ""},
		{name: "enabled", enabled: true, source: requestctx.SessionSourceHeader, want: "cluster-b"},
		{name: "enabled/payload session", enabled: true, source: requestctx.SessionSourcePayload, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reqCtx := lastClusterReqCtx(tc.source)
			var store *lastClusterStore
			if tc.enabled {
				store = &lastClusterStore{values: map[string]string{lastClusterKey(reqCtx): "cluster-b"}}
			}
			p, _, hints := newLastClusterProvider(t, store, http.StatusOK, nil)

			_, err := lastClusterCall(t, p, "Proxy", reqCtx, http.Header{headerStargateLastClusterID: {"client-forged"}})
			require.NoError(t, err)
			require.Equal(t, []string{tc.want}, hints())
		})
	}
}

func TestLastClusterNoWriteOnFailure(t *testing.T) {
	for _, path := range lastClusterPaths {
		for name, upstream := range map[string]struct {
			status int
			err    error
		}{
			"503":       {status: http.StatusServiceUnavailable},
			"transport": {err: errors.New("connection reset")},
		} {
			t.Run(path+"/"+name, func(t *testing.T) {
				store := &lastClusterStore{values: map[string]string{}}
				p, tracker, _ := newLastClusterProvider(t, store, upstream.status, upstream.err)

				_, _ = lastClusterCall(t, p, path, lastClusterReqCtx(requestctx.SessionSourceHeader), nil)
				tracker.Drain(context.Background())
				_, sets, _ := store.snapshot()
				require.Zero(t, sets)
			})
		}
	}
}

func TestLastClusterWritesBeforeStreamEnds(t *testing.T) {
	for _, path := range []string{"Stream", "Proxy"} {
		t.Run(path, func(t *testing.T) {
			store := &lastClusterStore{values: map[string]string{}}
			p, tracker, _ := newLastClusterProvider(t, store, http.StatusOK, nil)
			body, bodyWriter := io.Pipe()
			defer bodyWriter.Close()
			p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{headerContentType: {contentTypeSSE}, headerStargateClusterID: {"cluster-b"}},
					Body:       body,
					Request:    r,
				}, nil
			})}
			ctx := context.Background()
			reqCtx := lastClusterReqCtx(requestctx.SessionSourcePromptCacheKey)

			if path == "Stream" {
				events, err := p.Stream(ctx, reqCtx, lastClusterChat(reqCtx))
				require.NoError(t, err)
				defer func() {
					_ = bodyWriter.Close()
					for range events {
					}
				}()
			} else {
				resp, err := p.Proxy(ctx, reqCtx, &ProxyRequest{Method: http.MethodPost, Path: "/v1/responses", Body: http.NoBody})
				require.NoError(t, err)
				defer resp.Body.Close()
			}

			// The upstream body is still open, so the write cannot wait for it.
			tracker.Drain(ctx)
			_, _, values := store.snapshot()
			require.Equal(t, "cluster-b", values[lastClusterKey(reqCtx)])
		})
	}
}

func TestLastClusterStoreErrorsTransparent(t *testing.T) {
	for _, path := range lastClusterPaths {
		t.Run(path, func(t *testing.T) {
			reqCtx := lastClusterReqCtx(requestctx.SessionSourceHeader)
			healthy, _, _ := newLastClusterProvider(t, nil, http.StatusOK, nil)
			want, wantErr := lastClusterCall(t, healthy, path, reqCtx, nil)
			require.NoError(t, wantErr)

			store := &lastClusterStore{values: map[string]string{}, err: errors.New("olric unavailable")}
			failing, tracker, _ := newLastClusterProvider(t, store, http.StatusOK, nil)
			got, err := lastClusterCall(t, failing, path, reqCtx, nil)
			tracker.Drain(context.Background())

			require.NoError(t, err)
			require.Equal(t, want, got)
			gets, sets, _ := store.snapshot()
			require.Equal(t, 1, gets)
			require.Equal(t, 1, sets)
		})
	}
}
