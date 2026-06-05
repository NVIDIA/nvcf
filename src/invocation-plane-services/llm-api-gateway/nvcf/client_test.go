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
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	llmgatewaypb "github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/nvcf/pb"
)

func TestGRPCClientAuthorizeInvocation(t *testing.T) {
	t.Parallel()

	invocationService := &stubInvocationService{
		t:               t,
		clientProjectID: "project-789",
	}

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	llmgatewaypb.RegisterLlmGatewayServer(server, invocationService)

	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
	)
	if err != nil {
		t.Fatalf("create client conn: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})

	client := NewClientWithConn(conn, func() string { return "service-token" }, time.Second)

	authResponse, err := client.AuthorizeInvocation(context.Background(), "client-token", "fn-123")
	if err != nil {
		t.Fatalf("authorize invocation: %v", err)
	}
	if authResponse.RoutingKey != "fn-123" {
		t.Fatalf("routing key = %q, want fn-123", authResponse.RoutingKey)
	}
	if authResponse.ClientAuthID != "subject-123" {
		t.Fatalf("client auth id = %q, want subject-123", authResponse.ClientAuthID)
	}
	if authResponse.RateLimitKey != "nca-456" {
		t.Fatalf("rate limit key = %q, want nca-456", authResponse.RateLimitKey)
	}
	if authResponse.ProjectID != "project-789" {
		t.Fatalf("project id = %q, want project-789", authResponse.ProjectID)
	}
	if authResponse.AuthContext["ncaId"] != "nca-456" {
		t.Fatalf("auth context ncaId = %q, want nca-456", authResponse.AuthContext["ncaId"])
	}
	if authResponse.AuthContext["projectId"] != "project-789" {
		t.Fatalf("auth context projectId = %q, want project-789", authResponse.AuthContext["projectId"])
	}
	spec, ok := authResponse.ModelSpecs["gateway-model"]
	if !ok {
		t.Fatalf("model specs missing gateway-model: %#v", authResponse.ModelSpecs)
	}
	if spec.TokenRateLimit != "5-M,20-D" {
		t.Fatalf("token rate limit = %q, want 5-M,20-D", spec.TokenRateLimit)
	}
	if spec.RoutingMethod != "round_robin" {
		t.Fatalf("routing method = %q, want round_robin", spec.RoutingMethod)
	}
	if len(spec.URIs) != 1 || spec.URIs[0] != "https://example.com/model" {
		t.Fatalf("uris = %#v, want [https://example.com/model]", spec.URIs)
	}
}

func TestGRPCClientAuthorizeInvocationDoesNotFallbackRateLimitKey(t *testing.T) {
	t.Parallel()

	invocationService := &stubInvocationService{
		t:            t,
		clientNCAID:  "",
		clientAuthID: "subject-only",
	}

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	llmgatewaypb.RegisterLlmGatewayServer(server, invocationService)

	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
	)
	if err != nil {
		t.Fatalf("create client conn: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})

	client := NewClientWithConn(conn, func() string { return "service-token" }, time.Second)

	authResponse, err := client.AuthorizeInvocation(context.Background(), "client-token", "fn-123")
	if err != nil {
		t.Fatalf("authorize invocation: %v", err)
	}
	if authResponse.RateLimitKey != "" {
		t.Fatalf("rate limit key = %q, want empty", authResponse.RateLimitKey)
	}
}

func TestNewClientPropagatesTraceContext(t *testing.T) {
	oldPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTextMapPropagator(oldPropagator)
	})

	tracerProvider := sdktrace.NewTracerProvider()
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(context.Background())
	})

	invocationService := &stubInvocationService{
		t:               t,
		clientProjectID: "project-789",
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	llmgatewaypb.RegisterLlmGatewayServer(server, invocationService)

	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	secretsPath := t.TempDir() + "/secrets.json"
	if err := os.WriteFile(secretsPath, []byte(`{"nvcfApiToken":"service-token"}`), 0o600); err != nil {
		t.Fatalf("write secrets: %v", err)
	}

	client, err := NewClient(Config{
		Addr:        listener.Addr().String(),
		SecretsPath: secretsPath,
		Insecure:    true,
		Timeout:     time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})

	ctx, span := tracerProvider.Tracer("test").Start(context.Background(), "test-parent")
	defer span.End()

	if _, err := client.AuthorizeInvocation(ctx, "client-token", "fn-123"); err != nil {
		t.Fatalf("AuthorizeInvocation() error = %v", err)
	}

	if invocationService.traceparent == "" {
		t.Fatal("traceparent metadata is empty")
	}
	if !strings.Contains(invocationService.traceparent, span.SpanContext().TraceID().String()) {
		t.Fatalf("traceparent = %q, want trace id %s", invocationService.traceparent, span.SpanContext().TraceID())
	}
}

