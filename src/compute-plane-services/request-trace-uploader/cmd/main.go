// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// request-trace-uploader validates and discovers Dynamo request-trace segments.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/service"
)

func main() {
	cfg, warnings, err := config.LoadFromEnv()
	if err != nil {
		slog.Error("invalid request trace uploader configuration", "error", err)
		os.Exit(1)
	}
	for _, warning := range warnings {
		slog.Warn("request trace uploader configuration fallback", "setting", warning)
	}

	svc, err := service.New(cfg)
	if err != nil {
		slog.Error("create request trace uploader service", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := svc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("request trace uploader stopped", "error", err)
		os.Exit(1)
	}
}
