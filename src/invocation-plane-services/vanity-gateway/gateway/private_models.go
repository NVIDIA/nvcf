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

package gateway

import (
	"ai-api-gateway-service/middleware"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"
)

// PrivateModelsHeader reports how the private models for a keyed model request
// were resolved: authorized, unauthenticated, forbidden, or unavailable.
const PrivateModelsHeader = "NVCF-Private-Models"

const (
	// The NVCF API applies the same account and API key resource checks here
	// as it does to invocation.
	privateModelLookupPath       = "/v2/nvcf/functions/ids"
	privateModelLookupVisibility = "authorized,private"

	privateModelLookupTimeout     = 5 * time.Second
	privateModelLookupCacheTTL    = time.Minute
	privateModelLookupStaleTTL    = 15 * time.Minute
	privateModelLookupCacheSize   = 4096
	privateModelLookupConcurrency = 16
	privateModelLookupMaxBody     = 8 << 20

	privateModelLookupsMetricName = "nvcf_ai_api_gateway_private_model_lookups"
	privateModelLookupResultLabel = "result"
	privateModelLookupCachedLabel = "cached"

	privateModelsAuthorized      = "authorized"
	privateModelsUnauthenticated = "unauthenticated"
	privateModelsForbidden       = "forbidden"
	privateModelsUnavailable     = "unavailable"

	traceAttrPrivateModelsCached attribute.Key = "private_models.cached"
	traceAttrPrivateModelsResult attribute.Key = "private_models.result"
)

var privateModelLookupsCounter otelmetric.Int64Counter = metricnoop.Int64Counter{}

// privateModelAccessByEndpoint holds one access cache per NVCF API endpoint for
// the life of the process, so mapping reloads keep cached results.
var privateModelAccessByEndpoint sync.Map

type privateModelAccessEntry struct {
	functionIDs map[string]struct{}
	result      string
	fetched     time.Time
}

// privateModelAccess asks the NVCF API which private functions a caller's API
// key may access. Results are cached per key. Each entry keeps only function IDs
// that back private aliases, which bounds its size.
type privateModelAccess struct {
	lookupURL string
	client    *http.Client
	slots     *semaphore.Weighted
	lookups   singleflight.Group
	now       func() time.Time

	mu    sync.Mutex
	cache map[string]privateModelAccessEntry
}

func sharedPrivateModelAccess(endpoint string, transport http.RoundTripper) (*privateModelAccess, error) {
	if access, ok := privateModelAccessByEndpoint.Load(endpoint); ok {
		return access.(*privateModelAccess), nil
	}
	access, err := newPrivateModelAccess(endpoint, transport)
	if err != nil {
		return nil, err
	}
	shared, _ := privateModelAccessByEndpoint.LoadOrStore(endpoint, access)
	return shared.(*privateModelAccess), nil
}

// newPrivateModelAccess validates endpoint without echoing it, since a URL can
// carry credentials. A *url.Error repeats the whole URL, so only its cause is
// wrapped.
func newPrivateModelAccess(endpoint string, transport http.RoundTripper) (*privateModelAccess, error) {
	base, err := url.Parse(endpoint)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("invalid NVCF control plane endpoint: %w", err)
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, errors.New("invalid NVCF control plane endpoint: scheme and host are required")
	}
	lookupURL := base.JoinPath(privateModelLookupPath)
	lookupURL.RawQuery = url.Values{"visibility": {privateModelLookupVisibility}}.Encode()
	return &privateModelAccess{
		lookupURL: lookupURL.String(),
		client:    &http.Client{Transport: transport},
		slots:     semaphore.NewWeighted(privateModelLookupConcurrency),
		now:       time.Now,
		cache:     make(map[string]privateModelAccessEntry),
	}, nil
}