func TestNewClientFallsBackToOAuth2ClientCredentials(t *testing.T) {
	invocationService := &stubInvocationService{
		t:                  t,
		clientProjectID:    "project-789",
		expectedAuthHeader: "Bearer oauth-token",
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	llmgatewaypb.RegisterLlmGatewayServer(server, invocationService)

	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	oauth2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			t.Fatalf("oauth2 path = %q, want /token", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse oauth2 form: %v", err)
		}
		if got := r.Form.Get("scope"); got != oauth2InvocationScope {
			t.Fatalf("oauth2 scope = %q, want %s", got, oauth2InvocationScope)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"oauth-token","token_type":"Bearer","expires_in":300}`)
	}))
	t.Cleanup(oauth2Server.Close)

	secretsPath := t.TempDir() + "/secrets.json"
	if err := os.WriteFile(secretsPath, []byte(`{"id":"client-id","secret":"client-secret"}`), 0o600); err != nil {
		t.Fatalf("write secrets: %v", err)
	}

	client, err := NewClient(Config{
		Addr:               listener.Addr().String(),
		SecretsPath:        secretsPath,
		OAuth2ProviderHost: oauth2Server.URL,
		Insecure:           true,
		Timeout:            time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})

	if _, err := client.AuthorizeInvocation(context.Background(), "client-token", "fn-123"); err != nil {
		t.Fatalf("AuthorizeInvocation() error = %v", err)
	}
}

func TestNewAuthCredentialsRequiresTokenOrOAuth2Provider(t *testing.T) {
	secretsPath := t.TempDir() + "/secrets.json"
	if err := os.WriteFile(secretsPath, []byte(`{"id":"client-id","secret":"client-secret"}`), 0o600); err != nil {
		t.Fatalf("write secrets: %v", err)
	}

	_, _, err := newAuthCredentials(Config{SecretsPath: secretsPath})
	if err == nil {
		t.Fatal("newAuthCredentials() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "OAUTH2_PROVIDER_HOST") {
		t.Fatalf("newAuthCredentials() error = %q, want OAUTH2_PROVIDER_HOST", err)
	}
}

type stubInvocationService struct {
	llmgatewaypb.UnimplementedLlmGatewayServer

	t                  *testing.T
	clientAuthID       string
	clientNCAID        string
	clientProjectID    string
	expectedAuthHeader string
	traceparent        string
}

func (s *stubInvocationService) AuthLlmInvocation(
	ctx context.Context,
	req *llmgatewaypb.AuthLlmInvokeRequest,
) (*llmgatewaypb.AuthLlmInvokeResponse, error) {
	s.t.Helper()

	authHeader := incomingAuthorizationHeader(ctx)
	expectedAuthHeader := s.expectedAuthHeader
	if expectedAuthHeader == "" {
		expectedAuthHeader = "Bearer service-token"
	}
	if authHeader != expectedAuthHeader {
		s.t.Fatalf("authorization header = %q, want %s", authHeader, expectedAuthHeader)
	}
	if req.GetClientAuthorizationToken() != "client-token" {
		tok := req.GetClientAuthorizationToken()
		s.t.Fatalf("client authorization token = %q, want client-token", tok)
	}
	if req.GetRoutingKey() != "fn-123" {
		routingKey := req.GetRoutingKey()
		s.t.Fatalf("routing key = %q, want fn-123", routingKey)
	}
	s.traceparent = incomingMetadataValue(ctx, "traceparent")

	clientAuthID := s.clientAuthID
	if clientAuthID == "" {
		clientAuthID = "subject-123"
	}
	clientNCAID := s.clientNCAID
	if s.clientNCAID == "" && s.clientAuthID == "" {
		clientNCAID = "nca-456"
	}

	resp := &llmgatewaypb.AuthLlmInvokeResponse{
		RoutingKey:        "fn-123",
		ClientAuthSubject: clientAuthID,
		ModelSpecs: map[string]*llmgatewaypb.AuthLlmInvokeResponse_ModelSpec{
			"gateway-model": {
				Uris:           []string{"https://example.com/model"},
				TokenRateLimit: stringPtr("5-M,20-D"),
				RoutingMethod:  stringPtr("round_robin"),
			},
		},
	}
	if clientNCAID != "" {
		resp.AuthContext = map[string]string{
			"ncaId": clientNCAID,
		}
		if s.clientProjectID != "" {
			resp.AuthContext["projectId"] = s.clientProjectID
		}
	}

	return resp, nil
}

func incomingAuthorizationHeader(ctx context.Context) string {
	return incomingMetadataValue(ctx, "authorization")
}

func incomingMetadataValue(ctx context.Context, key string) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get(key)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func stringPtr(value string) *string {
	return &value
}
