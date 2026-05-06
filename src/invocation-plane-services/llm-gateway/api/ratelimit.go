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

package api

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	echo "github.com/labstack/echo/v4"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/ratelimit"
	"github.com/NVIDIA/nvcf/llm-api-gateway/requestctx"
)

type LimitResolver interface {
	ResolveLimits(
		ctx context.Context,
		reqCtx *requestctx.RequestContext,
		endpoint string,
	) ([]ratelimit.ResourceLimit, error)
}

type CompositeLimitResolver []LimitResolver

func (r CompositeLimitResolver) ResolveLimits(
	ctx context.Context,
	reqCtx *requestctx.RequestContext,
	endpoint string,
) ([]ratelimit.ResourceLimit, error) {
	if len(r) == 0 {
		return nil, nil
	}

	limits := make([]ratelimit.ResourceLimit, 0, len(r))
	for _, resolver := range r {
		if resolver == nil {
			continue
		}

		resolved, err := resolver.ResolveLimits(ctx, reqCtx, endpoint)
		if err != nil {
			return nil, err
		}
		limits = append(limits, resolved...)
	}

	return limits, nil
}

type CallerLimitResolver struct{}

func (r CallerLimitResolver) ResolveLimits(
	_ context.Context,
	reqCtx *requestctx.RequestContext,
	_ string,
) ([]ratelimit.ResourceLimit, error) {
	_ = r

	if reqCtx == nil || reqCtx.Model == "" || reqCtx.RoutingKey == "" || reqCtx.OrgID == "" {
		return nil, nil
	}

	spec, ok := reqCtx.ModelSpecs[reqCtx.Model]
	if !ok || spec.TokenRateLimit == "" {
		return nil, nil
	}

	parsedTokenLimits, err := parseTokenRateLimit(spec.TokenRateLimit)
	if err != nil {
		return nil, fmt.Errorf("parse token rate limit for model %q: %w", reqCtx.Model, err)
	}

	if parsedTokenLimits.empty() {
		return nil, nil
	}

	baseLimit := ratelimit.ResourceLimit{
		SubjectKey:        "routing_key:" + reqCtx.RoutingKey,
		SubjectRepr:       "routing key `" + reqCtx.RoutingKey + "`",
		Level:             ratelimit.LevelFunction,
		TokensPerMinute:   parsedTokenLimits.tokensPerMinute,
		TokensPerDay:      parsedTokenLimits.tokensPerDay,
	}

	switch {
	case reqCtx.ProjectID != "":
		return []ratelimit.ResourceLimit{
			scopedProjectLimit(baseLimit, reqCtx.OrgID, reqCtx.ProjectID, reqCtx.RoutingKey),
		}, nil
	case reqCtx.OrgID != "":
		return []ratelimit.ResourceLimit{
			scopedOrgLimit(baseLimit, reqCtx.OrgID, reqCtx.RoutingKey),
		}, nil
	default:
		return nil, nil
	}
}

type parsedTokenRateLimit struct {
	tokensPerMinute int64
	tokensPerDay    int64
}

func (p parsedTokenRateLimit) empty() bool {
	return p.tokensPerMinute == 0 && p.tokensPerDay == 0
}

func parseTokenRateLimit(raw string) (parsedTokenRateLimit, error) {
	if raw == "" {
		return parsedTokenRateLimit{}, nil
	}

	var (
		parsed                parsedTokenRateLimit
		sawTokensPerMinute    bool
		sawTokensPerDay       bool
	)
	for _, fragment := range strings.Split(raw, ",") {
		fragment = strings.TrimSpace(fragment)
		if fragment == "" {
			return parsedTokenRateLimit{}, fmt.Errorf("empty token rate limit fragment")
		}

		valuePart, levelPart, ok := strings.Cut(fragment, "-")
		if !ok {
			return parsedTokenRateLimit{}, fmt.Errorf("invalid token rate limit fragment %q", fragment)
		}

		value, err := strconv.ParseInt(strings.TrimSpace(valuePart), 10, 64)
		if err != nil {
			return parsedTokenRateLimit{}, fmt.Errorf("invalid token rate limit value %q", valuePart)
		}
		if value < 0 {
			return parsedTokenRateLimit{}, fmt.Errorf("token rate limit must be non-negative")
		}

		switch strings.ToUpper(strings.TrimSpace(levelPart)) {
		case "M":
			if sawTokensPerMinute {
				return parsedTokenRateLimit{}, fmt.Errorf("duplicate minute token rate limit")
			}
			sawTokensPerMinute = true
			parsed.tokensPerMinute = value
		case "D":
			if sawTokensPerDay {
				return parsedTokenRateLimit{}, fmt.Errorf("duplicate day token rate limit")
			}
			sawTokensPerDay = true
			parsed.tokensPerDay = value
		default:
			return parsedTokenRateLimit{}, fmt.Errorf("unsupported token rate limit level %q", levelPart)
		}
	}

	return parsed, nil
}

