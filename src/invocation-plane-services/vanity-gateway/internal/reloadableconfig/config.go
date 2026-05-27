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
	initializer   func(*T) error
	validators    []func(*T) error
	postLoadHooks []func(*T) error
}

func WithPreLoadInitializer[T any](initialize func(*T) error) ConfigOption[T] {
	return func(cd *configData[T]) {
		cd.initializer = initialize
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

func SetupConfig[T any](configFilename string, opts ...ConfigOption[T]) (ReloadableConfig[T], error) {
	c := &configData[T]{
		filename: configFilename,
	}
	for _, opt := range opts {
		opt(c)
	}

	if err := waitForFile(configFilename, defaultConfigLoadTimeout); err != nil {
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
		return err
	}
	if stat.Size() == 0 {
		return fmt.Errorf("config file %q is zero-length", c.filename)
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
	if err := yaml.Unmarshal(configBytes, newData); err != nil {
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

	reload := func() {
		zap.L().Info("Config file updated, reloading")
		if err := c.reloadConfig(); err != nil {
			zap.L().Error("could not reload config", zap.Error(err))
		}
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
				reload()
			case <-ticker.C:
				stat, err := os.Stat(c.filename)
				lastModTime := c.modTime.Load()
				if err == nil && lastModTime != nil && stat.ModTime().After(*lastModTime) {
					reload()
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return nil
}
