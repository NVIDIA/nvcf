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
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

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
	// as it does to invocation, and lists each function version it allows.
	privateModelLookupPath       = "/v2/nvcf/functions"
	privateModelLookupVisibility = "authorized,private"

	privateModelLookupTimeout     = 5 * time.Second
	privateModelLookupCacheTTL    = time.Minute
	privateModelLookupStaleTTL    = 15 * time.Minute
	privateModelLookupRetryAfter  = 10 * time.Second
	privateModelLookupCacheSize   = 4096
	privateModelLookupConcurrency = 16
	privateModelLookupMaxBody     = 64 << 20

	// Lookups for keys that have never been authorized share this per-pod
	// budget, so random tokens cannot drive a stream of NVCF API calls.
	privateModelLookupNewKeyRate  = 50
	privateModelLookupNewKeyBurst = 100

	privateModelLookupsMetricName = "nvcf_ai_api_gateway_private_model_lookups"
	privateModelLookupResultLabel = "result"
	privateModelLookupCachedLabel = "cached"

	privateModelsAuthorized      = "authorized"
	privateModelsUnauthenticated = "unauthenticated"
	privateModelsForbidden       = "forbidden"
	privateModelsUnavailable     = "unavailable"
	// privateModelsLimited is a metric result only: the gateway skipped the
	// lookup under its own limits, and the caller sees unavailable.
	privateModelsLimited = "limited"

	traceAttrPrivateModelsCached attribute.Key = "private_models.cached"
	traceAttrPrivateModelsResult attribute.Key = "private_models.result"
)

var privateModelLookupsCounter otelmetric.Int64Counter = metricnoop.Int64Counter{}

// privateModelAccessByEndpoint holds one access cache per NVCF API endpoint for
// the life of the process, so mapping reloads keep cached results.
var privateModelAccessByEndpoint sync.Map

// privateModelIndex maps each function behind a private alias to the aliases
// it serves, for one mapping snapshot.
type privateModelIndex struct {
	routes map[string][]privateModelRoute // keyed by lowercase function ID
	// pinned holds the versions that aliases pin, per function. A lookup keeps
	// only these versions, plus the presence of each function.
	pinned map[string]map[string]struct{}
	// fingerprint identifies the functions and pinned versions. A cached lookup
	// made for another set is refreshed rather than reused.
	fingerprint string
}

type privateModelRoute struct {
	model     ModelInfo
	versionID string // lowercase; empty when the alias does not pin a version
}

// nativeModelPaths are the sections whose aliases list supported_endpoints.
var nativeModelPaths = map[string]struct{}{"/v1/messages": {}, decisionsPath: {}, systemOnePath: {}}

// errPrivateModelLookupLimited means the gateway skipped a lookup under its
// own limits. It is not an NVCF API failure.
var errPrivateModelLookupLimited = errors.New("private model lookup skipped by the gateway's limits")

// authorizedVersions maps each accessible function behind a private alias to
// its accessible pinned versions.
type authorizedVersions map[string]map[string]struct{}

// allows reports whether a route to functionID and versionID is accessible. A
// route without a version is accessible when any version is, as invocation
// then picks among the versions the caller may use.
func (v authorizedVersions) allows(functionID, versionID string) bool {
	versions, ok := v[strings.ToLower(functionID)]
	if !ok {
		return false
	}
	if versionID == "" {
		return true
	}
	_, ok = versions[strings.ToLower(versionID)]
	return ok
}

type privateModelAccessEntry struct {
	versions    authorizedVersions
	result      string
	fetched     time.Time
	fingerprint string
	// failed is when the latest lookup for this key failed. Until
	// privateModelLookupRetryAfter passes, the key is answered from its last
	// good result without another NVCF API call.
	failed time.Time
}

// privateModelAccess asks the NVCF API which private function versions a
// caller's API key may access. Results are cached per key. Each entry keeps
// only functions that back private aliases, which bounds its size.
type privateModelAccess struct {
	lookupURL string
	client    *http.Client
	slots     *semaphore.Weighted
	newKeys   *tokenBucket
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
		client: &http.Client{
			Transport: transport,
			// The request carries the caller's API key, so a redirect is a
			// failed lookup rather than a request to another URL.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		slots:   semaphore.NewWeighted(privateModelLookupConcurrency),
		newKeys: newTokenBucket(privateModelLookupNewKeyRate, privateModelLookupNewKeyBurst),
		now:     time.Now,
		cache:   make(map[string]privateModelAccessEntry),
	}, nil
}

