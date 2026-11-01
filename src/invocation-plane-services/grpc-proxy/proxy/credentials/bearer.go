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
package credentials

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
)

// BearerTokenCredentials is a custom implementation of grpc.PerRPCCredentials
// that adds a bearer token to each request and automatically refreshes from a secrets file
type BearerTokenCredentials struct {
	token        atomic.Pointer[string]
	skipSecurity bool
	secretsPath  string
	tokenKey     string
	watcherDone  chan struct{}
	closeOnce    sync.Once
}

// NewBearerTokenCredentials creates a new BearerTokenCredentials that reads the token
// from the specified key in the secrets file
func NewBearerTokenCredentials(secretsPath, tokenKey string, skipSecurity bool) (*BearerTokenCredentials, error) {
	// Read the initial token
	token, err := ReadTokenFromFile(secretsPath, tokenKey)
	if err != nil {
		return nil, err
	}

	creds := &BearerTokenCredentials{
		skipSecurity: skipSecurity,
		secretsPath:  secretsPath,
		tokenKey:     tokenKey,
		watcherDone:  make(chan struct{}),
	}

	// Store initial token
	creds.token.Store(&token)
	zap.L().Debug("Initial token loaded", zap.String("token_key", tokenKey))

	// Start watching the file for changes
	if err := creds.startFileWatcher(); err != nil {
		return nil, err
	}

	return creds, nil
}

// GetRequestMetadata implements the grpc.PerRPCCredentials interface
func (c *BearerTokenCredentials) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	return map[string]string{
		"authorization": "Bearer " + *c.token.Load(),
	}, nil
}

// RequireTransportSecurity implements the grpc.PerRPCCredentials interface
func (c *BearerTokenCredentials) RequireTransportSecurity() bool {
	return !c.skipSecurity
}

// updateToken updates the bearer token
func (c *BearerTokenCredentials) updateToken(token string) {
	c.token.Store(&token)
}

// Close stops the file watcher
func (c *BearerTokenCredentials) Close() error {
	c.closeOnce.Do(func() {
		close(c.watcherDone)
	})
	return nil
}

// startFileWatcher sets up a file watcher that monitors changes to the secrets file
// and updates the bearer token when it changes
func (c *BearerTokenCredentials) startFileWatcher() error {
	if c.secretsPath == "" {
		zap.L().Info("Skipping bearer token file watch setup - no path provided")
		return nil
	}

	// creates a new file watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		zap.L().Error("Error setting up file watcher for bearer token",
			zap.String("secrets-file", c.secretsPath),
			zap.Error(err))
		return err
	}

	if err := watcher.Add(c.secretsPath); err != nil {
		zap.L().Error("Bearer token file watch failed",
			zap.String("secrets-file", c.secretsPath),
			zap.Error(err))
		watcher.Close()
		return err
	}

	zap.L().Info("Bearer token file watcher setup complete",
		zap.String("secrets-file", c.secretsPath),
		zap.String("token-key", c.tokenKey),
	)
	go func() {
		defer watcher.Close()
		for {
			select {
			// watch for events
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				zap.L().Debug("Received file watcher event",
					zap.String("secrets-file", event.Name),
					zap.Any("op", event.Op.String()))
				if !event.Op.Has(fsnotify.Write) {
					// In the case of any other events, file gets removed from the watcher. Add it again to continue receiving changes to the credentials file
					// See https://github.com/fsnotify/fsnotify/issues/363#issuecomment-993622433
					watcher.Add(event.Name)
				}
				zap.L().Debug("File write detected, updating token")
				// Update token if file changed
				if token, err := ReadTokenFromFile(c.secretsPath, c.tokenKey); err == nil {
					c.updateToken(token)
				} else {
					zap.L().Error("Error reading updated token", zap.Error(err))
				}
			// watch for errors
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				zap.L().Error("Error from watcher",
					zap.String("secrets-file", c.secretsPath),
					zap.Error(err))
			case <-c.watcherDone:
				zap.L().Info("Bearer token file watcher shutting down")
				return
			}
		}
	}()
	return nil
}

// readTokenFromFile reads the token from the specified key in the secrets file
func ReadTokenFromFile(secretsPath, tokenKey string) (string, error) {
	zap.L().Debug("Reading token from file",
		zap.String("path", secretsPath),
		zap.String("key", tokenKey))

	secrets, err := os.ReadFile(secretsPath)
	if err != nil {
		return "", fmt.Errorf("failed to read secrets file: %w", err)
	}

	var secretsMap map[string]any
	if err := json.Unmarshal(secrets, &secretsMap); err != nil {
		return "", fmt.Errorf("failed to unmarshal secrets: %w", err)
	}

	token, ok := secretsMap[tokenKey].(string)
	if !ok || token == "" {
		return "", fmt.Errorf("%s not found in secrets file or is empty", tokenKey)
	}

	return token, nil
}