type RateLimitContext struct {
	Stats   rateLimitStats
	Results rateLimitResults
	Applied []ratelimit.ResourceLimit

	limitingDimension     ratelimit.LimitDimension
	limitingResult        *ratelimit.RateLimitResult
	limitingResourceLimit ratelimit.ResourceLimit
}

func (ctx *RateLimitContext) RevertAndRecalculateStats() {
	if ctx == nil {
		return
	}

	for _, results := range ctx.Results {
		for _, result := range results {
			result.Revert()
		}
	}
	ctx.calculateStats()
}

func (ctx *RateLimitContext) calculateStats() {
	if ctx == nil {
		return
	}

	var (
		stats         rateLimitStats
		maxRetryAfter time.Duration
	)

	ctx.limitingDimension = ""
	ctx.limitingResult = nil
	ctx.limitingResourceLimit = ratelimit.ResourceLimit{}

	for i, results := range ctx.Results {
		for dim, result := range results {
			if result == nil {
				continue
			}
			if !result.Allowed() &&
				(ctx.limitingResult == nil || result.RetryAfter() > maxRetryAfter) {
				maxRetryAfter = result.RetryAfter()
				ctx.limitingDimension = dim
				ctx.limitingResult = result
				if i < len(ctx.Applied) {
					ctx.limitingResourceLimit = ctx.Applied[i]
				}
			}
		}

		if result := chooseRequestResult(results); result != nil {
			remaining := result.RemainingValue()
			if stats.RemainingRequests == nil || remaining < ptr.Deref(stats.RemainingRequests) {
				stats.LimitRequests = ptr.To(result.LimitValue())
				stats.RemainingRequests = ptr.To(remaining)
				stats.ResetRequests = ptr.To(result.ResetAfter())
			}
		}

		if limit, remaining, reset, ok := chooseTokenStats(results); ok {
			if stats.RemainingTokens == nil || remaining < ptr.Deref(stats.RemainingTokens) {
				stats.LimitTokens = ptr.To(limit)
				stats.RemainingTokens = ptr.To(remaining)
				stats.ResetTokens = ptr.To(reset)
			}
		}
	}

	if ctx.limitingResult != nil {
		retryAfter := int64(math.Ceil(ctx.limitingResult.RetryAfter().Seconds()))
		stats.RetryAfter = ptr.To(retryAfter)
		if retryAfter > 60 {
			stats.ShouldRetry = ptr.To(false)
		}
	}

	ctx.Stats = stats
}

func (ctx *RateLimitContext) applyRateLimitHeaders(dst http.Header) {
	if ctx == nil || dst == nil {
		return
	}

	for key, values := range ctx.Stats.headers() {
		if len(values) == 0 {
			continue
		}
		dst.Set(key, values[0])
	}
}

type rateLimitResults []map[ratelimit.LimitDimension]*ratelimit.RateLimitResult

type rateLimitStats struct {
	LimitRequests     *int64         // unused in the current token-only live path; set only when request-based dimensions are enabled
	RemainingRequests *int64         // unused in the current token-only live path; set only when request-based dimensions are enabled
	ResetRequests     *time.Duration // unused in the current token-only live path; set only when request-based dimensions are enabled

	LimitTokens     *int64
	RemainingTokens *int64
	ResetTokens     *time.Duration

	RetryAfter  *int64
	ShouldRetry *bool
}