// authorizedVersions returns which function versions in index the caller's
// bearer key may access, and the lookup result. The cache key and the upstream
// header both come from the parsed credential, so every spelling of one
// Authorization header shares one entry and one answer.
//
// A failed lookup is not cached as a result. The key is answered from its last
// good result while that is within the stale window, and no other lookup runs
// for it until privateModelLookupRetryAfter has passed.
func (a *privateModelAccess) authorizedVersions(request *http.Request, credential []byte, index privateModelIndex) (authorizedVersions, string) {
	authorization := "Bearer " + string(credential)
	digest := sha256.Sum256(credential)
	key := hex.EncodeToString(digest[:])
	ctx := request.Context()
	span := trace.SpanFromContext(ctx)

	if entry, ok := a.fresh(key, index); ok {
		recordPrivateModelLookup(ctx, entry.result, true)
		span.SetAttributes(traceAttrPrivateModelsCached.Bool(true), traceAttrPrivateModelsResult.String(entry.result))
		return entry.versions, entry.result
	}
	if a.retryLater(key) {
		recordPrivateModelLookup(ctx, privateModelsUnavailable, true)
		span.SetAttributes(traceAttrPrivateModelsCached.Bool(true), traceAttrPrivateModelsResult.String(privateModelsUnavailable))
		entry := a.lastGood(key)
		return entry.versions, entry.result
	}
	value, _, _ := a.lookups.Do(key+":"+index.fingerprint, func() (any, error) {
		if entry, ok := a.fresh(key, index); ok {
			recordPrivateModelLookup(ctx, entry.result, true)
			return entry, nil
		}
		entry, err := a.lookup(request, authorization, index, a.known(key))
		if err == nil {
			recordPrivateModelLookup(ctx, entry.result, false)
			a.store(key, entry)
			return entry, nil
		}
		if errors.Is(err, errPrivateModelLookupLimited) {
			recordPrivateModelLookup(ctx, privateModelsLimited, false)
			return a.lastGood(key), nil
		}
		recordPrivateModelLookup(ctx, privateModelsUnavailable, false)
		a.markFailed(key)
		last := a.lastGood(key)
		zap.L().Warn("private model lookup failed", zap.Error(err),
			zap.String("request_id", request.Header.Get(middleware.RequestIDHeader)),
			zap.Bool("served_last_good", last.result != privateModelsUnavailable))
		return last, nil
	})
	entry := value.(privateModelAccessEntry)
	span.SetAttributes(traceAttrPrivateModelsCached.Bool(false), traceAttrPrivateModelsResult.String(entry.result))
	return entry.versions, entry.result
}

// lookup runs detached from the caller's cancellation, because concurrent
// requests with the same key share its result, and from the request's
// processing record, because it is not the request's upstream dispatch. 401
// and 403 are results, not errors: the key is invalid or cannot list
// functions. A key that has never been authorized needs a token from the
// new-key budget, and no lookup waits for a slot.
func (a *privateModelAccess) lookup(request *http.Request, authorization string, index privateModelIndex, known bool) (privateModelAccessEntry, error) {
	unavailable := privateModelAccessEntry{result: privateModelsUnavailable}
	if !known && !a.newKeys.allow(a.now()) {
		return unavailable, errPrivateModelLookupLimited
	}
	if !a.slots.TryAcquire(1) {
		return unavailable, errPrivateModelLookupLimited
	}
	defer a.slots.Release(1)

	ctx, cancel := context.WithTimeout(middleware.WithoutRequestProcessing(context.WithoutCancel(request.Context())), privateModelLookupTimeout)
	defer cancel()
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

	versions, err := decodeAuthorizedVersions(io.LimitReader(resp.Body, privateModelLookupMaxBody), index)
	if err != nil {
		return unavailable, fmt.Errorf("decode NVCF API response: %w", err)
	}
	return privateModelAccessEntry{versions: versions, result: privateModelsAuthorized, fetched: now, fingerprint: index.fingerprint}, nil
}

