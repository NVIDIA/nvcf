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
	"testing"

	nvcffndstypes "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/fnds/common/core/types"
	common "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	listersv1 "k8s.io/client-go/listers/core/v1"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcav2beta1listers "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/listers/nvca/v2beta1"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// Define mocks for testing
type mockPodLister struct {
	mock.Mock
}

func (m *mockPodLister) List(selector labels.Selector) (ret []*corev1.Pod, err error) {
	args := m.Called(selector)
	return args.Get(0).([]*corev1.Pod), args.Error(1)
}

func (m *mockPodLister) Pods(namespace string) listersv1.PodNamespaceLister {
	args := m.Called(namespace)
	return args.Get(0).(listersv1.PodNamespaceLister)
}

type mockPodNamespaceLister struct {
	mock.Mock
}

func (m *mockPodNamespaceLister) List(selector labels.Selector) (ret []*corev1.Pod, err error) {
	args := m.Called(selector)
	return args.Get(0).([]*corev1.Pod), args.Error(1)
}

func (m *mockPodNamespaceLister) Get(name string) (*corev1.Pod, error) {
	args := m.Called(name)
	return args.Get(0).(*corev1.Pod), args.Error(1)
}

type mockNodeGetter struct {
	mock.Mock
}

func (m *mockNodeGetter) Get(name string) (*corev1.Node, error) {
	args := m.Called(name)
	return args.Get(0).(*corev1.Node), args.Error(1)
}

// mockICMSRequestNamespaceLister implements nvcav2beta1listers.ICMSRequestNamespaceLister
type mockICMSRequestNamespaceLister struct {
	mock.Mock
}

func (m *mockICMSRequestNamespaceLister) List(selector labels.Selector) (ret []*nvcav2beta1.ICMSRequest, err error) {
	args := m.Called(selector)
	return args.Get(0).([]*nvcav2beta1.ICMSRequest), args.Error(1)
}

func (m *mockICMSRequestNamespaceLister) Get(name string) (*nvcav2beta1.ICMSRequest, error) {
	args := m.Called(name)
	return args.Get(0).(*nvcav2beta1.ICMSRequest), args.Error(1)
}

// mockICMSRequestLister implements nvcav2beta1listers.ICMSRequestLister
type mockICMSRequestLister struct {
	mock.Mock
}

func (m *mockICMSRequestLister) List(selector labels.Selector) (ret []*nvcav2beta1.ICMSRequest, err error) {
	args := m.Called(selector)
	return args.Get(0).([]*nvcav2beta1.ICMSRequest), args.Error(1)
}

func (m *mockICMSRequestLister) ICMSRequests(namespace string) nvcav2beta1listers.ICMSRequestNamespaceLister {
	args := m.Called(namespace)
	return args.Get(0).(nvcav2beta1listers.ICMSRequestNamespaceLister)
}

// Mock FNDS client implementation
type mockFndsClient struct {
	mock.Mock
}

func (m *mockFndsClient) GetNcaId() string {
	args := m.Called()
	return args.String(0)
}

func (m *mockFndsClient) CreateEvent(ctx context.Context, eventData nvcffndstypes.StageTransitionEvent) error {
	args := m.Called(ctx, eventData)
	return args.Error(0)
}

func (m *mockFndsClient) NewStageTransitionEvent(ncaId string, functionId uuid.UUID, functionVersionId uuid.UUID, instanceId string, event string, eventType string, detailsJSON []byte) (nvcffndstypes.StageTransitionEvent, error) {
	args := m.Called(ncaId, functionId, functionVersionId, instanceId, event, eventType, detailsJSON)
	return args.Get(0).(nvcffndstypes.StageTransitionEvent), args.Error(1)
}

func TestProcessFnDSStageTransitionEventWithNilEvent(t *testing.T) {
	err := ProcessFnDSStageTransitionEvent(context.Background(), nil, nil, nil, nil, nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "received nil event")
}

