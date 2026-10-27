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

// Command gds-mcs-controller implements the Kubernetes Multi-Cluster
// Services API on top of the Global Discovery Service: it exports this
// cluster's ServiceExports to GDS and materialises the clusterset's
// ServiceImports, derived Services and EndpointSlices from it. See
// docs/mcs-controller.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/app"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/mcs/tracing"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/version"
)

func main() {
	cfg, zapOpts, code := parse(flag.CommandLine, os.Args[1:], os.Stderr)
	if code >= 0 {
		os.Exit(code)
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(zapOpts)))
	os.Exit(run(ctrl.SetupSignalHandler(), cfg))
}

// parse binds and parses the flags and validates the configuration. code is
// -1 when the controller should start, else the exit code.
func parse(fs *flag.FlagSet, args []string, stderr io.Writer) (config.Config, *zap.Options, int) {
	var cfg config.Config
	cfg.BindFlags(fs)
	zapOpts := &zap.Options{}
	zapOpts.BindFlags(fs)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return cfg, zapOpts, 0
		}
		return cfg, zapOpts, 2
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(stderr, "gds-mcs-controller: invalid configuration:\n%v\n", err)
		return cfg, zapOpts, 2
	}
	return cfg, zapOpts, -1
}

func run(ctx context.Context, cfg config.Config) int {
	log := ctrl.Log.WithName("setup")
	if cfg.OTelEnabled {
		shutdown, err := tracing.Setup(ctx, version.Service, version.Version)
		if err != nil {
			log.Error(err, "Tracing setup failed")
			return 1
		}
		defer func() {
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := shutdown(flushCtx); err != nil {
				log.Error(err, "Trace flush failed")
			}
		}()
	}
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		log.Error(err, "No Kubernetes configuration")
		return 1
	}
	a, err := app.New(ctx, cfg, restCfg, app.Options{Registry: ctrlmetrics.Registry})
	if err != nil {
		log.Error(err, "Start failed")
		return 1
	}
	if err := a.Run(ctx); err != nil {
		log.Error(err, "gds-mcs-controller stopped with an error")
		return 1
	}
	return 0
}
