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

// Package jwtparse provides JWKS-backed JWT parsing for ReVal authorization.
package jwtparse

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
)

var tracerScope = "github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/jwtparse"

var (
	ErrMissingJWKSURL   = errors.New("jwks url is missing")
	ErrJwtMissingKID    = errors.New("jwt missing kid")
	ErrJwtWrongKey      = errors.New("jwt wrong key")
	ErrFailedToFetchJWK = errors.New("failed to fetch jwk")
)

// Parser verifies JWTs against a remote JWKS URL.
type Parser struct {
	jwksURL  string
	jwtCache *jwk.Cache
}

// NewCachedParser builds a JWKS-backed parser and performs an initial refresh.
func NewCachedParser(ctx context.Context, jwksURL string) (*Parser, error) {
	if jwksURL == "" {
		return nil, ErrMissingJWKSURL
	}
	ctx, span := otel.Tracer(tracerScope).Start(ctx, "reval.jwtparse.init")
	defer span.End()

	client := &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport,
			otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
				return "http.jwks.fetch"
			}),
		),
	}

	jwtCache := jwk.NewCache(context.Background())
	err := jwtCache.Register(jwksURL, jwk.WithHTTPClient(client), jwk.WithRefreshInterval(24*time.Hour))
	if err != nil {
		return nil, err
	}
	_, err = jwtCache.Refresh(ctx, jwksURL)
	if err != nil {
		return nil, err
	}
	return &Parser{jwksURL: jwksURL, jwtCache: jwtCache}, nil
}

// Parse validates tokenString against the configured JWKS.
func (p *Parser) Parse(ctx context.Context, tokenString string) (*jwt.Token, error) {
	keyFunc := func(token *jwt.Token) (interface{}, error) {
		keySet, err := p.jwtCache.Get(ctx, p.jwksURL)
		if err != nil {
			return nil, errors.Join(ErrFailedToFetchJWK, err)
		}
		kid, ok := token.Header["kid"]
		if !ok {
			return nil, ErrJwtMissingKID
		}
		key, ok := keySet.LookupKeyID(fmt.Sprintf("%s", kid))
		if !ok {
			return nil, ErrJwtWrongKey
		}
		var raw interface{}
		return raw, key.Raw(&raw)
	}
	return jwt.Parse(tokenString, keyFunc)
}
