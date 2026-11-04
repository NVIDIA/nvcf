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

package token

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestCacheTokenRoundTrip(t *testing.T) {
	// Nested path whose parent does not exist yet, to exercise dir creation.
	file := filepath.Join(t.TempDir(), "sub", "token.json")
	tok := &oauth2.Token{AccessToken: "abc", TokenType: "Bearer"}

	require.NoError(t, CacheToken(file, tok))

	info, err := os.Stat(file)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm(), "token file must be 0600")

	loaded, err := LoadCachedTokenIfExists(file)
	require.NoError(t, err)
	require.Equal(t, "abc", loaded.AccessToken)
}

func TestLoadCachedTokenMissingFile(t *testing.T) {
	loaded, err := LoadCachedTokenIfExists(filepath.Join(t.TempDir(), "nope.json"))
	require.NoError(t, err)
	require.Nil(t, loaded, "a missing file yields (nil, nil)")
}

func TestLoadCachedTokenCorruptJSON(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(file, []byte("{not json"), 0600))
	_, err := LoadCachedTokenIfExists(file)
	require.Error(t, err)
}

func waitSignal(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(msg)
	}
}

func futureToken() (Token, error) {
	return Token{Token: "t", Expiration: time.Now().Add(time.Hour)}, nil
}

func signalOnce(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func TestStartTokenRefresherHappyPath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // unblocks the post-refresh sleep so the goroutine exits

	called := make(chan struct{}, 1)
	StartTokenRefresher(ctx, "happy", false,
		func(context.Context) (Token, error) { return futureToken() },
		func(Token) error { signalOnce(called); return nil },
	)
	waitSignal(t, called, "callback was not invoked on a successful refresh")
}

func TestStartTokenRefresherJitterPath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	called := make(chan struct{}, 1)
	StartTokenRefresher(ctx, "jitter", true, // exercises the jitter branch
		func(context.Context) (Token, error) { return futureToken() },
		func(Token) error { signalOnce(called); return nil },
	)
	waitSignal(t, called, "callback was not invoked with jitter enabled")
}

func TestStartTokenRefresherGetTokenError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // unblocks the 1-minute failure sleep

	attempted := make(chan struct{}, 1)
	StartTokenRefresher(ctx, "geterr", false,
		func(context.Context) (Token, error) {
			signalOnce(attempted)
			return Token{}, errors.New("fetch failed")
		},
		func(Token) error { return nil },
	)
	waitSignal(t, attempted, "getTokenFunc was not attempted")
}

func TestStartTokenRefresherCallbackError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calledBack := make(chan struct{}, 1)
	StartTokenRefresher(ctx, "cberr", false,
		func(context.Context) (Token, error) { return futureToken() },
		func(Token) error { signalOnce(calledBack); return errors.New("callback failed") },
	)
	waitSignal(t, calledBack, "callback was not invoked")
}
