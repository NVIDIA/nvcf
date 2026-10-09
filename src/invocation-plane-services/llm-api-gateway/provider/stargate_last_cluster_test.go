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
	"net/http/httptest"
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

// lastClusterStore is an in-memory lastcluster.Store that counts calls, can
// inject errors, and can block Get while ignoring the context.
type lastClusterStore struct {
	mu       sync.Mutex
	values   map[string]string
	gets     int
	sets     int
	getErr   error
	setErr   error
	blockGet chan struct{}
}

func newLastClusterStore() *lastClusterStore {
	return &lastClusterStore{values: map[string]string{}}
}

func (s *lastClusterStore) Get(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	s.gets++
	block, err := s.blockGet, s.getErr
	value, ok := s.values[key]
	s.mu.Unlock()
	if block != nil {
		<-block
	}
	if err != nil {
		return "", false, err
	}
	return value, ok, nil
}

func (s *lastClusterStore) Set(_ context.Context, key, value string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sets++
	if s.setErr != nil {
		return s.setErr
	}
	s.values[key] = value
	return nil
}

func (s *lastClusterStore) get(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key]
}

func (s *lastClusterStore) counts() (gets, sets int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets, s.sets
}

const lastClusterTestModel = "company/model"

func lastClusterReqCtx(source string) *requestctx.RequestContext {
	return &requestctx.RequestContext{
		RequestID:        "req-lc",
		RoutingKey:       "fn-lc",
		Model:            lastClusterTestModel,
		SessionID:        "session-1",
		SessionSource:    source,
		CacheAffinityKey: "mt:v1:session:lc-hash",
	}
}

func lastClusterKey(reqCtx *requestctx.RequestContext) string {
	return lastcluster.Key(reqCtx.RoutingKey, reqCtx.Model, reqCtx.CacheAffinityKey)
}

func lastClusterChatRequest() *NormalizedRequest {
	return &NormalizedRequest{
		ChatRequest: &models.ChatCompletionRequest{
			Model: lastClusterTestModel,
			Messages: &[]models.ChatMessage{
				{Role: models.ChatCompletionRoleUser, Content: models.SingleTextContent("hi")},
			},
		},
	}
}

func lastClusterSSE(t *testing.T) string {
	return sseChatBody(t, models.ChatCompletionChunk{
		ID:     "chatcmpl-lc",
		Object: models.ObjectChatCompletionChunk,
		Model:  lastClusterTestModel,
		Choices: []models.ChatCompletionChunkChoice{{
			Delta:        models.ChatCompletionChunkDelta{Content: ptr.To("ok")},
			FinishReason: ptr.To(models.FinishReasonStop),
		}},
	})
}

func newLastClusterTracker(store lastcluster.Store) *lastcluster.Tracker {
	return lastcluster.NewTracker(store, lastcluster.Options{TTL: time.Minute, LookupTimeout: 20 * time.Millisecond})
}

// lastClusterUpstream answers every request with status and the cluster ID
// header, and records the last-cluster hint each request carried.
type lastClusterUpstream struct {
	mu        sync.Mutex
	hints     []string
	status    int
	clusterID string
}

func (u *lastClusterUpstream) roundTrip(t *testing.T) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		u.mu.Lock()
		u.hints = append(u.hints, r.Header.Get(headerStargateLastClusterID))
		status, clusterID := u.status, u.clusterID
		u.mu.Unlock()
		header := http.Header{headerContentType: []string{contentTypeSSE}}
		if clusterID != "" {
			header.Set(headerStargateClusterID, clusterID)
		}
		body := lastClusterSSE(t)
		if status >= http.StatusBadRequest {
			header.Set(headerContentType, contentTypeJSON)
			body = `{"error":{"message":"upstream failed"}}`
		}
		return &http.Response{
			StatusCode: status,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	}
}

func (u *lastClusterUpstream) lastHint() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.hints) == 0 {
		return "<none>"
	}
	return u.hints[len(u.hints)-1]
}

