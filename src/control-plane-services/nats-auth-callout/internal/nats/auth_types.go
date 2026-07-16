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

package nats

import (
	"crypto/x509"
	"time"

	acConfig "github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins"

	"github.com/golang-jwt/jwt/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"
	"github.com/synadia-io/callout.go"
	"go.uber.org/zap"
)

const (
	CERTIFICATE = "CERTIFICATE"
)

type Service struct {
	Authorizer    *Authorizer
	Config        *acConfig.ServiceConfig
	PluginManager *plugins.Manager
	NatsConn      *nats.Conn
	jetstream     jetstream.JetStream
	logger        *zap.Logger
}

type Authorizer struct {
	AuthService    *callout.AuthorizationService
	tokenValidator *TokenValidator
	jwtGenerator   *JWTGenerator
	KnownCAs       map[string]*x509.Certificate
	TrustedCAs     string
	SigningKey     nkeys.KeyPair
	logger         *zap.Logger
}

type JWTGenerator struct {
	signingKey nkeys.KeyPair
	logger     *zap.Logger
}

type TokenValidator struct {
	logger *zap.Logger
}

type AuthRequest struct {
	UserNkey string
	Token    string
}

type AuthResult struct {
	JWT       string
	ExpiresAt time.Time
}

type TokenValidationResult struct {
	Claims      *parsedClaims
	AceID       string
	ServiceType ServiceType
	Namespace   string
}

type JWTInput struct {
	Account     string
	UserNkey    string
	AceID       string
	ServiceType ServiceType
	Claims      *parsedClaims
}

type parsedClaims struct {
	jwt.Claims
}

type K8sSubject struct {
	Namespace      string
	ServiceAccount string
}

type ServiceType string

const (
	ManagementAPI  ServiceType = "management-api"
	DGXCAgent      ServiceType = "dgxc-agent"
	UnknownService ServiceType = "unknown-service"
)
