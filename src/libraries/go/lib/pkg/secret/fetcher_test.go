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

package secret

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecret_NewKeyFileFetcher(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { os.Remove(dir) })
	parentCtx := context.Background()

	// pass all empty
	fetcher, err := NewKeyFileFetcher(parentCtx)
	assert.EqualError(t, err, "one of secret key, env var, or file path must be provided")
	assert.Nil(t, fetcher)

	// add api key file that does not exist
	fetcher, err = NewKeyFileFetcher(parentCtx, WithSecretKeyFile(filepath.Join(dir, "does-not-exist-api-key")))
	assert.ErrorContains(t, err, "no such file or directory")
	assert.Nil(t, fetcher)

	// add NGC API Key hard-coded
	fetcher, err = NewKeyFileFetcher(parentCtx, WithSecretKey("an-api-key"))
	assert.NoError(t, err)
	assert.NotNil(t, fetcher)
	apiKey, err := fetcher.FetchToken(parentCtx)
	assert.Equal(t, "an-api-key", apiKey)
	assert.NoError(t, err)

	// add NGC API Key from env
	os.Setenv("TEST_SEC_KEY_ENV", "an-api-key")
	t.Cleanup(func() { os.Unsetenv("TEST_SEC_KEY_ENV") })
	fetcher, err = NewKeyFileFetcher(parentCtx, WithSecretKeyEnvVar("TEST_SEC_KEY_ENV"))
	assert.NoError(t, err)
	assert.NotNil(t, fetcher)
	apiKeyFromEnv, err := fetcher.FetchToken(parentCtx)
	assert.Equal(t, "an-api-key", apiKeyFromEnv)
	assert.NoError(t, err)

	// set both static and file NGC API key sources
	ctx, cancel := context.WithCancel(parentCtx)
	t.Cleanup(cancel)
	ngcAPIKeyFile := filepath.Join(dir, "api-key")
	require.NoError(t, os.WriteFile(ngcAPIKeyFile, []byte{}, 0755))
	fetcher, err = NewKeyFileFetcher(ctx, WithSecretKey("an-api-key-test"), WithSecretKeyFile(ngcAPIKeyFile))
	assert.NoError(t, err)
	assert.NotNil(t, fetcher)

	// ensure the first token fetch fails at first with empty data
	_, err = fetcher.FetchToken(parentCtx)
	assert.EqualError(t, err, "the provided Secret key is empty")

	// ensure the token fetch works eventually
	apiKeyPrefix := "an-api-key-value-"
	require.NoError(t, os.WriteFile(ngcAPIKeyFile, []byte(apiKeyPrefix+"0"), 0755))
	require.Eventually(t, func() bool {
		token, err := fetcher.FetchToken(parentCtx)
		if err != nil {
			t.Logf("failed to fetch the token, error: %v", err)
			return false
		}
		if !strings.HasPrefix(token, apiKeyPrefix) {
			t.Logf("token %s does not yet have prefix", token)
			return false
		}
		return true
	}, 1*time.Second, 5*time.Millisecond)

	// Start a job queue to query the fetcher in multiple threads to attempt to get a data race
	workerCtx, workerCtxCancel := context.WithCancel(parentCtx)
	t.Cleanup(workerCtxCancel)
	for i := 0; i < 5; i++ {
		go func() {
			ticker := time.NewTicker(10 * time.Microsecond)
			for {
				select {
				case <-ticker.C:
					token, err := fetcher.FetchToken(workerCtx)
					assert.NoError(t, err)
					assert.True(t, strings.HasPrefix(token, apiKeyPrefix))
				case <-workerCtx.Done():
					return
				}
			}
		}()
	}

	type testTemplate struct {
		WriteByRename bool
		Title         string
	}

	tests := []testTemplate{
		{
			WriteByRename: false,
			Title:         "write directly to file",
		},
		{
			WriteByRename: true,
			Title:         "write to file by rename",
		},
	}
	for _, test := range tests {
		t.Run(test.Title, func(t *testing.T) {
			// Change the value 5 times with a slight pause in-between
			// in this case we're going to write the file without a rename
			lastWorker := 5
			for i := 1; i <= lastWorker; i++ {
				time.Sleep(5 * time.Millisecond)
				apiKeyVal := fmt.Sprintf("%s%d", apiKeyPrefix, i)
				if test.WriteByRename {
					// Move the file rather than writing this exposes a
					// potential issue with watching an individual file
					// in that the atomic move will break the test
					// See https://github.com/fsnotify/fsnotify?tab=readme-ov-file#watching-a-file-doesnt-work-well
					tmpFile, err := os.CreateTemp("", "test-secret-new-api-key-*")
					require.NoError(t, err)
					t.Cleanup(func() { os.Remove(tmpFile.Name()) })
					require.NoError(t, os.Chmod(tmpFile.Name(), 0755))
					require.NoError(t, os.WriteFile(tmpFile.Name(), []byte(apiKeyVal), 0755))
					require.NoError(t, os.Rename(tmpFile.Name(), ngcAPIKeyFile))
				} else {
					require.NoError(t, os.WriteFile(ngcAPIKeyFile, []byte(apiKeyVal), 0755))
				}
			}
			workerCtxCancel()

			expectedAPIKey := fmt.Sprintf("%s%d", apiKeyPrefix, lastWorker)
			verifyFinalAPIKeyFunc := func() bool {
				apiKey, err = fetcher.FetchToken(ctx)
				if err != nil {
					t.Logf("failed to retrieve token, error: %v", err)
					return false
				}
				if expectedAPIKey != apiKey {
					t.Logf("final apikey value does not match, got=%s, want=%s", expectedAPIKey, apiKey)
					return false
				}

				return true
			}
			require.Eventuallyf(t, verifyFinalAPIKeyFunc, 50*time.Millisecond, 5*time.Millisecond, "final apiKey did not match expected value %s", expectedAPIKey)
		})
	}
}

