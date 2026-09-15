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

package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

const (
	day  = 24 * time.Hour
	week = 7 * day
)

type LimitDimension string

type LimitLevel string

const (
	LevelFunction LimitLevel = "function"
	LevelOrg      LimitLevel = "org"
	LevelProject  LimitLevel = "project"
	LevelAPIKey   LimitLevel = "api_key"
)

const (
	RequestsPerMinute     LimitDimension = "requests_minute"
	RequestsPerDay        LimitDimension = "requests_day"
	TokensPerSecond       LimitDimension = "tokens_second"
	TokensPerMinute       LimitDimension = "tokens_minute"
	TokensPerHour         LimitDimension = "tokens_hour"
	TokensPerDay          LimitDimension = "tokens_day"
	TokensPerWeek         LimitDimension = "tokens_week"
	InputTokensPerMinute  LimitDimension = "input_tokens_minute"
	OutputTokensPerMinute LimitDimension = "output_tokens_minute"
)

var allLimitDimensions = []LimitDimension{
	RequestsPerMinute,
	RequestsPerDay,
	TokensPerSecond,
	TokensPerMinute,
	TokensPerHour,
	TokensPerDay,
	TokensPerWeek,
	InputTokensPerMinute,
	OutputTokensPerMinute,
}

type ResourceLimit struct {
	SubjectKey  string
	SubjectRepr string
	Level       LimitLevel

	RequestsPerMinute     int64
	RequestsPerDay        int64
	TokensPerSecond       int64
	TokensPerMinute       int64
	TokensPerHour         int64
	TokensPerDay          int64
	TokensPerWeek         int64
	InputTokensPerMinute  int64
	OutputTokensPerMinute int64
}

type OrgLimit struct {
	SubjectKey  string
	SubjectRepr string
	OrgID       string
	FunctionID  string

	RequestsPerMinute     int64
	RequestsPerDay        int64
	TokensPerSecond       int64
	TokensPerMinute       int64
	TokensPerHour         int64
	TokensPerDay          int64
	TokensPerWeek         int64
	InputTokensPerMinute  int64
	OutputTokensPerMinute int64
}

type ProjectLimit struct {
	SubjectKey  string
	SubjectRepr string
	OrgID       string
	ProjectID   string
	FunctionID  string

	RequestsPerMinute     int64
	RequestsPerDay        int64
	TokensPerSecond       int64
	TokensPerMinute       int64
	TokensPerHour         int64
	TokensPerDay          int64
	TokensPerWeek         int64
	InputTokensPerMinute  int64
	OutputTokensPerMinute int64
}

type APIKeyLimit struct {
	SubjectKey  string
	SubjectRepr string
	APIKeyID    string
	Endpoint    string
	FunctionID  string

	RequestsPerMinute     int64
	RequestsPerDay        int64
	TokensPerSecond       int64
	TokensPerMinute       int64
	TokensPerHour         int64
	TokensPerDay          int64
	TokensPerWeek         int64
	InputTokensPerMinute  int64
	OutputTokensPerMinute int64
}

func (r ResourceLimit) Empty() bool {
	return r == ResourceLimit{}
}