// decodeAuthorizedVersions reads the function list one entry at a time. It
// keeps each function in the index and only the versions that aliases pin. An
// account can own many functions with many versions, so the response is never
// held in memory whole.
func decodeAuthorizedVersions(body io.Reader, index privateModelIndex) (authorizedVersions, error) {
	decoder := stdjson.NewDecoder(body)
	if err := expectJSONDelim(decoder, '{'); err != nil {
		return nil, err
	}
	versions := make(authorizedVersions)
	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		if field != "functions" {
			var skipped stdjson.RawMessage
			if err := decoder.Decode(&skipped); err != nil {
				return nil, err
			}
			continue
		}
		if err := expectJSONDelim(decoder, '['); err != nil {
			return nil, err
		}
		for decoder.More() {
			var function struct {
				ID        string `json:"id"`
				VersionID string `json:"versionId"`
			}
			if err := decoder.Decode(&function); err != nil {
				return nil, err
			}
			id := strings.ToLower(function.ID)
			if _, ok := index.routes[id]; !ok {
				continue
			}
			if versions[id] == nil {
				versions[id] = make(map[string]struct{})
			}
			version := strings.ToLower(function.VersionID)
			if _, ok := index.pinned[id][version]; ok {
				versions[id][version] = struct{}{}
			}
		}
		if err := expectJSONDelim(decoder, ']'); err != nil {
			return nil, err
		}
	}
	return versions, expectJSONDelim(decoder, '}')
}

func expectJSONDelim(decoder *stdjson.Decoder, want stdjson.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != want {
		return fmt.Errorf("unexpected JSON token %v, want %v", token, want)
	}
	return nil
}

