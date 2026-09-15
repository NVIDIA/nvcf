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

package nvcf

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	llmgatewaypb "github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/nvcf/pb"
	nvauth "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/auth"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

const (
	metadataAuthorization = "authorization"
	nvcfAPITokenKey       = "nvcfApiToken"
	oauth2InvocationScope = "llm:check_invocation"
)

type Config struct {
	Addr               string
	SecretsPath        string
	OAuth2ProviderHost string
	Insecure           bool
	Timeout            time.Duration
}

type Client interface {
	io.Closer
	AuthorizeInvocation(
		ctx context.Context,
		clientAuthorizationToken string,
		functionID string,
	) (*InvocationAuthResponse, error)
}

type GRPCClient struct {
	client      llmgatewaypb.LlmGatewayClient
	closer      io.Closer
	tokenSource func() string
	timeout     time.Duration
}

func NewClient(cfg Config) (*GRPCClient, error) {
	if cfg.Addr == "" {
		return nil, fmt.Errorf("nvcf grpc addr is required")
	}
	if cfg.SecretsPath == "" {
		return nil, fmt.Errorf("nvcf secrets path is required")
	}

	var transportCredentials credentials.TransportCredentials
	if cfg.Insecure {
		transportCredentials = insecure.NewCredentials()
	} else {
		transportCredentials = credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS12,
		})
	}

	tokenSource, rpcCredentials, err := newAuthCredentials(cfg)
	if err != nil {
		return nil, err
	}

	dialOptions := []grpc.DialOption{
		grpc.WithTransportCredentials(transportCredentials),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	}
	if rpcCredentials != nil {
		dialOptions = append(dialOptions, grpc.WithPerRPCCredentials(rpcCredentials))
	}

	conn, err := grpc.NewClient(
		cfg.Addr,
		dialOptions...,
	)
	if err != nil {
		return nil, fmt.Errorf("create nvcf grpc client: %w", err)
	}

	return NewClientWithConn(conn, tokenSource, cfg.Timeout), nil
}

func newAuthCredentials(cfg Config) (func() string, credentials.PerRPCCredentials, error) {
	if token, err := readNVCFAPIToken(cfg.SecretsPath); err == nil && token != "" {
		return newCachedTokenSource(cfg.SecretsPath), nil, nil
	}

	if cfg.OAuth2ProviderHost == "" {
		return nil, nil, fmt.Errorf(
			"nvcf secrets file must contain %s or OAUTH2_PROVIDER_HOST must be configured",
			nvcfAPITokenKey,
		)
	}

	authnConfig := &nvauth.AuthnConfig{
		OIDCConfig: &nvauth.ProviderConfig{
			Host:            cfg.OAuth2ProviderHost,
			CredentialsFile: cfg.SecretsPath,
			Scopes:          []string{oauth2InvocationScope},
		},
		RefreshConfig: &nvauth.RefreshConfig{
			Interval: int64((5 * time.Minute).Seconds()),
		},
		DisableTransportSecurity: cfg.Insecure,
	}
	rpcCredentials, err := authnConfig.GRPCClientWithAuth()
	if err != nil {
		return nil, nil, fmt.Errorf("configure oauth2 nvcf grpc auth: %w", err)
	}
	if rpcCredentials == nil {
		return nil, nil, fmt.Errorf("configure oauth2 nvcf grpc auth: no credentials returned")
	}
	return nil, rpcCredentials, nil
}

func NewClientWithConn(
	conn grpc.ClientConnInterface,
	tokenSource func() string,
	timeout time.Duration,
) *GRPCClient {
	client := &GRPCClient{
		client:      llmgatewaypb.NewLlmGatewayClient(conn),
		tokenSource: tokenSource,
		timeout:     timeout,
	}
	if closer, ok := conn.(io.Closer); ok {
		client.closer = closer
	}
	return client
}

// TODO: replace with fsnotify file watcher for immediate refresh
const tokenCacheTTL = 60 * time.Second

func readNVCFAPIToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var secrets struct {
		NVCFApiToken string `json:"nvcfApiToken"`
	}
	if err := json.Unmarshal(data, &secrets); err != nil {
		return "", err
	}
	if secrets.NVCFApiToken == "" {
		return "", fmt.Errorf("%s is empty", nvcfAPITokenKey)
	}
	return secrets.NVCFApiToken, nil
}

func newCachedTokenSource(path string) func() string {
	var (
		mu        sync.Mutex
		cached    string
		expiresAt time.Time
	)
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		if time.Now().Before(expiresAt) {
			return cached
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return cached
		}
		var secrets struct {
			NVCFApiToken string `json:"nvcfApiToken"`
		}
		if err := json.Unmarshal(data, &secrets); err != nil {
			return cached
		}
		cached = secrets.NVCFApiToken
		expiresAt = time.Now().Add(tokenCacheTTL)
		return cached
	}
}

func (c *GRPCClient) Close() error {
	if c == nil || c.closer == nil {
		return nil
	}
	return c.closer.Close()
}

func (c *GRPCClient) AuthorizeInvocation(
	ctx context.Context,
	clientAuthorizationToken string,
	functionID string,
) (*InvocationAuthResponse, error) {
	if c == nil || c.client == nil {
		return nil, fmt.Errorf("nvcf grpc client is nil")
	}

	if ctx == nil {
		ctx = context.Background()
	}

	callCtx := ctx
	if c.timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	if c.tokenSource != nil {
		if token := c.tokenSource(); token != "" {
			callCtx = metadata.AppendToOutgoingContext(
				callCtx,
				metadataAuthorization,
				"Bearer "+token,
			)
		}
	}
	start := time.Now()
	resp, err := c.client.AuthLlmInvocation(callCtx, &llmgatewaypb.AuthLlmInvokeRequest{
		ClientAuthorizationToken: clientAuthorizationToken,
		RoutingKey:               functionID,
	})
	telemetry.RecordAuthInvocation(ctx, time.Since(start), status.Code(err).String())
	if err != nil {
		return nil, err
	}

	authContext := resp.GetAuthContext()
	return &InvocationAuthResponse{
		RoutingKey:   resp.GetRoutingKey(),
		ClientAuthID: resp.GetClientAuthSubject(),
		ProjectID:    deriveProjectID(authContext),
		AuthContext:  authContext,
		RateLimitKey: deriveRateLimitKey(authContext),
		ModelSpecs:   modelSpecsFromProto(resp.GetModelSpecs()),
		Priority:     priorityFromProto(resp),
	}, nil
}

func priorityFromProto(resp *llmgatewaypb.AuthLlmInvokeResponse) *uint32 {
	if resp == nil || resp.Priority == nil {
		return nil
	}
	value := resp.GetPriority()
	return &value
}

func modelSpecsFromProto(specs map[string]*llmgatewaypb.AuthLlmInvokeResponse_ModelSpec) map[string]ModelSpec {
	if specs == nil {
		return nil
	}
	if len(specs) == 0 {
		return map[string]ModelSpec{}
	}

	result := make(map[string]ModelSpec, len(specs))
	for key, spec := range specs {
		result[key] = ModelSpec{
			URIs:           spec.GetUris(),
			TokenRateLimit: spec.GetTokenRateLimit(),
			RoutingMethod:  spec.GetRoutingMethod(),
		}
	}
	return result
}
