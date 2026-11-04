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

package reloadableconfig

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
	"sigs.k8s.io/yaml"
)

const defaultConfigLoadTimeout = 15 * time.Second

var (
	// ConfigLoadTick and PollInterval are variables so tests can reduce waits.
	ConfigLoadTick = 500 * time.Millisecond
	PollInterval   = 30 * time.Second
	ReloadDelay    = 25 * time.Millisecond
)

// transientReloadError marks reload failures that routinely occur for a moment
// while a writer replaces the file (missing file, zero-length file). They are
// only reported to the reload error callback when they persist to a poll tick.
type transientReloadError struct{ err error }

func (e transientReloadError) Error() string { return e.err.Error() }
func (e transientReloadError) Unwrap() error { return e.err }

// fileState identifies the on-disk file a failed reload was attempted against,
// so one bad edit is reported once instead of on every poll tick.
type fileState struct {
	modTime time.Time
	size    int64
	valid   bool
}

type ReloadableConfig[T any] interface {
	Get() *T
	AddOnReloadFunc(func() error)
}

type ConfigOption[T any] func(*configData[T])

type configData[T any] struct {
	filename string

	modTime atomic.Pointer[time.Time]
	data    atomic.Pointer[T]

	mu            sync.Mutex
	loadTimeout   time.Duration
	initializer   func(*T) error
	unmarshal     func([]byte, *T) error
	validators    []func(*T) error
	postLoadHooks []func(*T) error
	onReloadError func(error)
}

func WithPreLoadInitializer[T any](initialize func(*T) error) ConfigOption[T] {
	return func(cd *configData[T]) {
		cd.initializer = initialize
	}
}

func WithUnmarshalFunc[T any](unmarshal func([]byte, *T) error) ConfigOption[T] {
	return func(cd *configData[T]) {
		cd.unmarshal = unmarshal
	}
}

func WithInitialLoadTimeout[T any](timeout time.Duration) ConfigOption[T] {
	return func(cd *configData[T]) {
		cd.loadTimeout = timeout
	}
}

func WithValidateFunc[T any](validator func(*T) error) ConfigOption[T] {
	return func(cd *configData[T]) {
		cd.validators = append(cd.validators, validator)
	}
}

func WithPostLoadFunc[T any](postLoadHook func(*T) error) ConfigOption[T] {
	return func(cd *configData[T]) {
		cd.postLoadHooks = append(cd.postLoadHooks, postLoadHook)
	}
}

// WithReloadErrorFunc registers a callback invoked when a file-watcher
// triggered reload fails. The previous config stays in place.
func WithReloadErrorFunc[T any](onReloadError func(error)) ConfigOption[T] {
	return func(cd *configData[T]) {
		cd.onReloadError = onReloadError
	}
}

func SetupConfig[T any](configFilename string, opts ...ConfigOption[T]) (ReloadableConfig[T], error) {
	c := &configData[T]{
		filename: configFilename,
	}
	for _, opt := range opts {
		opt(c)
	}

	loadTimeout := defaultConfigLoadTimeout
	if c.loadTimeout > 0 {
		loadTimeout = c.loadTimeout
	}
	if err := waitForFile(configFilename, loadTimeout); err != nil {
		return nil, err
	}
	if err := c.reloadConfig(); err != nil {
		return nil, err
	}
	if err := c.setupFileWatcher(context.Background()); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *configData[T]) Get() *T {
	return c.data.Load()
}

func (c *configData[T]) AddOnReloadFunc(f func() error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.postLoadHooks = append(c.postLoadHooks, func(*T) error { return f() })
}

func waitForFile(filename string, timeout time.Duration) error {
	if _, err := os.Stat(filename); err == nil {
		zap.L().Info("Config file is ready", zap.String("file", filename))
		return nil
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(ConfigLoadTick)
	defer ticker.Stop()

	for {
		select {
		case <-timer.C:
			return errors.New("timed out waiting for config to become available")
		case <-ticker.C:
			if _, err := os.Stat(filename); err == nil {
				zap.L().Info("Config file is ready", zap.String("file", filename))
				return nil
			}
		}
	}
}

func (c *configData[T]) reloadConfig() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	stat, err := os.Stat(c.filename)
	if err != nil {
		return transientReloadError{err}
	}
	if stat.Size() == 0 {
		return transientReloadError{fmt.Errorf("config file %q is zero-length", c.filename)}
	}

	newData := new(T)
	if c.initializer != nil {
		if err := c.initializer(newData); err != nil {
			return err
		}
	}

	configBytes, err := os.ReadFile(c.filename)
	if err != nil {
		return err
	}
	unmarshal := c.unmarshal
	if unmarshal == nil {
		unmarshal = func(data []byte, target *T) error { return yaml.Unmarshal(data, target) }
	}
	if err := unmarshal(configBytes, newData); err != nil {
		return err
	}
	for _, validator := range c.validators {
		if err := validator(newData); err != nil {
			return err
		}
	}

	modTime := stat.ModTime()
	c.modTime.Store(&modTime)
	c.data.Store(newData)

	for _, hook := range c.postLoadHooks {
		if err := hook(newData); err != nil {
			return err
		}
	}
	return nil
}

func (c *configData[T]) setupFileWatcher(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	watchPaths := []string{c.filename, filepath.Dir(c.filename)}
	for _, path := range watchPaths {
		if err := watcher.Add(path); err != nil {
			_ = watcher.Close()
			return err
		}
	}

	// reload runs only on the watcher goroutine, so lastReported needs no lock.
	var lastReported fileState
	reload := func(fromPoll bool) {
		zap.L().Info("Config file updated, reloading")
		err := c.reloadConfig()
		if err == nil {
			lastReported = fileState{}
			return
		}
		zap.L().Error("could not reload config", zap.Error(err))
		if c.onReloadError == nil {
			return
		}
		// A transient failure seen on a file event usually heals within
		// ReloadDelay; only report it if it is still failing on a poll tick.
		if errors.As(err, new(transientReloadError)) && !fromPoll {
			return
		}
		// The poll tick retries a bad file until it changes; report it once.
		state := fileState{}
		if stat, statErr := os.Stat(c.filename); statErr == nil {
			state = fileState{modTime: stat.ModTime(), size: stat.Size(), valid: true}
		}
		if state == lastReported {
			return
		}
		lastReported = state
		c.onReloadError(err)
	}

	go func() {
		defer func() {
			if err := watcher.Close(); err != nil {
				zap.L().Warn("failed to close config file watcher", zap.Error(err))
			}
		}()
		ticker := time.NewTicker(PollInterval)
		defer ticker.Stop()
		var reloadTimer *time.Timer
		var reloadC <-chan time.Time

		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove|fsnotify.Chmod) != 0 {
					if reloadTimer == nil {
						reloadTimer = time.NewTimer(ReloadDelay)
						reloadC = reloadTimer.C
					} else {
						reloadTimer.Reset(ReloadDelay)
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				zap.L().Error("config file watch error", zap.Error(err))
			case <-reloadC:
				if reloadTimer != nil {
					reloadTimer.Stop()
				}
				reloadC = nil
				reloadTimer = nil
				reload(false)
			case <-ticker.C:
				stat, err := os.Stat(c.filename)
				lastModTime := c.modTime.Load()
				if err == nil && lastModTime != nil && stat.ModTime().After(*lastModTime) {
					reload(true)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return nil
}
