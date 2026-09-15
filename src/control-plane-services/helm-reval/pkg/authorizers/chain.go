// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package authorizers

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"go.uber.org/zap"
)

// ErrDenied indicates a policy decision of deny without a transport error.
var ErrDenied = errors.New("authorization denied")

// Chain allows if any authorizer returns Allow=true (OR semantics).
// An empty or all-nil chain denies by default.
func Chain(steps []Authorizer) Authorizer {
	return chain{steps: steps}
}

type chain struct {
	steps []Authorizer
}

func (c chain) Evaluate(ctx context.Context, ac *AuthzContext) (AuthzResult, error) {
	for i, step := range c.steps {
		if step == nil {
			continue
		}
		res, err := step.Evaluate(ctx, ac)
		if err != nil {
			return AuthzResult{}, fmt.Errorf("authorizer step %d: %w", i, err)
		}
		if res.Allow {
			return AuthzResult{Allow: true}, nil
		}
	}
	return AuthzResult{Allow: false}, nil
}

// EvaluateMiddleware builds chi middleware from an Authorizer (typically Chain(...)).
func EvaluateMiddleware(a Authorizer, logger *zap.Logger, onFailure func(r *http.Request, w http.ResponseWriter, err error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok, err := BearerTokenFromRequest(r)
			if err != nil {
				onFailure(r, w, err)
				return
			}
			ac := &AuthzContext{
				Request: r,
				Input: AuthzInput{
					Method:      r.Method,
					Path:        r.URL.Path,
					BearerToken: tok,
				},
				Extra: map[string]any{
					"method": r.Method,
					"path":   r.URL.Path,
				},
			}
			res, err := a.Evaluate(r.Context(), ac)
			if err != nil {
				if logger != nil {
					logger.Debug("authorization error", zap.Error(err))
				}
				onFailure(r, w, err)
				return
			}
			if !res.Allow {
				if logger != nil {
					logger.Debug("authorization denied")
				}
				onFailure(r, w, ErrDenied)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
