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

package otel

import (
	"context"
	"sync/atomic"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	cmnotel "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/otel"
	"go.opentelemetry.io/otel"
	otelattr "go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
)

// OTel attributes follow the naming standards for Open Telemetry attributes
// specified here https://opentelemetry.io/docs/specs/semconv/general/attribute-naming/
const (
	NCAIDAttributeKey             = "nvca.nca_id"
	ClusterNameAttributeKey       = "nvca.cluster_name"
	ClusterGroupAttributeKey      = "nvca.cluster_group"
	VersionAttributeKey           = "nvca.version"
	ICMSRequestIDAttributeKey     = "nvca.spot_request_id"
	ICMSInstanceIDsAttributeKey   = "nvca.spot_instance_ids"
	FunctionIDAttributeKey        = "nvca.function_id"
	FunctionVersionIDAttributeKey = "nvca.function_version_id"
	DeploymentIDAttributeKey      = "nvca.deployment_id"
	TaskIDAttributeKey            = "nvca.task_id"
	MessageBatchIDAttributeKey    = "nvca.message_batch_id"
)

func init() {
	SetDefaultAttributes([]otelattr.KeyValue{})
}

var (
	defaultOTelAttributesVal = &atomic.Value{}
)

func GetDefaultAttributes() []otelattr.KeyValue {
	return defaultOTelAttributesVal.Load().([]otelattr.KeyValue)
}

func SetDefaultAttributes(otelAttrs []otelattr.KeyValue) {
	defaultOTelAttributesVal.Store(otelAttrs)
}

// InvokeWithSpan creates a span and passes it to the downstream
// function (next) that it invokes
// for tracing with OpenTelemetry which includes properly recording an error
// and setting status upon success.
// This additionally sets default attributes for the span
func InvokeWithSpan(ctx context.Context,
	tracer oteltrace.Tracer,
	spanName string,
	next func(ctx context.Context) error,
	opts ...oteltrace.SpanStartOption) error {
	return cmnotel.InvokeWithSpanWithSkip(
		ctx,
		tracer,
		spanName,
		next,
		1,
		append([]oteltrace.SpanStartOption{oteltrace.WithAttributes(GetDefaultAttributes()...)}, opts...)...)
}

// WithName gives the name of the tracer,
// otherwise the default "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca"
// will be used
func WithName(name string) TracerOption {
	return func(to *TracerOptions) {
		to.name = name
	}
}

type TracerOption func(*TracerOptions)

type TracerOptions struct {
	name string
}

// NewTracer constructs a tracer for the nvca package
// with the global attributes used for all traces created
// by this project. If name is left empty the default name
func NewTracer(opts ...TracerOption) oteltrace.Tracer {
	// Get the trace options
	traceOptions := TracerOptions{
		name: "nvca",
	}
	for _, o := range opts {
		o(&traceOptions)
	}
	return otel.Tracer(traceOptions.name)
}

func GetOTelAttributesFromICMSRequest(r *nvcav2beta1.ICMSRequest) []otelattr.KeyValue {
	kvs := []otelattr.KeyValue{
		otelattr.String(NCAIDAttributeKey, r.Spec.NCAId),
		otelattr.String(ICMSRequestIDAttributeKey, r.Spec.RequestID),
		otelattr.String(MessageBatchIDAttributeKey, r.Spec.MessageBatchID),
		otelattr.StringSlice(ICMSInstanceIDsAttributeKey, r.Status.GetInstanceIDs()),
	}

	// Add Deployment ID if it is set
	if r.Spec.CreationMsgInfo.DeploymentID != "" {
		kvs = append(kvs, otelattr.String(DeploymentIDAttributeKey, r.Spec.CreationMsgInfo.DeploymentID))
	}

	switch r.Spec.Action {
	case common.FunctionCreationAction:
		kvs = append(kvs,
			otelattr.String(FunctionIDAttributeKey, r.Spec.FunctionDetails.FunctionID),
			otelattr.String(FunctionVersionIDAttributeKey, r.Spec.FunctionDetails.FunctionVersionID),
		)
	case common.TaskCreationAction:
		kvs = append(kvs,
			otelattr.String(TaskIDAttributeKey, r.Spec.TaskDetails.TaskID),
		)
	}
	return kvs
}
