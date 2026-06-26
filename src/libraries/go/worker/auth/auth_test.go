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
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"google.golang.org/grpc/credentials/oauth"
)

type stubTokenSource struct {
	tok *oauth2.Token
	err error
}

func (s stubTokenSource) Token() (*oauth2.Token, error) { return s.tok, s.err }

func newSource(ts oauth2.TokenSource) allowInsecureOauthTokenSource {
	return allowInsecureOauthTokenSource{oauth.TokenSource{TokenSource: ts}}
}

func TestGetRequestMetadata(t *testing.T) {
	t.Run("formats a bearer authorization header", func(t *testing.T) {
		src := newSource(stubTokenSource{tok: &oauth2.Token{AccessToken: "abc", TokenType: "Bearer"}})
		md, err := src.GetRequestMetadata(context.Background())
		require.NoError(t, err)
		require.Equal(t, map[string]string{"authorization": "Bearer abc"}, md)
	})
	t.Run("propagates token source errors", func(t *testing.T) {
		src := newSource(stubTokenSource{err: errors.New("no token")})
		md, err := src.GetRequestMetadata(context.Background())
		require.Error(t, err)
		require.Nil(t, md)
	})
}

func TestRequireTransportSecurityIsFalse(t *testing.T) {
	// Deliberate posture: worker RPCs carry tokens over in-cluster plaintext.
	// Pin it so it is not flipped accidentally.
	require.False(t, newSource(stubTokenSource{}).RequireTransportSecurity())
}

func TestSettableTokenSource(t *testing.T) {
	first := &oauth2.Token{AccessToken: "first"}
	second := &oauth2.Token{AccessToken: "second"}
	ts := NewSettableTokenSource(stubTokenSource{tok: first})

	got, err := ts.Token()
	require.NoError(t, err)
	require.Equal(t, "first", got.AccessToken)

	ts.SetTokenSource(stubTokenSource{tok: second})
	got, err = ts.Token()
	require.NoError(t, err)
	require.Equal(t, "second", got.AccessToken, "Token reflects the swapped source")
}

// Concurrent Token()/SetTokenSource() must be race-free (atomic pointer swap).
// Meaningful under `go test -race`.
func TestSettableTokenSourceConcurrent(t *testing.T) {
	ts := NewSettableTokenSource(stubTokenSource{tok: &oauth2.Token{AccessToken: "x"}})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = ts.Token()
			ts.SetTokenSource(stubTokenSource{tok: &oauth2.Token{AccessToken: "y"}})
		}()
	}
	wg.Wait()
}