func (r *rateLimitStats) headers() http.Header {
	h := make(http.Header)
	if r == nil {
		return h
	}

	if r.LimitRequests != nil {
		h.Set("X-Ratelimit-Limit-Requests", strconv.FormatInt(ptr.Deref(r.LimitRequests), 10))
	}
	if r.RemainingRequests != nil {
		h.Set("X-Ratelimit-Remaining-Requests", strconv.FormatInt(ptr.Deref(r.RemainingRequests), 10))
	}
	if r.ResetRequests != nil {
		h.Set("X-Ratelimit-Reset-Requests", headerDuration(ptr.Deref(r.ResetRequests)).String())
	}
	if r.LimitTokens != nil {
		h.Set("X-Ratelimit-Limit-Tokens", strconv.FormatInt(ptr.Deref(r.LimitTokens), 10))
	}
	if r.RemainingTokens != nil {
		h.Set("X-Ratelimit-Remaining-Tokens", strconv.FormatInt(ptr.Deref(r.RemainingTokens), 10))
	}
	if r.ResetTokens != nil {
		h.Set("X-Ratelimit-Reset-Tokens", headerDuration(ptr.Deref(r.ResetTokens)).String())
	}
	if r.RetryAfter != nil {
		h.Set("Retry-After", strconv.FormatInt(ptr.Deref(r.RetryAfter), 10))
	}
	if r.ShouldRetry != nil {
		h.Set("X-Should-Retry", strconv.FormatBool(ptr.Deref(r.ShouldRetry)))
	}

	return h
}

type limitGroup struct {
	limits []ratelimit.ResourceLimit
	plans  []*ratelimit.AdmissionPlan
}

type AdmissionPlan struct {
	octx *GatewayContext

	groups     []limitGroup
	successCtx []*RateLimitContext
	failedCtx  *RateLimitContext
	rl         ratelimit.RateLimiter

	checkRequest   ratelimit.ResourceRequest
	consumeRequest ratelimit.ResourceRequest
	requestID      string
	finalized      bool

	finalizedPlans []*ratelimit.AdmissionPlan
}

func NewAdmissionPlan(
	octx *GatewayContext,
	reqCtx *requestctx.RequestContext,
	endpoint string,
	resolver LimitResolver,
	rl ratelimit.RateLimiter,
	checkRequest ratelimit.ResourceRequest,
	consumeRequest ratelimit.ResourceRequest,
) (*AdmissionPlan, error) {
	if octx == nil || reqCtx == nil || resolver == nil || rl == nil {
		return nil, nil
	}

	groups, err := getLimitGroups(octx.UserContext(), resolver, reqCtx, endpoint, rl, octx.RequestID())
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if len(groups) == 0 {
		return nil, nil
	}

	return &AdmissionPlan{
		octx:           octx,
		groups:         groups,
		rl:             rl,
		checkRequest:   checkRequest,
		consumeRequest: consumeRequest,
		requestID:      octx.RequestID(),
	}, nil
}

func (p *AdmissionPlan) Close() {
	if p == nil || p.finalized {
		return
	}

	var rlCtx *RateLimitContext
	switch {
	case p.failedCtx != nil:
		rlCtx = p.failedCtx
	case len(p.successCtx) > 0:
		rlCtx = p.successCtx[len(p.successCtx)-1]
	default:
		return
	}

	rlCtx.RevertAndRecalculateStats()
	rlCtx.applyRateLimitHeaders(p.octx.Response().Header())
	p.octx.SetRateLimitContext(rlCtx)
}

func (p *AdmissionPlan) CheckRequests(ctx context.Context) error {
	if p == nil || p.checkRequest.Requests == 0 {
		return nil
	}

	requestsOnly := ratelimit.ResourceRequest{Requests: p.checkRequest.Requests}
	passing := make([]limitGroup, 0, len(p.groups))
	successCtx := make([]*RateLimitContext, 0, len(p.groups))

	var (
		lastCtx *RateLimitContext
		lastErr error
	)

	for _, group := range p.groups {
		rlCtx, err := checkRateLimit(ctx, p.rl, group.limits, requestsOnly, p.requestID)
		if rlCtx != nil {
			lastCtx = rlCtx
		}
		if err != nil {
			lastErr = err
			continue
		}

		passing = append(passing, group)
		successCtx = append(successCtx, rlCtx)
	}

	p.groups = passing
	p.successCtx = successCtx
	if len(p.groups) == 0 {
		p.failedCtx = lastCtx
		return lastErr
	}

	return nil
}

