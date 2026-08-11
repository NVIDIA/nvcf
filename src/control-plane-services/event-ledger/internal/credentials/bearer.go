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
package credentials

import (
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
)

// BearerTokenReader reads a static bearer token from a JSON secrets file and
// refreshes it automatically when Vault Agent re-renders the file.
type BearerTokenReader struct {
	token       atomic.Pointer[string]
	secretsPath string
	tokenKey    string
	watcherDone chan struct{}
}

// NewBearerTokenReader creates a BearerTokenReader that reads the token from the
// specified key in a JSON secrets file and watches for updates.
func NewBearerTokenReader(secretsPath, tokenKey string) (*BearerTokenReader, error) {
	token, err := ReadTokenFromFile(secretsPath, tokenKey)
	if err != nil {
		return nil, err
	}

	r := &BearerTokenReader{
		secretsPath: secretsPath,
		tokenKey:    tokenKey,
		watcherDone: make(chan struct{}),
	}
	r.token.Store(&token)

	if err := r.startFileWatcher(); err != nil {
		return nil, err
	}

	return r, nil
}

// Token returns the current bearer token value.
func (r *BearerTokenReader) Token() string {
	return *r.token.Load()
}

// Close stops the file watcher goroutine.
func (r *BearerTokenReader) Close() error {
	close(r.watcherDone)
	return nil
}

func (r *BearerTokenReader) startFileWatcher() error {
	if r.secretsPath == "" {
		return nil
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create file watcher: %w", err)
	}

	if err := watcher.Add(r.secretsPath); err != nil {
		watcher.Close()
		return fmt.Errorf("failed to watch secrets file: %w", err)
	}

	zap.L().Info("bearer token file watcher started",
		zap.String("path", r.secretsPath),
		zap.String("key", r.tokenKey),
	)

	go func() {
		defer watcher.Close()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				// Vault Agent renames files on write; re-add to keep watching.
				// See https://github.com/fsnotify/fsnotify/issues/363
				if !event.Op.Has(fsnotify.Write) {
					watcher.Add(event.Name)
				}
				if token, err := ReadTokenFromFile(r.secretsPath, r.tokenKey); err == nil {
					r.token.Store(&token)
					zap.L().Debug("bearer token refreshed", zap.String("key", r.tokenKey))
				} else {
					zap.L().Error("failed to refresh bearer token", zap.Error(err))
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				zap.L().Error("file watcher error", zap.Error(err))
			case <-r.watcherDone:
				return
			}
		}
	}()

	return nil
}

// ReadTokenFromFile reads a string value by key from a JSON secrets file.
func ReadTokenFromFile(secretsPath, tokenKey string) (string, error) {
	data, err := os.ReadFile(secretsPath)
	if err != nil {
		return "", fmt.Errorf("failed to read secrets file: %w", err)
	}

	var secrets map[string]any
	if err := json.Unmarshal(data, &secrets); err != nil {
		return "", fmt.Errorf("failed to unmarshal secrets file: %w", err)
	}

	token, ok := secrets[tokenKey].(string)
	if !ok || token == "" {
		return "", fmt.Errorf("key %q not found or empty in secrets file", tokenKey)
	}

	return token, nil
}
