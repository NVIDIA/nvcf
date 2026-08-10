// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tokenwatcher

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// makeJWT crafts a minimal, structurally valid JWT with the given exp Unix timestamp.
// The signature segment is a placeholder — jwtExpiry only parses the payload.
func makeJWT(exp int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]int64{"exp": exp})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".fakesig"
}

func writeTokens(t *testing.T, path string, tokens apiTokens) {
	t.Helper()
	data, err := json.Marshal(tokens)
	if err != nil {
		t.Fatalf("marshal tokens: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write tokens file: %v", err)
	}
}

func nopCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(zerolog.New(os.Stderr).WithContext(context.Background()))
	t.Cleanup(cancel)
	return ctx
}

// ---- jwtExpiry ---------------------------------------------------------

func TestJwtExpiry(t *testing.T) {
	t.Parallel()

	exp := time.Now().Add(time.Hour).Unix()

	tests := []struct {
		name    string
		token   string
		wantErr bool
		wantExp int64
	}{
		{
			name:    "valid JWT returns correct expiry",
			token:   makeJWT(exp),
			wantExp: exp,
		},
		{
			name:    "only two segments",
			token:   "header.payload",
			wantErr: true,
		},
		{
			name:    "invalid base64 in payload",
			token:   "header.!!!.sig",
			wantErr: true,
		},
		{
			name:    "payload not JSON",
			token:   "h." + base64.RawURLEncoding.EncodeToString([]byte("notjson")) + ".s",
			wantErr: true,
		},
		{
			name:    "missing exp claim",
			token:   makeJWT(0),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := jwtExpiry(tt.token)
			if (err != nil) != tt.wantErr {
				t.Fatalf("jwtExpiry err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got.Unix() != tt.wantExp {
				t.Errorf("expiry = %d, want %d", got.Unix(), tt.wantExp)
			}
		})
	}
}

// ---- load --------------------------------------------------------------

