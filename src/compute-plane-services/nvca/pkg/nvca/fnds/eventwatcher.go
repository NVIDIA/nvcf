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

package fnds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	nvcffndstypes "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/fnds/common/core/types"
	common "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	otelattr "go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	listersv1 "k8s.io/client-go/listers/core/v1"

	nvcaotel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/otel"
	nvcav2beta1listers "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/listers/nvca/v2beta1"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	// EventType identifies the producer of events emitted by NVCA.
	EventType = "nvca"

	// Container Events are valid event types that can be sent to
	// Function Deployment Stages. They roughly correlate
	// to various events in the pod/container lifecycles.

	// ContainerStatusBuildingEvent emitted when a container has been created inside a pod that has successfully scheduled
	ContainerStatusBuildingEvent = "building"
	// ContainerStatusBuildingErrorEvent represents containers that have failed to schedule due to e.g. resource constraints
	ContainerStatusBuildingErrorEvent = "buildingError"
	// ContainerStatusDownloadingEvent represents containers that are downloading images
	ContainerStatusDownloadingEvent = "downloadingContainer"
	// ContainerStatusDownloadingErrorEvent represents containers that have failed to download images
	ContainerStatusDownloadingErrorEvent = "downloadingContainerError"
	// ContainerStatusDownloadingModelEvent represents containers that are downloading models
	ContainerStatusDownloadingModelEvent = "downloadingContainerModel"
	// ContainerStatusDownloadingModelErrorEvent represents containers that have failed to download models
	ContainerStatusDownloadingModelErrorEvent = "downloadingContainerModelError"
	// ContainerStatusInitializingEvent represents containers that are initializing
	ContainerStatusInitializingEvent = "initializingContainer"
	// ContainerStatusInitializingErrorEvent represents containers that have failed to initialize
	ContainerStatusInitializingErrorEvent = "initializingContainerError"
	// ContainerStatusTerminatingEvent represents containers that have been scheduled for deletion
	ContainerStatusTerminatingEvent = "destroyed"

	// Kubernetes Reasons and Events are what we use to determine the state of a container
	// and what event to send to FNDS. Events typically include "<reason> - <message>"

	// Kubernetes Reasons for container events
	K8sReasonFailedScheduling     = "FailedScheduling" // = buildingError
	K8sReasonContainerScheduled   = "Scheduled"        // = building
	K8sReasonContainerImagePulled = "Pulled"           // = downloadingContainer
	K8sReasonContainerCreated     = "Created"          // = initializingContainer
	K8sReasonContainerBackOff     = "BackOff"          // = initializingContainerError
	K8sReasonContainerStarted     = "Started"          // = ready
	K8sReasonContainerKilling     = "Killing"          // = destroying
	K8sReasonContainerFailed      = "Failed"           // = failed

	// Kubernetes Messages for container events
	K8sMessagePending           = "Pending"
	K8sMessageContainerCreating = "ContainerCreating"
	K8sMessagePullImage         = "PullImage"
	K8sMessageErrImagePull      = "ErrImagePull"
	K8sMessageImageNeverPull    = "ErrImageNeverPull"
	K8sMessageImagePullBackOff  = "ImagePullBackOff"
	K8sMessagePodInitializing   = "PodInitializing"
	K8sMessageUnhealthy         = "Unhealthy"
)

type EventDetails struct {
	ICMSDetailsInstanceType string `json:"instanceType"`
	EventLogMessage         string `json:"logMessage"`
	EventErrorMessage       string `json:"errorMessage"`
	// TerminationReason is set for destroyed events: "user_initiated" or "zone_initiated" (for FNDS SLO metrics)
	TerminationReason string `json:"terminationReason,omitempty"`
	// ContainerImages is a map of image names to metadata information. Example key: stg.nvcr.io/nvidia/nvcf-byoc/nvca-operator:0.0.1
	ContainerImages map[string]ImageMetadata `json:"containerImages"`
}

type ImageMetadata struct {
	SizeBytes int64 `json:"sizeBytes"`
}

