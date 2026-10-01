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

package statickeys

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func digestHex(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func writeKeyFile(t *testing.T, path string, keys ...FileKey) {
	t.Helper()
	data, err := json.Marshal(File{Keys: keys})
	if err != nil {
		t.Fatalf("marshal key file: %v", err)
	}
	writeRaw(t, path, string(data))
}

func writeRaw(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
}

// fakeClock drives the reload interval without sleeping.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestAuthorizer(t *testing.T, path string, clock *fakeClock) *Authorizer {
	t.Helper()
	a, err := New(path)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	a.now = clock.Now
	return a
}

// logContext routes the authorizer's logs into buf.
func logContext(buf *bytes.Buffer) context.Context {
	logger := zerolog.New(buf)
	return logger.WithContext(context.Background())
}

func requireUnauthenticated(t *testing.T, err error) {
	t.Helper()
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("error = %v, want code Unauthenticated", err)
	}
	if st, _ := status.FromError(err); st.Message() != "authentication failed" {
		t.Fatalf("error message = %q, want the generic message", st.Message())
	}
}

func TestNewRequiresPath(t *testing.T) {
	t.Parallel()

	if _, err := New(""); err == nil {
		t.Fatal("New(\"\") error = nil, want error")
	}
}

func TestAuthorizeInvocation(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "api-keys.json")
	writeKeyFile(t, path,
		FileKey{ID: "team-a", SHA256: digestHex("sk-team-a")},
		FileKey{ID: "team-b", SHA256: strings.ToUpper(digestHex("sk-team-b"))},
	)
	a := newTestAuthorizer(t, path, newFakeClock())

	tests := []struct {
		name   string
		token  string
		wantID string
	}{
		{name: "valid key", token: "sk-team-a", wantID: "team-a"},
		{name: "valid key with uppercase digest in file", token: "sk-team-b", wantID: "team-b"},
		{name: "wrong key", token: "sk-team-a-wrong"},
		{name: "key of an id not in the file", token: "sk-team-c"},
		{name: "digest presented instead of key", token: digestHex("sk-team-a")},
		{name: "empty token", token: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := a.AuthorizeInvocation(context.Background(), tc.token, "ignored-routing-key")
			if tc.wantID == "" {
				requireUnauthenticated(t, err)
				if resp != nil {
					t.Fatalf("response = %+v, want nil", resp)
				}
				return
			}
			if err != nil {
				t.Fatalf("AuthorizeInvocation() error = %v", err)
			}
			if resp.RoutingKey != "" {
				t.Fatalf("routing key = %q, want empty", resp.RoutingKey)
			}
			if resp.ClientAuthID != "api-key:"+tc.wantID {
				t.Fatalf("client auth id = %q, want api-key:%s", resp.ClientAuthID, tc.wantID)
			}
			if resp.RateLimitKey != tc.wantID {
				t.Fatalf("rate limit key = %q, want %s", resp.RateLimitKey, tc.wantID)
			}
			if resp.Priority != nil {
				t.Fatalf("priority = %d, want nil", *resp.Priority)
			}
			if resp.ModelSpecs != nil {
				t.Fatalf("model specs = %v, want nil", resp.ModelSpecs)
			}
			if resp.ProjectID != "" || resp.AuthContext != nil {
				t.Fatalf("unexpected project or auth context: %+v", resp)
			}
		})
	}
}

func TestAuthorizeInvocationReturnsIndependentResponses(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "api-keys.json")
	writeKeyFile(t, path, FileKey{ID: "team-a", SHA256: digestHex("sk-team-a")})
	a := newTestAuthorizer(t, path, newFakeClock())

	first, err := a.AuthorizeInvocation(context.Background(), "sk-team-a", "")
	if err != nil {
		t.Fatalf("AuthorizeInvocation() error = %v", err)
	}
	first.RateLimitKey = "mutated"

	second, err := a.AuthorizeInvocation(context.Background(), "sk-team-a", "")
	if err != nil {
		t.Fatalf("AuthorizeInvocation() error = %v", err)
	}
	if second.RateLimitKey != "team-a" {
		t.Fatalf("rate limit key = %q, want team-a", second.RateLimitKey)
	}
}