// fresh returns the key's entry if it is within the cache TTL and, for an
// authorized result, was computed for the same private function set.
func (a *privateModelAccess) fresh(key string, index privateModelIndex) (privateModelAccessEntry, bool) {
	entry, ok := a.cached(key, privateModelLookupCacheTTL)
	if !ok || (entry.result == privateModelsAuthorized && entry.fingerprint != index.fingerprint) {
		return privateModelAccessEntry{}, false
	}
	return entry, true
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

// lastGood returns the key's last successful result while it is within the
// stale window, or an unavailable result.
func (a *privateModelAccess) lastGood(key string) privateModelAccessEntry {
	if entry, ok := a.cached(key, privateModelLookupStaleTTL); ok {
		return entry
	}
	return privateModelAccessEntry{result: privateModelsUnavailable}
}

// known reports whether the key has an authorized result within the stale
// window. Such keys skip the new-key budget.
func (a *privateModelAccess) known(key string) bool {
	entry, ok := a.cached(key, privateModelLookupStaleTTL)
	return ok && entry.result == privateModelsAuthorized
}

// retryLater reports whether the key's latest lookup failed less than
// privateModelLookupRetryAfter ago.
func (a *privateModelAccess) retryLater(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry, ok := a.cache[key]
	return ok && !entry.failed.IsZero() && a.now().Before(entry.failed.Add(privateModelLookupRetryAfter))
}

// markFailed records a failed lookup next to the key's last good result.
func (a *privateModelAccess) markFailed(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry := a.cache[key]
	entry.failed = a.now()
	a.storeLocked(key, entry)
}

func (a *privateModelAccess) store(key string, entry privateModelAccessEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.storeLocked(key, entry)
}

// storeLocked bounds the cache when it is full. It drops entries past the
// stale window, which includes failure records without a good result, then
// results without access, so random keys cannot evict the last good results
// of real callers. If that is not enough, it drops everything.
func (a *privateModelAccess) storeLocked(key string, entry privateModelAccessEntry) {
	if _, ok := a.cache[key]; !ok && len(a.cache) >= privateModelLookupCacheSize {
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

// tokenBucket allows rate events per second, with bursts of up to burst.
type tokenBucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newTokenBucket(rate, burst float64) *tokenBucket {
	return &tokenBucket{rate: rate, burst: burst, tokens: burst}
}

func (b *tokenBucket) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.last.IsZero() && now.After(b.last) {
		b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	}
	if b.last.IsZero() || now.After(b.last) {
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// authorizedPrivateVersions looks up the private function versions the
// caller's bearer key may access. It returns nil without a lookup when private
// models are off, the mapping has no private aliases, or the request has no
// bearer key. Keyed responses are marked private and report the lookup result.
func (d *ModelDirector) authorizedPrivateVersions(writer http.ResponseWriter, request *http.Request) authorizedVersions {
	if d.privateModels == nil || len(d.privateIndex.routes) == 0 {
		return nil
	}
	credential, ok := bearerCredential(request)
	if !ok {
		return nil
	}
	writer.Header().Set("Cache-Control", "private, no-store")
	versions, result := d.privateModels.authorizedVersions(request, credential, d.privateIndex)
	writer.Header().Set(PrivateModelsHeader, result)
	return versions
}

// authorizedPrivateModels returns the private models the caller's key may use
// in at least one of sections, before expiry filtering.
func (d *ModelDirector) authorizedPrivateModels(writer http.ResponseWriter, request *http.Request, sections []endpointModelMapping) []ModelInfo {
	var models []ModelInfo
	seen := make(map[string]struct{})
	versions := d.authorizedPrivateVersions(writer, request)
	for id := range versions {
		for _, route := range d.privateIndex.routes[id] {
			if _, ok := seen[route.model.Id]; ok {
				continue
			}
			seen[route.model.Id] = struct{}{}
			if model, ok := usablePrivateModel(route.model, versions, sections); ok {
				models = append(models, model)
			}
		}
	}
	return models
}

// authorizedPrivateModel returns the private model with this name when the
// caller's key may use it in at least one of sections. Any name that is not
// public takes this path, so a caller without access gets the same answer for
// a private name as for an unknown one.
func (d *ModelDirector) authorizedPrivateModel(writer http.ResponseWriter, request *http.Request, name string, sections []endpointModelMapping) (ModelInfo, bool) {
	if d.privateModels != nil {
		writer.Header().Add("Vary", "Authorization")
	}
	versions := d.authorizedPrivateVersions(writer, request)
	for _, section := range sections {
		if model, ok := section.mapping.privateModelNameToModelInfo[name]; ok {
			return usablePrivateModel(model, versions, sections)
		}
	}
	return ModelInfo{}, false
}

// usablePrivateModel returns model when the key may use the function version
// that at least one of sections routes it to. An alias can route to different
// functions per section, so for an alias on a native protocol section,
// supported_endpoints lists only the sections the key may use, as it does for
// public models.
func usablePrivateModel(model ModelInfo, versions authorizedVersions, sections []endpointModelMapping) (ModelInfo, bool) {
	var usable []string
	native := false
	for _, section := range sections {
		route, ok := section.mapping.modelNameToNVCFUrl[model.Id]
		if !ok || !versions.allows(route.functionId, route.functionVersionId) {
			continue
		}
		usable = append(usable, section.path)
		_, isNative := nativeModelPaths[section.path]
		native = native || isNative
	}
	if len(usable) == 0 {
		return ModelInfo{}, false
	}
	if len(model.SupportedEndpoints) > 0 {
		model.SupportedEndpoints = nil
		if native {
			model.SupportedEndpoints = usable
		}
	}
	return model, true
}

// messagesSections is the one section Anthropic clients can use.
func (d *ModelDirector) messagesSections() []endpointModelMapping {
	return []endpointModelMapping{{"/v1/messages", d.messages}}
}

// indexPrivateModels maps each function behind a private name to the private
// models it serves and the version each one routes to.
func indexPrivateModels(models []ModelInfo, mappings ...map[string]FunctionInfo) privateModelIndex {
	type target struct{ functionID, versionID string }
	routes := make(map[string][]privateModelRoute)
	pinned := make(map[string]map[string]struct{})
	for _, model := range models {
		indexed := make(map[target]struct{})
		for _, mapping := range mappings {
			info, ok := mapping[model.Id]
			if !ok {
				continue
			}
			t := target{strings.ToLower(info.functionId), strings.ToLower(info.functionVersionId)}
			if _, ok := indexed[t]; ok {
				continue
			}
			indexed[t] = struct{}{}
			routes[t.functionID] = append(routes[t.functionID], privateModelRoute{model: model, versionID: t.versionID})
			if t.versionID != "" {
				if pinned[t.functionID] == nil {
					pinned[t.functionID] = make(map[string]struct{})
				}
				pinned[t.functionID][t.versionID] = struct{}{}
			}
		}
	}
	keys := make([]string, 0, len(routes))
	for id := range routes {
		keys = append(keys, id)
		for version := range pinned[id] {
			keys = append(keys, id+"/"+version)
		}
	}
	slices.Sort(keys)
	digest := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return privateModelIndex{routes: routes, pinned: pinned, fingerprint: hex.EncodeToString(digest[:])}
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
		privateModelsLimited,
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