// lastClusterCall runs one request through the named provider path and
// closes any response body.
func lastClusterCall(
	ctx context.Context,
	p *StargateProvider,
	path string,
	reqCtx *requestctx.RequestContext,
	inbound http.Header,
) (int, error) {
	switch path {
	case "Complete":
		_, err := p.Complete(ctx, reqCtx, lastClusterChatRequest())
		return 0, err
	case "Stream":
		events, err := p.Stream(ctx, reqCtx, lastClusterChatRequest())
		if err != nil {
			return 0, err
		}
		for range events {
		}
		return 0, nil
	default:
		resp, err := p.Proxy(ctx, reqCtx, &ProxyRequest{
			Method: http.MethodPost,
			Path:   "/v1/responses",
			Header: inbound,
			Body:   io.NopCloser(strings.NewReader(`{}`)),
		})
		if err != nil {
			return 0, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, nil
	}
}

var lastClusterPaths = []string{"Complete", "Stream", "Proxy"}

func TestLastClusterRoundTrip(t *testing.T) {
	for _, path := range lastClusterPaths {
		for _, source := range []string{
			requestctx.SessionSourcePromptCacheKey,
			requestctx.SessionSourceConversationID,
			requestctx.SessionSourceHeader,
		} {
			t.Run(path+"/"+source, func(t *testing.T) {
				ctx := context.Background()
				store := newLastClusterStore()
				tracker := newLastClusterTracker(store)
				upstream := &lastClusterUpstream{status: http.StatusOK, clusterID: "cluster-b"}
				p, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
				require.NoError(t, err)
				p.client = &http.Client{Transport: upstream.roundTrip(t)}
				p.SetLastClusterTracker(tracker)
				reqCtx := lastClusterReqCtx(source)

				_, err = lastClusterCall(ctx, p, path, reqCtx, nil)
				require.NoError(t, err)
				require.Empty(t, upstream.lastHint(), "first request must carry no hint")
				tracker.Drain(ctx)
				require.Equal(t, "cluster-b", store.get(lastClusterKey(reqCtx)))

				_, err = lastClusterCall(ctx, p, path, reqCtx, nil)
				require.NoError(t, err)
				require.Equal(t, "cluster-b", upstream.lastHint())
			})
		}
	}
}

func TestLastClusterInboundHintStripped(t *testing.T) {
	for _, path := range lastClusterPaths {
		for _, enabled := range []bool{false, true} {
			name := path + "/disabled"
			if enabled {
				name = path + "/enabled"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				upstream := &lastClusterUpstream{status: http.StatusOK}
				p, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
				require.NoError(t, err)
				p.client = &http.Client{Transport: upstream.roundTrip(t)}
				reqCtx := lastClusterReqCtx(requestctx.SessionSourceHeader)
				want := ""
				if enabled {
					store := newLastClusterStore()
					store.values[lastClusterKey(reqCtx)] = "cluster-b"
					p.SetLastClusterTracker(newLastClusterTracker(store))
					want = "cluster-b"
				}
				inbound := http.Header{}
				inbound.Set(headerStargateLastClusterID, "client-forged")

				_, err = lastClusterCall(ctx, p, path, reqCtx, inbound)
				require.NoError(t, err)
				require.Equal(t, want, upstream.lastHint())

				// Ineligible session: the forged value is still dropped.
				_, err = lastClusterCall(ctx, p, path, lastClusterReqCtx(requestctx.SessionSourcePayload), inbound)
				require.NoError(t, err)
				require.Empty(t, upstream.lastHint())
			})
		}
	}
}

func TestLastClusterDisabledProxyHeadersUnchanged(t *testing.T) {
	var got []http.Header
	p, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)
	p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = append(got, r.Header.Clone())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{headerStargateClusterID: []string{"cluster-b"}},
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Request:    r,
		}, nil
	})}
	reqCtx := lastClusterReqCtx(requestctx.SessionSourcePromptCacheKey)
	baseline := http.Header{"X-Client": []string{"1"}}
	withHint := baseline.Clone()
	withHint.Set(headerStargateLastClusterID, "client-forged")

	for _, inbound := range []http.Header{baseline, withHint, baseline} {
		_, err := lastClusterCall(context.Background(), p, "Proxy", reqCtx, inbound)
		require.NoError(t, err)
	}
	require.Len(t, got, 3)
	require.Equal(t, got[0], got[1], "inbound hint must only be stripped")
	require.Equal(t, got[0], got[2], "a disabled gateway must not learn from responses")
}