func TestProcessFnDSStageTransitionEventWithEmptyReasonOrMessage(t *testing.T) {
	// Create an event with empty reason
	event := &corev1.Event{
		InvolvedObject: corev1.ObjectReference{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Reason:  "",
		Message: "Some message",
	}

	err := ProcessFnDSStageTransitionEvent(context.Background(), event, nil, nil, nil, nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "received container event with empty reason or message")

	// Create an event with empty message
	event = &corev1.Event{
		InvolvedObject: corev1.ObjectReference{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Reason:  "Some reason",
		Message: "",
	}

	err = ProcessFnDSStageTransitionEvent(context.Background(), event, nil, nil, nil, nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "received container event with empty reason or message")
}

func TestProcessFnDSStageTransitionEventWithNilPodLister(t *testing.T) {
	event := &corev1.Event{
		InvolvedObject: corev1.ObjectReference{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Reason:  "Some reason",
		Message: "Some message",
	}

	err := ProcessFnDSStageTransitionEvent(context.Background(), event, nil, nil, nil, nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "podLister is nil")
}

func TestProcessFnDSStageTransitionEventWithValidEvent(t *testing.T) {
	// Setup mock pod lister
	mockPodNSLister := new(mockPodNamespaceLister)
	mockPodL := new(mockPodLister)
	mockPodL.On("Pods", "test-namespace").Return(mockPodNSLister)

	// Setup mock ICMS request lister and namespace lister
	mockICMSReqNamespaceLister := new(mockICMSRequestNamespaceLister)
	mockICMSReqLister := new(mockICMSRequestLister)
	// Connect the namespace lister to the ICMS request lister
	mockICMSReqLister.On("ICMSRequests", "test-namespace").Return(mockICMSReqNamespaceLister)

	// Setup mock FNDS client
	mockFnds := new(mockFndsClient)
	mockFnds.On("GetNcaId").Return("test-nca-id")

	// Setup mock node getter
	mockNodeGet := new(mockNodeGetter)
	mockNodeGet.On("Get", "test-node").Return(&corev1.Node{
		Status: corev1.NodeStatus{
			Images: []corev1.ContainerImage{
				{
					Names:     []string{"a1", "abc", "b2"},
					SizeBytes: 5000,
				},
			},
		},
	}, nil)

	// Create pod with required labels
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
			Labels: map[string]string{
				nvcatypes.ICMSRequestIDKey: "test-icms-request-id",
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "test-node",
			Containers: []corev1.Container{
				{
					Name:  "abc",
					Image: "abc",
				},
			},
		},
	}

	// Setup mock pod lister to return our test pod
	mockPodNSLister.On("Get", "test-pod").Return(pod, nil)

	// Create an ICMS request with function details
	functionId := uuid.New()
	functionVersionId := uuid.New()
	instanceTypeName := "test-instance-type"
	icmsRequest := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			FunctionDetails: function.Details{
				FunctionID:        functionId.String(),
				FunctionVersionID: functionVersionId.String(),
			},
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					InstanceTypeName: instanceTypeName,
				},
			},
		},
	}

	// Setup mock for labels selector requirement
	mockICMSReqLister.On("List", mock.Anything).Return([]*nvcav2beta1.ICMSRequest{icmsRequest}, nil)

	// Mock CreateEvent to succeed
	mockFnds.On("CreateEvent", mock.Anything, mock.Anything).Return(nil)

	// Create a valid event
	event := &corev1.Event{
		InvolvedObject: corev1.ObjectReference{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Reason:  K8sReasonContainerScheduled,
		Message: "Pod scheduled successfully",
	}

	// Define expected details and the event object
	expectedDetails := EventDetails{
		ICMSDetailsInstanceType: instanceTypeName,
		EventLogMessage:         event.Message,
		EventErrorMessage:       "",
		ContainerImages:         map[string]ImageMetadata{"abc": {SizeBytes: 5000}},
	}
	expectedDetailsJson, _ := json.Marshal(expectedDetails)
	expectedEvent := nvcffndstypes.StageTransitionEvent{
		NcaId:             "test-nca-id",
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
		InstanceId:        pod.Name,
		Event:             ContainerStatusBuildingEvent,
		EventType:         "nvca",
		Details:           expectedDetailsJson,
	}

	// Mock NewStageTransitionEvent
	mockFnds.On("NewStageTransitionEvent",
		"test-nca-id",
		mock.AnythingOfType("uuid.UUID"),
		mock.AnythingOfType("uuid.UUID"),
		pod.Name,
		ContainerStatusBuildingEvent,
		"nvca",
		expectedDetailsJson,
	).Return(expectedEvent, nil)

	// Mock CreateEvent to expect the exact event returned by NewStageTransitionEvent
	mockFnds.On("CreateEvent", mock.Anything, expectedEvent).Return(nil)

	// Test event handler with the standalone function
	err := ProcessFnDSStageTransitionEvent(context.Background(), event, mockPodL, mockNodeGet.Get, mockICMSReqLister, mockFnds, nil)
	assert.NoError(t, err)

	// Verify that CreateEvent was called with correct event type
	mockFnds.AssertCalled(t, "CreateEvent", mock.Anything, mock.MatchedBy(func(ste nvcffndstypes.StageTransitionEvent) bool {
		// Parse the details JSON
		var details EventDetails
		err := json.Unmarshal(ste.Details, &details)
		if err != nil {
			return false
		}

		metadata, ok := details.ContainerImages["abc"]
		if !ok {
			t.Errorf("expected abc image, but received containers %v", details.ContainerImages)
			return false
		}
		if metadata.SizeBytes != 5000 {
			t.Errorf("expected sizeBytes of 5000 but received %v", metadata.SizeBytes)
			return false
		}

		// Verify the event type, instanceType, and that logMessage is present and errorMessage is empty
		return ste.Event == ContainerStatusBuildingEvent &&
			details.ICMSDetailsInstanceType == "test-instance-type" &&
			details.EventLogMessage == "Pod scheduled successfully" && // Check logMessage
			details.EventErrorMessage == "" // Check errorMessage is empty
	}))
}

