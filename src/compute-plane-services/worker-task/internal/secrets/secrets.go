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

package secrets

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
)

// pollTimeout is the default interval at which the secrets file is polled as a
// fallback when fsnotify events are not delivered (for example on some network
// filesystems).
const pollTimeout = 30 * time.Minute

type secretsData struct {
	NgcApiKey string `json:"NGC_API_KEY"`
}

type Secrets struct {
	data            atomic.Pointer[secretsData]
	secretsFilePath string
	// pollInterval is the secrets file poll fallback interval. It defaults to
	// pollTimeout and is a per-instance field so tests can shorten it without
	// mutating shared global state.
	pollInterval time.Duration
}

// Constructor
func New(ctx context.Context, secretsFilePath string) (*Secrets, error) {
	s := &Secrets{
		secretsFilePath: secretsFilePath,
		pollInterval:    pollTimeout,
	}

	if err := s.load(); err != nil {
		return nil, err
	}

	go s.rotateSecrets(ctx)

	return s, nil
}

// Load new secrets from secrets file.
func (s *Secrets) load() error {
	var newSecrets secretsData

	if err := backoff.Retry(func() error {
		secretData, err := os.ReadFile(s.secretsFilePath)
		if err != nil {
			return err
		}

		if err := json.Unmarshal(secretData, &newSecrets); err != nil {
			return err
		}

		return nil
	}, backoff.WithMaxRetries(backoff.NewConstantBackOff(50*time.Millisecond), 10)); err != nil {
		return err
	}

	if s.data.Load() == nil || newSecrets != *s.data.Load() {
		s.data.Store(&newSecrets)
	}

	return nil
}

// Watch for updates in secret file and rotate them in memory.
func (s *Secrets) rotateSecrets(ctx context.Context) {
	zap.L().Info("Starting to watch for secrets update")

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		zap.L().Info("failed to start watcher on secret file", zap.Error(err))
		return
	}

	secretsDir := filepath.Dir(s.secretsFilePath)
	err = watcher.Add(secretsDir)
	if err != nil {
		zap.L().Info("failed to start watcher on secret file", zap.Error(err))
		return
	}

	s.watchLoop(ctx, watcher)
}

// watchLoop consumes fsnotify events and errors for the watched secrets
// directory, reloading the secrets cache on relevant create/write events and on
// a periodic poll. It returns when ctx is cancelled or the watcher channels
// close. It is split out from rotateSecrets so the loop can be exercised with a
// caller-supplied watcher in tests.
func (s *Secrets) watchLoop(ctx context.Context, watcher *fsnotify.Watcher) {
	secretsFileName := filepath.Base(s.secretsFilePath)

	// Poll secret file periodically in case fsnotify is not working on network fs.
	interval := s.pollInterval
	if interval <= 0 {
		interval = pollTimeout
	}
	pollTicker := time.NewTicker(interval)
	defer pollTicker.Stop()

	var err error
	for {
		select {
		case <-ctx.Done():
			zap.L().Info("stop watcher on secret file")
			return
		case err, ok := <-watcher.Errors:
			if !ok {
				// Errors channel has closed indicating watcher has closed.
				zap.L().Info("Progress monitor job is terminated")
				return
			}
			zap.L().Warn("progress watcher error", zap.Error(err))
		case event, ok := <-watcher.Events:
			if !ok {
				zap.L().Warn("secret file watcher is terminated")
				return
			}

			if filepath.Base(event.Name) != secretsFileName || !(event.Op == fsnotify.Create || event.Op == fsnotify.Write) {
				continue
			}

			if err = s.load(); err != nil {
				zap.L().Error("failed to load secrets file", zap.Error(err))
				continue
			}

			zap.L().Info("Successfully rotated secrets cache")
		case <-pollTicker.C:
			if err = s.load(); err != nil {
				zap.L().Error("failed to load secrets file", zap.Error(err))
				continue
			}

			zap.L().Info("Successfully polled secrets file")
		}
	}
}

// Get NGC API key
func (s *Secrets) NgcApiKey() string {
	return s.data.Load().NgcApiKey
}
