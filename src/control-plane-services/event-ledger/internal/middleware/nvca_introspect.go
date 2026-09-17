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

package middleware

import (
	"context"
	"errors"
	"net/http"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/mux"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"go.uber.org/zap"

	api_error "github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/cmd/api/error"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/nvca"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
)

const nvcaIdentityContextKey contextKey = "nvca_identity"

// NVCAIdentity is the caller identity established by introspecting NVCA's
// PSAT at SIS. ClusterID is the cluster SIS resolved for the token, and is
// authoritative over anything a request payload claims.
type NVCAIdentity struct {
	Subject   string
	ClusterID string
}

// WithNVCAIdentity attaches an NVCA identity to ctx. Production code reaches
// this only via a successful SIS introspection in
// newNVCAAwareJWTMiddleware; it is exported so other packages (and tests
// simulating an already-authenticated request) can do the same.
func WithNVCAIdentity(ctx context.Context, identity NVCAIdentity) context.Context {
	return context.WithValue(ctx, nvcaIdentityContextKey, identity)
}

// NVCAIdentityFromContext returns the NVCA identity established for this
// request by SIS introspection, if any.
func NVCAIdentityFromContext(ctx context.Context) (NVCAIdentity, bool) {
	identity, ok := ctx.Value(nvcaIdentityContextKey).(NVCAIdentity)
	return identity, ok
}

// newNVCAAwareJWTMiddleware tries local OpenBao JWT verification first. When
// the bearer token is not an OpenBao token, it falls back to SIS
// introspection for NVCA's PSAT, following the same ordered chain ReVal
// uses. It never inspects the unverified aud claim to route between the two;
// each path performs full local or remote verification.
func newNVCAAwareJWTMiddleware(opts JWTParserOptions, jwkCache *jwk.Cache, introspector nvca.Introspector) mux.MiddlewareFunc {
	if opts.Method == nil {
		opts.Method = jwt.SigningMethodES256
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			traceCtx := r.Context()
			logger := logging.GetLogger(traceCtx)
			errType := "NVCA Introspection Error"

			// Capture the token before processJWTToken consumes and removes
			// the Authorization header, so it is still available for the
			// introspection fallback below.
			token := bearerToken(r)

			newContext, err := processJWTToken(opts, jwkCache, w, r, false)
			if err == nil {
				next.ServeHTTP(w, r.WithContext(newContext))
				return
			}

			if token == "" {
				api_error.GenerateErrorResponse(traceCtx, errType, "Unauthorized", r.URL.Path, http.StatusUnauthorized, errors.New(ErrMissingAuthHeader), w)
				logging.LogHTTPResponse(traceCtx, logger, http.StatusUnauthorized, w.Header())
				return
			}
			if len(token) > nvca.MaxTokenSize {
				api_error.GenerateErrorResponse(traceCtx, errType, "Unauthorized", r.URL.Path, http.StatusUnauthorized, nvca.ErrTokenTooLarge, w)
				logging.LogHTTPResponse(traceCtx, logger, http.StatusUnauthorized, w.Header())
				return
			}

			result, ierr := introspector.Introspect(traceCtx, token)
			if ierr != nil {
				logger.ErrorContext(traceCtx, "nvca introspection call failed", zap.Error(ierr))
				api_error.GenerateErrorResponse(traceCtx, errType, "Service Unavailable", r.URL.Path, http.StatusServiceUnavailable, errors.New("introspection unavailable"), w)
				logging.LogHTTPResponse(traceCtx, logger, http.StatusServiceUnavailable, w.Header())
				return
			}
			if !result.Active {
				logger.WarnContext(traceCtx, "nvca introspection returned inactive token")
				api_error.GenerateErrorResponse(traceCtx, errType, "Unauthorized", r.URL.Path, http.StatusUnauthorized, errors.New(ErrInvalidToken), w)
				logging.LogHTTPResponse(traceCtx, logger, http.StatusUnauthorized, w.Header())
				return
			}
			if !nvca.IsValidNVCASubject(result.Sub) {
				logger.WarnContext(traceCtx, "nvca introspection returned non-nvca subject")
				api_error.GenerateErrorResponse(traceCtx, errType, "Forbidden", r.URL.Path, http.StatusForbidden, errors.New(ErrInsufficientPermissions), w)
				logging.LogHTTPResponse(traceCtx, logger, http.StatusForbidden, w.Header())
				return
			}
			if result.ClusterID == "" {
				logger.WarnContext(traceCtx, "nvca introspection returned no cluster identity")
				api_error.GenerateErrorResponse(traceCtx, errType, "Forbidden", r.URL.Path, http.StatusForbidden, errors.New(ErrInsufficientPermissions), w)
				logging.LogHTTPResponse(traceCtx, logger, http.StatusForbidden, w.Header())
				return
			}

			identity := NVCAIdentity{Subject: result.Sub, ClusterID: result.ClusterID}
			next.ServeHTTP(w, r.WithContext(WithNVCAIdentity(traceCtx, identity)))
		})
	}
}
