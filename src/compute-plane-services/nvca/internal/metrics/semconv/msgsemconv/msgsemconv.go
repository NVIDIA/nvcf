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

// Package msgsemconv builds the OpenTelemetry Semantic Conventions attribute
// set for messaging operations (for example SQS and NATS). It is consumed by the
// queue-client decorator in internal/metrics/clientmetrics.
package msgsemconv

import (
	"go.opentelemetry.io/otel/attribute"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/semconv"
)

// Attribute keys follow https://opentelemetry.io/docs/specs/semconv/messaging/messaging-metrics/
const (
	SystemKey          = attribute.Key("messaging.system")
	OperationKey       = attribute.Key("messaging.operation.name")
	DestinationNameKey = attribute.Key("messaging.destination.name")
)

// Known messaging.system values used by NVCA.
const (
	SystemSQS  = "aws_sqs"
	SystemNATS = "nats"
)

// ClientAttrs returns the semconv attribute set for one messaging operation.
// peerService groups by dependency; system, operation, and destination describe
// the messaging call; errType is included only on failure.
//
// Batch size is deliberately not an attribute. semconv defines
// messaging.batch.message_count, but as a metric label it multiplies the duration
// histogram by one series per distinct batch size (0..10 for SQS) while measuring
// throughput rather than identifying the call. Record it as its own instrument if
// it is ever needed.
func ClientAttrs(peerService, system, operation, destination string, errType string) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 5)
	if peerService != "" {
		attrs = append(attrs, semconv.PeerService(peerService))
	}
	if system != "" {
		attrs = append(attrs, SystemKey.String(system))
	}
	if operation != "" {
		attrs = append(attrs, OperationKey.String(operation))
	}
	if destination != "" {
		attrs = append(attrs, DestinationNameKey.String(destination))
	}
	if errType != "" {
		attrs = append(attrs, semconv.ErrorType(errType))
	}
	return attrs
}