func (p *AdmissionPlan) CheckTokensAndFinalize(ctx context.Context) (*RateLimitContext, error) {
	if p == nil {
		return nil, nil
	}

	tokenOnly := p.checkRequest
	tokenOnly.Requests = 0

	var (
		lastCtx   *RateLimitContext
		lastErr   error
		finalized []*ratelimit.AdmissionPlan
	)

	for i, firstPassCtx := range p.successCtx {
		group := p.groups[i]
		rlCtx, err := checkRateLimit(ctx, p.rl, group.limits, tokenOnly, p.requestID)
		combinedCtx := mergeRateLimitContexts(rlCtx, firstPassCtx)
		if combinedCtx != nil {
			lastCtx = combinedCtx
		}
		if err != nil {
			lastErr = err
			continue
		}

		for _, plan := range group.plans {
			if _, commitErr := plan.Commit(ctx, p.consumeRequest); commitErr != nil {
				return nil, echo.NewHTTPError(http.StatusInternalServerError, commitErr.Error())
			}
		}

		combinedCtx.applyRateLimitHeaders(p.octx.Response().Header())
		p.octx.SetRateLimitContext(combinedCtx)
		p.finalized = true
		finalized = group.plans
		p.finalizedPlans = finalized
		return combinedCtx, nil
	}

	p.failedCtx = lastCtx
	return lastCtx, lastErr
}

func (p *AdmissionPlan) FinalizeTokens(
	ctx context.Context,
	request ratelimit.ResourceRequest,
) (map[ratelimit.LimitDimension]*ratelimit.RateLimitResult, error) {
	if p == nil || len(p.finalizedPlans) == 0 {
		return nil, nil
	}

	merged := make(map[ratelimit.LimitDimension]*ratelimit.RateLimitResult)
	for _, plan := range p.finalizedPlans {
		results, err := plan.FinalizeTokens(ctx, request)
		if err != nil {
			return nil, err
		}
		mergeRateLimitResults(merged, results)
	}

	return merged, nil
}

func (p *AdmissionPlan) ReleaseOutputReservation(
	ctx context.Context,
) (map[ratelimit.LimitDimension]*ratelimit.RateLimitResult, error) {
	if p == nil || len(p.finalizedPlans) == 0 {
		return nil, nil
	}

	merged := make(map[ratelimit.LimitDimension]*ratelimit.RateLimitResult)
	for _, plan := range p.finalizedPlans {
		results, err := plan.ReleaseOutputReservation(ctx)
		if err != nil {
			return nil, err
		}
		mergeRateLimitResults(merged, results)
	}

	return merged, nil
}

func getLimitGroups(
	ctx context.Context,
	resolver LimitResolver,
	reqCtx *requestctx.RequestContext,
	endpoint string,
	rl ratelimit.RateLimiter,
	requestID string,
) ([]limitGroup, error) {
	limits, err := resolver.ResolveLimits(ctx, reqCtx, endpoint)
	if err != nil {
		return nil, err
	}
	if len(limits) == 0 {
		return nil, nil
	}

	group := limitGroup{
		limits: limits,
		plans:  make([]*ratelimit.AdmissionPlan, 0, len(limits)),
	}
	for _, limit := range limits {
		plan := ratelimit.NewAdmissionPlan(rl, limit, requestID)
		if plan == nil {
			continue
		}
		group.plans = append(group.plans, plan)
	}

	if len(group.plans) == 0 {
		return nil, nil
	}

	return []limitGroup{group}, nil
}