type ResourceRequest struct {
	Requests     int64 `json:"requests"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

func TestResourceLimit(
	ctx context.Context,
	limiter RateLimiter,
	limit ResourceLimit,
	request ResourceRequest,
	requestID string,
) (map[LimitDimension]*RateLimitResult, error) {
	return doResourceLimit(ctx, limiter, limit, request, true, requestID, false)
}

func ConsumeResourceLimit(
	ctx context.Context,
	limiter RateLimiter,
	limit ResourceLimit,
	request ResourceRequest,
	requestID string,
) (map[LimitDimension]*RateLimitResult, error) {
	return doResourceLimit(ctx, limiter, limit, request, false, requestID, false)
}

func MustConsumeResourceLimit(
	ctx context.Context,
	limiter RateLimiter,
	limit ResourceLimit,
	request ResourceRequest,
	requestID string,
) (map[LimitDimension]*RateLimitResult, error) {
	return doResourceLimit(ctx, limiter, limit, request, false, requestID, true)
}

func ResetAllLimits(limiter RateLimiter, limit ResourceLimit) error {
	keys := make([]string, len(allLimitDimensions))
	for i, dimension := range allLimitDimensions {
		keys[i] = keyForDimension(limit, dimension)
	}
	return limiter.Reset(context.Background(), keys...)
}

func doResourceLimit(
	ctx context.Context,
	limiter RateLimiter,
	resourceLimit ResourceLimit,
	resourceRequest ResourceRequest,
	testOnly bool,
	requestID string,
	mustConsume bool,
) (map[LimitDimension]*RateLimitResult, error) {
	log := telemetry.Logger(ctx)

	var (
		results  = make(map[LimitDimension]*RateLimitResult)
		eg, ectx = errgroup.WithContext(ctx)
		mu       sync.Mutex
	)

	for _, dimension := range allLimitDimensions {
		dimension := dimension
		eg.Go(func() error {
			var (
				units  int64
				limit  int64
				period time.Duration
			)

			switch dimension {
			case RequestsPerMinute:
				units = resourceRequest.Requests
				limit = resourceLimit.RequestsPerMinute
				period = time.Minute
			case RequestsPerDay:
				units = resourceRequest.Requests
				limit = resourceLimit.RequestsPerDay
				period = day
			case TokensPerSecond:
				units = resourceRequest.InputTokens + resourceRequest.OutputTokens
				limit = resourceLimit.TokensPerSecond
				period = time.Second
			case TokensPerMinute:
				units = resourceRequest.InputTokens + resourceRequest.OutputTokens
				limit = resourceLimit.TokensPerMinute
				period = time.Minute
			case TokensPerHour:
				units = resourceRequest.InputTokens + resourceRequest.OutputTokens
				limit = resourceLimit.TokensPerHour
				period = time.Hour
			case TokensPerDay:
				units = resourceRequest.InputTokens + resourceRequest.OutputTokens
				limit = resourceLimit.TokensPerDay
				period = day
			case TokensPerWeek:
				units = resourceRequest.InputTokens + resourceRequest.OutputTokens
				limit = resourceLimit.TokensPerWeek
				period = week
			case InputTokensPerMinute:
				units = resourceRequest.InputTokens
				limit = resourceLimit.InputTokensPerMinute
				period = time.Minute
			case OutputTokensPerMinute:
				units = resourceRequest.OutputTokens
				limit = resourceLimit.OutputTokensPerMinute
				period = time.Minute
			default:
				return fmt.Errorf("unknown limit dimension: %s", dimension)
			}

			if units == 0 || limit == 0 {
				return nil
			}

			result, err := limiter.CheckLimit(
				ectx,
				keyForDimension(resourceLimit, dimension),
				RateLimit{
					Limit:  limit,
					Period: period,
				},
				units,
				testOnly,
				requestID,
				mustConsume,
			)
			if err != nil {
				log.Error().
					Err(err).
					Str("dimension", string(dimension)).
					Msg("resource rate limit check failed")
				return err
			}

			mu.Lock()
			results[dimension] = result
			mu.Unlock()

			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	return results, nil
}

func keyForDimension(limit ResourceLimit, dimension LimitDimension) string {
	return fmt.Sprintf("%s:%s", limit.SubjectKey, dimension)
}

func ResourceLimitFromOrgLimit(orgLimit OrgLimit) ResourceLimit {
	subjectKey := orgLimit.SubjectKey
	if subjectKey == "" {
		subjectKey = fmt.Sprintf(
			"org_limit:%s:function:%s",
			orgLimit.OrgID,
			orgLimit.FunctionID,
		)
	}

	subjectRepr := orgLimit.SubjectRepr
	if subjectRepr == "" {
		subjectRepr = fmt.Sprintf(
			"org `%s` function `%s`",
			orgLimit.OrgID,
			orgLimit.FunctionID,
		)
	}

	return ResourceLimit{
		SubjectKey:            subjectKey,
		SubjectRepr:           subjectRepr,
		Level:                 LevelOrg,
		RequestsPerMinute:     orgLimit.RequestsPerMinute,
		RequestsPerDay:        orgLimit.RequestsPerDay,
		TokensPerSecond:       orgLimit.TokensPerSecond,
		TokensPerMinute:       orgLimit.TokensPerMinute,
		TokensPerHour:         orgLimit.TokensPerHour,
		TokensPerDay:          orgLimit.TokensPerDay,
		TokensPerWeek:         orgLimit.TokensPerWeek,
		InputTokensPerMinute:  orgLimit.InputTokensPerMinute,
		OutputTokensPerMinute: orgLimit.OutputTokensPerMinute,
	}
}

func ResourceLimitFromProjectLimit(projectLimit ProjectLimit) ResourceLimit {
	subjectKey := projectLimit.SubjectKey
	if subjectKey == "" {
		switch {
		case projectLimit.OrgID != "":
			subjectKey = fmt.Sprintf(
				"project_limit:%s:%s:function:%s",
				projectLimit.OrgID,
				projectLimit.ProjectID,
				projectLimit.FunctionID,
			)
		default:
			subjectKey = fmt.Sprintf(
				"project_limit:%s:function:%s",
				projectLimit.ProjectID,
				projectLimit.FunctionID,
			)
		}
	}

	subjectRepr := projectLimit.SubjectRepr
	if subjectRepr == "" {
		switch {
		case projectLimit.OrgID != "":
			subjectRepr = fmt.Sprintf(
				"org `%s` project `%s` function `%s`",
				projectLimit.OrgID,
				projectLimit.ProjectID,
				projectLimit.FunctionID,
			)
		default:
			subjectRepr = fmt.Sprintf(
				"project `%s` function `%s`",
				projectLimit.ProjectID,
				projectLimit.FunctionID,
			)
		}
	}

	return ResourceLimit{
		SubjectKey:            subjectKey,
		SubjectRepr:           subjectRepr,
		Level:                 LevelProject,
		RequestsPerMinute:     projectLimit.RequestsPerMinute,
		RequestsPerDay:        projectLimit.RequestsPerDay,
		TokensPerSecond:       projectLimit.TokensPerSecond,
		TokensPerMinute:       projectLimit.TokensPerMinute,
		TokensPerHour:         projectLimit.TokensPerHour,
		TokensPerDay:          projectLimit.TokensPerDay,
		TokensPerWeek:         projectLimit.TokensPerWeek,
		InputTokensPerMinute:  projectLimit.InputTokensPerMinute,
		OutputTokensPerMinute: projectLimit.OutputTokensPerMinute,
	}
}

func ResourceLimitFromAPIKeyLimit(apiKeyLimit APIKeyLimit) ResourceLimit {
	subjectKey := apiKeyLimit.SubjectKey
	if subjectKey == "" {
		subjectKey = fmt.Sprintf(
			"api_key_limit:%s:endpoint:%s:function:%s",
			apiKeyLimit.APIKeyID,
			apiKeyLimit.Endpoint,
			apiKeyLimit.FunctionID,
		)
	}

	subjectRepr := apiKeyLimit.SubjectRepr
	if subjectRepr == "" {
		subjectRepr = fmt.Sprintf(
			"api key `%s` endpoint `%s` function `%s`",
			apiKeyLimit.APIKeyID,
			apiKeyLimit.Endpoint,
			apiKeyLimit.FunctionID,
		)
	}

	return ResourceLimit{
		SubjectKey:            subjectKey,
		SubjectRepr:           subjectRepr,
		Level:                 LevelAPIKey,
		RequestsPerMinute:     apiKeyLimit.RequestsPerMinute,
		RequestsPerDay:        apiKeyLimit.RequestsPerDay,
		TokensPerSecond:       apiKeyLimit.TokensPerSecond,
		TokensPerMinute:       apiKeyLimit.TokensPerMinute,
		TokensPerHour:         apiKeyLimit.TokensPerHour,
		TokensPerDay:          apiKeyLimit.TokensPerDay,
		TokensPerWeek:         apiKeyLimit.TokensPerWeek,
		InputTokensPerMinute:  apiKeyLimit.InputTokensPerMinute,
		OutputTokensPerMinute: apiKeyLimit.OutputTokensPerMinute,
	}
}
