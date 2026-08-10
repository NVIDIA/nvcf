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

package workerinit

import (
	"context"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-init/configs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-init/pkg/ess"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metering"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/tracing"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"

	"go.opentelemetry.io/otel"
	"go.uber.org/zap"
)

type NvcfInitializer struct {
	baseInitializer

	// nil on construction. initialised by Setup
	client *nvcf.Client
}

func (in *NvcfInitializer) Setup() error {
	if err := ess.SetupEssAgent(
		in.config.SecretsAssertionToken,
		in.config.EssAgentConfigDir,
		configs.DefaultRawEssAgentConfigDir,
	); err != nil {
		return err
	}

	client, err := nvcf.CreateClient(
		in.config.NvcfFqdnGrpc,
		nil,
		in.config.NvcfWorkerToken,
		nil,
		in.config.NcaId,
		in.config.InstanceId,
		in.config.FunctionId,
		in.config.FunctionVersionId,
		in.config.SharedConfigDir,
		nvcf.DefaultNvcfClientTimeout,
	)
	if err != nil {
		return err
	}
	in.client = client

	return nil
}

func (in *NvcfInitializer) Run(ctx context.Context) error {
	ctx, span := otel.GetTracerProvider().Tracer("nvcf-worker-init").Start(tracing.PropagateCtxFromEnv(ctx), "Run Initializer")
	defer span.End()
	logger := zap.L().With(utils.PublicLogMarker)
	logger.Info("Initializing NVCF worker",
		zap.String("instance id", in.config.InstanceId),
		zap.String("function id", in.config.FunctionId),
		zap.String("function version id", in.config.FunctionVersionId),
	)
	defer in.client.Close()

	// Start infra metering
	infraMetering := metering.NewInfraMetering(ctx, metering.Function, in.meteringConfg, metering.InfraInitializing)
	defer utils.Close(infraMetering.Close)

	// Start ess agent assertion token refreshing
	in.startAssertionTokenRefresher(ctx, in.client.StartAssertionTokenRefresher)

	// Start metadata credentials refreshing
	in.client.StartMetadataCredentialsRefresher(ctx)

	// Start otel exporter
	if err := metrics.Initialize(metrics.NvcfRootNamespace, nil); err != nil {
		return tracing.RecordSpanError(span, err)
	}

	// TODO add a call to in.client.ConnectIndefinitely() and keep the worker token active on disk
	if err := in.downloadArtifacts(ctx, in.client.GetArtifacts); err != nil {
		logger.Error("failed to download artifacts", zap.Error(err))
		return tracing.RecordSpanError(span, err)
	}
	return nil
}