func TestProcessFnDSStageTransitionEventForDifferentReasons(t *testing.T) {
	const (
		testInstanceType = "test-instance-type"
	)
	tests := []struct {
		name              string
		reason            string
		message           string
		expectedEventType string
		expectError       bool                 // Flag to indicate if we expect errorMessage or logMessage
		icmsRequestAction common.MessageAction // For Killing: TerminationAction -> user_initiated_destroyed
	}{
		{
			name:              "FailedScheduling",
			reason:            K8sReasonFailedScheduling,
			message:           "0/1 nodes are available: 1 Insufficient memory.",
			expectedEventType: ContainerStatusBuildingErrorEvent,
			expectError:       true,
		},
		{
			name:              "Scheduled",
			reason:            K8sReasonContainerScheduled,
			message:           "Successfully assigned default/test-pod to node1",
			expectedEventType: ContainerStatusBuildingEvent,
			expectError:       false,
		},
		{
			name:              "ImagePulled",
			reason:            K8sReasonContainerImagePulled,
			message:           "Container image pulled",
			expectedEventType: ContainerStatusDownloadingEvent,
			expectError:       false,
		},
		{
			name:              "Created",
			reason:            K8sReasonContainerCreated,
			message:           "Created container",
			expectedEventType: ContainerStatusInitializingEvent,
			expectError:       false,
		},
		{
			name:              "KillingUserInitiated",
			reason:            K8sReasonContainerKilling,
			message:           "Killing container",
			expectedEventType: ContainerStatusTerminatingEvent,
			expectError:       false,
			icmsRequestAction: common.TerminationAction,
		},
		{
			name:              "KillingZoneInitiated",
			reason:            K8sReasonContainerKilling,
			message:           "Killing container",
			expectedEventType: ContainerStatusTerminatingEvent,
			expectError:       false,
			icmsRequestAction: common.FunctionCreationAction,
		},
		{
			name:              "BackOff",
			reason:            K8sReasonContainerBackOff,
			message:           "Back-off restarting failed container=test-container",
			expectedEventType: ContainerStatusInitializingErrorEvent,
			expectError:       true,
		},
		{
			name:              "FailedWithImagePullError",
			reason:            K8sReasonContainerFailed,
			message:           fmt.Sprintf("Error: %s", K8sMessageErrImagePull), // Correctly format the expected message
			expectedEventType: ContainerStatusDownloadingErrorEvent,
			expectError:       true,
		},
		{
			name:              "FailedWithOtherError", // Add a case for other 'Failed' reasons
			reason:            K8sReasonContainerFailed,
			message:           "some other container failure",
			expectedEventType: ContainerStatusInitializingErrorEvent,
			expectError:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mock pod lister
			mockPodNSLister := new(mockPodNamespaceLister)
			mockPodL := new(mockPodLister)
			mockPodL.On("Pods", "test-namespace").Return(mockPodNSLister)

			// Setup mock ICMS request lister
			mockICMSReqNamespaceLister := new(mockICMSRequestNamespaceLister)
			mockICMSReqLister := new(mockICMSRequestLister)
			// Connect the namespace lister to the ICMS request lister
			mockICMSReqLister.On("ICMSRequests", "test-namespace").Return(mockICMSReqNamespaceLister)

			// Setup mock FNDS client
			mockFnds := new(mockFndsClient)
			mockFnds.On("GetNcaId").Return("test-nca-id")

			// Setup mock node getter
			mockNodeGet := new(mockNodeGetter)
			mockNodeGet.On("Get", "test-node").Return(&corev1.Node{
				Status: corev1.NodeStatus{
					Images: []corev1.ContainerImage{
						{
							Names:     []string{"a1", "abc", "b2"},
							SizeBytes: 5000,
						},
					},
				},
			}, nil)

			// Create pod with required labels
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-namespace",
					Labels: map[string]string{
						nvcatypes.ICMSRequestIDKey: "test-icms-request-id",
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "test-node",
					Containers: []corev1.Container{
						{
							Name:  "abc",
							Image: "abc",
						},
					},
				},
			}

			// Setup mock pod lister to return our test pod
			mockPodNSLister.On("Get", "test-pod").Return(pod, nil)

			// Create an ICMS request with function details
			functionId := uuid.New()
			functionVersionId := uuid.New()
			instanceTypeName := "test-instance-type"
			icmsRequest := &nvcav2beta1.ICMSRequest{
				Spec: nvcav2beta1.ICMSRequestSpec{
					Action: tt.icmsRequestAction,
					FunctionDetails: function.Details{
						FunctionID:        functionId.String(),
						FunctionVersionID: functionVersionId.String(),
					},
					CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
						CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
							InstanceTypeName: instanceTypeName,
						},
					},
				},
			}

			// Setup mock for labels selector requirement
			mockICMSReqLister.On("List", mock.Anything).Return([]*nvcav2beta1.ICMSRequest{icmsRequest}, nil)

			// Define expected details and the event object based on the test case
			logMessage := ""
			errorMessage := ""
			if tt.expectError {
				errorMessage = tt.message
			} else {
				logMessage = tt.message
			}

			expectedDetails := EventDetails{
				ICMSDetailsInstanceType: testInstanceType,
				EventLogMessage:         logMessage,
				EventErrorMessage:       errorMessage,
				ContainerImages:         map[string]ImageMetadata{"abc": {SizeBytes: 5000}},
			}
			if tt.reason == K8sReasonContainerKilling {
				if tt.icmsRequestAction == common.TerminationAction {
					expectedDetails.TerminationReason = "user_initiated"
				} else {
					expectedDetails.TerminationReason = "zone_initiated"
				}
			}
			expectedDetailsJson, _ := json.Marshal(expectedDetails)
			expectedEvent := nvcffndstypes.StageTransitionEvent{
				NcaId:             "test-nca-id",
				FunctionId:        functionId,
				FunctionVersionId: functionVersionId,
				InstanceId:        pod.Name,
				Event:             tt.expectedEventType,
				EventType:         "nvca",
				Details:           expectedDetailsJson,
			}

			// Mock NewStageTransitionEvent
			mockFnds.On("NewStageTransitionEvent",
				"test-nca-id",
				mock.AnythingOfType("uuid.UUID"),
				mock.AnythingOfType("uuid.UUID"),
				pod.Name,
				tt.expectedEventType,
				"nvca",
				expectedDetailsJson,
			).Return(expectedEvent, nil)

			// Mock CreateEvent to expect the exact event returned by NewStageTransitionEvent
			mockFnds.On("CreateEvent", mock.Anything, expectedEvent).Return(nil)

			// Create a valid event
			event := &corev1.Event{
				InvolvedObject: corev1.ObjectReference{
					Name:      "test-pod",
					Namespace: "test-namespace",
				},
				Reason:  tt.reason,
				Message: tt.message,
			}

			// Test event handler with the standalone function
			err := ProcessFnDSStageTransitionEvent(context.Background(), event, mockPodL, mockNodeGet.Get, mockICMSReqLister, mockFnds, nil)
			assert.NoError(t, err)

			// Verify that CreateEvent was called with correct event type
			mockFnds.AssertCalled(t, "CreateEvent", mock.Anything, mock.MatchedBy(func(ste nvcffndstypes.StageTransitionEvent) bool {
				// Parse the details JSON
				var details EventDetails
				err := json.Unmarshal(ste.Details, &details)
				if err != nil {
					return false
				}

				// Verify event type, instanceType, and the presence/absence of log/error messages
				eventMatch := ste.Event == tt.expectedEventType &&
					details.ICMSDetailsInstanceType == testInstanceType

				if tt.expectError {
					eventMatch = eventMatch && details.EventErrorMessage == tt.message && details.EventLogMessage == ""
				} else {
					eventMatch = eventMatch && details.EventLogMessage == tt.message && details.EventErrorMessage == ""
				}
				return eventMatch
			}))
		})
	}
}