func TestKeyFileFetcherWithUpdateListener(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	f := t.TempDir() + "/test.txt"

	called := atomic.Int32{}
	expectedResults := [][]byte{
		[]byte("test-key-1"),
		[]byte("test-key-2"),
	}
	done := make(chan interface{})
	var closeOnce sync.Once
	updateFunc := func(ctx context.Context, r io.Reader) {
		callCount := int(called.Add(1))
		if callCount <= len(expectedResults) {
			b, err := io.ReadAll(r)
			require.NoError(t, err)
			assert.Equal(t, expectedResults[callCount-1], b)
		}
		if callCount >= len(expectedResults) {
			closeOnce.Do(func() { close(done) })
		}
	}

	assert.NoError(t, os.WriteFile(f, expectedResults[0], 0755))
	fetcher, err := NewKeyFileFetcher(ctx,
		WithSecretKeyFile(f),
		WithOnKeyFileUpdateListener(updateFunc))
	require.NoError(t, err)
	assert.NotNil(t, fetcher)

	assert.NoError(t, os.WriteFile(f, expectedResults[1], 0755))

	select {
	case <-ctx.Done():
		t.Errorf("timeout waiting for updatefunc calls")
	case <-done:
	}
	assert.GreaterOrEqual(t, int(called.Load()), len(expectedResults))
}

func TestKeyFileFetcherWithFileFetcherOptionsCreateDirAndAllowInitialFailure(t *testing.T) {
	parentCtx := context.Background()
	ctx, cancel := context.WithCancel(parentCtx)
	t.Cleanup(cancel)

	keyFilePath := filepath.Join(t.TempDir(), "does-not-exist-yet", "secret.key")
	fetcher, err := NewKeyFileFetcher(ctx,
		WithSecretKeyFile(keyFilePath),
		WithKeyFileFetcherOptions(
			WithAllowInitialFailure(true),
			WithCreateDirIfNotExist(true),
			WithForceFileRefreshInterval(5*time.Millisecond),
		),
	)
	require.NoError(t, err)
	require.NotNil(t, fetcher)

	expectedToken := "key-from-file"
	require.NoError(t, os.WriteFile(keyFilePath, []byte(expectedToken), 0755))
	require.Eventually(t, func() bool {
		token, err := fetcher.FetchToken(parentCtx)
		if err != nil {
			t.Logf("failed to fetch key from file: %v", err)
			return false
		}
		return token == expectedToken
	}, 500*time.Millisecond, 5*time.Millisecond)
}