func ProcessFnDSStageTransitionEvent(
	ctx context.Context,
	event *corev1.Event,
	podLister listersv1.PodLister,
	getNode func(name string) (*corev1.Node, error),
	icmsRequestLister nvcav2beta1listers.ICMSRequestLister,
	fndsClient Client,
	tracer oteltrace.Tracer,
) error {
	log := core.GetLogger(ctx)

	if event == nil {
		log.Debugf("received nil event")
		return fmt.Errorf("received nil event")
	}

	var podName string
	var podNamespace string

	podName = event.InvolvedObject.Name
	podNamespace = event.InvolvedObject.Namespace

	if event.Reason == "" || event.Message == "" {
		log.Debugf("received container event with empty reason or message for pod %s/%s", podNamespace, podName)
		return fmt.Errorf("received container event with empty reason or message for pod %s/%s", podNamespace, podName)
	}

	log.WithFields(logrus.Fields{
		"pod":     fmt.Sprintf("%s/%s", podNamespace, podName),
		"reason":  event.Reason,
		"message": event.Message,
	}).Debug("Received container event")

	if podLister == nil {
		log.Debugf("podLister is nil, cannot get pod %s/%s", podNamespace, podName)
		return fmt.Errorf("podLister is nil, cannot get pod %s/%s", podNamespace, podName)
	}

	pod, err := podLister.Pods(podNamespace).Get(podName)
	if err != nil {
		log.WithError(err).Debugf("failed to get pod %s/%s: %v", podNamespace, podName, err)
		return fmt.Errorf("failed to get pod %s/%s: %v", podNamespace, podName, err)
	}

	if len(pod.Labels) == 0 {
		log.Debugf("pod %s/%s has no labels", podNamespace, podName)
		return fmt.Errorf("pod %s/%s has no labels", podNamespace, podName)
	}

	containerImages := make(map[string]ImageMetadata, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	for _, c := range append(pod.Spec.Containers, pod.Spec.InitContainers...) {
		containerImages[c.Image] = ImageMetadata{}
	}

	node, err := getNode(pod.Spec.NodeName)
	if err != nil {
		log.WithError(err).Debugf("failed to get node %s: %v", pod.Spec.NodeName, err)
		return fmt.Errorf("failed to get node %s: %v", pod.Spec.NodeName, err)
	}

	for _, i := range node.Status.Images {
		for _, name := range i.Names {
			if img, ok := containerImages[name]; ok {
				img.SizeBytes = i.SizeBytes
				containerImages[name] = img
			}
		}
	}

	icmsReqID, hasICMSRequestLabel := pod.Labels[nvcatypes.ICMSRequestIDKey]
	if !hasICMSRequestLabel {
		log.Debugf("pod %s/%s is missing required label %s", podNamespace, podName, nvcatypes.ICMSRequestIDKey)
		return fmt.Errorf("pod %s/%s is missing required label %s", podNamespace, podName, nvcatypes.ICMSRequestIDKey)
	}

	lblICMSRequestIDReq, err := labels.NewRequirement(nvcatypes.ICMSRequestIDKey, selection.Equals, []string{icmsReqID})
	if err != nil {
		log.WithError(err).Debugf("failed to create label requirement for pod %s/%s: %v", podNamespace, podName, err)
		return fmt.Errorf("failed to create label requirement for pod %s/%s: %v", podNamespace, podName, err)
	}

	if icmsRequestLister == nil {
		log.Debugf("icmsRequestLister is nil, cannot list ICMS requests for pod %s/%s", podNamespace, podName)
		return fmt.Errorf("icmsRequestLister is nil, cannot list ICMS requests for pod %s/%s", podNamespace, podName)
	}

	icmsRequests, err := icmsRequestLister.List(labels.NewSelector().Add(*lblICMSRequestIDReq))
	if err != nil {
		log.WithError(err).Debugf("failed to get ICMS request for pod %s/%s: %v", podNamespace, podName, err)
		return fmt.Errorf("failed to get ICMS request for pod %s/%s: %v", podNamespace, podName, err)
	}

	if len(icmsRequests) < 1 {
		log.Debugf("no ICMS request found for pod %s/%s", podNamespace, podName)
		return fmt.Errorf("no ICMS request found for pod %s/%s", podNamespace, podName)
	}

	icmsRequest := icmsRequests[0]

	if fndsClient == nil {
		log.Debugf("fndsClient is nil")
		return fmt.Errorf("fndsClient is nil")
	}

	// Call directly without assigning to a variable to avoid issues
	ncaId := fndsClient.GetNcaId()
	if ncaId == "" {
		log.Debugf("fndsClient is missing NCA ID")
		return fmt.Errorf("fndsClient is missing NCA ID")
	}

	// Check if we have function details
	if icmsRequest.Spec.FunctionDetails.FunctionID == "" || icmsRequest.Spec.FunctionDetails.FunctionVersionID == "" {
		log.Debugf("ICMS request %s/%s is missing function details", podNamespace, podName)
		return fmt.Errorf("ICMS request %s/%s is missing function details", podNamespace, podName)
	}

	// Parse function IDs directly when creating the event
	functionId, err := uuid.Parse(icmsRequest.Spec.FunctionDetails.FunctionID)
	if err != nil {
		log.WithError(err).Debugf("invalid function ID format for ICMS request %s/%s: %v", podNamespace, podName, err)
		return fmt.Errorf("invalid function ID format for ICMS request %s/%s: %v", podNamespace, podName, err)
	}

	functionVersionId, err := uuid.Parse(icmsRequest.Spec.FunctionDetails.FunctionVersionID)
	if err != nil {
		log.WithError(err).Debugf("invalid function version ID format for ICMS request %s/%s: %v", podNamespace, podName, err)
		return fmt.Errorf("invalid function version ID format for ICMS request %s/%s: %v", podNamespace, podName, err)
	}

	instanceTypeName := icmsRequest.Spec.CreationMsgInfo.InstanceTypeName
	if instanceTypeName == "" {
		log.Warnf("missing instance type name in ICMS request for pod %s/%s", podNamespace, podName)
	}

	// Map Kubernetes event reasons to StageTransitionEvent events
	var eventReason string
	var logMessage string
	var errorMessage string
	switch event.Reason {
	case K8sReasonFailedScheduling:
		eventReason = ContainerStatusBuildingErrorEvent
		errorMessage = event.Message
	case K8sReasonContainerScheduled:
		eventReason = ContainerStatusBuildingEvent
		logMessage = event.Message
	case K8sReasonContainerImagePulled:
		eventReason = ContainerStatusDownloadingEvent
		logMessage = event.Message
	case K8sReasonContainerCreated:
		eventReason = ContainerStatusInitializingEvent
		logMessage = event.Message
	case K8sReasonContainerKilling:
		// Use "destroyed" (FNDS accepts it). TerminationReason in Details distinguishes user vs zone for SLO metrics.
		eventReason = ContainerStatusTerminatingEvent
		logMessage = event.Message
	case K8sReasonContainerBackOff:
		eventReason = ContainerStatusInitializingErrorEvent
		errorMessage = event.Message
	case K8sReasonContainerFailed:
		if event.Message == fmt.Sprintf("Error: %s", K8sMessageErrImagePull) {
			eventReason = ContainerStatusDownloadingErrorEvent
			errorMessage = event.Message
		} else {
			eventReason = ContainerStatusInitializingErrorEvent
			errorMessage = event.Message
		}
	case "":
		log.Debugf("empty event reason in stage transition event for pod %s/%s", podNamespace, podName)
		return fmt.Errorf("empty event reason in stage transition event for pod %s/%s", podNamespace, podName)
	default:
		log.Debugf("unhandled event reason in stage transition event for pod %s/%s: %s", podNamespace, podName, event.Reason)
		return nil
	}

	details := EventDetails{
		ICMSDetailsInstanceType: instanceTypeName,
		EventLogMessage:         logMessage,
		EventErrorMessage:       errorMessage,
		ContainerImages:         containerImages,
	}
	if event.Reason == K8sReasonContainerKilling {
		if icmsRequest.Spec.Action == common.TerminationAction {
			details.TerminationReason = "user_initiated"
		} else {
			details.TerminationReason = "zone_initiated"
		}
	}
	detailsJson, err := json.Marshal(details)
	if err != nil {
		log.WithError(err).Debugf("failed to marshal ICMS event details for pod %s/%s: %v", podNamespace, podName, err)
		return fmt.Errorf("failed to marshal ICMS event details for pod %s/%s: %v", podNamespace, podName, err)
	}

	ctx = nvcaotel.ContextWithParentSpanFromICMS(ctx, icmsRequest.Spec.GetTraceContext())
	if tracer != nil {
		var span oteltrace.Span
		ctx, span = tracer.Start(
			ctx,
			fmt.Sprintf("%s.ProcessPodEvent.%s", EventType, event.Reason),
			oteltrace.WithSpanKind(oteltrace.SpanKindProducer),
			oteltrace.WithAttributes(nvcaotel.GetDefaultAttributes()...),
			oteltrace.WithAttributes(nvcaotel.GetOTelAttributesFromICMSRequest(icmsRequest)...),
			oteltrace.WithAttributes(
				otelattr.String("k8s.pod.name", event.InvolvedObject.Name),
				otelattr.String("k8s.pod.namespace", event.InvolvedObject.Namespace),
				otelattr.String("event.reason", event.Reason),
			))
		defer span.End()
	}

	// Create a stage transition event
	ste, err := fndsClient.NewStageTransitionEvent(ncaId, functionId, functionVersionId, pod.Name, eventReason, EventType, detailsJson)
	if err != nil {
		log.WithError(err).Debugf("failed to create stage transition event for pod %s/%s: %v", podNamespace, podName, err)
		return fmt.Errorf("failed to create stage transition event for pod %s/%s: %v", podNamespace, podName, err)
	}

	if ste.Event != "" {
		log.WithFields(logrus.Fields{
			"kind":              "StageTransitionEvent",
			"pod":               fmt.Sprintf("%s/%s", podNamespace, podName),
			"event":             ste.Event,
			"functionId":        ste.FunctionId.String(),
			"functionVersionId": ste.FunctionVersionId.String(),
		}).Debug("Creating stage transition event")

		err = fndsClient.CreateEvent(ctx, ste)
		if err != nil {
			var steError nvcffndstypes.ProblemDetails
			jsonErr := json.Unmarshal([]byte(err.Error()), &steError)

			if jsonErr == nil && steError.Title != "" {
				log.WithError(jsonErr).Debugf("failed to create stage transition event for pod %s/%s: %s", podNamespace, podName, steError.Title)
				return errors.New(steError.Title)
			}

			log.WithError(err).Debugf("failed to create stage transition event for pod %s/%s: %s", podNamespace, podName, strings.TrimSpace(err.Error()))
			return errors.New(strings.TrimSpace(err.Error()))
		}
	}

	return nil
}