func TestLoad(t *testing.T) {
	t.Parallel()

	longJWT := makeJWT(time.Now().Add(24 * time.Hour).Unix())

	tokens := apiTokens{
		NvcfApiToken: longJWT,
		NvctApiToken: longJWT,
		SisApiToken:  longJWT,
	}

	tests := []struct {
		name    string
		setup   func(path string)
		wantErr bool
	}{
		{
			name: "valid file sets tokens and valid flags",
			setup: func(path string) {
				writeTokens(t, path, tokens)
			},
		},
		{
			name:    "missing file returns error",
			setup:   func(path string) { /* don't create the file */ },
			wantErr: true,
		},
		{
			name: "invalid JSON returns error",
			setup: func(path string) {
				_ = os.WriteFile(path, []byte("{notjson"), 0o600)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tokens.json")
			tt.setup(path)

			w := &Watcher{ctx: nopCtx(t), tokensPath: path}
			err := w.load()

			if (err != nil) != tt.wantErr {
				t.Fatalf("load err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}

			nvcf, nvcfOK := w.NVCFToken()
			nvct, nvctOK := w.NVCTToken()
			sis, sisOK := w.SISToken()

			if nvcf != longJWT || !nvcfOK {
				t.Errorf("NVCFToken = (%q, %v), want (%q, true)", nvcf, nvcfOK, longJWT)
			}
			if nvct != longJWT || !nvctOK {
				t.Errorf("NVCTToken = (%q, %v), want (%q, true)", nvct, nvctOK, longJWT)
			}
			if sis != longJWT || !sisOK {
				t.Errorf("SISToken = (%q, %v), want (%q, true)", sis, sisOK, longJWT)
			}
		})
	}
}

// TestUnmarshalRejectsEmptyTokens covers the fail-open case: an empty or absent
// token is not a usable credential, so decoding must fail rather than hand back
// a token the proxy would forward as an empty Authorization header. The error
// names the offending fields by their json tags, matching the file on disk.
func TestUnmarshalRejectsEmptyTokens(t *testing.T) {
	t.Parallel()

	longJWT := makeJWT(time.Now().Add(24 * time.Hour).Unix())

	tests := []struct {
		name        string
		contents    string
		wantErr     bool
		wantInError []string
	}{
		{
			name:     "every token present",
			contents: `{"nvcfApiToken":"a","nvctApiToken":"b","sisApiToken":"c"}`,
		},
		{
			name:        "empty token value",
			contents:    `{"nvcfApiToken":"","nvctApiToken":"` + longJWT + `","sisApiToken":"` + longJWT + `"}`,
			wantErr:     true,
			wantInError: []string{"nvcfApiToken"},
		},
		{
			name:        "absent field",
			contents:    `{"nvctApiToken":"` + longJWT + `"}`,
			wantErr:     true,
			wantInError: []string{"nvcfApiToken", "sisApiToken"},
		},
		{
			name:        "no tokens at all",
			contents:    `{}`,
			wantErr:     true,
			wantInError: []string{"nvcfApiToken", "nvctApiToken", "sisApiToken"},
		},
		{
			name:     "whitespace is not treated as empty",
			contents: `{"nvcfApiToken":" ","nvctApiToken":"b","sisApiToken":"c"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got apiTokens
			err := json.Unmarshal([]byte(tt.contents), &got)

			if (err != nil) != tt.wantErr {
				t.Fatalf("Unmarshal err = %v, wantErr %v", err, tt.wantErr)
			}
			for _, field := range tt.wantInError {
				if !strings.Contains(err.Error(), field) {
					t.Errorf("error %q does not name the missing field %q", err, field)
				}
			}
		})
	}
}

// TestUnmarshalLeavesTargetUntouchedOnError checks a rejected file does not
// half-apply. The reload path keeps serving the previous tokens on error, which
// only holds if a failed decode writes nothing.
func TestUnmarshalLeavesTargetUntouchedOnError(t *testing.T) {
	t.Parallel()

	before := apiTokens{NvcfApiToken: "a", NvctApiToken: "b", SisApiToken: "c"}
	got := before

	if err := json.Unmarshal([]byte(`{"nvcfApiToken":"new"}`), &got); err == nil {
		t.Fatal("Unmarshal returned no error for an incomplete file, want one")
	}
	if got != before {
		t.Errorf("tokens = %+v after a failed decode, want them unchanged (%+v)", got, before)
	}
}

// TestLoadRejectsIncompleteFile checks the decode error propagates out of load,
// so startup fails loudly instead of serving an unusable token.
func TestLoadRejectsIncompleteFile(t *testing.T) {
	t.Parallel()

	longJWT := makeJWT(time.Now().Add(24 * time.Hour).Unix())
	path := filepath.Join(t.TempDir(), "tokens.json")
	writeTokens(t, path, apiTokens{NvctApiToken: longJWT, SisApiToken: longJWT})

	w := &Watcher{ctx: nopCtx(t), tokensPath: path}
	err := w.load()
	if err == nil {
		t.Fatal("load returned no error for a file missing a token, want one")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the tokens file %q", err, path)
	}
	if _, ok := w.NVCFToken(); ok {
		t.Error("NVCF token reported valid after a failed load")
	}
}

// TestLoadKeepsPreviousTokensOnBadReload covers a rotation that renders an
// incomplete file: the watcher must keep serving the tokens it already has
// rather than dropping them.
func TestLoadKeepsPreviousTokensOnBadReload(t *testing.T) {
	t.Parallel()

	longJWT := makeJWT(time.Now().Add(24 * time.Hour).Unix())
	path := filepath.Join(t.TempDir(), "tokens.json")
	writeTokens(t, path, apiTokens{NvcfApiToken: longJWT, NvctApiToken: longJWT, SisApiToken: longJWT})

	w := &Watcher{ctx: nopCtx(t), tokensPath: path}
	if err := w.load(); err != nil {
		t.Fatalf("load err = %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"nvcfApiToken":""}`), 0o600); err != nil {
		t.Fatalf("write tokens file: %v", err)
	}
	if err := w.load(); err == nil {
		t.Fatal("reload of an incomplete file returned no error, want one")
	}

	if value, ok := w.NVCFToken(); !ok || value != longJWT {
		t.Errorf("NVCFToken after failed reload = (%q, %v), want (%q, true)", value, ok, longJWT)
	}
}

// ---- expiry timer ------------------------------------------------------

func TestExpiryTimerInvalidatesToken(t *testing.T) {
	t.Parallel()

	// Token expires just past the warning window so the timer fires in ~1.5s.
	// The margin must exceed 1s to survive Unix timestamp truncation.
	exp := time.Now().Add(expiryWarning + 1500*time.Millisecond)
	jwt := makeJWT(exp.Unix())

	path := filepath.Join(t.TempDir(), "tokens.json")
	writeTokens(t, path, apiTokens{
		NvcfApiToken: jwt,
		NvctApiToken: jwt,
		SisApiToken:  jwt,
	})

	w := &Watcher{ctx: nopCtx(t), tokensPath: path}
	if err := w.load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	// All tokens valid immediately after load.
	if _, ok := w.NVCFToken(); !ok {
		t.Fatal("expected NVCFToken to be valid after load")
	}

	// Wait for all three expiry timers to fire.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		_, nvcfOK := w.NVCFToken()
		_, nvctOK := w.NVCTToken()
		_, sisOK := w.SISToken()
		if !nvcfOK && !nvctOK && !sisOK {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, ok := w.NVCFToken(); ok {
		t.Error("NVCFToken should be invalid after expiry warning window")
	}
	if _, ok := w.NVCTToken(); ok {
		t.Error("NVCTToken should be invalid after expiry warning window")
	}
	if _, ok := w.SISToken(); ok {
		t.Error("SISToken should be invalid after expiry warning window")
	}
}

// ---- token rotation cancels old expiry goroutine -----------------------

func TestRotationCancelsExpiryGoroutine(t *testing.T) {
	t.Parallel()

	// First load: token expires just past warning window (~1.5s timer).
	shortExp := time.Now().Add(expiryWarning + 1500*time.Millisecond)
	shortJWT := makeJWT(shortExp.Unix())

	// Second load: token valid for 24h — expiry goroutine won't fire in test.
	longJWT := makeJWT(time.Now().Add(24 * time.Hour).Unix())

	path := filepath.Join(t.TempDir(), "tokens.json")
	writeTokens(t, path, apiTokens{
		NvcfApiToken: shortJWT,
		NvctApiToken: shortJWT,
		SisApiToken:  shortJWT,
	})

	w := &Watcher{ctx: nopCtx(t), tokensPath: path}
	if err := w.load(); err != nil {
		t.Fatalf("first load: %v", err)
	}

	// Rotate tokens before the short-expiry timer fires.
	writeTokens(t, path, apiTokens{
		NvcfApiToken: longJWT,
		NvctApiToken: longJWT,
		SisApiToken:  longJWT,
	})
	if err := w.load(); err != nil {
		t.Fatalf("second load: %v", err)
	}

	// Wait longer than the original timer would have fired (1.5s + buffer).
	time.Sleep(2500 * time.Millisecond)

	// Tokens must still be valid — the old goroutine was cancelled.
	if _, ok := w.NVCFToken(); !ok {
		t.Error("NVCFToken should still be valid after rotation")
	}
	if _, ok := w.NVCTToken(); !ok {
		t.Error("NVCTToken should still be valid after rotation")
	}
	if _, ok := w.SISToken(); !ok {
		t.Error("SISToken should still be valid after rotation")
	}
}

// ---- file watcher integration ------------------------------------------

func TestWatchReloadsOnFileChange(t *testing.T) {
	t.Parallel()

	longJWT := makeJWT(time.Now().Add(24 * time.Hour).Unix())
	updatedJWT := makeJWT(time.Now().Add(48 * time.Hour).Unix())

	path := filepath.Join(t.TempDir(), "tokens.json")
	writeTokens(t, path, apiTokens{
		NvcfApiToken: longJWT,
		NvctApiToken: longJWT,
		SisApiToken:  longJWT,
	})

	ctx, cancel := context.WithCancel(zerolog.New(os.Stderr).WithContext(context.Background()))
	t.Cleanup(cancel)

	w := newWatcher(ctx, path)

	if token, _ := w.NVCFToken(); token != longJWT {
		t.Fatalf("initial NVCFToken = %q, want %q", token, longJWT)
	}

	// Overwrite the file to simulate an OpenBao token rotation.
	writeTokens(t, path, apiTokens{
		NvcfApiToken: updatedJWT,
		NvctApiToken: updatedJWT,
		SisApiToken:  updatedJWT,
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if token, _ := w.NVCFToken(); token == updatedJWT {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if token, _ := w.NVCFToken(); token != updatedJWT {
		t.Errorf("NVCFToken after rotation = %q, want %q", token, updatedJWT)
	}
}

// writeTokensAtomic renders the tokens file the way OpenBao Agent does: write
// to a temp file in the same directory, then rename(2) over the destination.
// Every call installs a new inode at `path`, which is what breaks a watch on
// the file's inode (vs. a watch on the parent directory).
func writeTokensAtomic(t *testing.T, path string, tokens apiTokens) {
	t.Helper()
	data, err := json.Marshal(tokens)
	if err != nil {
		t.Fatalf("marshal tokens: %v", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		t.Fatalf("write temp tokens file: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("rename tokens file: %v", err)
	}
}

// TestWatchReloadsOnAtomicRename mirrors production: OpenBao rotates the token
// file via temp-then-rename, swapping the inode on every render. A file-inode
// watch would fire at most once and then go silent; the directory watch must
// pick up every rotation. Two rotations catch the "works once, then stops" mode.
func TestWatchReloadsOnAtomicRename(t *testing.T) {
	t.Parallel()

	jwt1 := makeJWT(time.Now().Add(24 * time.Hour).Unix())
	jwt2 := makeJWT(time.Now().Add(48 * time.Hour).Unix())
	jwt3 := makeJWT(time.Now().Add(72 * time.Hour).Unix())

	path := filepath.Join(t.TempDir(), "tokens.json")
	writeTokensAtomic(t, path, apiTokens{NvcfApiToken: jwt1, NvctApiToken: jwt1, SisApiToken: jwt1})

	ctx, cancel := context.WithCancel(zerolog.New(os.Stderr).WithContext(context.Background()))
	t.Cleanup(cancel)

	w := newWatcher(ctx, path)
	if token, _ := w.NVCFToken(); token != jwt1 {
		t.Fatalf("initial NVCFToken = %q, want %q", token, jwt1)
	}

	waitForToken := func(want string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if token, _ := w.NVCFToken(); token == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		token, _ := w.NVCFToken()
		t.Fatalf("NVCFToken after atomic rename = %q, want %q", token, want)
	}

	writeTokensAtomic(t, path, apiTokens{NvcfApiToken: jwt2, NvctApiToken: jwt2, SisApiToken: jwt2})
	waitForToken(jwt2)

	// The second rotation is the one a file-inode watch would miss.
	writeTokensAtomic(t, path, apiTokens{NvcfApiToken: jwt3, NvctApiToken: jwt3, SisApiToken: jwt3})
	waitForToken(jwt3)
}

// ---- Wait blocks until goroutines exit ---------------------------------

func TestWaitReturnsAfterContextCancel(t *testing.T) {
	t.Parallel()

	longJWT := makeJWT(time.Now().Add(24 * time.Hour).Unix())

	path := filepath.Join(t.TempDir(), "tokens.json")
	writeTokens(t, path, apiTokens{
		NvcfApiToken: longJWT,
		NvctApiToken: longJWT,
		SisApiToken:  longJWT,
	})

	ctx, cancel := context.WithCancel(zerolog.New(os.Stderr).WithContext(context.Background()))
	w := newWatcher(ctx, path)

	// Cancelling the context must make the fsnotify loop and every per-token
	// expiry timer observe ctx.Done() and exit, so Wait() returns promptly.
	cancel()

	done := make(chan struct{})
	go func() {
		w.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after context cancellation — a goroutine is hanging")
	}
}

// ---- Watch resolves the tokens path from the environment ---------------

func TestWatchResolvesPathFromEnv(t *testing.T) {
	// Not parallel: t.Setenv mutates process-global state.
	longJWT := makeJWT(time.Now().Add(24 * time.Hour).Unix())

	path := filepath.Join(t.TempDir(), "tokens.json")
	writeTokens(t, path, apiTokens{
		NvcfApiToken: longJWT,
		NvctApiToken: longJWT,
		SisApiToken:  longJWT,
	})

	t.Setenv(tokensFilePath, path)

	ctx, cancel := context.WithCancel(zerolog.New(os.Stderr).WithContext(context.Background()))
	t.Cleanup(cancel)

	w := Watch(ctx)

	if w.tokensPath != path {
		t.Errorf("tokensPath = %q, want %q (from %s env var)", w.tokensPath, path, tokensFilePath)
	}
	if token, ok := w.NVCFToken(); token != longJWT || !ok {
		t.Errorf("NVCFToken = (%q, %v), want (%q, true)", token, ok, longJWT)
	}
}
