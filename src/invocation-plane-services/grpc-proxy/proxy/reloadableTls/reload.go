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
// lovingly modified from https://github.com/dexidp/dex/blob/master/cmd/dex/serve.go

package reloadableTls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"github.com/fsnotify/fsnotify"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// NewTLSReloader returns a [tls.Config] with GetCertificate or GetConfigForClient set
// to reload certificates from the given paths on SIGHUP or on file creates (atomic update via rename).
func NewTLSReloader(certFile, keyFile, caFile string, baseConfig *tls.Config) (*tls.Config, error) {
	// trigger reload on channel
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGHUP)

	// files to watch
	watchFiles := map[string]struct{}{
		certFile: {},
		keyFile:  {},
	}
	if caFile != "" {
		watchFiles[caFile] = struct{}{}
	}
	watchDirs := make(map[string]struct{}) // dedupe dirs
	for f := range watchFiles {
		dir := filepath.Dir(f)
		if !strings.HasPrefix(f, dir) {
			// normalize name to have ./ prefix if only a local path was provided
			// can't pass "" to watcher.Add
			watchFiles[dir+string(filepath.Separator)+f] = struct{}{}
		}
		watchDirs[dir] = struct{}{}
	}
	// trigger reload on file change
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create watcher for TLS reloader: %v", err)
	}
	// recommended by fsnotify: watch the dir to handle renames
	// https://pkg.go.dev/github.com/fsnotify/fsnotify#hdr-Watching_files
	for dir := range watchDirs {
		zap.L().Debug("watching certs dir", zap.String("dir", dir))
		err := watcher.Add(dir)
		if err != nil {
			return nil, fmt.Errorf("watch dir for TLS reloader: %v", err)
		}
	}

	var initialConfig *tls.Config
	// load once outside the goroutine so we can return an error on misconfig
	_, _, err = lo.AttemptWithDelay(5, 500*time.Millisecond, func(index int, duration time.Duration) error {
		var err error
		initialConfig, err = loadTLSConfig(certFile, keyFile, caFile, baseConfig)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("load TLS config: %v", err)
	}

	// stored version of current tls config
	ptr := &atomic.Pointer[tls.Config]{}
	ptr.Store(initialConfig)

	// start background worker to reload certs
	go func() {
	loop:
		for {
			select {
			case sig := <-sigc:
				zap.L().Debug("reloading cert from signal", zap.Any("signal", sig))
			case evt := <-watcher.Events:
				if _, ok := watchFiles[evt.Name]; !ok || !evt.Has(fsnotify.Create) {
					continue loop
				}
				zap.L().Debug("reloading cert from fsnotify", zap.String("name", evt.Name), zap.Any("operation", evt.Op))
			case err := <-watcher.Errors:
				zap.L().Error("TLS reloader watch", zap.Error(err))
			}

			loaded, err := loadTLSConfig(certFile, keyFile, caFile, baseConfig)
			if err != nil {
				zap.L().Error("failed to reload TLS config", zap.Error(err))
				continue loop
			}
			zap.L().Info("reloaded TLS config")
			ptr.Store(loaded)
		}
	}()

	conf := &tls.Config{}
	// https://pkg.go.dev/crypto/tls#baseConfig
	// Server configurations must set one of Certificates, GetCertificate or GetConfigForClient.
	if caFile != "" {
		// grpc will use this via tls.Server for mTLS
		conf.GetConfigForClient = func(chi *tls.ClientHelloInfo) (*tls.Config, error) { return ptr.Load(), nil }
	} else {
		// net/http only uses Certificates or GetCertificate
		conf.GetCertificate = func(chi *tls.ClientHelloInfo) (*tls.Certificate, error) { return &ptr.Load().Certificates[0], nil }
	}
	return conf, nil
}

// loadTLSConfig loads the given file paths into a [tls.Config]
func loadTLSConfig(certFile, keyFile, caFile string, baseConfig *tls.Config) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading TLS keypair: %v", err)
	}
	loadedConfig := baseConfig.Clone() // copy
	loadedConfig.Certificates = []tls.Certificate{cert}
	if caFile != "" {
		cPool := x509.NewCertPool()
		clientCert, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("reading from client CA file: %v", err)
		}
		if !cPool.AppendCertsFromPEM(clientCert) {
			return nil, errors.New("failed to parse client CA")
		}

		loadedConfig.ClientAuth = tls.RequireAndVerifyClientCert
		loadedConfig.ClientCAs = cPool
	}
	return loadedConfig, nil
}
