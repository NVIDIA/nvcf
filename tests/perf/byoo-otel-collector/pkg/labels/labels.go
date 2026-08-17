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

// Package labels holds the Kubernetes labels every object the performance suite
// creates carries. They are shared so the deploy, sink, and loadgen packages
// tag their resources identically, which lets cleanup delete exactly what the
// suite created (scoped by PartOf) and never touch anything else.
package labels

const (
	// PartOf tags every object the suite owns. Cleanup is scoped to it.
	PartOf = "app.kubernetes.io/part-of"
	// PartOfValue is the shared value for PartOf.
	PartOfValue = "byoo-perf"

	// Instance uniquely identifies a single deployed workload so a Service
	// selects only its own pod.
	Instance = "app.kubernetes.io/instance"

	// ManagedBy marks ownership for observability.
	ManagedBy = "app.kubernetes.io/managed-by"
	// ManagedByValue is the shared value for ManagedBy.
	ManagedByValue = "byoo-perf"

	// Component distinguishes the roles the suite deploys (collector under
	// test, otlp sink, load generator).
	Component = "app.kubernetes.io/component"
)

// Component values.
const (
	ComponentCollector = "collector"
	ComponentSink      = "otlp-sink"
	ComponentLoadgen   = "loadgen"
)

// Base returns the labels every suite object carries.
func Base() map[string]string {
	return map[string]string{
		PartOf:    PartOfValue,
		ManagedBy: ManagedByValue,
	}
}

// Selector returns the label selector that matches every object the suite
// created, for scoped listing and deletion.
func Selector() string {
	return PartOf + "=" + PartOfValue
}
