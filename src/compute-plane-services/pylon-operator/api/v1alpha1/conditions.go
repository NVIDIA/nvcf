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

package v1alpha1

// ConditionType names a condition in InferenceEndpoint status.
type ConditionType string

const (
	// ConditionReady is True when the Service has ready endpoints and answers
	// health.path on service.port with the expected model.
	ConditionReady ConditionType = "Ready"
	// ConditionTransportReady is True when the transport pods run and Pylon's
	// reverse tunnel to the router is connected.
	ConditionTransportReady ConditionType = "TransportReady"
	// ConditionRegistered is True when the router admitted a registration
	// stream from at least one transport pod.
	ConditionRegistered ConditionType = "Registered"
)

// ConditionTypes lists every condition the operator writes, in display order.
func ConditionTypes() []ConditionType {
	return []ConditionType{ConditionReady, ConditionTransportReady, ConditionRegistered}
}

// ReadyReason is a reason for the Ready condition.
type ReadyReason string

const (
	// ReadyReasonHealthProbeSucceeded: the Service has ready endpoints, the
	// health probe returned 2xx and the model listing, when parseable,
	// contains spec.modelName.
	ReadyReasonHealthProbeSucceeded ReadyReason = "HealthProbeSucceeded"
	// ReadyReasonServiceNotFound: spec.service.name does not exist.
	ReadyReasonServiceNotFound ReadyReason = "ServiceNotFound"
	// ReadyReasonNoReadyEndpoints: the Service has no ready endpoint.
	ReadyReasonNoReadyEndpoints ReadyReason = "NoReadyEndpoints"
	// ReadyReasonHealthProbeFailed: the health probe failed, timed out or
	// returned a non-2xx status.
	ReadyReasonHealthProbeFailed ReadyReason = "HealthProbeFailed"
	// ReadyReasonModelNameMismatch: the backend's model listing does not
	// contain spec.modelName.
	ReadyReasonModelNameMismatch ReadyReason = "ModelNameMismatch"
)

// ReadyFailureReasons lists every reason that sets Ready to False.
func ReadyFailureReasons() []ReadyReason {
	return []ReadyReason{
		ReadyReasonServiceNotFound,
		ReadyReasonNoReadyEndpoints,
		ReadyReasonHealthProbeFailed,
		ReadyReasonModelNameMismatch,
	}
}

// TransportReadyReason is a reason for the TransportReady condition.
type TransportReadyReason string

const (
	// TransportReadyReasonPylonConnected: transport pods are ready and at
	// least one reports a connected reverse tunnel.
	TransportReadyReasonPylonConnected TransportReadyReason = "PylonConnected"
	// TransportReadyReasonTunnelNotConnected: no transport pod reports a
	// connected reverse tunnel.
	TransportReadyReasonTunnelNotConnected TransportReadyReason = "TunnelNotConnected"
	// TransportReadyReasonTransportPodsNotRunning: the transport Deployment
	// has no running pods.
	TransportReadyReasonTransportPodsNotRunning TransportReadyReason = "TransportPodsNotRunning"
	// TransportReadyReasonWaitingForUpstream: Ready is False, so Pylon idles.
	TransportReadyReasonWaitingForUpstream TransportReadyReason = "WaitingForUpstream"
	// TransportReadyReasonScaledToZero: the transport is scaled to zero
	// because of a model name mismatch.
	TransportReadyReasonScaledToZero TransportReadyReason = "ScaledToZero"
	// TransportReadyReasonPending: the registration observer has not
	// reported yet.
	TransportReadyReasonPending TransportReadyReason = "Pending"
)

// RegisteredReason is a reason for the Registered condition.
type RegisteredReason string

const (
	// RegisteredReasonRegisteredWithRouter: at least one transport pod has an
	// open registration stream to at least one router.
	RegisteredReasonRegisteredWithRouter RegisteredReason = "RegisteredWithRouter"
	// RegisteredReasonPending: a transport pod is still connecting, or the
	// registration observer has not reported yet.
	RegisteredReasonPending RegisteredReason = "Pending"
	// RegisteredReasonRouterUnreachable: the backend is healthy and the pods
	// run, but no registration stream is open.
	RegisteredReasonRouterUnreachable RegisteredReason = "RouterUnreachable"
	// RegisteredReasonRegistrationRejected: the router closed the stream with
	// unauthenticated or invalid_argument.
	RegisteredReasonRegistrationRejected RegisteredReason = "RegistrationRejected"
	// RegisteredReasonWaitingForUpstream: Ready is False, so Pylon opens no
	// stream.
	RegisteredReasonWaitingForUpstream RegisteredReason = "WaitingForUpstream"
	// RegisteredReasonTransportPodsNotRunning: the transport Deployment has
	// no running pods.
	RegisteredReasonTransportPodsNotRunning RegisteredReason = "TransportPodsNotRunning"
	// RegisteredReasonScaledToZero: the transport is scaled to zero because
	// of a model name mismatch.
	RegisteredReasonScaledToZero RegisteredReason = "ScaledToZero"
)
