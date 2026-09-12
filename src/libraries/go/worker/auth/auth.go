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

package auth

import (
	"context"
	"sync/atomic"

	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/oauth"
)

type allowInsecureOauthTokenSource struct {
	oauth.TokenSource
	requireTLS bool
}

func (ts allowInsecureOauthTokenSource) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	token, err := ts.Token()
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"authorization": token.Type() + " " + token.AccessToken,
	}, nil
}

// RequireTransportSecurity is false for legacy NVCF-issued tokens, which may travel over
// in-cluster plaintext, and true for a mounted projected ServiceAccount token, which must
// only be presented over TLS.
func (ts allowInsecureOauthTokenSource) RequireTransportSecurity() bool {
	return ts.requireTLS
}

// GrpcTokenFromSource attaches ts as per-RPC bearer credentials. requireTLS must be true
// when ts serves a mounted projected ServiceAccount token.
func GrpcTokenFromSource(ts oauth2.TokenSource, requireTLS bool) grpc.CallOption {
	return grpc.PerRPCCredentials(allowInsecureOauthTokenSource{
		TokenSource: oauth.TokenSource{TokenSource: ts},
		requireTLS:  requireTLS,
	})
}

func NewSettableTokenSource(originalSource oauth2.TokenSource) *SettableTokenSource {
	ts := &SettableTokenSource{}
	ts.source.Store(&originalSource)
	return ts
}

type SettableTokenSource struct {
	source atomic.Pointer[oauth2.TokenSource]
}

func (ts *SettableTokenSource) Token() (*oauth2.Token, error) {
	return (*ts.source.Load()).Token()
}

func (ts *SettableTokenSource) SetTokenSource(newSource oauth2.TokenSource) {
	ts.source.Store(&newSource)
}