func TestAuthorizeInvocationRejectsMalformedFiles(t *testing.T) {
	t.Parallel()

	valid := digestHex("sk-team-a")
	tests := []struct {
		name    string
		content string
	}{
		{name: "not json", content: "keys: [team-a]"},
		{name: "trailing garbage", content: `{"keys":[{"id":"team-a","sha256":"` + valid + `"}]} extra`},
		{name: "empty object", content: `{}`},
		{name: "empty key list", content: `{"keys":[]}`},
		{name: "missing id", content: `{"keys":[{"sha256":"` + valid + `"}]}`},
		{name: "id with separator", content: `{"keys":[{"id":"team:a","sha256":"` + valid + `"}]}`},
		{name: "missing digest", content: `{"keys":[{"id":"team-a"}]}`},
		{name: "short digest", content: `{"keys":[{"id":"team-a","sha256":"abcd"}]}`},
		{name: "non hex digest", content: `{"keys":[{"id":"team-a","sha256":"` + strings.Repeat("zz", 32) + `"}]}`},
		{
			name: "duplicate id",
			content: `{"keys":[{"id":"team-a","sha256":"` + valid + `"},` +
				`{"id":"team-a","sha256":"` + digestHex("sk-other") + `"}]}`,
		},
		{
			name: "duplicate digest",
			content: `{"keys":[{"id":"team-a","sha256":"` + valid + `"},` +
				`{"id":"team-b","sha256":"` + valid + `"}]}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "api-keys.json")
			writeRaw(t, path, tc.content)
			a := newTestAuthorizer(t, path, newFakeClock())

			// A key that is valid inside the malformed file is refused too:
			// the whole file is rejected, not only the offending entry.
			_, err := a.AuthorizeInvocation(context.Background(), "sk-team-a", "")
			requireUnauthenticated(t, err)
		})
	}
}

func TestAuthorizeInvocationMissingFileFailsClosedAndLogsOnce(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "api-keys.json")
	clock := newFakeClock()
	a := newTestAuthorizer(t, path, clock)

	var logs bytes.Buffer
	ctx := logContext(&logs)
	for range 3 {
		_, err := a.AuthorizeInvocation(ctx, "sk-team-a", "")
		requireUnauthenticated(t, err)
		// Cross the reload interval so the next call re-reads the file and
		// fails again with the same error.
		clock.Advance(ReloadInterval)
	}

	if got := strings.Count(logs.String(), "static API key file unusable"); got != 1 {
		t.Fatalf("error logged %d times, want 1:\n%s", got, logs.String())
	}
	if strings.Contains(logs.String(), "sk-team-a") || strings.Contains(logs.String(), digestHex("sk-team-a")) {
		t.Fatalf("logs contain the presented key or its digest:\n%s", logs.String())
	}
}

func TestAuthorizeInvocationLogsEachDistinctError(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "api-keys.json")
	clock := newFakeClock()
	a := newTestAuthorizer(t, path, clock)

	var logs bytes.Buffer
	ctx := logContext(&logs)

	_, err := a.AuthorizeInvocation(ctx, "sk-team-a", "")
	requireUnauthenticated(t, err)

	writeRaw(t, path, "not json")
	for range 2 {
		clock.Advance(ReloadInterval)
		_, err = a.AuthorizeInvocation(ctx, "sk-team-a", "")
		requireUnauthenticated(t, err)
	}

	if got := strings.Count(logs.String(), "static API key file unusable"); got != 2 {
		t.Fatalf("error logged %d times, want 2 (missing, then malformed):\n%s", got, logs.String())
	}
}

func TestAuthorizeInvocationRecoversWhenFileBecomesValid(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "api-keys.json")
	clock := newFakeClock()
	a := newTestAuthorizer(t, path, clock)

	var logs bytes.Buffer
	ctx := logContext(&logs)

	_, err := a.AuthorizeInvocation(ctx, "sk-team-a", "")
	requireUnauthenticated(t, err)

	writeKeyFile(t, path, FileKey{ID: "team-a", SHA256: digestHex("sk-team-a")})

	// A failed read is cached for the reload interval as well.
	clock.Advance(ReloadInterval - time.Nanosecond)
	_, err = a.AuthorizeInvocation(ctx, "sk-team-a", "")
	requireUnauthenticated(t, err)

	clock.Advance(time.Nanosecond)
	resp, err := a.AuthorizeInvocation(ctx, "sk-team-a", "")
	if err != nil {
		t.Fatalf("AuthorizeInvocation() after recovery error = %v", err)
	}
	if resp.RateLimitKey != "team-a" {
		t.Fatalf("rate limit key = %q, want team-a", resp.RateLimitKey)
	}
	if got := strings.Count(logs.String(), "loaded static API key file"); got != 1 {
		t.Fatalf("load logged %d times, want 1:\n%s", got, logs.String())
	}

	// Re-reading an unchanged file does not log again.
	clock.Advance(ReloadInterval)
	if _, err := a.AuthorizeInvocation(ctx, "sk-team-a", ""); err != nil {
		t.Fatalf("AuthorizeInvocation() error = %v", err)
	}
	if got := strings.Count(logs.String(), "loaded static API key file"); got != 1 {
		t.Fatalf("load logged %d times after an unchanged re-read, want 1", got)
	}
}

func TestAuthorizeInvocationRotatedFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "api-keys.json")
	writeKeyFile(t, path, FileKey{ID: "team-a", SHA256: digestHex("sk-old")})
	clock := newFakeClock()
	a := newTestAuthorizer(t, path, clock)
	ctx := context.Background()

	if _, err := a.AuthorizeInvocation(ctx, "sk-old", ""); err != nil {
		t.Fatalf("old key before rotation: %v", err)
	}

	writeKeyFile(t, path,
		FileKey{ID: "team-a", SHA256: digestHex("sk-new")},
		FileKey{ID: "team-b", SHA256: digestHex("sk-team-b")},
	)

	// Within the reload interval the previous file stays in effect.
	clock.Advance(ReloadInterval - time.Nanosecond)
	if _, err := a.AuthorizeInvocation(ctx, "sk-old", ""); err != nil {
		t.Fatalf("old key within reload interval: %v", err)
	}
	_, err := a.AuthorizeInvocation(ctx, "sk-new", "")
	requireUnauthenticated(t, err)

	clock.Advance(time.Nanosecond)
	_, err = a.AuthorizeInvocation(ctx, "sk-old", "")
	requireUnauthenticated(t, err)
	resp, err := a.AuthorizeInvocation(ctx, "sk-new", "")
	if err != nil {
		t.Fatalf("new key after rotation: %v", err)
	}
	if resp.RateLimitKey != "team-a" {
		t.Fatalf("rate limit key = %q, want team-a", resp.RateLimitKey)
	}
	resp, err = a.AuthorizeInvocation(ctx, "sk-team-b", "")
	if err != nil {
		t.Fatalf("added key after rotation: %v", err)
	}
	if resp.RateLimitKey != "team-b" {
		t.Fatalf("rate limit key = %q, want team-b", resp.RateLimitKey)
	}

	// Deleting the file revokes every key at the next reload.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove key file: %v", err)
	}
	clock.Advance(ReloadInterval)
	_, err = a.AuthorizeInvocation(ctx, "sk-new", "")
	requireUnauthenticated(t, err)
}

func TestAuthorizeInvocationComparesEveryKey(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "api-keys.json")
	writeKeyFile(t, path,
		FileKey{ID: "team-a", SHA256: digestHex("sk-team-a")},
		FileKey{ID: "team-b", SHA256: digestHex("sk-team-b")},
		FileKey{ID: "team-c", SHA256: digestHex("sk-team-c")},
	)

	tests := []struct {
		token  string
		wantID string
	}{
		{token: "sk-team-a", wantID: "team-a"},
		{token: "sk-team-b", wantID: "team-b"},
		{token: "sk-team-c", wantID: "team-c"},
		{token: "sk-unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.token, func(t *testing.T) {
			t.Parallel()

			a := newTestAuthorizer(t, path, newFakeClock())
			calls := 0
			a.compare = func(x, y []byte) int {
				calls++
				return subtle.ConstantTimeCompare(x, y)
			}

			resp, err := a.AuthorizeInvocation(context.Background(), tc.token, "")
			if calls != 3 {
				t.Fatalf("compared %d keys, want all 3", calls)
			}
			if tc.wantID == "" {
				requireUnauthenticated(t, err)
				return
			}
			if err != nil {
				t.Fatalf("AuthorizeInvocation() error = %v", err)
			}
			if resp.RateLimitKey != tc.wantID {
				t.Fatalf("rate limit key = %q, want %s", resp.RateLimitKey, tc.wantID)
			}
		})
	}
}

func TestAuthorizeInvocationConcurrentUse(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "api-keys.json")
	writeKeyFile(t, path, FileKey{ID: "team-a", SHA256: digestHex("sk-team-a")})
	clock := newFakeClock()
	a := newTestAuthorizer(t, path, clock)

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%4 == 0 {
				clock.Advance(ReloadInterval)
			}
			if _, err := a.AuthorizeInvocation(context.Background(), "sk-team-a", ""); err != nil {
				t.Errorf("AuthorizeInvocation() error = %v", err)
			}
		}()
	}
	wg.Wait()
}