// authorizedFunctionIDs returns which privateFunctions the caller's bearer key
// may access, and the lookup result. A failed lookup is not cached. It falls
// back to the key's last good result while that is within the stale window.
func (a *privateModelAccess) authorizedFunctionIDs(request *http.Request, privateFunctions map[string][]ModelInfo) (map[string]struct{}, string) {
	authorization := request.Header.Get("Authorization")
	digest := sha256.Sum256([]byte(authorization))
	key := hex.EncodeToString(digest[:])
	span := trace.SpanFromContext(request.Context())

	if entry, ok := a.cached(key, privateModelLookupCacheTTL); ok {
		recordPrivateModelLookup(request.Context(), entry.result, true)
		span.SetAttributes(traceAttrPrivateModelsCached.Bool(true), traceAttrPrivateModelsResult.String(entry.result))
		return entry.functionIDs, entry.result
	}
	value, _, _ := a.lookups.Do(key, func() (any, error) {
		if entry, ok := a.cached(key, privateModelLookupCacheTTL); ok {
			recordPrivateModelLookup(request.Context(), entry.result, true)
			return entry, nil
		}
		entry, err := a.lookup(request, authorization, privateFunctions)
		recordPrivateModelLookup(request.Context(), entry.result, false)
		if err == nil {
			a.store(key, entry)
			return entry, nil
		}
		last, stale := a.cached(key, privateModelLookupStaleTTL)
		zap.L().Warn("private model lookup failed", zap.Error(err),
			zap.String("request_id", request.Header.Get(middleware.RequestIDHeader)),
			zap.Bool("served_last_good", stale))
		if stale {
			return last, nil
		}
		return entry, nil
	})
	entry := value.(privateModelAccessEntry)
	span.SetAttributes(traceAttrPrivateModelsCached.Bool(false), traceAttrPrivateModelsResult.String(entry.result))
	return entry.functionIDs, entry.result
}

