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

// Command gds-server is the Global Discovery Service server. It parses the
// configuration (flags with GDS_ environment fallbacks), opens the store,
// wires the view, the watch hub, the refresher, the compactor and the API,
// and serves gRPC, REST and admin listeners until SIGINT or SIGTERM, then
// shuts down gracefully.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/server"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/storefactory"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/version"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.LookupEnv, os.Stderr)
	stop()
	os.Exit(code)
}

// newLogger returns a JSON zap logger at level writing to w.
func newLogger(level string, w io.Writer) (*zap.Logger, error) {
	lvl, err := zapcore.ParseLevel(level)
	if err != nil {
		return nil, err
	}
	enc := zap.NewProductionEncoderConfig()
	enc.TimeKey = "ts"
	enc.EncodeTime = zapcore.RFC3339NanoTimeEncoder
	core := zapcore.NewCore(zapcore.NewJSONEncoder(enc), zapcore.AddSync(w), lvl)
	return zap.New(core, zap.AddCaller()).With(zap.String("service", version.Service)), nil
}

func run(ctx context.Context, args []string, lookupEnv func(string) (string, bool), stderr io.Writer) int {
	cfg, err := config.Parse(args, lookupEnv, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "gds-server: %v\n", err)
		return 2
	}
	log, err := newLogger(cfg.LogLevel, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "gds-server: %v\n", err)
		return 2
	}
	defer func() { _ = log.Sync() }()

	if cfg.OTelEnabled {
		shutdown, err := server.SetupTracing(ctx, version.Service, version.Version)
		if err != nil {
			log.Error("tracing setup failed", zap.Error(err))
			return 1
		}
		defer func() {
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := shutdown(flushCtx); err != nil {
				log.Warn("trace flush failed", zap.Error(err))
			}
		}()
	}

	st, err := storefactory.Open(ctx, cfg)
	if err != nil {
		log.Error("open store failed", zap.String("store", cfg.Store), zap.Error(err))
		return 1
	}
	defer func() { _ = st.Close() }()

	app, err := server.NewApp(cfg, server.AppOptions{Store: st, Logger: log})
	if err != nil {
		log.Error("start failed", zap.Error(err))
		return 1
	}
	log.Info("starting gds-server", zap.String("version", version.Version), zap.String("git_hash", version.GitHash),
		zap.String("region", cfg.Region), zap.String("store", cfg.Store),
		zap.Duration("refresh_interval", cfg.RefreshInterval), zap.Duration("lease_skew", cfg.LeaseSkew),
		zap.Bool("otel", cfg.OTelEnabled))
	if err := app.Server.Run(ctx); err != nil {
		log.Error("server failed", zap.Error(err))
		return 1
	}
	return 0
}