func TestProcessFnDSStageTransitionEventWithErrorConditions(t *testing.T) {
	tests := []struct {
		name           string
		setupMocks     func(podLister *mockPodLister, podNsLister *mockPodNamespaceLister, icmsReqLister *mockICMSRequestLister, fndsClient *mockFndsClient)
		expectedErrMsg string
	}{
		{
			name: "ErrorGettingPod",
			setupMocks: func(podLister *mockPodLister, podNsLister *mockPodNamespaceLister, icmsReqLister *mockICMSRequestLister, fndsClient *mockFndsClient) {
				podLister.On("Pods", "test-namespace").Return(podNsLister)
				podNsLister.On("Get", "test-pod").Return(&corev1.Pod{}, assert.AnError)
			},
			expectedErrMsg: "failed to get pod",
		},
		{
			name: "PodWithNoLabels",
			setupMocks: func(podLister *mockPodLister, podNsLister *mockPodNamespaceLister, icmsReqLister *mockICMSRequestLister, fndsClient *mockFndsClient) {
				podLister.On("Pods", "test-namespace").Return(podNsLister)
				podNsLister.On("Get", "test-pod").Return(&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "test-namespace",
						Labels:    map[string]string{},
					},
				}, nil)
			},
			expectedErrMsg: "pod test-namespace/test-pod has no labels",
		},
		{
			name: "PodMissingLegacyRequestIDLabel",
			setupMocks: func(podLister *mockPodLister, podNsLister *mockPodNamespaceLister, icmsReqLister *mockICMSRequestLister, fndsClient *mockFndsClient) {
				podLister.On("Pods", "test-namespace").Return(podNsLister)
				podNsLister.On("Get", "test-pod").Return(&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "test-namespace",
						Labels:    map[string]string{"some-label": "some-value"},
					},
				}, nil)
			},
			expectedErrMsg: "pod test-namespace/test-pod is missing required label",
		},
		{
			name: "NoICMSRequestsFound",
			setupMocks: func(podLister *mockPodLister, podNsLister *mockPodNamespaceLister, icmsReqLister *mockICMSRequestLister, fndsClient *mockFndsClient) {
				podLister.On("Pods", "test-namespace").Return(podNsLister)
				podNsLister.On("Get", "test-pod").Return(&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "test-namespace",
						Labels:    map[string]string{nvcatypes.ICMSRequestIDKey: "test-icms-request-id"},
					},
				}, nil)
				icmsReqLister.On("List", mock.Anything).Return([]*nvcav2beta1.ICMSRequest{}, nil)
			},
			expectedErrMsg: "no ICMS request found for pod",
		},
		{
			name: "MissingFunctionDetails",
			setupMocks: func(podLister *mockPodLister, podNsLister *mockPodNamespaceLister, icmsReqLister *mockICMSRequestLister, fndsClient *mockFndsClient) {
				podLister.On("Pods", "test-namespace").Return(podNsLister)
				podNsLister.On("Get", "test-pod").Return(&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "test-namespace",
						Labels:    map[string]string{nvcatypes.ICMSRequestIDKey: "test-icms-request-id"},
					},
				}, nil)
				icmsReqLister.On("List", mock.Anything).Return([]*nvcav2beta1.ICMSRequest{
					{
						Spec: nvcav2beta1.ICMSRequestSpec{
							FunctionDetails: function.Details{
								// Missing function ID and version ID
							},
						},
					},
				}, nil)
				fndsClient.On("GetNcaId").Return("test-nca-id")
			},
			expectedErrMsg: "missing function details",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mocks
			mockPodLister := new(mockPodLister)
			mockPodNSLister := new(mockPodNamespaceLister)
			mockICMSReqLister := new(mockICMSRequestLister)
			mockFndsClient := new(mockFndsClient)
			mockNodeGet := new(mockNodeGetter)
			mockNodeGet.On("Get", "").Return(&corev1.Node{
				Status: corev1.NodeStatus{
					Images: []corev1.ContainerImage{
						{
							Names:     []string{"a1", "abc", "b2"},
							SizeBytes: 5000,
						},
					},
				},
			}, nil)

			// Configure mocks based on test case
			tt.setupMocks(mockPodLister, mockPodNSLister, mockICMSReqLister, mockFndsClient)

			// Create a valid event
			event := &corev1.Event{
				InvolvedObject: corev1.ObjectReference{
					Name:      "test-pod",
					Namespace: "test-namespace",
				},
				Reason:  "SomeReason",
				Message: "Some message",
			}

			// Test event handler with the standalone function
			err := ProcessFnDSStageTransitionEvent(context.Background(), event, mockPodLister, mockNodeGet.Get, mockICMSReqLister, mockFndsClient, nil)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErrMsg)

			// Ensure CreateEvent and NewStageTransitionEvent were not called in these error cases
			mockFndsClient.AssertNotCalled(t, "NewStageTransitionEvent", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			mockFndsClient.AssertNotCalled(t, "CreateEvent", mock.Anything, mock.Anything)
		})
	}
}

