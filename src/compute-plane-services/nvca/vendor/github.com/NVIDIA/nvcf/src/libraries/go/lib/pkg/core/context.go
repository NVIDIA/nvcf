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

package core

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
)

var defaultLogLevel = logrus.InfoLevel

// NewDefaultContext provides a default context for applications.
func NewDefaultContext(parent context.Context) context.Context {
	ctx := WithAppName(parent, DefaultAppName())
	ctx = WithDefaultLogger(ctx)
	ctx = WithSignalHandler(ctx)
	ctx = WithRandomSeed(ctx, time.Now().UnixNano())
	return ctx
}

type key int

const (
	appNameKey key = iota
	loggerKey
	randKey
	clockKey
	ObjectNameField = "metadata.name"
)

func SetDefaultLogLevel(l logrus.Level) {
	defaultLogLevel = l
}

func DefaultAppName() string {
	exePath, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Base(exePath)
}

func WithAppName(parent context.Context, appName string) context.Context {
	return context.WithValue(parent, appNameKey, appName)
}

func GetAppName(ctx context.Context) string {
	if appName, ok := ctx.Value(appNameKey).(string); ok {
		return appName
	}
	return ""
}

func WithDefaultLogger(parent context.Context) context.Context {
	logger := logrus.New()
	logger.SetLevel(defaultLogLevel)
	logger.Out = os.Stderr
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02T15:04:05.999Z07:00",
		CallerPrettyfier: func(f *runtime.Frame) (string, string) {
			fileBase := filepath.Base(f.File)
			// The logrusr package adds one frame to the call stack so all controller-runtime
			// logs contain the wrong file reference. This prettifier should skip on this case
			// and defer to the logrusr.WithReportCaller() option.
			if fileBase == "logrusr.go" {
				return "", ""
			}
			return "", fmt.Sprintf("%s:%d", fileBase, f.Line)
		},
	})
	logger.SetReportCaller(true)
	return WithLogger(parent, logger.WithContext(parent))
}

func WithTestingLogger(parent context.Context) (context.Context, *test.Hook) {
	logger, hook := test.NewNullLogger()
	return WithLogger(parent, logger.WithContext(parent)), hook
}

func WithLogger(parent context.Context, entry *logrus.Entry) context.Context {
	return context.WithValue(parent, loggerKey, entry)
}

func GetLogger(ctx context.Context) *logrus.Entry {
	if logger, ok := ctx.Value(loggerKey).(*logrus.Entry); ok && logger != nil {
		return logger
	}
	noopLogger := logrus.New()
	noopLogger.Out = io.Discard
	return noopLogger.WithContext(ctx)
}
func SetLevel(entry *logrus.Entry, level string) error {
	lvl, err := logrus.ParseLevel(level)
	if err != nil {
		return err
	}

	entry.Logger.SetLevel(lvl)
	return nil
}

// Following setup are from k8s.io/sample-controller/pkg/signals

var onlyOneSignalHandler = make(chan struct{})
var shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

func WithSignalHandler(parent context.Context) context.Context {
	// Panics if called twice
	close(onlyOneSignalHandler)

	// Panics if parent context do not have logger
	log := GetLogger(parent)

	ctx, cancel := context.WithCancel(parent)
	c := make(chan os.Signal, 2)
	signal.Notify(c, shutdownSignals...)
	go func() {
		s := <-c
		log.Infof("Received os.Signal %v, shutting down ...", s)
		cancel()

		s = <-c
		log.Infof("Received second os.Signal %v, exit immediately !", s)
		os.Exit(1)
	}()

	return ctx
}

// Rand wraps *rand.Rand, provides thread-safe access to a subset of
// methods used in EGX.
type Rand struct {
	sync.Mutex
	*rand.Rand
}

func (r *Rand) Float64() float64 {
	r.Lock()
	defer r.Unlock()
	return r.Rand.Float64()
}

func (r *Rand) Intn(n int) int {
	r.Lock()
	defer r.Unlock()
	return r.Rand.Intn(n)
}

func (r *Rand) RandomBytes(n int) []byte {
	b := make([]byte, n)
	r.Lock()
	defer r.Unlock()
	r.Read(b)
	return b
}

// nolint gosec=G404
var defaultRand = &Rand{Rand: rand.New(rand.NewSource(0))}

func WithRandomSeed(parent context.Context, seed int64) context.Context {
	// nolint gosec=G404
	r := &Rand{Rand: rand.New(rand.NewSource(seed))}
	return context.WithValue(parent, randKey, r)
}

func GetRandFloat64(ctx context.Context) float64 {
	if r, ok := ctx.Value(randKey).(*Rand); ok {
		return r.Float64()
	}
	return defaultRand.Float64()
}

func GetRandIntn(ctx context.Context, n int) int {
	if r, ok := ctx.Value(randKey).(*Rand); ok {
		return r.Intn(n)
	}
	return defaultRand.Intn(n)
}

func GetRandomBytes(ctx context.Context, n int) []byte {
	if r, ok := ctx.Value(randKey).(*Rand); ok {
		return r.RandomBytes(n)
	}
	return defaultRand.RandomBytes(n)
}

var defaultClock = time.Now

func WithClock(parent context.Context, clockFunc func() time.Time) context.Context {
	return context.WithValue(parent, clockKey, clockFunc)
}

func GetCurrentTime(ctx context.Context) time.Time {
	if clock, ok := ctx.Value(clockKey).(func() time.Time); ok {
		return clock()
	}
	return defaultClock()
}

// Helper functions to check and remove string from a slice of strings.
func ContainsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func RemoveString(slice []string, s string) (result []string) {
	for _, item := range slice {
		if item == s {
			continue
		}
		result = append(result, item)
	}
	return
}
