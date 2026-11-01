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
	"go.opentelemetry.io/otel"
	otelattr "go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"

	nvcfv1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

// OTel attributes follow the naming standards for Open Telemetry attributes
// specified here https://opentelemetry.io/docs/specs/semconv/general/attribute-naming/
const (
	NCAIDAttributeKey            = "nvca-operator.nca_id"
	ClusterNameAttributeKey      = "nvca-operator.clustername"
	VersionAttributeKey          = "nvca-operator.version"
	NVCAClusterNameAttributeKey  = "nvca.clustername"
	NVCAClusterGroupAttributeKey = "nvca.clustergroupname"
	NVCAVersionAttributeKey      = "nvca.version"
)

type AttributesGetter interface {
	GetOTelAttributes() []otelattr.KeyValue
}

// NewTracer constructs a tracer for the bart package
// with the global attributes used for all traces created
// by this project
func NewTracer(otelAttrGetters ...AttributesGetter) oteltrace.Tracer {
	var defaultOTelAttributes []otelattr.KeyValue
	for _, v := range otelAttrGetters {
		defaultOTelAttributes = append(defaultOTelAttributes, v.GetOTelAttributes()...)
	}
	return otel.Tracer("github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/reconcile",
		oteltrace.WithInstrumentationAttributes(defaultOTelAttributes...))
}

func GetOTelAttributesFromNVCFBackend(nb *nvcfv1.NVCFBackend) []otelattr.KeyValue {
	return []otelattr.KeyValue{
		otelattr.String(NVCAClusterNameAttributeKey, nb.Spec.ClusterConfig.ClusterName),
		otelattr.String(NVCAClusterGroupAttributeKey, nb.Spec.ClusterConfig.ClusterGroupName),
		otelattr.String(NVCAVersionAttributeKey, nb.Spec.Version),
	}
}