func TestProcessFnDSStageTransitionEventAdditionalErrors(t *testing.T) {
	tests := []struct {
		name           string
		setupMocks     func(podLister *mockPodLister, podNsLister *mockPodNamespaceLister, icmsReqLister *mockICMSRequestLister, fndsClient *mockFndsClient)
		expectedErrMsg string
	}{
		{
			name: "NilICMSRequestLister",
			setupMocks: func(podLister *mockPodLister, podNsLister *mockPodNamespaceLister, icmsReqLister *mockICMSRequestLister, fndsClient *mockFndsClient) {
				podLister.On("Pods", "test-namespace").Return(podNsLister)
				podNsLister.On("Get", "test-pod").Return(&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "test-namespace",
						Labels:    map[string]string{nvcatypes.ICMSRequestIDKey: "test-icms-request-id"},
					},
				}, nil)
			},
			expectedErrMsg: "icmsRequestLister is nil",
		},
		{
			name: "NilFndsClient",
			setupMocks: func(podLister *mockPodLister, podNsLister *mockPodNamespaceLister, icmsReqLister *mockICMSRequestLister, fndsClient *mockFndsClient) {
				podLister.On("Pods", "test-namespace").Return(podNsLister)
				podNsLister.On("Get", "test-pod").Return(&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "test-namespace",
						Labels:    map[string]string{nvcatypes.ICMSRequestIDKey: "test-icms-request-id"},
					},
				}, nil)
				icmsReqLister.On("List", mock.Anything).Return([]*nvcav2beta1.ICMSRequest{
					{
						Spec: nvcav2beta1.ICMSRequestSpec{
							FunctionDetails: function.Details{
								FunctionID:        uuid.New().String(),
								FunctionVersionID: uuid.New().String(),
							},
						},
					},
				}, nil)
			},
			expectedErrMsg: "fndsClient is nil",
		},
		{
			name: "EmptyNcaId",
			setupMocks: func(podLister *mockPodLister, podNsLister *mockPodNamespaceLister, icmsReqLister *mockICMSRequestLister, fndsClient *mockFndsClient) {
				podLister.On("Pods", "test-namespace").Return(podNsLister)
				podNsLister.On("Get", "test-pod").Return(&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "test-namespace",
						Labels:    map[string]string{nvcatypes.ICMSRequestIDKey: "test-icms-request-id"},
					},
				}, nil)
				icmsReqLister.On("List", mock.Anything).Return([]*nvcav2beta1.ICMSRequest{
					{
						Spec: nvcav2beta1.ICMSRequestSpec{
							FunctionDetails: function.Details{
								FunctionID:        uuid.New().String(),
								FunctionVersionID: uuid.New().String(),
							},
						},
					},
				}, nil)
				fndsClient.On("GetNcaId").Return("")
			},
			expectedErrMsg: "missing NCA ID",
		},
		{
			name: "InvalidFunctionID",
			setupMocks: func(podLister *mockPodLister, podNsLister *mockPodNamespaceLister, icmsReqLister *mockICMSRequestLister, fndsClient *mockFndsClient) {
				podLister.On("Pods", "test-namespace").Return(podNsLister)
				podNsLister.On("Get", "test-pod").Return(&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "test-namespace",
						Labels:    map[string]string{nvcatypes.ICMSRequestIDKey: "test-icms-request-id"},
					},
				}, nil)
				icmsReqLister.On("List", mock.Anything).Return([]*nvcav2beta1.ICMSRequest{
					{
						Spec: nvcav2beta1.ICMSRequestSpec{
							FunctionDetails: function.Details{
								FunctionID:        "invalid-uuid",
								FunctionVersionID: uuid.New().String(),
							},
						},
					},
				}, nil)
				fndsClient.On("GetNcaId").Return("test-nca-id")
			},
			expectedErrMsg: "invalid function ID format",
		},
		{
			name: "InvalidFunctionVersionID",
			setupMocks: func(podLister *mockPodLister, podNsLister *mockPodNamespaceLister, icmsReqLister *mockICMSRequestLister, fndsClient *mockFndsClient) {
				podLister.On("Pods", "test-namespace").Return(podNsLister)
				podNsLister.On("Get", "test-pod").Return(&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "test-namespace",
						Labels:    map[string]string{nvcatypes.ICMSRequestIDKey: "test-icms-request-id"},
					},
				}, nil)
				icmsReqLister.On("List", mock.Anything).Return([]*nvcav2beta1.ICMSRequest{
					{
						Spec: nvcav2beta1.ICMSRequestSpec{
							FunctionDetails: function.Details{
								FunctionID:        uuid.New().String(),
								FunctionVersionID: "invalid-uuid",
							},
						},
					},
				}, nil)
				fndsClient.On("GetNcaId").Return("test-nca-id")
			},
			expectedErrMsg: "invalid function version ID format",
		},
		{
			name: "CreateEventError",
			setupMocks: func(podLister *mockPodLister, podNsLister *mockPodNamespaceLister, icmsReqLister *mockICMSRequestLister, fndsClient *mockFndsClient) {
				podLister.On("Pods", "test-namespace").Return(podNsLister)
				// Define pod first as it's needed for expectedEvent
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "test-namespace",
						Labels:    map[string]string{nvcatypes.ICMSRequestIDKey: "test-icms-request-id"},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Image: "abc",
							},
						},
					},
				}
				podNsLister.On("Get", "test-pod").Return(pod, nil)

				// Define container images map
				containerImages := map[string]ImageMetadata{
					"abc": {SizeBytes: 5000},
				}

				// Define function IDs and instance type
				functionId := uuid.New()
				functionVersionId := uuid.New()
				instanceTypeName := "test-instance-type" // Ensure this is defined

				// Setup SpotRequest Lister to return a ICMSRequest WITH CreationMsgInfo
				mockICMSReqNamespaceLister := new(mockICMSRequestNamespaceLister) // Need the namespace lister
				icmsReqLister.On("ICMSRequests", "test-namespace").Return(mockICMSReqNamespaceLister)
				icmsReq := &nvcav2beta1.ICMSRequest{
					Spec: nvcav2beta1.ICMSRequestSpec{
						FunctionDetails: function.Details{
							FunctionID:        functionId.String(),
							FunctionVersionID: functionVersionId.String(),
						},
						// *** Ensure CreationMsgInfo is included in the returned ICMSRequest ***
						CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
							CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
								InstanceTypeName: instanceTypeName,
							},
						},
					},
				}
				icmsReqLister.On("List", mock.Anything).Return([]*nvcav2beta1.ICMSRequest{icmsReq}, nil)

				fndsClient.On("GetNcaId").Return("test-nca-id")

				// Define expected details and the event object
				expectedDetails := EventDetails{
					ICMSDetailsInstanceType: instanceTypeName,
					EventLogMessage:         "Pod scheduled successfully",
					EventErrorMessage:       "",
					ContainerImages:         containerImages,
				}
				expectedDetailsJson, _ := json.Marshal(expectedDetails)
				expectedEvent := nvcffndstypes.StageTransitionEvent{
					NcaId:             "test-nca-id",
					FunctionId:        functionId,
					FunctionVersionId: functionVersionId,
					InstanceId:        pod.Name,
					Event:             ContainerStatusBuildingEvent,
					EventType:         "nvca",
					Details:           expectedDetailsJson,
				}

				// Mock NewStageTransitionEvent to succeed
				fndsClient.On("NewStageTransitionEvent",
					"test-nca-id",
					functionId,
					functionVersionId,
					pod.Name,
					ContainerStatusBuildingEvent,
					"nvca",
					expectedDetailsJson,
				).Return(expectedEvent, nil)

				// Mock CreateEvent to return an error
				fndsClient.On("CreateEvent", mock.Anything, expectedEvent).Return(assert.AnError)
			},
			expectedErrMsg: "assert.AnError general error for testing",
		},
		{
			name: "CreateEventErrorWithProblemDetails",
			setupMocks: func(podLister *mockPodLister, podNsLister *mockPodNamespaceLister, icmsReqLister *mockICMSRequestLister, fndsClient *mockFndsClient) {
				podLister.On("Pods", "test-namespace").Return(podNsLister)
				// Define pod first
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "test-namespace",
						Labels:    map[string]string{nvcatypes.ICMSRequestIDKey: "test-icms-request-id"},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Image: "abc",
							},
						},
					},
				}
				podNsLister.On("Get", "test-pod").Return(pod, nil)

				// Define container images map
				containerImages := map[string]ImageMetadata{
					"abc": {SizeBytes: 5000},
				}

				// Define function IDs and instance type
				functionId := uuid.New()
				functionVersionId := uuid.New()
				instanceTypeName := "test-instance-type" // Ensure this is defined

				// Setup SpotRequest Lister to return a ICMSRequest WITH CreationMsgInfo
				mockICMSReqNamespaceLister := new(mockICMSRequestNamespaceLister) // Need the namespace lister
				icmsReqLister.On("ICMSRequests", "test-namespace").Return(mockICMSReqNamespaceLister)
				icmsReq := &nvcav2beta1.ICMSRequest{
					Spec: nvcav2beta1.ICMSRequestSpec{
						FunctionDetails: function.Details{
							FunctionID:        functionId.String(),
							FunctionVersionID: functionVersionId.String(),
						},
						// *** Ensure CreationMsgInfo is included ***
						CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
							CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
								InstanceTypeName: instanceTypeName,
							},
						},
					},
				}
				icmsReqLister.On("List", mock.Anything).Return([]*nvcav2beta1.ICMSRequest{icmsReq}, nil)

				fndsClient.On("GetNcaId").Return("test-nca-id")
				problemDetails := `{"title": "Custom Error Title", "status": 400}`

				// Define expected details and the event object
				expectedDetails := EventDetails{
					ICMSDetailsInstanceType: instanceTypeName,
					EventLogMessage:         "Pod scheduled successfully",
					EventErrorMessage:       "",
					ContainerImages:         containerImages,
				}
				expectedDetailsJson, _ := json.Marshal(expectedDetails)
				expectedEvent := nvcffndstypes.StageTransitionEvent{
					NcaId:             "test-nca-id",
					FunctionId:        functionId,
					FunctionVersionId: functionVersionId,
					InstanceId:        pod.Name,
					Event:             ContainerStatusBuildingEvent,
					EventType:         "nvca",
					Details:           expectedDetailsJson,
				}

				// Mock NewStageTransitionEvent to succeed
				fndsClient.On("NewStageTransitionEvent",
					"test-nca-id",
					functionId,
					functionVersionId,
					pod.Name,
					ContainerStatusBuildingEvent,
					"nvca",
					expectedDetailsJson,
				).Return(expectedEvent, nil)

				// Mock CreateEvent to return the problem details error
				fndsClient.On("CreateEvent", mock.Anything, expectedEvent).Return(errors.New(problemDetails))
			},
			expectedErrMsg: "Custom Error Title",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mocks
			mockPodLister := new(mockPodLister)
			mockPodNSLister := new(mockPodNamespaceLister)
			mockICMSReqLister := new(mockICMSRequestLister)
			mockFndsClient := new(mockFndsClient)
			mockNodeGet := new(mockNodeGetter)
			mockNodeGet.On("Get", "").Return(&corev1.Node{
				Status: corev1.NodeStatus{
					Images: []corev1.ContainerImage{
						{
							Names:     []string{"a1", "abc", "b2"},
							SizeBytes: 5000,
						},
					},
				},
			}, nil)

			// Configure mocks based on test case
			tt.setupMocks(mockPodLister, mockPodNSLister, mockICMSReqLister, mockFndsClient)

			// Create a valid event
			event := &corev1.Event{
				InvolvedObject: corev1.ObjectReference{
					Name:      "test-pod",
					Namespace: "test-namespace",
				},
				Reason:  K8sReasonContainerScheduled,
				Message: "Pod scheduled successfully",
			}

			var fndsClientArg Client
			if tt.name != "NilFndsClient" {
				fndsClientArg = mockFndsClient
			}

			var icmsReqListerArg nvcav2beta1listers.ICMSRequestLister
			if tt.name != "NilICMSRequestLister" {
				icmsReqListerArg = mockICMSReqLister
			}

			// Test event handler
			err := ProcessFnDSStageTransitionEvent(context.Background(), event, mockPodLister, mockNodeGet.Get, icmsReqListerArg, fndsClientArg, nil)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErrMsg)
		})
	}
}