func checkRateLimit(
	ctx context.Context,
	rl ratelimit.RateLimiter,
	resourceLimits []ratelimit.ResourceLimit,
	request ratelimit.ResourceRequest,
	requestID string,
) (*RateLimitContext, error) {
	rlCtx := &RateLimitContext{
		Applied: resourceLimits,
		Results: make(rateLimitResults, 0, len(resourceLimits)),
	}

	limited := false
	for _, resourceLimit := range resourceLimits {
		results, err := ratelimit.TestResourceLimit(ctx, rl, resourceLimit, request, requestID)
		if err != nil {
			return nil, err
		}
		for _, result := range results {
			if result != nil && !result.Allowed() {
				limited = true
				break
			}
		}
		rlCtx.Results = append(rlCtx.Results, results)
	}

	rlCtx.calculateStats()
	if limited {
		return rlCtx, echo.NewHTTPError(http.StatusTooManyRequests, "rate limit exceeded")
	}

	return rlCtx, nil
}

func mergeRateLimitContexts(first *RateLimitContext, second *RateLimitContext) *RateLimitContext {
	switch {
	case first == nil:
		return second
	case second == nil:
		return first
	}

	merged := &RateLimitContext{
		Applied: first.Applied,
		Results: make(rateLimitResults, 0, len(first.Results)),
	}
	for i := range first.Results {
		result := make(map[ratelimit.LimitDimension]*ratelimit.RateLimitResult)
		for dim, value := range first.Results[i] {
			result[dim] = value
		}
		if i < len(second.Results) {
			for dim, value := range second.Results[i] {
				result[dim] = value
			}
		}
		merged.Results = append(merged.Results, result)
	}
	merged.calculateStats()
	return merged
}

func mergeRateLimitResults(
	dst map[ratelimit.LimitDimension]*ratelimit.RateLimitResult,
	src map[ratelimit.LimitDimension]*ratelimit.RateLimitResult,
) {
	for dim, result := range src {
		dst[dim] = result
	}
}

func scopedOrgLimit(
	base ratelimit.ResourceLimit,
	orgID string,
	routingKey string,
) ratelimit.ResourceLimit {
	limit := base
	limit.SubjectKey = rateLimitSubjectKey(orgID, "", routingKey)
	limit.SubjectRepr = "org `" + orgID + "` routing key `" + routingKey + "`"
	limit.Level = ratelimit.LevelOrg
	return limit
}

func scopedProjectLimit(
	base ratelimit.ResourceLimit,
	orgID string,
	projectID string,
	routingKey string,
) ratelimit.ResourceLimit {
	limit := base
	limit.SubjectKey = rateLimitSubjectKey(orgID, projectID, routingKey)
	limit.SubjectRepr = "org `" + orgID + "` project `" + projectID + "` routing key `" + routingKey + "`"
	limit.Level = ratelimit.LevelProject
	return limit
}

func chooseRequestResult(
	results map[ratelimit.LimitDimension]*ratelimit.RateLimitResult,
) *ratelimit.RateLimitResult {
	for _, dim := range []ratelimit.LimitDimension{
		ratelimit.RequestsPerMinute,
		ratelimit.RequestsPerDay,
	} {
		if result := results[dim]; result != nil {
			return result
		}
	}

	return nil
}

func chooseTokenStats(
	results map[ratelimit.LimitDimension]*ratelimit.RateLimitResult,
) (int64, int64, time.Duration, bool) {
	for _, dim := range []ratelimit.LimitDimension{
		ratelimit.TokensPerMinute,
		ratelimit.TokensPerDay,
	} {
		if result := results[dim]; result != nil {
			return result.LimitValue(), result.RemainingValue(), result.ResetAfter(), true
		}
	}

	var (
		hasLimit   bool
		limit      int64
		remaining  int64
		resetAfter time.Duration
	)
	for _, dim := range []ratelimit.LimitDimension{
		ratelimit.InputTokensPerMinute,
		ratelimit.OutputTokensPerMinute,
	} {
		result := results[dim]
		if result == nil {
			continue
		}
		hasLimit = true
		limit += result.LimitValue()
		remaining += result.RemainingValue()
		if result.ResetAfter() > resetAfter {
			resetAfter = result.ResetAfter()
		}
	}

	return limit, remaining, resetAfter, hasLimit
}

func headerDuration(d time.Duration) time.Duration {
	const resolution = time.Millisecond
	if d <= 0 {
		return resolution
	}
	return max(resolution, d.Truncate(resolution))
}
