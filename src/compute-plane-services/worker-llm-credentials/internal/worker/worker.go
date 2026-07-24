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

package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/token"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-llm-credentials/configs"
)

type Worker struct {
	config configs.Config
	client *nvcf.Client
}

func New(cfg configs.Config) (*Worker, error) {
	if cfg.SharedConfigDir == "" {
		cfg.SharedConfigDir = configs.DefaultSharedConfigDir
	}
	if cfg.WorkerTokenPath == "" {
		cfg.WorkerTokenPath = configs.DefaultWorkerTokenPath
	}

	client, err := nvcf.CreateClient(
		cfg.NvcfFqdnGrpc,
		nil,
		cfg.NvcfWorkerToken,
		nil,
		cfg.NcaId,
		cfg.InstanceId,
		cfg.FunctionId,
		cfg.FunctionVersionId,
		cfg.SharedConfigDir,
		nvcf.DefaultNvcfClientTimeout,
	)
	if err != nil {
		return nil, err
	}

	return &Worker{
		config: cfg,
		client: client,
	}, nil
}

func (w *Worker) Run(ctx context.Context) error {
	defer w.client.Close()

	zap.L().Info("starting NVCF LLM credentials worker",
		zap.String("instance_id", w.config.InstanceId),
		zap.String("function_id", w.config.FunctionId),
		zap.String("function_version_id", w.config.FunctionVersionId),
	)

	ctx, err := w.client.ConnectIndefinitely(ctx)
	if err != nil {
		return err
	}

	token.StartTokenRefresher(ctx, "llm worker token", true,
		func(ctx context.Context) (token.Token, error) {
			t, err := w.client.NvcfTokenProvider.Token()
			if err != nil {
				return token.Token{}, err
			}
			return token.Token{Token: t.AccessToken, Expiration: t.Expiry}, nil
		},
		func(t token.Token) error {
			zap.L().Info("writing worker token to disk", zap.String("path", w.config.WorkerTokenPath))
			dir := filepath.Dir(w.config.WorkerTokenPath)
			if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
				if err := utils.CreateDirectory(dir, os.FileMode(0755)); err != nil {
					return err
				}
			}
			tmp := w.config.WorkerTokenPath + ".tmp"
			// 0644 so the pylon sidecar (a different non-root UID) can read the
			// token from the shared emptyDir; only these two infra sidecars
			// mount it, not the customer workload container.
			//nolint:gosec // G306: intentionally readable by the pylon sidecar
			if err := os.WriteFile(tmp, []byte(t.Token), 0644); err != nil {
				return err
			}
			// WriteFile honors umask and skips the mode when tmp already exists;
			// chmod guarantees 0644 so the sidecar can read it.
			//nolint:gosec // G302: intentionally readable by the pylon sidecar
			if err := os.Chmod(tmp, 0644); err != nil {
				return err
			}
			return os.Rename(tmp, w.config.WorkerTokenPath)
		},
	)

	<-ctx.Done()
	return nil
}
