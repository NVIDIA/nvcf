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

package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/types"
)

const tracerName = "nvcf-nats-auth-callout-service/auth"

// AuthHandler handles NATS authentication callout requests
type AuthHandler struct {
	pluginManager *plugins.Manager
	logger        *zap.Logger
	signingKey    nkeys.KeyPair
	tracer        trace.Tracer
}

// NewAuthHandler creates a new authentication handler
func NewAuthHandler(pm *plugins.Manager, logger *zap.Logger, signingKey nkeys.KeyPair) *AuthHandler {
	return &AuthHandler{
		pluginManager: pm,
		logger:        logger,
		signingKey:    signingKey,
		tracer:        otel.Tracer(tracerName),
	}
}

func (ah *AuthHandler) HandleAuthRequest(ctx context.Context, req *jwt.AuthorizationRequest) (string, error) {
	ctx, span := ah.tracer.Start(ctx, "auth.HandleAuthRequest", trace.WithSpanKind(trace.SpanKindServer))
	defer span.End()

	// Add basic request attributes
	span.SetAttributes(
		attribute.String("client.type", req.ClientInformation.Type),
		attribute.String("client.name", req.ClientInformation.Name),
		attribute.String("user.nkey", req.UserNkey),
	)

	ah.logger.Info("Auth request received",
		zap.String("client_type", req.ClientInformation.Type),
		zap.String("client_name", req.ClientInformation.Name),
	)

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	timer := prometheus.NewTimer(AuthRequestDuration)
	defer timer.ObserveDuration()

	ar, err := parseRequest(req)
	if err != nil {
		ah.logger.Error("Failed to parse auth request", zap.Error(err))
		recordError(span, nil, nil, err)
		return "", err
	}

	// Add parsed request attributes
	span.SetAttributes(
		attribute.String("account", ar.Account),
		attribute.String("plugin.name", ar.PluginName),
	)

	// Authenticate with plugin manager
	result, err := ah.pluginManager.Authenticate(ctx, ar)
	if err != nil {
		ah.logger.Error("Authentication failed",
			zap.Error(err),
			zap.String("account", ar.Account),
			zap.String("plugin_name", ar.PluginName),
		)
		recordError(span, ar, nil, err)
		return "", err
	}

	// Add result attributes
	span.SetAttributes(
		attribute.String("user.id", result.UserID),
		attribute.String("result.account", result.Account),
	)

	// Generate JWT
	token, err := ah.generateJWTFromResult(req, result)
	if err != nil {
		recordError(span, ar, result, err)
		return "", err
	}

	ah.logger.Info("Authentication successful",
		zap.String("client_info", req.ClientInformation.Name),
		zap.String("client_type", req.ClientInformation.Type),
		zap.String("account", result.Account),
		zap.String("plugin_name", ar.PluginName),
	)
	RecordAuthSuccess(ar.PluginName, result.Account)

	return token, nil
}

func parseRequest(req *jwt.AuthorizationRequest) (*types.Request, error) {
	var ar *types.Request
	switch {
	case req.ConnectOptions.Token != "":
		var err error
		ar, err = decodeB64Token(req.ConnectOptions.Token)
		if err != nil {
			return nil, fmt.Errorf("failed to decode auth request: %w", err)
		}
	case req.ConnectOptions.Nkey != "":
		ar = &types.Request{
			// the nkey plugin maintains its own internal account list and is special cased in the plugin manager
			PluginName: "nkey",
		}
	default:
		return nil, fmt.Errorf("missing token in auth request")
	}
	ar.FullRequest = req
	return ar, nil
}

func (ah *AuthHandler) generateJWTFromResult(req *jwt.AuthorizationRequest, result *types.Result) (string, error) {
	// Create user claims using the request's UserNkey
	uc := jwt.NewUserClaims(req.UserNkey)

	// Set the account audience
	uc.Audience = result.Account

	// Set user name if available
	if result.UserID != "" {
		uc.Name = result.UserID
	}

	// Set expiration
	now := time.Now()
	uc.IssuedAt = now.Unix()
	if result.TTL > 0 {
		uc.Expires = now.Add(result.TTL).Unix()
	}
	uc.NotBefore = now.Unix()

	// Set permissions if provided
	if result.Permissions != nil {
		if result.Permissions.Publish != nil {
			uc.Pub.Allow = result.Permissions.Publish.Allow
			uc.Pub.Deny = result.Permissions.Publish.Deny
		}
		if result.Permissions.Subscribe != nil {
			uc.Sub.Allow = result.Permissions.Subscribe.Allow
			uc.Sub.Deny = result.Permissions.Subscribe.Deny
		}
		if result.Permissions.Response != nil {
			uc.Resp = &jwt.ResponsePermission{
				MaxMsgs: result.Permissions.Response.MaxMsgs,
				Expires: result.Permissions.Response.TTL,
			}
		}
	}

	uc.Subs = -1           // Unlimited subscriptions
	uc.Data = -1           // Unlimited data
	uc.Limits.Payload = -1 // Unlimited payload

	// Generate the JWT using the cached signing key
	token, err := uc.Encode(ah.signingKey)
	if err != nil {
		return "", fmt.Errorf("failed to encode JWT: %w", err)
	}
	return token, nil
}

// allow padded and unpadded url base64 encoding
func decodeB64Token(token string) (*types.Request, error) {
	enc := base64.URLEncoding
	if len(token)%4 != 0 {
		enc = base64.RawURLEncoding
	}
	jsonBytes, err := enc.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("failed to decode auth request: %w", err)
	}
	var ar types.Request
	err = json.Unmarshal(jsonBytes, &ar)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal auth request: %w", err)
	}
	return &ar, nil
}

func recordError(span trace.Span, ar *types.Request, result *types.Result, err error) {
	span.RecordError(err)
	appError := &types.Error{}
	cause := CauseServer
	if errors.As(err, &appError) {
		if appError.Code/100 == 4 {
			cause = CauseClient
		}
	}
	if cause == CauseServer {
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "client caused error")
	}
	pluginName := "unknown"
	account := "unknown"
	if ar != nil {
		pluginName = ar.PluginName
		account = ar.Account
	}
	if result != nil {
		account = result.Account
	}
	RecordAuthFailure(pluginName, account, cause)
}
