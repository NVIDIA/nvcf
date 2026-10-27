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

// Command gds-server is the Global Discovery Service server. This build only
// parses and validates its configuration and exits; the listeners, the view
// and the store wiring are added in a later change.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.LookupEnv, os.Stderr))
}

func run(args []string, lookupEnv func(string) (string, bool), stderr io.Writer) int {
	cfg, err := config.Parse(args, lookupEnv, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "gds-server: %v\n", err)
		return 2
	}
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	log.Info("configuration is valid; the server is not implemented in this build",
		append(version.KeysAndValues(),
			"grpcListenAddress", cfg.GRPCListenAddress,
			"httpListenAddress", cfg.HTTPListenAddress,
			"adminListenAddress", cfg.AdminListenAddress,
			"region", cfg.Region,
			"store", cfg.Store,
		)...)
	return 0
}