func TestKeyFileFetcherWithFileFetcherOptionsMaxFileSize(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	keyFilePath := filepath.Join(t.TempDir(), "secret.key")
	require.NoError(t, os.WriteFile(keyFilePath, []byte("123456"), 0755))

	fetcher, err := NewKeyFileFetcher(ctx,
		WithSecretKeyFile(keyFilePath),
		WithKeyFileFetcherOptions(WithMaxFileSize(4)),
	)
	require.NoError(t, err)
	require.NotNil(t, fetcher)

	token, err := fetcher.FetchToken(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1234", token)
}
func TestKeyFileFetcher_FetchSecretKey(t *testing.T) {
	ctx := context.Background()

	// Test with static key
	fetcher, err := NewKeyFileFetcher(ctx, WithSecretKey("static-key"))
	require.NoError(t, err)

	key, err := fetcher.FetchSecretKey(ctx)
	require.NoError(t, err)
	assert.Equal(t, "static-key", key)

	// Test with empty env var
	os.Unsetenv("NON_EXISTENT_ENV_VAR")
	fetcher, err = NewKeyFileFetcher(ctx, WithSecretKeyEnvVar("NON_EXISTENT_ENV_VAR"))
	require.NoError(t, err)

	_, err = fetcher.FetchSecretKey(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the provided Secret key is empty")
}

func TestKeyFileFetcher_MissingOptions(t *testing.T) {
	ctx := context.Background()

	// Test with no options
	fetcher, err := NewKeyFileFetcher(ctx)
	require.Error(t, err)
	assert.Nil(t, fetcher)
	assert.Contains(t, err.Error(), "one of secret key, env var, or file path must be provided")
}

func TestKeyFileFetcher_EnvVarWithValue(t *testing.T) {
	ctx := context.Background()

	// Set env var with value
	os.Setenv("TEST_KEY_ENV_VAR", "env-var-key-value")
	t.Cleanup(func() { os.Unsetenv("TEST_KEY_ENV_VAR") })

	fetcher, err := NewKeyFileFetcher(ctx, WithSecretKeyEnvVar("TEST_KEY_ENV_VAR"))
	require.NoError(t, err)
	require.NotNil(t, fetcher)

	key, err := fetcher.FetchToken(ctx)
	require.NoError(t, err)
	assert.Equal(t, "env-var-key-value", key)
}

func TestKeyFileFetcher_WithUpdateListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key-file")
	require.NoError(t, os.WriteFile(keyFile, []byte("initial-key"), 0644))

	updateCount := &atomic.Int32{}

	listener := func(ctx context.Context, r io.Reader) {
		updateCount.Add(1)
	}

	fetcher, err := NewKeyFileFetcher(ctx,
		WithSecretKeyFile(keyFile),
		WithOnKeyFileUpdateListener(listener),
	)
	require.NoError(t, err)

	// Wait for initial load
	require.Eventually(t, func() bool {
		key, err := fetcher.FetchSecretKey(ctx)
		return err == nil && key == "initial-key"
	}, 2*time.Second, 10*time.Millisecond)

	// Update file
	require.NoError(t, os.WriteFile(keyFile, []byte("updated-key"), 0644))

	// Wait for update
	require.Eventually(t, func() bool {
		key, _ := fetcher.FetchSecretKey(ctx)
		return key == "updated-key"
	}, 2*time.Second, 10*time.Millisecond)

	assert.Greater(t, updateCount.Load(), int32(0))
}

func TestKeyFileFetcher_FetchTokenAdapter(t *testing.T) {
	ctx := context.Background()
	fetcher, err := NewKeyFileFetcher(ctx, WithSecretKey("test-token"))
	require.NoError(t, err)

	// FetchToken should work as an adapter
	token, err := fetcher.FetchToken(ctx)
	require.NoError(t, err)
	assert.Equal(t, "test-token", token)
}

func TestKeyFileFetcher_EmptyEnvVar(t *testing.T) {
	ctx := context.Background()

	// Unset env var
	os.Unsetenv("TEST_EMPTY_ENV")

	fetcher, err := NewKeyFileFetcher(ctx, WithSecretKeyEnvVar("TEST_EMPTY_ENV"))
	require.NoError(t, err)

	_, err = fetcher.FetchSecretKey(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the provided Secret key is empty")
}

func TestKeyFileFetcher_FileReadError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Try to create a fetcher for a directory (not a file), which should fail
	dir := t.TempDir()
	_, err := NewKeyFileFetcher(ctx, WithSecretKeyFile(dir))
	require.Error(t, err)
}

func TestKeyFileFetcher_WithKeyFileFetcherOptions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key-file")
	require.NoError(t, os.WriteFile(keyFile, []byte("test-key"), 0644))

	called := false
	listener := func(ctx context.Context, r io.Reader) {
		called = true
	}

	fetcher, err := NewKeyFileFetcher(ctx,
		WithSecretKeyFile(keyFile),
		WithKeyFileFetcherOptions(WithOnFileUpdateListener(listener)),
	)
	require.NoError(t, err)

	// Wait for initial load which should trigger listener
	require.Eventually(t, func() bool {
		key, err := fetcher.FetchSecretKey(ctx)
		return err == nil && key == "test-key" && called
	}, 2*time.Second, 10*time.Millisecond)

	assert.True(t, called, "Listener should have been called")
}