func TestLastClusterPayloadSessionSkipped(t *testing.T) {
	for _, path := range lastClusterPaths {
		t.Run(path, func(t *testing.T) {
			ctx := context.Background()
			store := newLastClusterStore()
			tracker := newLastClusterTracker(store)
			upstream := &lastClusterUpstream{status: http.StatusOK, clusterID: "cluster-b"}
			p, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
			require.NoError(t, err)
			p.client = &http.Client{Transport: upstream.roundTrip(t)}
			p.SetLastClusterTracker(tracker)

			for i := 0; i < 2; i++ {
				_, err := lastClusterCall(ctx, p, path, lastClusterReqCtx(requestctx.SessionSourcePayload), nil)
				require.NoError(t, err)
				require.Empty(t, upstream.lastHint())
			}
			tracker.Drain(ctx)
			gets, sets := store.counts()
			require.Zero(t, gets)
			require.Zero(t, sets)
		})
	}
}

func TestLastClusterKeyIsolation(t *testing.T) {
	ctx := context.Background()
	store := newLastClusterStore()
	upstream := &lastClusterUpstream{status: http.StatusOK}
	p, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
	require.NoError(t, err)
	p.client = &http.Client{Transport: upstream.roundTrip(t)}
	p.SetLastClusterTracker(newLastClusterTracker(store))
	seeded := lastClusterReqCtx(requestctx.SessionSourceHeader)
	store.values[lastClusterKey(seeded)] = "cluster-b"

	otherModel := lastClusterReqCtx(requestctx.SessionSourceHeader)
	otherModel.Model = "company/other-model"
	otherRoutingKey := lastClusterReqCtx(requestctx.SessionSourceHeader)
	otherRoutingKey.RoutingKey = "fn-other"

	for _, reqCtx := range []*requestctx.RequestContext{otherModel, otherRoutingKey} {
		_, err := lastClusterCall(ctx, p, "Proxy", reqCtx, nil)
		require.NoError(t, err)
		require.Empty(t, upstream.lastHint())
	}
	_, err = lastClusterCall(ctx, p, "Proxy", seeded, nil)
	require.NoError(t, err)
	require.Equal(t, "cluster-b", upstream.lastHint())
}

func TestLastClusterNoWriteOnFailure(t *testing.T) {
	for _, path := range lastClusterPaths {
		t.Run(path, func(t *testing.T) {
			ctx := context.Background()
			store := newLastClusterStore()
			tracker := newLastClusterTracker(store)
			reqCtx := lastClusterReqCtx(requestctx.SessionSourceHeader)
			store.values[lastClusterKey(reqCtx)] = "cluster-a"
			p, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
			require.NoError(t, err)
			p.SetLastClusterTracker(tracker)

			for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusBadRequest} {
				upstream := &lastClusterUpstream{status: status, clusterID: "cluster-c"}
				p.client = &http.Client{Transport: upstream.roundTrip(t)}
				_, _ = lastClusterCall(ctx, p, path, reqCtx, nil)
				require.Equal(t, "cluster-a", upstream.lastHint())
			}
			p.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("connection reset")
			})}
			_, err = lastClusterCall(ctx, p, path, reqCtx, nil)
			require.Error(t, err)

			tracker.Drain(ctx)
			require.Equal(t, "cluster-a", store.get(lastClusterKey(reqCtx)))
			_, sets := store.counts()
			require.Zero(t, sets)
		})
	}
}

func TestLastClusterStreamWritesBeforeEnd(t *testing.T) {
	for _, path := range []string{"Stream", "Proxy"} {
		t.Run(path, func(t *testing.T) {
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(headerContentType, contentTypeSSE)
				w.Header().Set(headerStargateClusterID, "cluster-b")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[]}\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-release:
				case <-r.Context().Done():
				}
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer server.Close()
			var releaseOnce sync.Once
			releaseStream := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseStream()

			ctx := context.Background()
			store := newLastClusterStore()
			tracker := newLastClusterTracker(store)
			p, err := NewStargateProvider(config.StargateConfig{URL: server.URL})
			require.NoError(t, err)
			p.SetLastClusterTracker(tracker)
			reqCtx := lastClusterReqCtx(requestctx.SessionSourcePromptCacheKey)

			var body io.Closer
			if path == "Stream" {
				events, err := p.Stream(ctx, reqCtx, lastClusterChatRequest())
				require.NoError(t, err)
				<-events
				defer func() {
					releaseStream()
					for range events {
					}
				}()
			} else {
				resp, err := p.Proxy(ctx, reqCtx, &ProxyRequest{
					Method: http.MethodPost,
					Path:   "/v1/responses",
					Body:   io.NopCloser(strings.NewReader(`{}`)),
				})
				require.NoError(t, err)
				body = resp.Body
				defer body.Close()
			}

			require.Eventually(t, func() bool {
				return store.get(lastClusterKey(reqCtx)) == "cluster-b"
			}, 2*time.Second, 5*time.Millisecond, "write must land while the stream is still open")
		})
	}
}

