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

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	llmgatewaypb "github.com/NVIDIA/nvcf/llm-api-gateway/nvcf/pb"
)

const (
	metadataAuthorization = "authorization"
)

type Config struct {
	Addr        string
	SecretsPath string
	Insecure    bool
	Timeout     time.Duration
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

	conn, err := grpc.NewClient(
		cfg.Addr,
		grpc.WithTransportCredentials(transportCredentials),
	)
	if err != nil {
		return nil, fmt.Errorf("create nvcf grpc client: %w", err)
	}

	tokenSource := newCachedTokenSource(cfg.SecretsPath)

	return NewClientWithConn(conn, tokenSource, cfg.Timeout), nil
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
	resp, err := c.client.AuthLlmInvocation(callCtx, &llmgatewaypb.AuthLlmInvokeRequest{
		ClientAuthorizationToken: clientAuthorizationToken,
		RoutingKey:               functionID,
	})
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
	}, nil
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
			Tokenizer:      spec.GetTokenizer(),
			RoutingMethod:  spec.GetRoutingMethod(),
		}
	}
	return result
}
