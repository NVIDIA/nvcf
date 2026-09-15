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

// nvsnap-blobstore — durable backstop for Phase 5d peer fanout.
//
// Single-replica HTTP service backed by a PVC. Agents upload
// captures here after a successful local dump; restore-side
// cascade falls back here when no peer can serve a checkpoint.
//
// Protocol is documented in docs/archive/PHASE5D-PEER-FANOUT-BLOB-STORE.md
// (lines 105-119) and implemented in internal/blobstore/server.go.
package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/blobstore"
)

var version = "0.1.0"

func main() {
	var (
		listenAddr = flag.String("listen", ":9000",
			"HTTP listen address.")
		dataDir = flag.String("data-dir", "/data",
			"Root of the on-disk store. Should be a PVC mountpoint for durability.")
		logLevel = flag.String("log-level", "info",
			"Log level: debug, info, warn, error.")
		readTimeout = flag.Duration("read-timeout", 10*time.Minute,
			"Max time to read a request body (e.g. a 28 GB blob upload).")
		writeTimeout = flag.Duration("write-timeout", 10*time.Minute,
			"Max time to write a response (large blob downloads).")
	)
	flag.Parse()

	log := logrus.New()
	log.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	if lvl, err := logrus.ParseLevel(*logLevel); err == nil {
		log.SetLevel(lvl)
	}
	log.Infof("nvsnap-blobstore v%s starting; data-dir=%s listen=%s",
		version, *dataDir, *listenAddr)

	store, err := blobstore.New(*dataDir)
	if err != nil {
		log.WithError(err).Fatal("init store")
	}

	srv := &http.Server{
		Addr:         *listenAddr,
		Handler:      blobstore.NewServer(store).Handler(),
		ReadTimeout:  *readTimeout,
		WriteTimeout: *writeTimeout,
		// IdleTimeout small so port-forward dev sessions don't pile
		// up zombie conns; uploads/downloads are bounded by Read/Write.
		IdleTimeout: 90 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Infof("listening on %s", *listenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		log.Infof("received %s, shutting down", sig)
	case err := <-errCh:
		log.WithError(err).Error("server error")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.WithError(err).Warn("graceful shutdown failed")
	}
}