// lookup runs detached from the caller's cancellation because concurrent
// requests with the same key share its result. 401 and 403 are results, not
// errors: the key is invalid or cannot list functions.
func (a *privateModelAccess) lookup(request *http.Request, authorization string, privateFunctions map[string][]ModelInfo) (privateModelAccessEntry, error) {
	unavailable := privateModelAccessEntry{result: privateModelsUnavailable}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), privateModelLookupTimeout)
	defer cancel()
	if err := a.slots.Acquire(ctx, 1); err != nil {
		return unavailable, fmt.Errorf("wait for a lookup slot: %w", err)
	}
	defer a.slots.Release(1)

	ctx = middleware.ContextWithCallKind(ctx, middleware.CallKindModelAccess)
	upstream, err := http.NewRequestWithContext(ctx, http.MethodGet, a.lookupURL, nil)
	if err != nil {
		return unavailable, fmt.Errorf("build request: %w", err)
	}
	upstream.Header.Set("Authorization", authorization)
	upstream.Header.Set("Accept", contentTypeApplicationJSON)
	if requestID := request.Header.Get(middleware.RequestIDHeader); requestID != "" {
		upstream.Header.Set(middleware.RequestIDHeader, requestID)
	}
	resp, err := a.client.Do(upstream)
	if err != nil {
		return unavailable, fmt.Errorf("call NVCF API: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, privateModelLookupMaxBody))
		_ = resp.Body.Close()
	}()

	now := a.now()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return privateModelAccessEntry{result: privateModelsUnauthenticated, fetched: now}, nil
	case http.StatusForbidden:
		return privateModelAccessEntry{result: privateModelsForbidden, fetched: now}, nil
	default:
		return unavailable, fmt.Errorf("NVCF API returned status %d", resp.StatusCode)
	}

	var body struct {
		FunctionIDs []string `json:"functionIds"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, privateModelLookupMaxBody)).Decode(&body); err != nil {
		return unavailable, fmt.Errorf("decode NVCF API response: %w", err)
	}
	authorized := make(map[string]struct{})
	for _, id := range body.FunctionIDs {
		id = strings.ToLower(id)
		if _, ok := privateFunctions[id]; ok {
			authorized[id] = struct{}{}
		}
	}
	return privateModelAccessEntry{functionIDs: authorized, result: privateModelsAuthorized, fetched: now}, nil
}

// cached returns the key's entry if it was fetched within maxAge.
func (a *privateModelAccess) cached(key string, maxAge time.Duration) (privateModelAccessEntry, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry, ok := a.cache[key]
	if !ok || !a.now().Before(entry.fetched.Add(maxAge)) {
		return privateModelAccessEntry{}, false
	}
	return entry, true
}

// store bounds the cache when it is full. It drops entries past the stale
// window, then results without access, so random keys cannot evict the last
// good results of real callers. If that is not enough, it drops everything.
func (a *privateModelAccess) store(key string, entry privateModelAccessEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.cache) >= privateModelLookupCacheSize {
		staleBefore := a.now().Add(-privateModelLookupStaleTTL)
		for _, drop := range []func(privateModelAccessEntry) bool{
			func(e privateModelAccessEntry) bool { return !e.fetched.After(staleBefore) },
			func(e privateModelAccessEntry) bool { return e.result != privateModelsAuthorized },
			func(privateModelAccessEntry) bool { return true },
		} {
			for cachedKey, cachedEntry := range a.cache {
				if drop(cachedEntry) {
					delete(a.cache, cachedKey)
				}
			}
			if len(a.cache) < privateModelLookupCacheSize {
				break
			}
		}
	}
	a.cache[key] = entry
}

// authorizedPrivateFunctionIDs looks up the private functions the caller's
// bearer key may access. It returns nil without a lookup when private models
// are off or the request has no bearer key. Keyed responses are marked private
// and report the lookup result.
func (d *ModelDirector) authorizedPrivateFunctionIDs(writer http.ResponseWriter, request *http.Request) map[string]struct{} {
	if d.privateModels == nil {
		return nil
	}
	if _, ok := bearerCredential(request); !ok {
		return nil
	}
	writer.Header().Set("Cache-Control", "private, no-store")
	functionIDs, result := d.privateModels.authorizedFunctionIDs(request, d.privateModelsByFunction)
	writer.Header().Set(PrivateModelsHeader, result)
	return functionIDs
}

// authorizedPrivateModels returns the private models the caller's key may
// access, before expiry filtering.
func (d *ModelDirector) authorizedPrivateModels(writer http.ResponseWriter, request *http.Request) []ModelInfo {
	var models []ModelInfo
	seen := make(map[string]struct{})
	for id := range d.authorizedPrivateFunctionIDs(writer, request) {
		for _, model := range d.privateModelsByFunction[id] {
			if _, ok := seen[model.Id]; !ok {
				seen[model.Id] = struct{}{}
				models = append(models, model)
			}
		}
	}
	return models
}

// authorizedPrivateModel returns the private model with this name in mappings
// when the caller's key may access its function. Any name that is not public
// takes this path, so a caller without access gets the same answer for a
// private name as for an unknown one.
func (d *ModelDirector) authorizedPrivateModel(writer http.ResponseWriter, request *http.Request, name string, mappings ...ModelMapping) (ModelInfo, bool) {
	if d.privateModels != nil {
		writer.Header().Add("Vary", "Authorization")
	}
	functionIDs := d.authorizedPrivateFunctionIDs(writer, request)
	for _, mapping := range mappings {
		info, ok := mapping.privateModelNameToModelInfo[name]
		if !ok {
			continue
		}
		if _, ok := functionIDs[strings.ToLower(mapping.modelNameToNVCFUrl[name].functionId)]; ok {
			return info, true
		}
	}
	return ModelInfo{}, false
}

// indexPrivateModels maps each function ID behind a private name to the
// private models it serves.
func indexPrivateModels(models []ModelInfo, mappings ...map[string]FunctionInfo) map[string][]ModelInfo {
	index := make(map[string][]ModelInfo)
	for _, model := range models {
		indexed := make(map[string]struct{})
		for _, mapping := range mappings {
			info, ok := mapping[model.Id]
			if !ok {
				continue
			}
			id := strings.ToLower(info.functionId)
			if _, ok := indexed[id]; !ok {
				indexed[id] = struct{}{}
				index[id] = append(index[id], model)
			}
		}
	}
	return index
}

func setupPrivateModelMetrics() error {
	counter, err := newPrivateModelLookupsCounter(otel.GetMeterProvider().Meter(shadowMetricsScope))
	if err != nil {
		return err
	}
	privateModelLookupsCounter = counter
	initializePrivateModelLookupMetrics()
	return nil
}

func newPrivateModelLookupsCounter(meter otelmetric.Meter) (otelmetric.Int64Counter, error) {
	counter, err := meter.Int64Counter(
		privateModelLookupsMetricName,
		otelmetric.WithDescription("Number of private model access lookups by result and whether the result was cached."),
	)
	if err != nil {
		return nil, fmt.Errorf("create private model lookups counter: %w", err)
	}
	return counter, nil
}

// initializePrivateModelLookupMetrics emits every label combination at zero so
// the series exist on the first scrape.
func initializePrivateModelLookupMetrics() {
	for _, result := range []string{
		privateModelsAuthorized,
		privateModelsUnauthenticated,
		privateModelsForbidden,
		privateModelsUnavailable,
	} {
		for _, cached := range []bool{true, false} {
			privateModelLookupsCounter.Add(context.Background(), 0, otelmetric.WithAttributes(
				attribute.String(privateModelLookupResultLabel, result),
				attribute.Bool(privateModelLookupCachedLabel, cached),
			))
		}
	}
}

func recordPrivateModelLookup(ctx context.Context, result string, cached bool) {
	privateModelLookupsCounter.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String(privateModelLookupResultLabel, result),
		attribute.Bool(privateModelLookupCachedLabel, cached),
	))
}
