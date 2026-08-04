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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog"

	"github.com/NVIDIA/nvcf/src/control-plane-services/nvcf-ui/backend/internal/utils"
)

const (
	expiryWarning         = 5 * time.Minute
	tokensFilePath        = "TOKENS_PATH"
	defaultTokensFilePath = "/var/run/secrets/vault/tokens.json"
)

type apiTokens struct {
	NvcfApiToken string `json:"nvcfApiToken"`
	NvctApiToken string `json:"nvctApiToken"`
	SisApiToken  string `json:"sisApiToken"`
}

type tokenState struct {
	value  string
	valid  bool
	cancel context.CancelFunc
}

type Watcher struct {
	mu         sync.RWMutex
	wg         sync.WaitGroup
	ctx        context.Context
	tokensPath string
	nvcf       tokenState
	nvct       tokenState
	sis        tokenState
}

// Wait blocks until every goroutine the watcher started has exited. Call it
// after the watcher's context is cancelled to guarantee a clean shutdown.
func (w *Watcher) Wait() {
	w.wg.Wait()
}

func (w *Watcher) NVCFToken() (string, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.nvcf.value, w.nvcf.valid
}

func (w *Watcher) NVCTToken() (string, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.nvct.value, w.nvct.valid
}

func (w *Watcher) SISToken() (string, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.sis.value, w.sis.valid
}

func jwtExpiry(token string) (time.Time, error) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("invalid JWT format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, err
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, err
	}
	if claims.Exp == 0 {
		return time.Time{}, fmt.Errorf("no exp claim in token")
	}
	return time.Unix(claims.Exp, 0), nil
}

// startExpiryWatcher fires invalidate() 5 minutes before the token's exp claim.
// ctx is per-token — cancel it to garbage-collect this goroutine when the token rotates.
func (w *Watcher) startExpiryWatcher(ctx context.Context, name, token string, invalidate func()) {
	logger := zerolog.Ctx(w.ctx)

	expiry, err := jwtExpiry(token)
	if err != nil {
		logger.Warn().Err(err).Msgf("could not parse %s token JWT expiry; token will not auto-invalidate", name)
		return
	}

	warnIn := time.Until(expiry) - expiryWarning
	if warnIn <= 0 {
		logger.Warn().Msgf("%s token already within warning window; marking invalid", name)
		invalidate()
		return
	}

	w.wg.Go(func() {
		select {
		case <-ctx.Done():
			// Token was rotated before the timer fired — nothing to do.
		case <-time.After(warnIn):
			invalidate()
			logger.Warn().Msgf("%s token is about to expire", name)
		}
	})
}

func (w *Watcher) load() error {
	data, err := os.ReadFile(w.tokensPath)
	if err != nil {
		return err
	}
	var t apiTokens
	if err := json.Unmarshal(data, &t); err != nil {
		return err
	}

	w.mu.Lock()

	// Mark all tokens valid and cancel any in-flight expiry goroutines.
	w.nvcf.value, w.nvcf.valid = t.NvcfApiToken, true
	w.nvct.value, w.nvct.valid = t.NvctApiToken, true
	w.sis.value, w.sis.valid = t.SisApiToken, true

	if w.nvcf.cancel != nil {
		w.nvcf.cancel()
	}
	if w.nvct.cancel != nil {
		w.nvct.cancel()
	}
	if w.sis.cancel != nil {
		w.sis.cancel()
	}

	// Fresh per-token contexts for the new expiry goroutines.
	nvcfCtx, nvcfCancel := context.WithCancel(w.ctx)
	nvctCtx, nvctCancel := context.WithCancel(w.ctx)
	sisCtx, sisCancel := context.WithCancel(w.ctx)
	w.nvcf.cancel = nvcfCancel
	w.nvct.cancel = nvctCancel
	w.sis.cancel = sisCancel

	w.mu.Unlock()

	w.startExpiryWatcher(nvcfCtx, "nvcf", t.NvcfApiToken, func() { w.mu.Lock(); w.nvcf.valid = false; w.mu.Unlock() })
	w.startExpiryWatcher(nvctCtx, "nvct", t.NvctApiToken, func() { w.mu.Lock(); w.nvct.valid = false; w.mu.Unlock() })
	w.startExpiryWatcher(sisCtx, "sis", t.SisApiToken, func() { w.mu.Lock(); w.sis.valid = false; w.mu.Unlock() })

	return nil
}

func newWatcher(ctx context.Context, path string) *Watcher {
	logger := zerolog.Ctx(ctx)
	// Canonicalize so it matches filepath.Clean(event.Name) from the dir watch.
	path = filepath.Clean(path)
	w := &Watcher{ctx: ctx, tokensPath: path}

	if err := w.load(); err != nil {
		logger.Fatal().Err(err).Msg("failed to load API tokens")
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to create file watcher")
	}

	// Watch the parent directory rather than the file itself. OpenBao Agent
	// renders tokens atomically (write-temp-then-rename), so every rotation
	// swaps in a new inode at `path`. A watch on the file's inode fires at most
	// once and then goes silent; a watch on the (stable) directory inode sees
	// the rename as a Create/Write on the target path across every rotation.
	watchDir := filepath.Dir(path)
	if err := fsw.Add(watchDir); err != nil {
		logger.Fatal().Err(err).Msg("failed to add directory to watcher")
	}

	w.wg.Go(func() {
		defer func() { _ = fsw.Close() }()
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-fsw.Events:
				// Directory watch reports every entry; only react to our file.
				if filepath.Clean(event.Name) != path {
					continue
				}
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
					if err := w.load(); err != nil {
						logger.Error().Err(err).Msg("failed to reload API tokens")
					}
				}
			case err := <-fsw.Errors:
				logger.Error().Err(err).Msg("file watcher error")
			}
		}
	})

	return w
}

// Watch loads tokens from TokensFilePath on startup and reloads them
// whenever the file is written or recreated (e.g. by the OpenBao agent).
// Expects a zerolog.Logger stored in ctx via zerolog.WithContext.
func Watch(ctx context.Context) *Watcher {
	return newWatcher(ctx, utils.GetEnvOr(tokensFilePath, defaultTokensFilePath))
}