// Test case specifically for when InstanceTypeName is missing
func TestProcessFnDSStageTransitionEventWithMissingInstanceTypeName(t *testing.T) {
	// Setup mock pod lister
	mockPodNSLister := new(mockPodNamespaceLister)
	mockPodL := new(mockPodLister)
	mockPodL.On("Pods", "test-namespace").Return(mockPodNSLister)

	// Setup mock ICMS request lister and namespace lister
	mockICMSReqNamespaceLister := new(mockICMSRequestNamespaceLister)
	mockICMSReqLister := new(mockICMSRequestLister)
	mockICMSReqLister.On("ICMSRequests", "test-namespace").Return(mockICMSReqNamespaceLister)

	// Setup mock FNDS client
	mockFnds := new(mockFndsClient)
	mockFnds.On("GetNcaId").Return("test-nca-id")

	// Setup mock node getter
	mockNodeGet := new(mockNodeGetter)
	mockNodeGet.On("Get", "").Return(&corev1.Node{}, nil)

	// Create pod with required labels
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
			Labels: map[string]string{
				nvcatypes.ICMSRequestIDKey: "test-icms-request-id",
			},
		},
	}

	// Setup mock pod lister to return our test pod
	mockPodNSLister.On("Get", "test-pod").Return(pod, nil)

	// Create an ICMS request with function details BUT MISSING InstanceTypeName
	functionId := uuid.New()
	functionVersionId := uuid.New()
	icmsRequest := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			FunctionDetails: function.Details{
				FunctionID:        functionId.String(),
				FunctionVersionID: functionVersionId.String(),
			},
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					InstanceTypeName: "", // Ensure this is empty
				},
			},
		},
	}

	// Setup mock for labels selector requirement
	mockICMSReqLister.On("List", mock.Anything).Return([]*nvcav2beta1.ICMSRequest{icmsRequest}, nil)

	// Create a valid event (reason doesn't matter much here, just needs to trigger event creation)
	event := &corev1.Event{
		InvolvedObject: corev1.ObjectReference{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Reason:  K8sReasonContainerScheduled,
		Message: "Pod scheduled successfully",
	}

	// Define expected details and event object
	expectedDetails := EventDetails{
		ICMSDetailsInstanceType: "",
		EventLogMessage:         "Pod scheduled successfully",
		EventErrorMessage:       "",
		ContainerImages:         map[string]ImageMetadata{},
	}
	expectedDetailsJson, _ := json.Marshal(expectedDetails)
	expectedEvent := nvcffndstypes.StageTransitionEvent{
		NcaId:             "test-nca-id",
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
		InstanceId:        pod.Name,
		Event:             ContainerStatusBuildingEvent,
		EventType:         "nvca",
		Details:           expectedDetailsJson,
	}

	// Mock NewStageTransitionEvent
	mockFnds.On("NewStageTransitionEvent",
		"test-nca-id",
		mock.AnythingOfType("uuid.UUID"),
		mock.AnythingOfType("uuid.UUID"),
		pod.Name,
		ContainerStatusBuildingEvent,
		"nvca",
		mock.MatchedBy(func(details []byte) bool {
			var actualDetails EventDetails
			if err := json.Unmarshal(details, &actualDetails); err != nil {
				return false
			}
			// Verify the expected contents of the details
			return actualDetails.ICMSDetailsInstanceType == "" &&
				actualDetails.EventLogMessage == "Pod scheduled successfully" &&
				actualDetails.EventErrorMessage == ""
		}),
	).Return(expectedEvent, nil)

	// Mock CreateEvent to succeed
	mockFnds.On("CreateEvent", mock.Anything, expectedEvent).Return(nil)

	// Test event handler - expect no error
	err := ProcessFnDSStageTransitionEvent(context.Background(), event, mockPodL, mockNodeGet.Get, mockICMSReqLister, mockFnds, nil)
	assert.NoError(t, err)

	// Verify that CreateEvent was called with correct details (empty instanceType)
	mockFnds.AssertCalled(t, "CreateEvent", mock.Anything, mock.MatchedBy(func(ste nvcffndstypes.StageTransitionEvent) bool {
		// Parse the details JSON
		var details EventDetails
		err := json.Unmarshal(ste.Details, &details)
		if err != nil {
			t.Errorf("Failed to unmarshal details JSON: %v", err)
			return false
		}

		// Verify the event type, empty instanceType, and presence/absence of log/error messages
		return ste.Event == ContainerStatusBuildingEvent &&
			details.ICMSDetailsInstanceType == "" &&
			details.EventLogMessage == "Pod scheduled successfully" &&
			details.EventErrorMessage == ""
	}))
}