func TestLastClusterLookupTimeoutProceeds(t *testing.T) {
	for _, path := range lastClusterPaths {
		t.Run(path, func(t *testing.T) {
			store := newLastClusterStore()
			store.blockGet = make(chan struct{})
			defer close(store.blockGet)
			p, err := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
			require.NoError(t, err)
			p.SetLastClusterTracker(newLastClusterTracker(store))
			var arrived time.Time
			upstream := &lastClusterUpstream{status: http.StatusOK}
			inner := upstream.roundTrip(t)
			p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				arrived = time.Now()
				return inner(r)
			})}

			start := time.Now()
			_, err = lastClusterCall(context.Background(), p, path, lastClusterReqCtx(requestctx.SessionSourceHeader), nil)
			require.NoError(t, err)
			require.Less(t, arrived.Sub(start), 25*time.Millisecond)
			require.Empty(t, upstream.lastHint())
		})
	}
}

func TestLastClusterStoreErrorsTransparent(t *testing.T) {
	for _, path := range lastClusterPaths {
		t.Run(path, func(t *testing.T) {
			ctx := context.Background()
			// run returns everything the client would see: the error, plus the
			// proxy status and body, the aggregated completion, or every stream
			// event.
			run := func(tracker *lastcluster.Tracker) (status int, err error, output string) {
				p, newErr := NewStargateProvider(config.StargateConfig{URL: "http://stargate.example"})
				require.NoError(t, newErr)
				p.SetLastClusterTracker(tracker)
				upstream := &lastClusterUpstream{status: http.StatusOK, clusterID: "cluster-b"}
				p.client = &http.Client{Transport: upstream.roundTrip(t)}
				reqCtx := lastClusterReqCtx(requestctx.SessionSourceHeader)
				defer tracker.Drain(ctx)

				switch path {
				case "Complete":
					response, err := p.Complete(ctx, reqCtx, lastClusterChatRequest())
					raw, marshalErr := json.Marshal(response)
					require.NoError(t, marshalErr)
					return 0, err, string(raw)
				case "Stream":
					events, err := p.Stream(ctx, reqCtx, lastClusterChatRequest())
					if err != nil {
						return 0, err, ""
					}
					var seen []string
					for event := range events {
						raw, marshalErr := json.Marshal(event.Chunk)
						require.NoError(t, marshalErr)
						seen = append(seen, fmt.Sprintf("%s err=%v", raw, event.Err))
					}
					return 0, nil, strings.Join(seen, "\n")
				default:
					resp, err := p.Proxy(ctx, reqCtx, &ProxyRequest{
						Method: http.MethodPost, Path: "/v1/responses", Body: io.NopCloser(strings.NewReader(`{}`)),
					})
					require.NoError(t, err)
					raw, _ := io.ReadAll(resp.Body)
					_ = resp.Body.Close()
					return resp.StatusCode, nil, string(raw)
				}
			}

			store := newLastClusterStore()
			store.getErr = errors.New("olric unavailable")
			store.setErr = errors.New("olric unavailable")
			gotStatus, gotErr, gotOutput := run(newLastClusterTracker(store))
			wantStatus, wantErr, wantOutput := run(nil)

			require.Equal(t, wantStatus, gotStatus)
			require.Equal(t, wantErr, gotErr)
			require.NotEmpty(t, wantOutput)
			require.Equal(t, wantOutput, gotOutput)
			gets, sets := store.counts()
			require.Equal(t, 1, gets)
			require.Equal(t, 1, sets)
		})
	}
}
