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

package nvca

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/informers"
	k8sinformers "k8s.io/client-go/informers"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	listersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	nvcaauth "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/icms"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/envutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	k8smock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil/mock"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	fakebartclient "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/fake"
	nvcainformers "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/informers/externalversions"
	nvcav2beta1listers "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/listers/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
	queuemock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue/mock"
	queuesqs "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue/sqs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// Helper function to safely update mock transport
func setMockResponse(m *mockTransport, code int, body string) {
	m.setCode(code)
	m.setBody(body)
}

func newTestContext(level ...logrus.Level) context.Context {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = nvcametrics.WithDefaultMetrics(ctx,
		"my-nca-id", "my-cluster", "my-cluster", "1.0.0",
		nvcametrics.WithEventErrorTotalDefaultEvents(append(getAgentEvents(), getNVCAMetricEvents()...)),
		nvcametrics.WithContainerCrashAndRestartTotalDefaultContainerNames(GetDefaultWorkloadContainerNamesToWatch()),
		nvcametrics.WithRegisterer(reg))
	if len(level) != 0 {
		ctx = core.WithDefaultLogger(ctx)
		log := core.GetLogger(ctx)
		log.Logger.SetLevel(level[0])
	}
	return ctx
}

var (
	//go:embed testdata/gpus_single.json
	gpusSingle string
	//go:embed testdata/gpus_multiple.json
	gpusMultiple string
)

func bartBackendGPUsConfigMap() *v1.ConfigMap {
	return makeBackendGPUsConfigMap(gpusMultiple)
}

func newBackendGPUsConfigMapSingle() *v1.ConfigMap {
	return makeBackendGPUsConfigMap(gpusSingle)
}

func makeBackendGPUsConfigMap(gpus string) *v1.ConfigMap {
	return &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodefeatures.ConfigMapName,
			Namespace: SystemNamespace,
		},
		Data: map[string]string{
			nodefeatures.ConfigMapGPUsKey: gpus,
		},
	}
}

func bartClientWithPresetSRAndPods(reqs []*nvcav2beta1.ICMSRequest, pods []*v1.Pod, extra ...runtime.Object) *kubeclients.KubeClients {
	sch := newMiniServiceScheme()

	fakeClient := ctrlfake.NewClientBuilder().
		WithScheme(sch).
		Build()

	reqObjs := make([]runtime.Object, len(reqs))
	for i, req := range reqs {
		reqObjs[i] = req
	}

	podObjects := make([]runtime.Object, len(pods))
	for i, pod := range pods {
		podObjects[i] = pod
	}

	k8sClient := fakek8sclient.NewSimpleClientset(append(podObjects, extra...)...)
	k8sClient.Resources = newResourceList()

	return &kubeclients.KubeClients{
		Config:          newRESTConfig(),
		BART:            fakebartclient.NewSimpleClientset(reqObjs...),
		K8s:             k8sClient,
		HelmV2:          fakeClient,
		DiscoveryClient: k8sClient.Discovery(),
	}
}

func bartClientWithPresetSR(reqs []*nvcav2beta1.ICMSRequest, pod *v1.Pod) *kubeclients.KubeClients {
	sch := newMiniServiceScheme()

	fakeClient := ctrlfake.NewClientBuilder().
		WithScheme(sch).
		Build()

	reqObjs := make([]runtime.Object, len(reqs))
	for i, req := range reqs {
		reqObjs[i] = req
	}
	if pod != nil {
		k8sClient := fakek8sclient.NewSimpleClientset(
			pod,
			bartBackendGPUsConfigMap(),
			k8smock.NewNetworkPolicyConfigMap(SystemNamespace),
			newHelmInstanceRBACConfigMap(),
		)
		k8sClient.Resources = newResourceList()
		return &kubeclients.KubeClients{
			Config:          newRESTConfig(),
			BART:            fakebartclient.NewSimpleClientset(reqObjs...),
			K8s:             k8sClient,
			HelmV2:          fakeClient,
			DiscoveryClient: k8sClient.Discovery(),
		}
	}

	k8sClient := fakek8sclient.NewSimpleClientset(
		bartBackendGPUsConfigMap(),
		k8smock.NewNetworkPolicyConfigMap(SystemNamespace),
		newHelmInstanceRBACConfigMap(),
	)
	k8sClient.Resources = newResourceList()

	return &kubeclients.KubeClients{
		Config:          newRESTConfig(),
		BART:            fakebartclient.NewSimpleClientset(reqObjs...),
		K8s:             k8sClient,
		HelmV2:          fakeClient,
		DiscoveryClient: k8sClient.Discovery(),
	}
}

func mockKubeClients(extraK8sObjs ...runtime.Object) *kubeclients.KubeClients {
	return makeMockKubeClients(append(extraK8sObjs, bartBackendGPUsConfigMap())...)
}

func mockKubeClientsSingleGPU(extraK8sObjs ...runtime.Object) *kubeclients.KubeClients {
	return makeMockKubeClients(append(extraK8sObjs, newBackendGPUsConfigMapSingle())...)
}

func mockKubeClientsDynamicGPUs(extraK8sObjs ...runtime.Object) *kubeclients.KubeClients {
	return makeMockKubeClients(extraK8sObjs...)
}

func makeMockKubeClients(extraK8sObjs ...runtime.Object) *kubeclients.KubeClients {
	sch := newMiniServiceScheme()

	fakeClient := ctrlfake.NewClientBuilder().
		WithScheme(sch).
		WithStatusSubresource(&v1alpha1.MiniService{}).
		Build()

	extraK8sObjs = append(extraK8sObjs,
		k8smock.NewNetworkPolicyConfigMap(SystemNamespace),
		newHelmInstanceRBACConfigMap(),
	)

	k8sClient := fakek8sclient.NewSimpleClientset(extraK8sObjs...)
	k8sClient.Resources = newResourceList()

	return &kubeclients.KubeClients{
		Config:          newRESTConfig(),
		BART:            fakebartclient.NewSimpleClientset(),
		K8s:             k8sClient,
		HelmV2:          fakeClient,
		DiscoveryClient: k8sClient.Discovery(),
	}
}

func newResourceList() []*metav1.APIResourceList {
	return []*metav1.APIResourceList{
		{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{
					Name:         "pods",
					SingularName: "pod",
					Namespaced:   true,
					Group:        "",
					Version:      "v1",
					Kind:         "Pod",
					Verbs:        metav1.Verbs{"create", "delete", "deletecollection", "get", "list", "patch", "update", "watch"},
					Categories:   []string{"all"},
				},
				{
					Name:         "secrets",
					SingularName: "secret",
					Namespaced:   true,
					Group:        "",
					Version:      "v1",
					Kind:         "Secret",
					Verbs:        metav1.Verbs{"create", "delete", "deletecollection", "get", "list", "patch", "update", "watch"},
					Categories:   []string{"all"},
				},
				{
					Name:         "configmaps",
					SingularName: "configmap",
					Namespaced:   true,
					Group:        "",
					Version:      "v1",
					Kind:         "ConfigMap",
					Verbs:        metav1.Verbs{"create", "delete", "deletecollection", "get", "list", "patch", "update", "watch"},
					Categories:   []string{"all"},
				},
				{
					Name:         "persistentvolumeclaims",
					SingularName: "persistentvolumeclaim",
					Namespaced:   true,
					Group:        "",
					Version:      "v1",
					Kind:         "PersistentVolumeClaim",
					Verbs:        metav1.Verbs{"create", "delete", "deletecollection", "get", "list", "patch", "update", "watch"},
					Categories:   []string{"all"},
				},
			},
		},
	}
}

func newMiniServiceScheme() *runtime.Scheme {
	sch := runtime.NewScheme()
	utilruntime.Must(v1alpha1.AddToScheme(sch))
	return sch
}

func newRESTConfig() *rest.Config {
	cfg := &rest.Config{Host: "http://test.k8s.local"}
	cfg.CAData = []byte("localKubeconfigCA")
	return cfg
}

func getArtifactsBase64(t *testing.T, fp string) string {
	v, err := os.ReadFile(fp)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(v)
}

func getArtifactsList(t *testing.T) []function.LaunchArtifact {
	pod := "testdata/test_pod.yaml"
	secret := "testdata/test_secret.yaml"
	cm := "testdata/test_cm.yaml"
	var artifacts []function.LaunchArtifact

	artifacts = append(artifacts, function.LaunchArtifact{Type: function.LaunchArtifactTypePod, Specification: getArtifactsBase64(t, pod)})
	artifacts = append(artifacts, function.LaunchArtifact{Type: function.LaunchArtifactTypeSecret, Specification: getArtifactsBase64(t, secret)})
	artifacts = append(artifacts, function.LaunchArtifact{Type: function.LaunchArtifactTypeConfigmap, Specification: getArtifactsBase64(t, cm)})
	workerSecretArt := function.LaunchArtifact{
		Type:          function.LaunchArtifactTypeSecret,
		Specification: "YXBpVmVyc2lvbjogdjEKa2luZDogU2VjcmV0Cm1ldGFkYXRhOgogIG5hbWU6IGluZmVyZW5jZS1jb250YWluZXItcHVsbC13b3JrZXIKdHlwZToga3ViZXJuZXRlcy5pby9kb2NrZXJjb25maWdqc29uCmRhdGE6CiAgLmRvY2tlcmNvbmZpZ2pzb246ICJleUpoZFhSb2N5STZleUp6ZEdjdWJuWmpjaTVwYnlJNmV5SmhkWFJvSWpvaUpHOWhkWFJvZEc5clpXNDZZbUY2WW5WbU1USXpJbjE5ZlE9PSIK",
	}
	artifacts = append(artifacts, workerSecretArt)
	workloadSecretArt := function.LaunchArtifact{
		Type:          function.LaunchArtifactTypeSecret,
		Specification: "YXBpVmVyc2lvbjogdjEKa2luZDogU2VjcmV0Cm1ldGFkYXRhOgogIG5hbWU6IGluZmVyZW5jZS1jb250YWluZXItcHVsbC1zZWNyZXQKdHlwZToga3ViZXJuZXRlcy5pby9kb2NrZXJjb25maWdqc29uCmRhdGE6CiAgLmRvY2tlcmNvbmZpZ2pzb246ICJleUpoZFhSb2N5STZleUp6ZEdjdWJuWmpjaTVwYnlJNmV5SmhkWFJvSWpvaUpHOWhkWFJvZEc5clpXNDZZbUY2WW5WbU1USXpJbjE5ZlE9PSIK",
	}
	artifacts = append(artifacts, workloadSecretArt)

	return artifacts
}

func TestGetPodName(t *testing.T) {
	assert.Equal(t, len(getPodName("abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz")), 63)
	assert.Equal(t, len(getPodName("abcdefghijklmnopqrstuvwxyz")), 26)
}

func TestTrimDNS1123Label(t *testing.T) {
	assert.Equal(t, len(trimDNS1123Label("abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz", 0)), 63)
	assert.Equal(t, len(trimDNS1123Label("abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz", 5)), 58)
	assert.Equal(t, len(trimDNS1123Label("abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz", -1)), 63)
	assert.Equal(t, len(trimDNS1123Label("abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz", validation.DNS1123LabelMaxLength+1)), 63)
	assert.Equal(t, len(trimDNS1123Label("abcdefghijklmnopqrstuvwxyz", 0)), 26)
	assert.Equal(t, len(trimDNS1123Label("abcdefghijklmnopqrstuvwxyz", 5)), 26)
	assert.Equal(t, len(trimDNS1123Label("abcdefghijklmnopqrstuvwxyz", -1)), 26)
	assert.Equal(t, len(trimDNS1123Label("abcdefghijklmnopqrstuvwxyz", validation.DNS1123LabelMaxLength+1)), 26)
}

func TestMergeMaps(t *testing.T) {
	m1 := map[string]string{"a": "a", "b": "b", "c": "c"}
	m2 := map[string]string{"d": "d", "e": "e", "f": "f"}
	assert.Equal(t, len(mergeMaps(m1, m2)), 6)

	m1 = map[string]string{"a": "a", "b": "b"}
	m2 = map[string]string{"d": "d", "e": "e", "f": "f"}
	assert.Equal(t, len(mergeMaps(m1, m2)), 5)

	m1 = map[string]string{"a": "a", "b": "b", "c": "c"}
	m2 = map[string]string{"d": "d"}
	assert.Equal(t, len(mergeMaps(m1, m2)), 4)
}

func getTestCreationMessageQueueInfo(mod bool) queue.MessageQueueInfo {
	mi := queue.MessageQueueInfo{
		GPU:          string(testGPUNameDefault),
		QueueURL:     "http://localhost:4566/000000000000/bart-test-request",
		QueueType:    "FifoQueue",
		AccessKey:    "randomAccessKey",
		SecretKey:    "randomSecretKey",
		SessionToken: "randomSessionToken",
	}
	if mod {
		mi.QueueURL = "https://aws.icms.modifiedcreationqueue"
	}
	return mi
}

func getTestTerminationMessageQueueInfo(mod bool) queue.MessageQueueInfo {
	mi := queue.MessageQueueInfo{
		QueueURL:     "http://localhost:4566/000000000000/bart-test-request",
		QueueType:    "FifoQueue",
		AccessKey:    "randomAccessKey",
		SecretKey:    "randomSecretKey",
		SessionToken: "randomSessionToken",
	}
	if mod {
		mi.QueueURL = "https://aws.icms.modifiedterminationqueue"
	}
	return mi
}

func cleanupAllSPOTRequests(ctx context.Context, bc *BackendK8sCache) error {
	bc.ForceSync(ctx)
	allReqs, err := bc.icmsRequestLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("failed listing all SPOTRequests, err: %v", err)
	}
	for _, req := range allReqs {
		err = bc.clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Delete(ctx, req.Name, metav1.DeleteOptions{})
		if err != nil {
			return fmt.Errorf("failed delete, err: %v", err)
		}
	}
	bc.ForceSync(ctx)
	return nil
}

func updateAllRequestToPending(ctx context.Context, bc *BackendK8sCache) error {
	reqList, err := bc.clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed listing all SPOTRequests, err: %v", err)
	}
	for _, req := range reqList.Items {
		// Must copy to avoid DATA race since the lister caches the object in memory it
		// must be treated as immutable
		req := req.DeepCopy()
		modify := func(ctx context.Context, sr *nvcav2beta1.ICMSRequest) {
			sr.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusPending
		}
		if !bc.applyICMSRequestStatusChange(ctx, req, modify) {
			return fmt.Errorf("failed to update status for Request %v/%v", req.Namespace, req.Name)
		}
	}
	bc.ForceSync(ctx)
	return nil
}

func updateRequestToPending(ctx context.Context, bc *BackendK8sCache, reqID, msgID string) error {
	req, err := bc.clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Get(ctx,
		getICMSRequestObjectMeta(types.DeploymentInfo{
			RequestID:      reqID,
			MessageID:      msgID,
			NCAID:          "ncaID",
			MessageBatchID: "batch1",
			GPUType:        "a100",
		}).Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	modify := func(ctx context.Context, sr *nvcav2beta1.ICMSRequest) {
		sr.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusPending
	}
	if !bc.applyICMSRequestStatusChange(ctx, req, modify) {
		return fmt.Errorf("failed to update status for Request %v/%v, err: %v", req.Namespace, req.Name, err)
	}
	bc.ForceSync(ctx)
	return nil
}

func getSPOTDummyRegistrationResponse(t *testing.T) types.ICMSRegistrationResponse {
	t.Helper()
	v, err := os.ReadFile("testdata/reg_response.txt")
	require.NoError(t, err)
	res := types.ICMSRegistrationResponse{}
	err = json.Unmarshal(v, &res)
	require.NoError(t, err)
	return res
}

func getSPOTDummyCredsResponse(t *testing.T) types.ICMSCredentialResponse {
	t.Helper()
	res := types.ICMSCredentialResponse{}
	err := json.Unmarshal([]byte(queueCreds), &res)
	require.NoError(t, err)
	return res
}

func TestFinalizerHelperAPIs(t *testing.T) {
	srcSlice := []string{"apple", "ball", "cat", "apple"}
	assert.True(t, containsString(srcSlice, "ball"))
	assert.True(t, containsString(srcSlice, "cat"))
	assert.True(t, containsString(srcSlice, "apple"))
	assert.False(t, containsString(srcSlice, "dog"))

	assert.False(t, containsString(removeString(srcSlice, "apple"), "apple"))
	assert.True(t, containsString(removeString(srcSlice, "dog"), "ball"))
	assert.False(t, containsString(removeString(srcSlice, "ball"), "ball"))
	assert.False(t, containsString(removeString(srcSlice, "cat"), "cat"))
}

func TestAutoPurgeWorkerDeletion(t *testing.T) {
	ctx := newTestContext()

	b := NewBackendk8sCacheBuilder().
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithStaticGPUCapacity(uint64(10)).
		WithWorkerDegradationHandler(true).
		WithLowLatencyStreaming(true)
	assert.NotNil(t, b)

	srs := []*nvcav2beta1.ICMSRequest{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req1", Namespace: "nvcf-backend"},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p1": {Type: nvcav2beta1.InstanceTypePod, ID: "p1", Status: "running"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req2", Namespace: "nvcf-backend"},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated: &metav1.Time{Time: core.GetCurrentTime(ctx)},
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p2": {Type: nvcav2beta1.InstanceTypePod, ID: "p2", Status: "running", LastReportedStatus: "running"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req3", Namespace: "nvcf-backend"},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated: &metav1.Time{Time: core.GetCurrentTime(ctx)},
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p3": {Type: nvcav2beta1.InstanceTypePod, ID: "p3", Status: "running", LastReportedStatus: "running"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req4", Namespace: "nvcf-backend"},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated: &metav1.Time{Time: core.GetCurrentTime(ctx)},
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p4": {Type: nvcav2beta1.InstanceTypePod, ID: "p4", Status: "running", LastReportedStatus: "running"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req5", Namespace: "nvcf-backend"},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated: &metav1.Time{Time: core.GetCurrentTime(ctx)},
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p5": {Type: nvcav2beta1.InstanceTypePod, ID: "p5", Status: "running", LastReportedStatus: "running"},
				},
			},
		},
	}

	pods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "nvcf-backend"},
			Status: v1.PodStatus{
				Phase: v1.PodRunning,
				Conditions: []v1.PodCondition{
					{
						Type:   v1.PodInitialized,
						Status: v1.ConditionTrue,
					},
					{
						Type:   v1.ContainersReady,
						Status: v1.ConditionTrue,
					},
					{
						Type:   v1.PodReady,
						Status: v1.ConditionTrue,
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p2", Namespace: "nvcf-backend"},
			Status: v1.PodStatus{
				StartTime: &[]metav1.Time{metav1.NewTime(time.Now().Add(-time.Minute * 51))}[0],
				Phase:     v1.PodRunning,
				Conditions: []v1.PodCondition{
					{
						Type:   v1.PodInitialized,
						Status: v1.ConditionTrue,
					},
					{
						Type:   v1.ContainersReady,
						Status: v1.ConditionFalse,
					},
					{
						Type:               v1.PodReady,
						Status:             v1.ConditionFalse,
						LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Minute * 50)),
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p3", Namespace: "nvcf-backend"},
			Status: v1.PodStatus{
				StartTime: &[]metav1.Time{metav1.NewTime(time.Now().Add(-time.Minute * 21))}[0],
				Phase:     v1.PodRunning,
				Conditions: []v1.PodCondition{
					{
						Type:   v1.PodInitialized,
						Status: v1.ConditionTrue,
					},
					{
						Type:   v1.ContainersReady,
						Status: v1.ConditionFalse,
					},
					{
						Type:               v1.PodReady,
						Status:             v1.ConditionFalse,
						LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Minute * 20)),
					},
				},
			},
		},
		// p4 should not be cleaned up since it is within the startup window
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p4", Namespace: "nvcf-backend"},
			Status: v1.PodStatus{
				StartTime: &[]metav1.Time{metav1.NewTime(time.Now().Add(-1 * b.k8sTimeConfig.WorkerStartupTimeout).Add(-1 * time.Second))}[0],
				Phase:     v1.PodRunning,
				Conditions: []v1.PodCondition{
					{
						Type:   v1.PodInitialized,
						Status: v1.ConditionTrue,
					},
					{
						Type:   v1.ContainersReady,
						Status: v1.ConditionFalse,
					},
					{
						Type:               v1.PodReady,
						Status:             v1.ConditionFalse,
						LastTransitionTime: metav1.NewTime(time.Now().Add(-1 * b.k8sTimeConfig.WorkerStartupTimeout).Add(-1 * time.Second)),
					},
				},
			},
		},
		// p5 should not be cleaned up since it is within the startup window
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p5", Namespace: "nvcf-backend"},
			Status: v1.PodStatus{
				StartTime: &[]metav1.Time{metav1.NewTime(time.Now().Add(-1 * b.k8sTimeConfig.WorkerStartupTimeout).Add(time.Minute))}[0],
				Phase:     v1.PodRunning,
				Conditions: []v1.PodCondition{
					{
						Type:   v1.PodInitialized,
						Status: v1.ConditionTrue,
					},
					{
						Type:   v1.ContainersReady,
						Status: v1.ConditionFalse,
					},
					{
						Type:               v1.PodReady,
						Status:             v1.ConditionFalse,
						LastTransitionTime: metav1.NewTime(time.Now().Add(-1 * b.k8sTimeConfig.WorkerStartupTimeout).Add(time.Minute)),
					},
				},
			},
		},
	}

	clients := bartClientWithPresetSRAndPods(srs, pods,
		k8smock.NewNetworkPolicyConfigMap(SystemNamespace),
		bartBackendGPUsConfigMap(),
	)
	bc, _, err := b.WithClients(clients).Start(ctx)
	require.NoError(t, err)

	assert.True(t, bc.autoPurgeDegradedWorkers)

	for _, r := range srs {
		bc.icmsRequestHelper.GetICMSRequestStatusUpdatesForRequest(ctx, r)
	}

	expectedCleaned := []string{"p2", "p4"}
	for _, p := range expectedCleaned {
		_, err := bc.clients.K8s.CoreV1().Pods("nvcf-backend").Get(ctx, p, metav1.GetOptions{})
		assert.True(t, apierrors.IsNotFound(err))
	}

	expectedNotCleaned := []string{"p1", "p3", "p5"}
	for _, p := range expectedNotCleaned {
		o, err := bc.clients.K8s.CoreV1().Pods("nvcf-backend").Get(ctx, p, metav1.GetOptions{})
		assert.NoError(t, err)
		assert.NotNil(t, o)
	}
}

func TestCleanupFailedButNoInstances(t *testing.T) {
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(false)
	t.Cleanup(func() { SetUseUUIDForRequestObjName(origUUID) })
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	timeWait := 200 * time.Millisecond
	b := NewBackendk8sCacheBuilder().
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithStaticGPUCapacity(uint64(10))
	assert.NotNil(t, b)

	srs := []*nvcav2beta1.ICMSRequest{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req1", Namespace: "nvcf-backend"},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated: &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 10)},
				RequestStatus:     nvcav2beta1.ICMSRequestStatusFailureAcknowledged,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req2", Namespace: "nvcf-backend"},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated: &metav1.Time{Time: core.GetCurrentTime(ctx)},
				RequestStatus:     nvcav2beta1.ICMSRequestStatusFailureAcknowledged,
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p2": {Type: nvcav2beta1.InstanceTypePod, ID: "p2", Status: "terminated", LastReportedStatus: ""},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req3", Namespace: "nvcf-backend"},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated: &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 20)},
				RequestStatus:     nvcav2beta1.ICMSRequestStatusCompleted,
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p3": {Type: nvcav2beta1.InstanceTypePod, ID: "p3", Status: "running", LastReportedStatus: "running"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req4", Namespace: "nvcf-backend"},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated: &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 20)},
				RequestStatus:     nvcav2beta1.ICMSRequestStatusPending,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req5", Namespace: "nvcf-backend"},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated: &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 70)},
				RequestStatus:     nvcav2beta1.ICMSRequestStatusPending,
			},
		},
	}

	clients := bartClientWithPresetSR(srs, nil)
	bc, _, err := b.WithClients(clients).Start(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)
	time.Sleep(timeWait)

	assert.True(t, bc.icmsRequestHelper.AllInstancesTerminatedAndReported(ctx, srs[0]))
	assert.False(t, bc.icmsRequestHelper.AllInstancesTerminatedAndReported(ctx, srs[1]))
	assert.False(t, bc.icmsRequestHelper.AllInstancesTerminatedAndReported(ctx, srs[2]))
	assert.False(t, bc.icmsRequestHelper.AllInstancesTerminatedAndReported(ctx, srs[3]))
	assert.True(t, bc.icmsRequestHelper.AllInstancesTerminatedAndReported(ctx, srs[4]))
}

func TestCleanupFailedRunningInstances(t *testing.T) {
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(false)
	t.Cleanup(func() { SetUseUUIDForRequestObjName(origUUID) })
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	timeWait := 200 * time.Millisecond
	b := NewBackendk8sCacheBuilder().
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithStaticGPUCapacity(uint64(10))
	assert.NotNil(t, b)

	pods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p2", Namespace: "nvcf-backend"},
			Status: v1.PodStatus{
				Phase: v1.PodRunning,
				Conditions: []v1.PodCondition{
					{
						Type:   v1.PodInitialized,
						Status: v1.ConditionTrue,
					},
					{
						Type:   v1.ContainersReady,
						Status: v1.ConditionTrue,
					},
					{
						Type:   v1.PodReady,
						Status: v1.ConditionTrue,
					},
				},
			},
		},
	}

	srs := []*nvcav2beta1.ICMSRequest{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req1", Namespace: "nvcf-backend"},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated: &metav1.Time{Time: core.GetCurrentTime(ctx)},
				RequestStatus:     nvcav2beta1.ICMSRequestStatusFailureAcknowledged,
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p2": {Type: nvcav2beta1.InstanceTypePod, ID: "p1", Status: "terminated", LastReportedStatus: ""},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req2", Namespace: "nvcf-backend"},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated: &metav1.Time{Time: core.GetCurrentTime(ctx)},
				RequestStatus:     nvcav2beta1.ICMSRequestStatusFailureAcknowledged,
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p2": {Type: nvcav2beta1.InstanceTypePod, ID: "p2", Status: "running", LastReportedStatus: "running"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req3", Namespace: "nvcf-backend"},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated: &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Hour * 2)},
				RequestStatus:     nvcav2beta1.ICMSRequestStatusFailureAcknowledged,
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p3": {Type: nvcav2beta1.InstanceTypePod, ID: "p3", Status: "terminated", LastReportedStatus: "terminated"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req4", Namespace: "nvcf-backend"},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated: &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Hour * 2)},
				RequestStatus:     nvcav2beta1.ICMSRequestStatusFailureAcknowledged,
			},
		},
	}

	clients := bartClientWithPresetSRAndPods(srs, pods,
		k8smock.NewNetworkPolicyConfigMap(SystemNamespace),
		bartBackendGPUsConfigMap(),
	)
	bc, _, err := b.WithClients(clients).Start(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)
	time.Sleep(timeWait)

	err = bc.syncICMSRequest(ctx, srs[0])
	assert.NoError(t, err)

	bc.ForceSync(ctx)

	req, err := bc.clients.BART.NvcaV2beta1().ICMSRequests("nvcf-backend").Get(ctx, "req1", metav1.GetOptions{})
	require.NoError(t, err)

	assert.Equal(t, req.Status.RequestStatus, nvcav2beta1.ICMSRequestStatusFailureAcknowledged)

	err = bc.syncICMSRequest(ctx, srs[1])
	assert.NoError(t, err)

	bc.ForceSync(ctx)

	req, err = bc.clients.BART.NvcaV2beta1().ICMSRequests("nvcf-backend").Get(ctx, "req2", metav1.GetOptions{})
	require.NoError(t, err)

	assert.Equal(t, req.Status.RequestStatus, nvcav2beta1.ICMSRequestStatusCompleted)

	_, err = bc.clients.BART.NvcaV2beta1().ICMSRequests("nvcf-backend").Get(ctx, "req3", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))

	_, err = bc.clients.BART.NvcaV2beta1().ICMSRequests("nvcf-backend").Get(ctx, "req4", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
}

func TestCacheCleanupAPIs(t *testing.T) {
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(false)
	t.Cleanup(func() { SetUseUUIDForRequestObjName(origUUID) })
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	timeWait := 200 * time.Millisecond
	b := NewBackendk8sCacheBuilder().
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithStaticGPUCapacity(uint64(10))
	assert.NotNil(t, b)

	srs := []*nvcav2beta1.ICMSRequest{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req1", Namespace: "nvcf-backend"},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated:  &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 70)},
				CacheReferenceName: "ropvc1",
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p1": {Type: nvcav2beta1.InstanceTypePod, ID: "p1", Status: "terminated", LastReportedStatus: "terminated"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req2", Namespace: "nvcf-backend"},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated:  &metav1.Time{Time: core.GetCurrentTime(ctx)},
				CacheReferenceName: "ropvc2",
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p2": {Type: nvcav2beta1.InstanceTypePod, ID: "p2", Status: "terminated", LastReportedStatus: "terminated"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req3", Namespace: "nvcf-backend"},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated:  &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 70)},
				CacheReferenceName: "ropvc3",
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p3": {Type: nvcav2beta1.InstanceTypePod, ID: "p3", Status: "terminated", LastReportedStatus: "terminated"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req4", Namespace: "nvcf-backend"},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated:  &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 30)},
				CacheReferenceName: "ropvc2",
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p4": {Type: nvcav2beta1.InstanceTypePod, ID: "p4", Status: "terminated", LastReportedStatus: "terminated"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req5", Namespace: "nvcf-backend"},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated:  &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 65)},
				CacheReferenceName: "ropvc1",
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p5": {Type: nvcav2beta1.InstanceTypePod, ID: "p5", Status: "terminated", LastReportedStatus: "terminated"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req6", Namespace: "nvcf-backend"},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated:  nil,
				CacheReferenceName: "ropvc3",
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p6": {Type: nvcav2beta1.InstanceTypePod, ID: "p6", Status: "terminated", LastReportedStatus: "terminated"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req7", Namespace: "nvcf-backend"},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated:  &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 70)},
				CacheReferenceName: "ropvc4",
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p7": {Type: nvcav2beta1.InstanceTypePod, ID: "p7", Status: "terminated", LastReportedStatus: ""},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req8", Namespace: "nvcf-backend"},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated:  &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 70)},
				CacheReferenceName: "ropvc5",
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p6": {Type: nvcav2beta1.InstanceTypePod, ID: "p8", Status: "running", LastReportedStatus: "running"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req9", Namespace: "nvcf-backend"},
			Status: nvcav2beta1.ICMSRequestStatus{
				LastStatusUpdated:  &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 70)},
				CacheReferenceName: "ropvc5",
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p7": {Type: nvcav2beta1.InstanceTypePod, ID: "p9", Status: "running", LastReportedStatus: "running"},
				},
			},
		},
	}
	clients := bartClientWithPresetSR(srs, nil)
	bc, _, err := b.WithClients(clients).Start(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)
	time.Sleep(timeWait)

	bc.CleanupCachedResources(ctx, srs)

	expectedCleaned := []string{"req1", "req3", "req5"}
	for _, s := range expectedCleaned {
		_, err := bc.clients.BART.NvcaV2beta1().ICMSRequests("nvcf-backend").Get(ctx, s, metav1.GetOptions{})
		assert.NotNil(t, err)
		assert.Contains(t, err.Error(), "not found")
	}

	expectedNotCleaned := []string{"req2", "req4", "req6", "req7", "req8", "req9"}
	for _, s := range expectedNotCleaned {
		o, err := bc.clients.BART.NvcaV2beta1().ICMSRequests("nvcf-backend").Get(ctx, s, metav1.GetOptions{})
		assert.NoError(t, err)
		assert.NotNil(t, o)
	}
}

func TestPeriodicInstanceStatusUpdate(t *testing.T) {
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(false)
	t.Cleanup(func() { SetUseUUIDForRequestObjName(origUUID) })
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	b := NewBackendk8sCacheBuilder().
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithStaticGPUCapacity(uint64(10)).
		WithPeriodicInstanceStatusUpdate(true, 5*time.Minute)
	assert.NotNil(t, b)

	p1Pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "nvcf-backend"},
		Status: v1.PodStatus{
			Phase: v1.PodRunning,
			Conditions: []v1.PodCondition{
				{
					Type: v1.PodReady,
				},
			},
		},
	}

	srs := []*nvcav2beta1.ICMSRequest{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req1", Namespace: "nvcf-backend"},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				CacheReferenceName: "ropvc1",
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p1": {
						Type: nvcav2beta1.InstanceTypePod, ID: "p1", Status: "starting", LastReportedStatus: "starting",
						LastReportedTimestamp: &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 3)},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req2", Namespace: "nvcf-backend"},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				CacheReferenceName: "ropvc2",
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p2": {Type: nvcav2beta1.InstanceTypePod, ID: "p2", Status: "terminated", LastReportedStatus: "terminated",
						LastReportedTimestamp: &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 10)},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req3", Namespace: "nvcf-backend"},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				CacheReferenceName: "ropvc3",
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p3": {Type: nvcav2beta1.InstanceTypePod, ID: "p3", Status: "running", LastReportedStatus: "running",
						LastReportedTimestamp: &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 20)},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req4", Namespace: "nvcf-backend"},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				CacheReferenceName: "ropvc2",
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p4": {Type: nvcav2beta1.InstanceTypePod, ID: "p4", Status: "terminated",
						LastReportedTimestamp: &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 10)}},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req5", Namespace: "nvcf-backend"},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				CacheReferenceName: "ropvc1",
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p5": {Type: nvcav2beta1.InstanceTypeMiniService, ID: "p5", Status: "terminated", LastReportedStatus: "terminated"},
				},
			},
		},
	}
	clients := bartClientWithPresetSR(srs, p1Pod)
	bc, _, err := b.WithClients(clients).Start(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)

	up, err := bc.icmsRequestHelper.GetICMSRequestStatusUpdatesForRequest(ctx, srs[0])
	assert.NoError(t, err)
	assert.Empty(t, up)

	up, err = bc.icmsRequestHelper.GetICMSRequestStatusUpdatesForRequest(ctx, srs[1])
	assert.NoError(t, err)
	assert.Empty(t, up)

	up, err = bc.icmsRequestHelper.GetICMSRequestStatusUpdatesForRequest(ctx, srs[2])
	assert.NoError(t, err)
	assert.NotEmpty(t, up)

	up, err = bc.icmsRequestHelper.GetICMSRequestStatusUpdatesForRequest(ctx, srs[3])
	assert.NoError(t, err)
	assert.NotEmpty(t, up)

	up, err = bc.icmsRequestHelper.GetICMSRequestStatusUpdatesForRequest(ctx, srs[4])
	assert.NoError(t, err)
	assert.NotEmpty(t, up)
}

func TestRequestCleanupBYOCUnhealthy(t *testing.T) {
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(false)
	t.Cleanup(func() { SetUseUUIDForRequestObjName(origUUID) })
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	srs := []*nvcav2beta1.ICMSRequest{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req0", Namespace: RequestsNamespace},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				RequestStatus: nvcav2beta1.ICMSRequestStatusPending,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req1", Namespace: RequestsNamespace},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				RequestStatus:     nvcav2beta1.ICMSRequestStatusPending,
				LastStatusUpdated: &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 10)},
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p1": {Type: nvcav2beta1.InstanceTypePod, ID: "p1", Status: "running", LastReportedStatus: "running"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req2", Namespace: RequestsNamespace},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				RequestStatus:     nvcav2beta1.ICMSRequestStatusInProgress,
				LastStatusUpdated: &metav1.Time{Time: core.GetCurrentTime(ctx)},
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p2": {Type: nvcav2beta1.InstanceTypePod, ID: "p2", Status: "terminated", LastReportedStatus: ""},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req3", Namespace: RequestsNamespace},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				RequestStatus:     nvcav2beta1.ICMSRequestStatusInProgress,
				LastStatusUpdated: &metav1.Time{Time: core.GetCurrentTime(ctx)},
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p3": {Type: nvcav2beta1.InstanceTypePod, ID: "p3", Status: "terminated", LastReportedStatus: "terminated"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req4", Namespace: RequestsNamespace},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				RequestStatus:      nvcav2beta1.ICMSRequestStatusInProgress,
				LastStatusUpdated:  &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 30)},
				CacheReferenceName: "ropvc2",
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p4": {Type: nvcav2beta1.InstanceTypePod, ID: "p4", Status: "terminated", LastReportedStatus: "terminated"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req5", Namespace: RequestsNamespace},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				RequestStatus:     nvcav2beta1.ICMSRequestStatusInstancesInProgress,
				LastStatusUpdated: &metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 5)},
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p5": {Type: nvcav2beta1.InstanceTypePod, ID: "p5", Status: "running", LastReportedStatus: "running"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req6", Namespace: RequestsNamespace},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				RequestStatus: nvcav2beta1.ICMSRequestStatusInstancesInProgress,
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p6": {Type: nvcav2beta1.InstanceTypePod, ID: "p6", Status: "starting", LastReportedStatus: "running"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "req7", Namespace: RequestsNamespace},
			Spec: nvcav2beta1.ICMSRequestSpec{
				Action: common.FunctionCreationAction,
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				RequestStatus: nvcav2beta1.ICMSRequestStatusInstancesInProgress,
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p6": {Type: nvcav2beta1.InstanceTypePod, ID: "p6", Status: "terminated", LastReportedStatus: "terminated"},
				},
			},
		},
	}

	clients := bartClientWithPresetSR(srs, nil)
	b := NewBackendk8sCacheBuilder().
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithStaticGPUCapacity(uint64(10)).
		WithClients(clients)
	require.NotNil(t, b)

	bc, _, err := b.Start(ctx)
	require.NoError(t, err)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	require.NoError(t, err)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		expectedCleaned := []string{"req3", "req7"}
		for _, s := range expectedCleaned {
			_, err := bc.clients.BART.NvcaV2beta1().ICMSRequests(RequestsNamespace).Get(ctx, s, metav1.GetOptions{})
			assert.True(ct, apierrors.IsNotFound(err), err)
		}
	}, 2*time.Second, 100*time.Millisecond)

	expectedNotCleaned := []string{"req0", "req1", "req2", "req4", "req5", "req6"}
	for _, s := range expectedNotCleaned {
		_, err := bc.clients.BART.NvcaV2beta1().ICMSRequests(RequestsNamespace).Get(ctx, s, metav1.GetOptions{})
		assert.NoError(t, err)
	}
}

func TestNVCAUpgradeStatus(t *testing.T) {
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(true)
	t.Cleanup(func() { SetUseUUIDForRequestObjName(origUUID) })
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	timeWait := 200 * time.Millisecond
	b := NewBackendk8sCacheBuilder().
		WithNamespaceLabels(labels.Set{"foo": "bar"})
	assert.NotNil(t, b)

	nvcaDep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      NVCADeploymentName,
			Namespace: SystemNamespace,
		},
		Status: appsv1.DeploymentStatus{
			Replicas:      2,
			ReadyReplicas: 1,
			Conditions: []appsv1.DeploymentCondition{
				{
					Type:           appsv1.DeploymentProgressing,
					LastUpdateTime: metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 5)},
					Reason:         "ReplicasetUpdated",
				},
			},
		},
	}

	clients := mockKubeClients(nvcaDep)
	bc, _, err := b.WithClients(clients).Start(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)
	time.Sleep(timeWait)

	assert.Equal(t, bc.getNVCAUpgradeStatus(ctx), types.NVCAUpgradeInProgress)

	nvcaDep.Status.Conditions = []appsv1.DeploymentCondition{
		{
			Type:           appsv1.DeploymentProgressing,
			LastUpdateTime: metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 35)},
			Reason:         "ProgressDeadlineExceeded",
		},
	}

	clients = mockKubeClients(nvcaDep)
	bc, _, err = b.WithClients(clients).Start(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)
	time.Sleep(timeWait)

	assert.Equal(t, bc.getNVCAUpgradeStatus(ctx), types.NVCAUpgradeStatusFailed)

	// NewReplicSetAvailable condition updated 5mins ago
	nvcaDep.Status = appsv1.DeploymentStatus{
		ReadyReplicas: 1,
		Replicas:      1,
		Conditions: []appsv1.DeploymentCondition{
			{
				Type:           appsv1.DeploymentProgressing,
				LastUpdateTime: metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 5)},
				Reason:         "NewReplicaSetAvailable",
			},
		},
	}

	clients = mockKubeClients(nvcaDep)
	bc, _, err = b.WithClients(clients).Start(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)
	time.Sleep(timeWait)

	assert.Equal(t, bc.getNVCAUpgradeStatus(ctx), types.NVCAUpgradeStatusSuccess)

	// NewReplicSetAvailable condition updated 20mins ago
	nvcaDep.Status = appsv1.DeploymentStatus{
		ReadyReplicas: 1,
		Replicas:      1,
		Conditions: []appsv1.DeploymentCondition{
			{
				Type:           appsv1.DeploymentProgressing,
				LastUpdateTime: metav1.Time{Time: core.GetCurrentTime(ctx).Add(-time.Minute * 70)},
				Reason:         "NewReplicaSetAvailable",
			},
		},
	}

	clients = mockKubeClients(nvcaDep)
	bc, _, err = b.WithClients(clients).Start(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)
	time.Sleep(timeWait)

	assert.Equal(t, bc.getNVCAUpgradeStatus(ctx), types.NVCAUpgradeNoStatus)
}

func TestHelperAPIs(t *testing.T) {
	stTime := metav1.Time{Time: time.Now().Add(-20 * time.Minute)}
	st := v1.PodStatus{
		Phase:     v1.PodRunning,
		StartTime: &stTime,
	}
	st.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionTrue,
		},
	}
	defaultTimeConfig := (&k8sutil.TimeConfig{}).Complete()

	// podPhaseToInstanceState
	is, _ := podPhaseToInstanceState(&v1.Pod{Status: v1.PodStatus{Phase: v1.PodPending}}, defaultTimeConfig)
	assert.Equal(t, is, types.ICMSInstanceStarted)

	is, _ = podPhaseToInstanceState(&v1.Pod{Status: v1.PodStatus{Phase: v1.PodUnknown}}, defaultTimeConfig)
	assert.Equal(t, is, types.ICMSInstanceStarted)

	is, _ = podPhaseToInstanceState(&v1.Pod{Status: v1.PodStatus{Phase: v1.PodRunning}}, defaultTimeConfig)
	assert.Equal(t, is, types.ICMSInstanceStarted)

	is, _ = podPhaseToInstanceState(&v1.Pod{Status: st}, defaultTimeConfig)
	assert.Equal(t, is, types.ICMSInstanceRunning)

	is, _ = podPhaseToInstanceState(&v1.Pod{Status: v1.PodStatus{Phase: v1.PodSucceeded}}, defaultTimeConfig)
	assert.Equal(t, is, types.ICMSInstanceRunning)

	is, _ = podPhaseToInstanceState(&v1.Pod{Status: v1.PodStatus{Phase: v1.PodFailed}}, defaultTimeConfig)
	assert.Equal(t, is, types.ICMSInstanceFailed)

	is, _ = podPhaseToInstanceState(&v1.Pod{Status: v1.PodStatus{}}, defaultTimeConfig)
	assert.Equal(t, is, types.ICMSInstanceStarted)

	st.Phase = v1.PodPending
	is, _ = podPhaseToInstanceState(&v1.Pod{Status: st}, defaultTimeConfig)
	assert.Equal(t, is, types.ICMSInstanceKilledNoCapacity)

	st.Phase = v1.PodRunning
	st.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionFalse,
		},
		{
			Type:   v1.PodInitialized,
			Status: v1.ConditionTrue,
		},
		{
			Type:               v1.PodScheduled,
			LastTransitionTime: metav1.Time{Time: time.Now().Add(-20 * time.Minute)},
		},
	}

	st.ContainerStatuses = []v1.ContainerStatus{
		{
			RestartCount: int32(2),
		},
	}
	is, _ = podPhaseToInstanceState(&v1.Pod{Status: st}, defaultTimeConfig)
	assert.Equal(t, is, types.ICMSInstanceStarted)

	// CreateContainerError should be treated as terminal and terminate the instance.
	// Use a separate pod status so the shared st is not mutated for following cases.
	stCreateErr := v1.PodStatus{
		Phase:     v1.PodPending,
		StartTime: &metav1.Time{Time: time.Now().Add(-5 * time.Minute)},
		Conditions: []v1.PodCondition{
			{Type: v1.PodScheduled, Status: v1.ConditionTrue},
		},
		ContainerStatuses: []v1.ContainerStatus{
			{
				State: v1.ContainerState{
					Waiting: &v1.ContainerStateWaiting{
						Reason:  "CreateContainerError",
						Message: "failed to create container",
					},
				},
			},
		},
	}
	is, _ = podPhaseToInstanceState(&v1.Pod{Status: stCreateErr}, defaultTimeConfig)
	assert.Equal(t, types.ICMSInstanceFailedCreateContainerError, is)

	st.ContainerStatuses = []v1.ContainerStatus{
		{
			RestartCount: int32(5),
		},
	}
	is, _ = podPhaseToInstanceState(&v1.Pod{Status: st}, defaultTimeConfig)
	assert.Equal(t, is, types.ICMSInstanceFailedContainerRestartLoop)

	st.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionFalse,
		},
		{
			Type:   v1.PodInitialized,
			Status: v1.ConditionFalse,
		},
		{
			Type:               v1.PodScheduled,
			LastTransitionTime: metav1.Time{Time: time.Now().Add(-20 * time.Minute)},
		},
	}
	st.InitContainerStatuses = []v1.ContainerStatus{
		{
			RestartCount: int32(2),
		},
	}
	st.StartTime = &metav1.Time{Time: time.Now().Add(-210 * time.Minute)}
	is, _ = podPhaseToInstanceState(&v1.Pod{Status: st}, defaultTimeConfig)
	assert.Equal(t, types.ICMSInstanceFailedInitContainerStuck, is)

	st.InitContainerStatuses = []v1.ContainerStatus{
		{
			RestartCount: int32(5),
		},
	}
	is, _ = podPhaseToInstanceState(&v1.Pod{Status: st}, defaultTimeConfig)
	assert.Equal(t, types.ICMSInstanceFailedInitContainerRestartLoop, is)

	st.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodReady,
			Status: v1.ConditionFalse,
		},
		{
			Type:   v1.PodInitialized,
			Status: v1.ConditionFalse,
		},
		{
			Type:               v1.PodScheduled,
			LastTransitionTime: metav1.Time{Time: time.Now().Add(-121 * time.Minute)},
		},
	}
	st.Phase = v1.PodPending
	stTime = metav1.Time{Time: time.Now().Add(-31 * time.Minute)}
	st.InitContainerStatuses = []v1.ContainerStatus{
		{
			RestartCount: int32(2),
			State: v1.ContainerState{
				Waiting: &v1.ContainerStateWaiting{
					Reason: ImagePullIssueReason,
				},
			},
		},
	}
	st.Conditions = []v1.PodCondition{
		{
			Type:   v1.PodScheduled,
			Status: v1.ConditionTrue,
		},
	}
	is, _ = podPhaseToInstanceState(&v1.Pod{Status: st}, defaultTimeConfig)
	assert.Equal(t, is, types.ICMSInstanceFailedImagePullIssues)
	st.InitContainerStatuses = []v1.ContainerStatus{
		{
			RestartCount: int32(0),
		},
	}

	st.ContainerStatuses = []v1.ContainerStatus{
		{
			RestartCount: int32(2),
			State: v1.ContainerState{
				Waiting: &v1.ContainerStateWaiting{
					Reason: ImagePullIssueReason,
				},
			},
		},
	}
	is, _ = podPhaseToInstanceState(&v1.Pod{Status: st}, defaultTimeConfig)
	assert.Equal(t, is, types.ICMSInstanceFailedImagePullIssues)

	st.InitContainerStatuses = []v1.ContainerStatus{
		{
			RestartCount: int32(4),
		},
	}

	st.Phase = v1.PodRunning
	stTime = metav1.Time{Time: time.Now().Add(-11 * time.Minute)}
	is, _ = podPhaseToInstanceState(&v1.Pod{Status: st}, defaultTimeConfig)
	assert.Equal(t, is, types.ICMSInstanceFailedInitContainerRestartLoop)

	st.Reason = "Random Reason"
	assert.False(t, IsPodAdmissionRejected(st))

	st.Reason = UnexpectedAdmissionErrReason
	assert.True(t, IsPodAdmissionRejected(st))

	// reqStatusToState
	assert.Equal(t, reqStatusToState(nvcav2beta1.ICMSRequestStatusCompleted), types.ICMSInstanceRequestActive)
	assert.Equal(t, reqStatusToState(nvcav2beta1.ICMSRequestStatusPending), types.ICMSInstanceRequestActive)
	assert.Equal(t, reqStatusToState(nvcav2beta1.ICMSRequestStatusInProgress), types.ICMSInstanceRequestActive)
	assert.Equal(t, reqStatusToState(nvcav2beta1.ICMSRequestStatusFailed), types.ICMSInstanceRequestClosed)
	assert.Equal(t, reqStatusToState(nvcav2beta1.RequestStatus("")), types.ICMSInstanceRequestActive)
}

func TestGetObjectName(t *testing.T) {
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(false)
	t.Cleanup(func() { SetUseUUIDForRequestObjName(origUUID) })
	srObjMeta := getICMSRequestObjectMeta(
		types.DeploymentInfo{
			RequestID:      "randomRequestID1",
			MessageID:      "randomId1",
			NCAID:          "ncaID",
			MessageBatchID: "batch1",
			GPUType:        "a100",
		})
	strReqName := fmt.Sprintf("%v-%v", "sr", "randomRequestID1")
	assert.Equal(t, strReqName, srObjMeta.Name)

	SetUseUUIDForRequestObjName(true)
	srObjMeta = getICMSRequestObjectMeta(
		types.DeploymentInfo{
			RequestID:      "randomRequestID1",
			MessageID:      "randomId2",
			NCAID:          "ncaID",
			MessageBatchID: "batch1",
			GPUType:        "a100",
		})
	assert.NotEqual(t, strReqName, srObjMeta.Name)
}

func TestCreateSPOTRequestLaunchSpec(t *testing.T) {
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(true)
	t.Cleanup(func() { SetUseUUIDForRequestObjName(origUUID) })
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	clients := mockKubeClients()

	fff := &featureflagmock.Fetcher{}
	bc, _, err := NewBackendk8sCacheBuilder().
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithClients(clients).
		WithFeatureFlagFetcher(fff).
		Start(ctx)
	require.NoError(t, err)

	// BYOO without flag enabled.

	// Container function.
	containerFuncMsgBYOO := decodeCM(t, creationgMsgFunctionContainerLaunchSpecBYOO)

	_, err = bc.CreateICMSCreationMessageRequest(ctx, containerFuncMsgBYOO, "mrecpt", "msg", "")
	require.EqualError(t, err, "telemetries is set but required features are disabled: BYOObservability, UseFunctionTranslator")

	fff.EnabledFFs = append(fff.EnabledFFs,
		featureflag.BYOObservability,
		featureflag.UseFunctionTranslator,
	)

	containerSRBYOO, err := bc.CreateICMSCreationMessageRequest(ctx, containerFuncMsgBYOO, "mrecpt", "msg", "")
	require.NoError(t, err)

	err = clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Delete(ctx, containerSRBYOO.Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	// Helm function.
	fff.EnabledFFs = nil
	helmFuncMsgBYOO := decodeCM(t, creationgMsgFunctionHelmLaunchSpecBYOO)

	_, err = bc.CreateICMSCreationMessageRequest(ctx, helmFuncMsgBYOO, "mrecpt", "msg", "")
	require.EqualError(t, err, "telemetries is set but required features are disabled: BYOObservability, UseFunctionTranslator")

	fff.EnabledFFs = append(fff.EnabledFFs,
		featureflag.BYOObservability,
		featureflag.UseFunctionTranslator,
	)

	helmSRBYOO, err := bc.CreateICMSCreationMessageRequest(ctx, helmFuncMsgBYOO, "mrecpt", "msg", "")
	require.NoError(t, err)

	err = clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Delete(ctx, helmSRBYOO.Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	fff.EnabledFFs = nil

	// This section tests the launch artifact code path,
	// which may be enabled explicitly by cluster owners until ICMS removes them
	// from their creation messages.
	// Launch artifacts will be used downstream.

	cmsgLS := function.CreationQueueMessage{
		CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
			RequestID:      "randomRequestID3",
			NCAID:          "randomID",
			MessageBatchID: "randomMessageBatchID",
			Action:         common.FunctionCreationAction,
			InstanceCount:  1,
		},
		Details: function.Details{
			FunctionID:        "functionId",
			FunctionVersionID: "functionVersionId",
		},
		LaunchSpecification: &function.LaunchSpecification{
			EnvironmentB64: base64.StdEncoding.EncodeToString([]byte(`FOO=bar`)),
		},
		LaunchArtifacts: getArtifactsList(t),
	}

	sr, err := bc.CreateICMSCreationMessageRequest(ctx, cmsgLS, "randomMsgReceipt1", "randomId1", "")
	require.NoError(t, err)
	assert.NotNil(t, sr)

	srList, err := clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, srList.Items, 1)
	assert.NotNil(t, srList.Items[0].Spec.CreationMsgInfo.FunctionLaunchSpecification)
	assert.Len(t, srList.Items[0].Spec.CreationMsgInfo.LaunchArtifacts, 5)

	err = clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Delete(ctx, srList.Items[0].Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	// Turning on the function translation feature flag should result in no
	// launch artifacts populated.
	fff.SetFeatureFlags(featureflag.UseFunctionTranslator)

	sr, err = bc.CreateICMSCreationMessageRequest(ctx, cmsgLS, "randomMsgReceipt1", "randomId1", "")
	require.NoError(t, err)
	assert.NotNil(t, sr)

	srList, err = clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, srList.Items, 1)
	assert.NotNil(t, srList.Items[0].Spec.CreationMsgInfo.FunctionLaunchSpecification)
	// The only difference is no launch artifacts are set when function translation is used.
	assert.Len(t, srList.Items[0].Spec.CreationMsgInfo.LaunchArtifacts, 0)

	err = clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Delete(ctx, srList.Items[0].Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	// This message should result in launch artifacts being populated
	// because the launch spec is not set.
	cmsgNoLS := function.CreationQueueMessage{
		CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
			RequestID:      "randomRequestID3",
			NCAID:          "randomID",
			MessageBatchID: "randomMessageBatchID",
			Action:         common.FunctionCreationAction,
			InstanceCount:  1,
		},
		Details: function.Details{
			FunctionID:        "functionId",
			FunctionVersionID: "functionVersionId",
		},
		LaunchArtifacts: getArtifactsList(t),
	}

	sr, err = bc.CreateICMSCreationMessageRequest(ctx, cmsgNoLS, "randomMsgReceipt1", "randomId1", "")
	require.NoError(t, err)
	assert.NotNil(t, sr)

	srList, err = clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, srList.Items, 1)
	assert.Nil(t, srList.Items[0].Spec.CreationMsgInfo.FunctionLaunchSpecification)
	assert.Len(t, srList.Items[0].Spec.CreationMsgInfo.LaunchArtifacts, 5)

	err = clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Delete(ctx, srList.Items[0].Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	// This message should result in launch artifacts being populated
	// because the launch spec is invalid.
	cmsgEmptyLS := function.CreationQueueMessage{
		CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
			RequestID:      "randomRequestID3",
			NCAID:          "randomID",
			MessageBatchID: "randomMessageBatchID",
			Action:         common.FunctionCreationAction,
			InstanceCount:  1,
		},
		Details: function.Details{
			FunctionID:        "functionId",
			FunctionVersionID: "functionVersionId",
		},
		LaunchSpecification: &function.LaunchSpecification{
			CloudProvider: "foobar",
		},
		LaunchArtifacts: getArtifactsList(t),
	}

	sr, err = bc.CreateICMSCreationMessageRequest(ctx, cmsgEmptyLS, "randomMsgReceipt1", "randomId1", "")
	require.NoError(t, err)
	assert.NotNil(t, sr)

	srList, err = clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, srList.Items, 1)
	assert.NotNil(t, srList.Items[0].Spec.CreationMsgInfo.FunctionLaunchSpecification)
	assert.Len(t, srList.Items[0].Spec.CreationMsgInfo.LaunchArtifacts, 5)

	err = clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Delete(ctx, srList.Items[0].Name, metav1.DeleteOptions{})
	require.NoError(t, err)
}

func TestCreateICMSCreationMessageRequestWithEnvOverrides(t *testing.T) {
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(true)
	t.Cleanup(func() { SetUseUUIDForRequestObjName(origUUID) })
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	clients := mockKubeClients()

	// Helper function to decode env vars using nvcf-icms-translate
	decodeEnv := func(envB64 string) map[string]string {
		if envB64 == "" {
			return nil
		}
		envs, err := common.DecodeEnvironmentB64(envB64, common.EnvDecoderText)
		require.NoError(t, err)
		return envs
	}

	t.Run("function env overrides are applied", func(t *testing.T) {
		fff := &featureflagmock.Fetcher{}
		fff.SetFeatureFlags(featureflag.UseFunctionTranslator)

		functionOverrides := map[string]string{
			"INIT_CONTAINER":  "nvcr.io/custom/init:v1.0",
			"UTILS_CONTAINER": "nvcr.io/custom/utils:v1.0",
		}

		bc, _, err := NewBackendk8sCacheBuilder().
			WithNamespaceLabels(labels.Set{"foo": "bar"}).
			WithClients(clients).
			WithFeatureFlagFetcher(fff).
			WithEnvOverrides(functionOverrides, nil).
			Start(ctx)
		require.NoError(t, err)

		originalEnv := map[string]string{
			"EXISTING_VAR":   "existing_value",
			"INIT_CONTAINER": "nvcr.io/original/init:v0.9",
		}

		cmsg := function.CreationQueueMessage{
			CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
				RequestID:      "env-override-func-test",
				NCAID:          "randomID",
				MessageBatchID: "randomMessageBatchID",
				Action:         common.FunctionCreationAction,
				InstanceCount:  1,
			},
			Details: function.Details{
				FunctionID:        "functionId",
				FunctionVersionID: "functionVersionId",
			},
			LaunchSpecification: &function.LaunchSpecification{
				EnvironmentB64: envutil.EncodeEnvB64(originalEnv),
			},
		}

		sr, err := bc.CreateICMSCreationMessageRequest(ctx, cmsg, "mrecpt1", "msgid1", "")
		require.NoError(t, err)
		assert.NotNil(t, sr)

		// Verify overrides were applied
		resultEnv := decodeEnv(sr.Spec.CreationMsgInfo.FunctionLaunchSpecification.EnvironmentB64)
		assert.Equal(t, "nvcr.io/custom/init:v1.0", resultEnv["INIT_CONTAINER"], "INIT_CONTAINER should be overridden")
		assert.Equal(t, "nvcr.io/custom/utils:v1.0", resultEnv["UTILS_CONTAINER"], "UTILS_CONTAINER should be added")
		assert.Equal(t, "existing_value", resultEnv["EXISTING_VAR"], "Existing vars should be preserved")

		err = clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Delete(ctx, sr.Name, metav1.DeleteOptions{})
		require.NoError(t, err)
	})

	t.Run("task env overrides are applied", func(t *testing.T) {
		fff := &featureflagmock.Fetcher{}

		taskOverrides := map[string]string{
			"ESS_AGENT_CONTAINER": "nvcr.io/custom/ess:v2.0",
		}

		bc, _, err := NewBackendk8sCacheBuilder().
			WithNamespaceLabels(labels.Set{"foo": "bar"}).
			WithClients(clients).
			WithFeatureFlagFetcher(fff).
			WithEnvOverrides(nil, taskOverrides).
			Start(ctx)
		require.NoError(t, err)

		originalEnv := map[string]string{
			"TASK_VAR": "task_value",
		}

		cmsg := task.CreationQueueMessage{
			CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
				RequestID:      "env-override-task-test",
				NCAID:          "randomID",
				MessageBatchID: "randomMessageBatchID",
				Action:         common.TaskCreationAction,
				InstanceCount:  1,
			},
			Details: task.Details{
				TaskID: "taskId",
			},
			LaunchSpecification: task.LaunchSpecification{
				EnvironmentB64: envutil.EncodeEnvB64(originalEnv),
			},
		}

		sr, err := bc.CreateICMSCreationMessageRequest(ctx, cmsg, "mrecpt2", "msgid2", "")
		require.NoError(t, err)
		assert.NotNil(t, sr)

		// Verify overrides were applied
		resultEnv := decodeEnv(sr.Spec.CreationMsgInfo.TaskLaunchSpecification.EnvironmentB64)
		assert.Equal(t, "nvcr.io/custom/ess:v2.0", resultEnv["ESS_AGENT_CONTAINER"], "ESS_AGENT_CONTAINER should be added")
		assert.Equal(t, "task_value", resultEnv["TASK_VAR"], "Existing vars should be preserved")

		err = clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Delete(ctx, sr.Name, metav1.DeleteOptions{})
		require.NoError(t, err)
	})

	t.Run("empty overrides do not modify environment", func(t *testing.T) {
		fff := &featureflagmock.Fetcher{}
		fff.SetFeatureFlags(featureflag.UseFunctionTranslator)

		bc, _, err := NewBackendk8sCacheBuilder().
			WithNamespaceLabels(labels.Set{"foo": "bar"}).
			WithClients(clients).
			WithFeatureFlagFetcher(fff).
			WithEnvOverrides(nil, nil).
			Start(ctx)
		require.NoError(t, err)

		originalEnv := map[string]string{
			"FOO": "bar",
		}
		originalEnvB64 := envutil.EncodeEnvB64(originalEnv)

		cmsg := function.CreationQueueMessage{
			CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
				RequestID:      "env-override-empty-test",
				NCAID:          "randomID",
				MessageBatchID: "randomMessageBatchID",
				Action:         common.FunctionCreationAction,
				InstanceCount:  1,
			},
			Details: function.Details{
				FunctionID:        "functionId",
				FunctionVersionID: "functionVersionId",
			},
			LaunchSpecification: &function.LaunchSpecification{
				EnvironmentB64: originalEnvB64,
			},
		}

		sr, err := bc.CreateICMSCreationMessageRequest(ctx, cmsg, "mrecpt3", "msgid3", "")
		require.NoError(t, err)
		assert.NotNil(t, sr)

		// Verify env was not modified
		assert.Equal(t, originalEnvB64, sr.Spec.CreationMsgInfo.FunctionLaunchSpecification.EnvironmentB64)

		err = clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Delete(ctx, sr.Name, metav1.DeleteOptions{})
		require.NoError(t, err)
	})

	t.Run("nil launch spec does not cause error with overrides", func(t *testing.T) {
		fff := &featureflagmock.Fetcher{}

		functionOverrides := map[string]string{
			"INIT_CONTAINER": "nvcr.io/custom/init:v1.0",
		}

		bc, _, err := NewBackendk8sCacheBuilder().
			WithNamespaceLabels(labels.Set{"foo": "bar"}).
			WithClients(clients).
			WithFeatureFlagFetcher(fff).
			WithEnvOverrides(functionOverrides, nil).
			Start(ctx)
		require.NoError(t, err)

		cmsg := function.CreationQueueMessage{
			CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
				RequestID:      "env-override-nil-spec-test",
				NCAID:          "randomID",
				MessageBatchID: "randomMessageBatchID",
				Action:         common.FunctionCreationAction,
				InstanceCount:  1,
			},
			Details: function.Details{
				FunctionID:        "functionId",
				FunctionVersionID: "functionVersionId",
			},
			LaunchSpecification: nil,
			LaunchArtifacts:     getArtifactsList(t),
		}

		sr, err := bc.CreateICMSCreationMessageRequest(ctx, cmsg, "mrecpt4", "msgid4", "")
		require.NoError(t, err)
		assert.NotNil(t, sr)
		// Overrides should not be applied when launch spec is nil
		assert.Nil(t, sr.Spec.CreationMsgInfo.FunctionLaunchSpecification)

		err = clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Delete(ctx, sr.Name, metav1.DeleteOptions{})
		require.NoError(t, err)
	})
}

func TestBatchingRequests(t *testing.T) {
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(true)
	t.Cleanup(func() { SetUseUUIDForRequestObjName(origUUID) })
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	agentOpts := AgentOptions{
		TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
			TokenURL:           "http://localhost",
			SSATokenScope:      "byoc_registration",
			SSAClientID:        "foo",
			SSAClientSecretKey: "bar",
		},
		NCAId:                          "randomNCAId123",
		ClusterName:                    "bartnvbackend",
		ClusterID:                      "clusterid-1",
		ClusterDescription:             "this is a test cluster",
		ClusterGroupName:               "group of all A30",
		ICMSURL:                        "https://icms.nvcf.nvidia.com",
		CloudProvider:                  "on-prem",
		KubeConfigPath:                 "testdata/kubeconfig.yaml",
		NamespaceLabels:                labels.Set{"foo": "bar"},
		NVCASvcAddress:                 "localhost",
		NVCAAdminAddr:                  "localhost",
		CredRenewInterval:              DefaultCredRenewInterval,
		HeartbeatInterval:              DefaultHeartBeatInterval,
		SyncQueueInterval:              defaultSyncQueueInterval,
		SyncRequestStatusInterval:      DefaultSyncRequestStatusInterval,
		SyncAcknowledgeRequestInterval: ackReqInterval,
		GPUCapacity:                    uint64(20),
		FeatureFlagFetcher:             featureflag.DefaultFetcher,
	}
	ag, err := newAgent(ctx, &agentOpts)
	require.NoError(t, err)

	timeWait := 200 * time.Millisecond
	b := NewBackendk8sCacheBuilder().
		WithNamespaceLabels(agentOpts.NamespaceLabels).
		WithStaticGPUCapacity(ag.GPUCapacity)
	assert.NotNil(t, b)

	clients := mockKubeClients()
	bc, _, err := b.WithClients(clients).Start(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)
	time.Sleep(timeWait)

	ag.backendk8scache = bc

	sp, _ := setupTestICMSClient()

	ag.icmsClient = sp
	queueCreds := getTestQueueCreds(false)
	metrics := nvcametrics.FromContext(ctx)
	ag.queueManager = NewQueueManager(bc, ag.backendHealthCache, queuesqs.NewClient("http://localhost:4566", ""), queueCreds, featureflag.DefaultFetcher,
		types.MaintenanceModeNone, metrics)

	it, err := bc.GetAllBackendGPUs(ctx)
	assert.NoError(t, err)
	assert.Equal(t, len(it), 3)

	assert.Equal(t, bc.systemNamespace, SystemNamespace)
	assert.Equal(t, bc.requestsNamespace, RequestsNamespace)
	assert.Equal(t, bc.podInstanceNamespace, RequestsNamespace)

	cmsg := function.CreationQueueMessage{
		CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
			RequestID:      "randomRequestID1",
			NCAID:          "randomID",
			MessageBatchID: "randomMessageBatchID",
			Action:         common.FunctionCreationAction,
			InstanceCount:  1,
		},
		Details: function.Details{
			FunctionID:        "functionId",
			FunctionVersionID: "functionVersionId",
		},
		LaunchArtifacts: getArtifactsList(t),
	}

	sr, err := bc.CreateICMSCreationMessageRequest(ctx, cmsg, "randomMsgReceipt1", "randomId1", "")
	assert.NoError(t, err)
	assert.NotNil(t, sr)

	sr, err = bc.CreateICMSCreationMessageRequest(ctx, cmsg, "randomMsgReceipt2", "randomId2", "")
	assert.NoError(t, err)
	assert.NotNil(t, sr)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	assert.NoError(t, err)
	bc.ForceSync(ctx)

	// transition All RequestStatus for test, normally it would be done by Agent
	err = updateAllRequestToPending(ctx, bc)
	assert.NoError(t, err)

	verifyPodsCreated := func(ct *assert.CollectT) {
		// use local variable to avoid data race
		err := bc.SyncAllICMSRequests(ctx)
		assert.NoError(ct, err)
		bc.ForceSync(ctx)
		srList, err := clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).List(ctx, metav1.ListOptions{})
		if assert.NoError(ct, err) {
			for _, sr := range srList.Items {
				assert.NotEmpty(ct, sr.Status.RequestStatus)
				assert.NotEqual(ct, nvcav2beta1.ICMSRequestStatusPending, sr.Status.RequestStatus)
			}
		}
	}
	assert.EventuallyWithT(t, verifyPodsCreated, 60*time.Second, 100*time.Millisecond)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		// use local variable to avoid data race
		err := bc.SyncAllICMSRequests(ctx)
		assert.NoError(ct, err)
		bc.ForceSync(ctx)
		srList, err := clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).List(ctx, metav1.ListOptions{})
		if assert.NoError(ct, err) {
			for _, sr := range srList.Items {
				assert.Equal(ct, nvcav2beta1.ICMSRequestStatusInstancesInProgress, sr.Status.RequestStatus)
			}
		}
	}, 30*time.Second, 100*time.Millisecond)

	// Verify both pods are created for the batched requests
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		// use local variable to avoid data race
		err := bc.SyncAllICMSRequests(ctx)
		assert.NoError(ct, err)
		bc.ForceSync(ctx)
		ps, err := bc.GetAllPodsForRequest(ctx, "randomRequestID1")
		if assert.NoErrorf(ct, err, "failed retrieving pods for request %s, error: %v", "randomRequestID1", err) {
			assert.Len(ct, ps, 2)
		}
	}, 30*time.Second, 100*time.Millisecond)
}

func TestBackendK8sCacheQueryAPIs(t *testing.T) {
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(false)
	t.Cleanup(func() { SetUseUUIDForRequestObjName(origUUID) })
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	agentOpts := AgentOptions{
		TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
			TokenURL:           "http://localhost",
			SSATokenScope:      "byoc_registration",
			SSAClientID:        "foo",
			SSAClientSecretKey: "bar",
		},
		NCAId:                          "randomNCAId123",
		ClusterName:                    "bartnvbackend",
		ClusterID:                      "clusterid-1",
		ClusterDescription:             "this is a test cluster",
		ClusterGroupName:               "group of all A30",
		ICMSURL:                        "http://localhost",
		CloudProvider:                  "on-prem",
		KubeConfigPath:                 "testdata/kubeconfig.yaml",
		NamespaceLabels:                labels.Set{"foo": "bar"},
		NVCASvcAddress:                 "localhost",
		NVCAAdminAddr:                  "localhost",
		GPUCapacity:                    10,
		CredRenewInterval:              DefaultCredRenewInterval,
		HeartbeatInterval:              DefaultHeartBeatInterval,
		SyncQueueInterval:              defaultSyncQueueInterval,
		SyncRequestStatusInterval:      DefaultSyncRequestStatusInterval,
		SyncAcknowledgeRequestInterval: ackReqInterval,
		LogPostingEnabled:              true,
		FeatureFlagFetcher:             featureflag.DefaultFetcher,
		K8sTimeConfig: (&k8sutil.TimeConfig{
			ModelCacheIdlePeriod:                 20 * time.Minute,
			FailingObjectsBackoffTimeout:         1 * time.Millisecond, // Very short for tests
			FailingObjectsBackoffRequeueInterval: 1 * time.Millisecond, // Very short for tests
		}).Complete(),
	}
	ag, err := newAgent(ctx, &agentOpts)
	require.NoError(t, err)

	timeWait := 200 * time.Millisecond
	b := NewBackendk8sCacheBuilder().
		WithNamespaceLabels(agentOpts.NamespaceLabels).
		WithStaticGPUCapacity(ag.GPUCapacity).
		WithTimeConfig(ag.K8sTimeConfig)
	assert.NotNil(t, b)

	clients := mockKubeClients()
	bc, _, err := b.WithClients(clients).Start(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)
	time.Sleep(timeWait)

	ag.backendk8scache = bc

	s, m := setupTestICMSClient()

	ag.icmsClient = s
	queueCreds := getTestQueueCreds(false)
	metrics := nvcametrics.FromContext(ctx)
	ag.queueManager = NewQueueManager(bc, ag.backendHealthCache, queuesqs.NewClient("http://localhost:4566", ""), queueCreds, featureflag.DefaultFetcher,
		types.MaintenanceModeNone, metrics)

	it, err := bc.GetAllBackendGPUs(ctx)
	assert.NoError(t, err)
	assert.Equal(t, len(it), 3)

	cmsg := function.CreationQueueMessage{
		CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
			RequestID:     "randomRequestID1",
			NCAID:         "randomID",
			Action:        common.FunctionCreationAction,
			InstanceCount: 5,
		},
		Details: function.Details{
			FunctionID:        "functionId",
			FunctionVersionID: "functionVersionId",
		},
		LaunchArtifacts: getArtifactsList(t),
	}

	creds := getSPOTDummyCredsResponse(t).QueueCredentials
	assert.Len(t, creds.CreationQueues, 1)
	if assert.Contains(t, creds.CreationQueues, testGPUNameDefault) {
		createQueue := creds.CreationQueues[testGPUNameDefault]
		assert.NotEmpty(t, createQueue.SecretKey)
		assert.NotEmpty(t, createQueue.SessionToken)
		assert.NotEmpty(t, createQueue.AccessKey)
	}
	assert.NotEmpty(t, creds.TerminationQueue.SecretKey)
	assert.NotEmpty(t, creds.TerminationQueue.SessionToken)
	assert.NotEmpty(t, creds.TerminationQueue.AccessKey)

	sr, err := bc.CreateICMSCreationMessageRequest(ctx, cmsg, "randomMsgReceipt", "randomId1", "")
	assert.NoError(t, err)
	assert.NotNil(t, sr)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	assert.NoError(t, err)

	reqs, err := bc.icmsRequestLister.List(labels.Everything())
	assert.NoError(t, err)

	// ensure LastUpdatedTimestamp
	for _, r := range reqs {
		assert.NotNil(t, r.Status.LastStatusUpdated)
	}

	bc.ForceSync(ctx)

	setMockResponse(m, http.StatusOK, `Acknowledgement accepted`)
	err = ag.PutICMSRequestAcknowledgement(ctx)
	assert.NoError(t, err)

	setMockResponse(m, http.StatusOK, `Request accepted`)
	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	assert.NoError(t, err)

	// transition RequestStatus for test, normally it would be done by Agent
	err = updateRequestToPending(ctx, bc, "randomRequestID1", "randomId1")
	assert.NoError(t, err)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	assert.NoError(t, err)

	verifyPodsCreated := func() bool {
		ps, err := bc.GetAllPodsForRequest(ctx, "randomRequestID1")
		if err != nil {
			t.Logf("failed retrieving pods for request %s, error: %v", "randomRequestID1", err)
		}

		if len(ps) > 0 {
			assert.NotNil(t, ps[0].Spec.TerminationGracePeriodSeconds)
			assert.EqualValues(t, *ps[0].Spec.TerminationGracePeriodSeconds, DefaultTerminationGracePeriodSeconds)
			for c := range ps[0].Spec.InitContainers {
				_, _, err := bc.k8sArtifactHelper.GetErroredPodLogs(ctx, &ps[0], "", MaxBytesForPodLogs)
				assert.NoError(t, err)
				for _, e := range ps[0].Spec.InitContainers[c].Env {
					if e.Name == nvcatypes.InstanceIDEnvKey {
						assert.NotEmpty(t, e.Value)
					}
				}
			}

			for c := range ps[0].Spec.Containers {
				for _, e := range ps[0].Spec.Containers[c].Env {
					if e.Name == nvcatypes.InstanceIDEnvKey {
						assert.NotEmpty(t, e.Value)
					}
				}
			}
		}
		return len(ps) == 5
	}
	assert.Eventually(t, verifyPodsCreated, 60*time.Second, 100*time.Millisecond)

	sRegResp := getSPOTDummyRegistrationResponse(t)

	err = bc.StoreICMSRegistrationResponse(ctx, &sRegResp)
	assert.NoError(t, err)

	res, err := bc.FetchICMSRegistrationResponse(ctx)
	assert.NoError(t, err)
	assert.Equal(t, res.ClusterID, "testClusterID")

	creds = getTestQueueCreds(true)
	err = bc.StoreUpdatedCredentials(ctx, creds)
	assert.NoError(t, err)

	bc.ForceSync(ctx)

	res, err = bc.FetchICMSRegistrationResponse(ctx)
	assert.NoError(t, err)
	assert.Equal(t, res.ClusterID, "testClusterID")
	creds = getTestQueueCreds(true)
	assert.Equal(t, res.Credentials.CreationQueues, creds.CreationQueues)
	assert.Equal(t, res.Credentials.TerminationQueue, creds.TerminationQueue)

	tmsg := types.ICMSTerminationMessage{
		RequestID:   "randomRequestID2",
		NCAId:       "randomID",
		ClusterName: "TestCluster",
		Action:      common.TerminationAction,
		InstanceIds: []string{"0-sr-randomRequestID1", "1-sr-randomRequestID1", "2-sr-randomRequestID1", "3-sr-randomRequestID1", "4-sr-randomRequestID1"},
	}

	err = bc.CreateICMSTerminationMessageRequest(ctx, tmsg, "randomMsgReceipt2", "randomId2")
	assert.NoError(t, err)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	assert.NoError(t, err)
	bc.ForceSync(ctx)

	setMockResponse(m, http.StatusOK, `Acknowledgement accepted`)
	err = ag.PutICMSRequestAcknowledgement(ctx)
	assert.NoError(t, err)

	setMockResponse(m, http.StatusOK, `Request accepted`)
	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	assert.NoError(t, err)

	req, err := bc.clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Get(ctx, "sr-randomRequestID1", metav1.GetOptions{})
	require.NoError(t, err)

	err = ag.PutICMSRequestAcknowledgement(ctx)
	assert.NoError(t, err)

	setMockResponse(m, http.StatusOK, `Request accepted`)
	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	assert.NoError(t, err)

	err = ag.handleInstanceStatusPreconditionFailure(ctx, req, "0-sr-randomRequestID1")
	assert.NoError(t, err)

	// transition RequestStatus for test, normally it would be done by Agent
	err = updateRequestToPending(ctx, bc, "randomRequestID2", "randomId2")
	assert.NoError(t, err)

	reqs, err = bc.icmsRequestLister.List(labels.Everything())
	assert.NoError(t, err)

	// ensure LastUpdatedTimestamp
	for _, r := range reqs {
		assert.NotNil(t, r.Status.LastStatusUpdated)
	}

	bc.ForceSync(ctx)

	verifyTerminationRequest := func() bool {
		err = bc.SyncAllICMSRequests(ctx)
		assert.NoError(t, err)
		bc.ForceSync(ctx)
		reqs, err = bc.icmsRequestLister.List(labels.Everything())
		assert.NoError(t, err)
		for _, r := range reqs {
			assert.NotNil(t, r.Status.LastStatusUpdated)
			up, _ := bc.icmsRequestHelper.GetICMSRequestStatusUpdatesForRequest(ctx, r)
			if r.Spec.Action == common.TerminationAction {
				if len(up) == 5 {
					return true
				}
			}
		}
		return false
	}
	assert.Eventually(t, verifyTerminationRequest, 60*time.Second, 100*time.Millisecond)

	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	assert.NoError(t, err)

	verifyPodsDeleted := func() bool {
		err := bc.SyncAllICMSRequests(ctx)
		assert.NoError(t, err)
		bc.ForceSync(ctx)
		ps, err := bc.GetAllPodsForRequest(ctx, "randomRequestID1")
		if err != nil {
			t.Logf("failed retrieving pods for request %s, error: %v", "randomRequestID1", err)
		}
		return len(ps) == 0
	}
	assert.Eventually(t, verifyPodsDeleted, 10*time.Second, 100*time.Millisecond)

	bc.ForceSync(ctx)

	verifyRequestDeleted := func() bool {
		err := bc.SyncAllICMSRequests(ctx)
		assert.NoError(t, err)
		bc.ForceSync(ctx)
		_, err = bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).
			Get(getICMSRequestObjectMeta(
				types.DeploymentInfo{
					RequestID:      "randomRequestID2",
					MessageID:      "randomId2",
					NCAID:          "ncaID",
					MessageBatchID: "batch1",
					GPUType:        "a100",
				}).Name)
		if err != nil && apierrors.IsNotFound(err) {
			return true
		}
		return false
	}
	assert.Eventually(t, verifyRequestDeleted, 10*time.Second, 100*time.Millisecond)

	for _, r := range reqs {
		assert.NotNil(t, r.Status.LastStatusUpdated)
		if r.Spec.Action == common.FunctionCreationAction {
			continue
		}
		_, err := bc.icmsRequestHelper.GetICMSRequestStatusUpdatesForRequest(ctx, r)
		assert.NoError(t, err)
	}

	setMockResponse(m, http.StatusOK, `Acknowledgement accepted`)
	err = ag.PutICMSRequestAcknowledgement(ctx)
	assert.NoError(t, err)

	setMockResponse(m, http.StatusOK, `Request accepted`)
	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	assert.NoError(t, err)

	err = ag.RenewICMSQueueCreds(ctx)
	assert.NotNil(t, err)
}

func TestPostICMSInstanceRequestStatusUpdates(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	ncaID := "ncaId1"
	gpuType := "A100"
	funcQueueURL := "func_queue_url"
	funcMsgID := "msgId1"
	funcSR := &nvcav2beta1.ICMSRequest{}
	funcSR.Spec = nvcav2beta1.ICMSRequestSpec{
		NCAId:          ncaID,
		MessageReceipt: "rhdl1",
		RequestID:      "reqId1",
		MessageBatchID: "msgBatchId1",
		Action:         common.FunctionCreationAction,
		FunctionDetails: function.Details{
			FunctionID: "funcId1", FunctionVersionID: "funcVerId1",
		},
		CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
			GPUName:  gpuType,
			QueueURL: funcQueueURL,
			CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
				GPUType: gpuType,
			},
		},
	}
	funcSR.ObjectMeta = getICMSRequestObjectMeta(nvcatypes.DeploymentInfo{
		GPUType: gpuType, RequestID: funcSR.Spec.RequestID, NCAID: ncaID,
		FunctionID:        funcSR.Spec.FunctionDetails.FunctionID,
		FunctionVersionID: funcSR.Spec.FunctionDetails.FunctionVersionID,
		MessageID:         funcMsgID, MessageBatchID: funcSR.Spec.MessageBatchID,
	})
	funcSR.Namespace = RequestsNamespace

	taskQueueURL := "task_queue_url"
	taskMsgID := "msgId2"
	taskSR := &nvcav2beta1.ICMSRequest{}
	taskSR.Spec = nvcav2beta1.ICMSRequestSpec{
		NCAId:          ncaID,
		MessageReceipt: "rhdl2",
		RequestID:      "reqId2",
		MessageBatchID: "msgBatchId2",
		Action:         common.TaskCreationAction,
		TaskDetails:    task.Details{TaskID: "taskId1"},
		CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
			QueueURL: taskQueueURL,
			GPUName:  gpuType,
			CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
				GPUType: gpuType,
			},
			TaskLaunchSpecification: &task.LaunchSpecification{
				MaxRuntimeDuration: "PT1H",
				MaxQueuedDuration:  "PT1H",
			},
		},
	}
	taskSR.ObjectMeta = getICMSRequestObjectMeta(nvcatypes.DeploymentInfo{
		GPUType: gpuType, RequestID: taskSR.Spec.RequestID, NCAID: ncaID,
		TaskID:    taskSR.Spec.TaskDetails.TaskID,
		MessageID: taskMsgID, MessageBatchID: taskSR.Spec.MessageBatchID,
	})
	taskSR.Namespace = RequestsNamespace

	instancePod := &corev1.Pod{}
	instancePod.Name, instancePod.Namespace = "instance-pod", RequestsNamespace
	instancePod.Spec.Containers = []corev1.Container{{
		Name: "foo",
	}}
	instancePod.Status = corev1.PodStatus{
		Phase: corev1.PodPending,
		Conditions: []corev1.PodCondition{{
			Type:   corev1.PodScheduled,
			Status: corev1.ConditionFalse,
		}},
	}

	clients := mockKubeClients(instancePod)

	fff := &featureflagmock.Fetcher{
		EnabledFFs: []*featureflag.FeatureFlag{
			featureflag.AutoPurgeDegradedWorkers,
		},
	}

	queueClient := &queuemock.Client{}

	icmsClient, mockICMSClientTransport := setupTestICMSClient()

	srInfFactory := nvcainformers.NewSharedInformerFactoryWithOptions(clients.BART, 10*time.Second)
	icmsReqGenInf, err := srInfFactory.ForResource(nvcav2beta1.SchemeGroupVersion.WithResource("icmsrequests"))
	require.NoError(t, err)
	srLister := nvcav2beta1listers.NewICMSRequestLister(icmsReqGenInf.Informer().GetIndexer())
	srInfFactory.Start(ctx.Done())
	cctx, ccancel := context.WithTimeout(ctx, 10*time.Second)
	syncs := srInfFactory.WaitForCacheSync(cctx.Done())
	require.True(t, syncs[reflect.TypeOf(&nvcav2beta1.ICMSRequest{})])
	ccancel()

	bc := &BackendK8sCache{
		clients:                  clients,
		featureFlagFetcher:       fff,
		icmsRequestLister:        srLister,
		requestsNamespace:        RequestsNamespace,
		podInstanceNamespace:     RequestsNamespace,
		k8sTimeConfig:            (&k8sutil.TimeConfig{}).Complete(),
		eventRecorder:            record.NewFakeRecorder(100),
		autoPurgeDegradedWorkers: true,
	}
	bc.icmsRequestHelper, _ = NewK8sComputeBackend(clients, bc)

	ag := &Agent{
		AgentOptions: &AgentOptions{
			GPUCapacity:             10,
			FeatureFlagFetcher:      fff,
			ClusterTargetingEnabled: true,
			ClusterID:               "xxxx",
		},
		backendk8scache: bc,
		icmsClient:      icmsClient,
		queueManager: &QueueManager{
			Client: queueClient,
			qcreds: nvcatypes.QueueCredentials{
				ClusterCreationQueues: nvcatypes.CreationQueueInfoSet{
					nvcatypes.GPUName(gpuType): queue.MessageQueueInfo{
						GPU:       gpuType,
						QueueURL:  funcQueueURL,
						QueueType: queue.CreationQueue,
					},
				},
				TaskClusterCreationQueues: nvcatypes.CreationQueueInfoSet{
					nvcatypes.GPUName(gpuType): queue.MessageQueueInfo{
						GPU:       gpuType,
						QueueURL:  taskQueueURL,
						QueueType: queue.CreationQueue,
					},
				},
			},
		},
	}

	now := time.Now()
	srClient := clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace)

	setMockResponse(mockICMSClientTransport, http.StatusOK, "Good update")

	_, err = srClient.Create(ctx, funcSR, metav1.CreateOptions{})
	require.NoError(t, err)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		_, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).Get(funcSR.Name)
		assert.NoError(ct, err)
	}, 5*time.Second, 50*time.Millisecond)

	queueClient.AddMessage(funcQueueURL, queue.ReceiveMessageOutput{
		MessageID:     funcMsgID,
		ReceiptHandle: funcSR.Spec.MessageReceipt,
	})

	// Post without instances, should get no updates. Messages should still exist.
	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	require.NoError(t, err)
	ag.instStatusThreadPool.Wait()
	ag.instStatusThreadPool = nil

	gotFuncQueueMessages, err := queueClient.ReceiveMessage(ctx, queue.ReceiveMessageInput{
		QueueInfo:           queue.MessageQueueInfo{GPU: gpuType, QueueURL: funcQueueURL, QueueType: queue.CreationQueue},
		MaxNumberOfMessages: 1,
	})
	require.NoError(t, err)
	assert.Len(t, gotFuncQueueMessages, 1)

	assert.Nil(t, mockICMSClientTransport.getRequest())

	// Set requests to caching with ack without instances, should get no updates. Messages should be deleted.
	funcSR.Status = nvcav2beta1.ICMSRequestStatus{
		RequestStatus:     nvcav2beta1.ICMSRequestStatusCachingInProgress,
		LastStatusUpdated: &metav1.Time{Time: now},
		LastACKTimestamp:  &metav1.Time{Time: now.Add(1 * time.Second)},
	}
	_, err = srClient.UpdateStatus(ctx, funcSR, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		srList, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).List(labels.Everything())
		if assert.NoError(ct, err) && assert.Len(ct, srList, 1) {
			for _, sr := range srList {
				assert.Equal(ct, nvcav2beta1.ICMSRequestStatusCachingInProgress, sr.Status.RequestStatus, sr.Spec.Action)
			}
		}
	}, 2*time.Second, 50*time.Millisecond)

	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	require.NoError(t, err)
	ag.instStatusThreadPool.Wait()
	ag.instStatusThreadPool = nil

	gotFuncQueueMessages, err = queueClient.ReceiveMessage(ctx, queue.ReceiveMessageInput{
		QueueInfo:           queue.MessageQueueInfo{GPU: gpuType, QueueURL: funcQueueURL, QueueType: queue.CreationQueue},
		MaxNumberOfMessages: 1,
	})
	require.NoError(t, err)
	assert.Len(t, gotFuncQueueMessages, 0)

	assert.Nil(t, mockICMSClientTransport.getRequest())

	// Set requests to pending without instances, should get no updates.
	funcSR.Status = nvcav2beta1.ICMSRequestStatus{
		RequestStatus:     nvcav2beta1.ICMSRequestStatusPending,
		LastStatusUpdated: &metav1.Time{Time: now},
		LastACKTimestamp:  &metav1.Time{Time: now.Add(-1 * time.Second)},
	}
	_, err = srClient.UpdateStatus(ctx, funcSR, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		srList, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).List(labels.Everything())
		if assert.NoError(ct, err) && assert.Len(ct, srList, 1) {
			for _, sr := range srList {
				assert.Equal(ct, nvcav2beta1.ICMSRequestStatusPending, sr.Status.RequestStatus, sr.Spec.Action)
			}
		}
	}, 2*time.Second, 50*time.Millisecond)

	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	require.NoError(t, err)
	ag.instStatusThreadPool.Wait()
	ag.instStatusThreadPool = nil

	// Set requests to in progress without instances, should get no updates.
	funcSR.Status = nvcav2beta1.ICMSRequestStatus{
		RequestStatus:     nvcav2beta1.ICMSRequestStatusInProgress,
		LastStatusUpdated: &metav1.Time{Time: now},
		LastACKTimestamp:  &metav1.Time{Time: now.Add(-1 * time.Second)},
	}
	_, err = srClient.UpdateStatus(ctx, funcSR, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		srList, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).List(labels.Everything())
		if assert.NoError(ct, err) && assert.Len(ct, srList, 1) {
			for _, sr := range srList {
				assert.Equal(ct, nvcav2beta1.ICMSRequestStatusInProgress, sr.Status.RequestStatus, sr.Spec.Action)
			}
		}
	}, 2*time.Second, 50*time.Millisecond)

	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	require.NoError(t, err)
	ag.instStatusThreadPool.Wait()
	ag.instStatusThreadPool = nil

	assert.Nil(t, mockICMSClientTransport.getRequest())

	// Set requests to instances in progress with instances, should get updates.
	funcSR.Status = nvcav2beta1.ICMSRequestStatus{
		RequestStatus:     nvcav2beta1.ICMSRequestStatusInstancesInProgress,
		LastStatusUpdated: &metav1.Time{Time: now},
		LastACKTimestamp:  &metav1.Time{Time: now.Add(-1 * time.Second)},
		Instances: map[string]nvcav2beta1.InstanceStatus{
			instancePod.Name: {
				ID:   instancePod.Name,
				Type: nvcav2beta1.InstanceTypePod,
			},
		},
	}
	_, err = srClient.UpdateStatus(ctx, funcSR, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		srList, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).List(labels.Everything())
		if assert.NoError(ct, err) && assert.Len(ct, srList, 1) {
			for _, sr := range srList {
				assert.Equal(ct, nvcav2beta1.ICMSRequestStatusInstancesInProgress, sr.Status.RequestStatus, sr.Spec.Action)
			}
		}
	}, 2*time.Second, 50*time.Millisecond)

	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	require.NoError(t, err)
	ag.instStatusThreadPool.Wait()
	ag.instStatusThreadPool = nil

	assert.NotNil(t, mockICMSClientTransport.getRequest())

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		srList, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).List(labels.Everything())
		if assert.NoError(ct, err) {
			assert.Len(ct, srList, 1)
		}
	}, 2*time.Second, 50*time.Millisecond)

	mockICMSClientTransport.setRequest(nil)
	err = srClient.Delete(ctx, funcSR.Name, metav1.DeleteOptions{})
	require.NoError(t, err)
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		srList, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).List(labels.Everything())
		if assert.NoError(ct, err) {
			assert.Len(ct, srList, 0)
		}
	}, 2*time.Second, 50*time.Millisecond)

	// Task test.

	queueClient.AddMessage(taskQueueURL, queue.ReceiveMessageOutput{
		MessageID:     taskMsgID,
		ReceiptHandle: taskSR.Spec.MessageReceipt,
	})

	_, err = srClient.Create(ctx, taskSR, metav1.CreateOptions{})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		srList, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).List(labels.Everything())
		if assert.NoError(ct, err) {
			assert.Len(ct, srList, 1)
		}
	}, 2*time.Second, 50*time.Millisecond)

	// Post without instances, should get no updates. Messages should still exist.
	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	require.NoError(t, err)
	ag.instStatusThreadPool.Wait()
	ag.instStatusThreadPool = nil

	gotTaskQueueMessages, err := queueClient.ReceiveMessage(ctx, queue.ReceiveMessageInput{
		QueueInfo:           queue.MessageQueueInfo{GPU: gpuType, QueueURL: taskQueueURL, QueueType: queue.CreationQueue},
		MaxNumberOfMessages: 1,
	})
	require.NoError(t, err)
	assert.Len(t, gotTaskQueueMessages, 1)

	assert.Nil(t, mockICMSClientTransport.getRequest())

	// Set requests to caching without ack, should get no updates. Messages should be deleted.
	taskSR.Status = nvcav2beta1.ICMSRequestStatus{
		RequestStatus:     nvcav2beta1.ICMSRequestStatusCachingInProgress,
		LastStatusUpdated: &metav1.Time{Time: now},
	}
	_, err = srClient.UpdateStatus(ctx, taskSR, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		srList, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).List(labels.Everything())
		if assert.NoError(ct, err) && assert.Len(ct, srList, 1) {
			for _, sr := range srList {
				assert.Equal(ct, nvcav2beta1.ICMSRequestStatusCachingInProgress, sr.Status.RequestStatus, sr.Spec.Action)
			}
		}
	}, 2*time.Second, 50*time.Millisecond)

	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	require.NoError(t, err)
	ag.instStatusThreadPool.Wait()
	ag.instStatusThreadPool = nil

	assert.Nil(t, mockICMSClientTransport.getRequest())

	gotTaskQueueMessages, err = queueClient.PeekMessage(ctx, queue.ReceiveMessageInput{
		QueueInfo:           queue.MessageQueueInfo{GPU: gpuType, QueueURL: taskQueueURL, QueueType: queue.CreationQueue},
		MaxNumberOfMessages: 1,
	})
	require.NoError(t, err)
	assert.Len(t, gotTaskQueueMessages, 0)

	// Ack the instance, should result in ack with message deleted.
	queueClient.AddMessage(taskQueueURL, queue.ReceiveMessageOutput{
		MessageID:     taskMsgID,
		ReceiptHandle: taskSR.Spec.MessageReceipt,
	})

	err = ag.PutICMSRequestAcknowledgement(ctx)
	require.NoError(t, err)
	ag.ackThreadPool.Wait()
	ag.ackThreadPool = nil

	assert.NotNil(t, mockICMSClientTransport.getRequest())
	mockICMSClientTransport.setRequest(nil)

	gotTaskQueueMessages, err = queueClient.PeekMessage(ctx, queue.ReceiveMessageInput{
		QueueInfo:           queue.MessageQueueInfo{GPU: gpuType, QueueURL: taskQueueURL, QueueType: queue.CreationQueue},
		MaxNumberOfMessages: 0,
	})
	require.NoError(t, err)
	assert.Len(t, gotTaskQueueMessages, 0)

	// Set requests to pending without instances, should get no updates.
	taskSR.Status = nvcav2beta1.ICMSRequestStatus{
		RequestStatus:     nvcav2beta1.ICMSRequestStatusPending,
		LastStatusUpdated: &metav1.Time{Time: now},
	}
	_, err = srClient.UpdateStatus(ctx, taskSR, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		srList, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).List(labels.Everything())
		if assert.NoError(ct, err) && assert.Len(ct, srList, 1) {
			for _, sr := range srList {
				assert.Equal(ct, nvcav2beta1.ICMSRequestStatusPending, sr.Status.RequestStatus, sr.Spec.Action)
			}
		}
	}, 2*time.Second, 50*time.Millisecond)

	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	require.NoError(t, err)
	ag.instStatusThreadPool.Wait()
	ag.instStatusThreadPool = nil

	// Set requests to in progress without instances, should get no updates.
	taskSR.Status = nvcav2beta1.ICMSRequestStatus{
		RequestStatus:     nvcav2beta1.ICMSRequestStatusInProgress,
		LastStatusUpdated: &metav1.Time{Time: now},
		LastACKTimestamp:  &metav1.Time{Time: now.Add(1 * time.Second)},
	}
	_, err = srClient.UpdateStatus(ctx, taskSR, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		srList, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).List(labels.Everything())
		if assert.NoError(ct, err) && assert.Len(ct, srList, 1) {
			for _, sr := range srList {
				assert.Equal(ct, nvcav2beta1.ICMSRequestStatusInProgress, sr.Status.RequestStatus, sr.Spec.Action)
			}
		}
	}, 2*time.Second, 50*time.Millisecond)

	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	require.NoError(t, err)
	ag.instStatusThreadPool.Wait()
	ag.instStatusThreadPool = nil

	assert.Nil(t, mockICMSClientTransport.getRequest())

	// Set requests to instances in progress with instances, should get updates.
	taskSR.Status = nvcav2beta1.ICMSRequestStatus{
		RequestStatus:     nvcav2beta1.ICMSRequestStatusInstancesInProgress,
		LastStatusUpdated: &metav1.Time{Time: now},
		LastACKTimestamp:  &metav1.Time{Time: now.Add(1 * time.Second)},
		Instances: map[string]nvcav2beta1.InstanceStatus{
			instancePod.Name: {
				ID:   instancePod.Name,
				Type: nvcav2beta1.InstanceTypePod,
			},
		},
	}
	_, err = srClient.UpdateStatus(ctx, taskSR, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		sr, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).Get(taskSR.Name)
		if assert.NoError(ct, err) {
			assert.Equal(ct, taskSR.Status, sr.Status)
		}
	}, 2*time.Second, 50*time.Millisecond)

	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	require.NoError(t, err)
	ag.instStatusThreadPool.Wait()
	ag.instStatusThreadPool = nil

	assert.NotNil(t, mockICMSClientTransport.getRequest())
	mockICMSClientTransport.setRequest(nil)

	// Set requests to instances in progress without ack with instances, should get updates.
	taskSR.Status = nvcav2beta1.ICMSRequestStatus{
		RequestStatus:     nvcav2beta1.ICMSRequestStatusInstancesInProgress,
		LastStatusUpdated: &metav1.Time{Time: now},
		Instances: map[string]nvcav2beta1.InstanceStatus{
			instancePod.Name: {
				ID:   instancePod.Name,
				Type: nvcav2beta1.InstanceTypePod,
			},
		},
	}
	_, err = srClient.UpdateStatus(ctx, taskSR, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		sr, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).Get(taskSR.Name)
		if assert.NoError(ct, err) {
			assert.Equal(ct, taskSR.Status, sr.Status)
		}
	}, 2*time.Second, 50*time.Millisecond)

	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	require.NoError(t, err)
	ag.instStatusThreadPool.Wait()
	ag.instStatusThreadPool = nil

	assert.NotNil(t, mockICMSClientTransport.getRequest())
	mockICMSClientTransport.setRequest(nil)

	// Task with no ack before pods scheduled

	fff.EnabledFFs = append(fff.EnabledFFs, featureflag.AckTaskRequestAfterPodsScheduled)

	// Set requests to caching, should not get updates. Messages should still exist.
	queueClient.AddMessage(taskQueueURL, queue.ReceiveMessageOutput{
		MessageID:     taskMsgID,
		ReceiptHandle: taskSR.Spec.MessageReceipt,
	})

	taskSR.Status = nvcav2beta1.ICMSRequestStatus{
		RequestStatus:     nvcav2beta1.ICMSRequestStatusCachingInProgress,
		LastStatusUpdated: &metav1.Time{Time: now.Add(time.Duration(-1*(1+creationQueueVisibilityTimeoutSeconds)) * time.Second)},
		LastACKTimestamp:  &metav1.Time{Time: now.Add(1 * time.Second)},
		Instances: map[string]nvcav2beta1.InstanceStatus{
			instancePod.Name: {
				ID:   instancePod.Name,
				Type: nvcav2beta1.InstanceTypePod,
			},
		},
	}
	_, err = srClient.UpdateStatus(ctx, taskSR, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		sr, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).Get(taskSR.Name)
		if assert.NoError(ct, err) {
			assert.Equal(ct, taskSR.Status, sr.Status)
		}
	}, 2*time.Second, 50*time.Millisecond)

	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	require.NoError(t, err)
	ag.instStatusThreadPool.Wait()
	ag.instStatusThreadPool = nil

	assert.Nil(t, mockICMSClientTransport.getRequest())

	gotTaskQueueMessages, err = queueClient.ReceiveMessage(ctx, queue.ReceiveMessageInput{
		QueueInfo:           queue.MessageQueueInfo{GPU: gpuType, QueueURL: taskQueueURL, QueueType: queue.CreationQueue},
		MaxNumberOfMessages: 1,
	})
	require.NoError(t, err)
	assert.Len(t, gotTaskQueueMessages, 1)

	// Ack the instance, should result in ack with message timeout vis extended.
	err = ag.PutICMSRequestAcknowledgement(ctx)
	require.NoError(t, err)
	ag.ackThreadPool.Wait()
	ag.ackThreadPool = nil

	assert.NotNil(t, mockICMSClientTransport.getRequest())
	mockICMSClientTransport.setRequest(nil)

	gotTaskQueueMessages, err = queueClient.ReceiveMessage(ctx, queue.ReceiveMessageInput{
		QueueInfo:           queue.MessageQueueInfo{GPU: gpuType, QueueURL: taskQueueURL, QueueType: queue.CreationQueue},
		MaxNumberOfMessages: 1,
	})
	require.NoError(t, err)
	assert.Len(t, gotTaskQueueMessages, 0)
	gotTaskQueueMessages, err = queueClient.PeekMessage(ctx, queue.ReceiveMessageInput{
		QueueInfo:           queue.MessageQueueInfo{GPU: gpuType, QueueURL: taskQueueURL, QueueType: queue.CreationQueue},
		MaxNumberOfMessages: 1,
	})
	require.NoError(t, err)
	assert.Len(t, gotTaskQueueMessages, 1)

	// Set requests to instances in progress without ack with instances, should not get updates.
	// Messages should still exist.
	taskSR.Status = nvcav2beta1.ICMSRequestStatus{
		RequestStatus:     nvcav2beta1.ICMSRequestStatusInstancesInProgress,
		LastStatusUpdated: &metav1.Time{Time: now},
		Instances: map[string]nvcav2beta1.InstanceStatus{
			instancePod.Name: {
				ID:   instancePod.Name,
				Type: nvcav2beta1.InstanceTypePod,
			},
		},
	}
	_, err = srClient.UpdateStatus(ctx, taskSR, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		sr, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).Get(taskSR.Name)
		if assert.NoError(ct, err) {
			assert.Equal(ct, taskSR.Status, sr.Status)
		}
	}, 2*time.Second, 50*time.Millisecond)

	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	require.NoError(t, err)
	ag.instStatusThreadPool.Wait()
	ag.instStatusThreadPool = nil

	assert.Nil(t, mockICMSClientTransport.getRequest())

	gotTaskQueueMessages, err = queueClient.PeekMessage(ctx, queue.ReceiveMessageInput{
		QueueInfo:           queue.MessageQueueInfo{GPU: gpuType, QueueURL: taskQueueURL, QueueType: queue.CreationQueue},
		MaxNumberOfMessages: 1,
	})
	require.NoError(t, err)
	assert.Len(t, gotTaskQueueMessages, 1)

	// Ack the instance, should result in ack with message deleted.
	err = ag.PutICMSRequestAcknowledgement(ctx)
	require.NoError(t, err)
	ag.ackThreadPool.Wait()
	ag.ackThreadPool = nil

	assert.NotNil(t, mockICMSClientTransport.getRequest())
	mockICMSClientTransport.setRequest(nil)

	gotTaskQueueMessages, err = queueClient.PeekMessage(ctx, queue.ReceiveMessageInput{
		QueueInfo:           queue.MessageQueueInfo{GPU: gpuType, QueueURL: taskQueueURL, QueueType: queue.CreationQueue},
		MaxNumberOfMessages: 0,
	})
	require.NoError(t, err)
	assert.Len(t, gotTaskQueueMessages, 0)

	// Schedule pod.
	instancePod.Status = corev1.PodStatus{
		Phase: corev1.PodRunning,
	}
	_, err = clients.K8s.CoreV1().Pods(bc.podInstanceNamespace).UpdateStatus(ctx, instancePod, metav1.UpdateOptions{})
	require.NoError(t, err)

	// Set requests to completed, should get updates.
	taskSR.Status = nvcav2beta1.ICMSRequestStatus{
		RequestStatus:     nvcav2beta1.ICMSRequestStatusCompleted,
		LastStatusUpdated: &metav1.Time{Time: now},
		Instances: map[string]nvcav2beta1.InstanceStatus{
			instancePod.Name: {
				ID:   instancePod.Name,
				Type: nvcav2beta1.InstanceTypePod,
			},
		},
	}
	_, err = srClient.UpdateStatus(ctx, taskSR, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		sr, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).Get(taskSR.Name)
		if assert.NoError(ct, err) {
			assert.Equal(ct, taskSR.Status, sr.Status)
		}
	}, 2*time.Second, 50*time.Millisecond)

	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	require.NoError(t, err)
	ag.instStatusThreadPool.Wait()
	ag.instStatusThreadPool = nil

	assert.NotNil(t, mockICMSClientTransport.getRequest())
	mockICMSClientTransport.setRequest(nil)

	// Set requests to instances in progress without ack with instances, should not see updates.
	taskSR.Status = nvcav2beta1.ICMSRequestStatus{
		RequestStatus:     nvcav2beta1.ICMSRequestStatusInstancesInProgress,
		LastStatusUpdated: &metav1.Time{Time: now},
		Instances: map[string]nvcav2beta1.InstanceStatus{
			instancePod.Name: {
				ID:   instancePod.Name,
				Type: nvcav2beta1.InstanceTypePod,
			},
		},
	}
	_, err = srClient.UpdateStatus(ctx, taskSR, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		sr, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).Get(taskSR.Name)
		if assert.NoError(ct, err) {
			assert.Equal(ct, taskSR.Status, sr.Status)
		}
	}, 2*time.Second, 50*time.Millisecond)

	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	require.NoError(t, err)
	ag.instStatusThreadPool.Wait()
	ag.instStatusThreadPool = nil

	assert.Nil(t, mockICMSClientTransport.getRequest())

	// Set requests to instances in progress with ack with instances, should see updates.
	taskSR.Status = nvcav2beta1.ICMSRequestStatus{
		RequestStatus:     nvcav2beta1.ICMSRequestStatusInstancesInProgress,
		LastStatusUpdated: &metav1.Time{Time: now},
		LastACKTimestamp:  &metav1.Time{Time: now},
		Instances: map[string]nvcav2beta1.InstanceStatus{
			instancePod.Name: {
				ID:   instancePod.Name,
				Type: nvcav2beta1.InstanceTypePod,
			},
		},
	}
	_, err = srClient.UpdateStatus(ctx, taskSR, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		sr, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).Get(taskSR.Name)
		if assert.NoError(ct, err) {
			assert.Equal(ct, taskSR.Status, sr.Status)
		}
	}, 2*time.Second, 50*time.Millisecond)

	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	require.NoError(t, err)
	ag.instStatusThreadPool.Wait()
	ag.instStatusThreadPool = nil

	assert.NotNil(t, mockICMSClientTransport.getRequest())
	mockICMSClientTransport.setRequest(nil)

	// Pod is failed, should see updates without ack.
	instancePod.Status = corev1.PodStatus{
		Phase: corev1.PodFailed,
	}
	_, err = clients.K8s.CoreV1().Pods(bc.podInstanceNamespace).UpdateStatus(ctx, instancePod, metav1.UpdateOptions{})
	require.NoError(t, err)

	taskSR.Status = nvcav2beta1.ICMSRequestStatus{
		RequestStatus:     nvcav2beta1.ICMSRequestStatusInstancesInProgress,
		LastStatusUpdated: &metav1.Time{Time: now},
		Instances: map[string]nvcav2beta1.InstanceStatus{
			instancePod.Name: {
				ID:   instancePod.Name,
				Type: nvcav2beta1.InstanceTypePod,
			},
		},
	}
	_, err = srClient.UpdateStatus(ctx, taskSR, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		sr, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).Get(taskSR.Name)
		if assert.NoError(ct, err) {
			assert.Equal(ct, taskSR.Status, sr.Status)
		}
	}, 2*time.Second, 50*time.Millisecond)

	err = ag.PostICMSInstanceRequestStatusUpdates(ctx)
	require.NoError(t, err)
	ag.instStatusThreadPool.Wait()
	ag.instStatusThreadPool = nil

	assert.NotNil(t, mockICMSClientTransport.getRequest())
	mockICMSClientTransport.setRequest(nil)
}

func TestBackendK8sCache(t *testing.T) {
	ctx := context.Background()
	instanceID := "test-instance"
	periodicInterval := 60 * time.Second
	now := time.Now()

	tests := []struct {
		name                  string
		request               *nvcav2beta1.ICMSRequest
		currentStatus         string
		lastReportedStatus    string
		lastReportedTimestamp *metav1.Time
		periodicEnabled       bool
		expectedResult        bool
		description           string
	}{
		{
			name: "status changed - should report",
			request: &nvcav2beta1.ICMSRequest{Status: nvcav2beta1.ICMSRequestStatus{
				RequestStatus: nvcav2beta1.ICMSRequestStatusPending,
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p1": {
						Status:             string(types.ICMSInstanceStarted),
						LastReportedStatus: string(types.ICMSInstanceStateNoStatus),
					},
				},
			}},
			currentStatus:         string(types.ICMSInstanceStarted),
			lastReportedStatus:    string(types.ICMSInstanceStateNoStatus),
			lastReportedTimestamp: &metav1.Time{Time: now},
			periodicEnabled:       false,
			expectedResult:        true,
			description:           "Should report when status changes regardless of other conditions",
		},
		{
			name: "no timestamp - should report",
			request: &nvcav2beta1.ICMSRequest{Status: nvcav2beta1.ICMSRequestStatus{
				RequestStatus: nvcav2beta1.ICMSRequestStatusPending,
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p1": {
						Status:             string(types.ICMSInstanceStarted),
						LastReportedStatus: string(types.ICMSInstanceStarted),
					},
				},
			}},
			currentStatus:         string(types.ICMSInstanceStarted),
			lastReportedStatus:    string(types.ICMSInstanceStarted),
			lastReportedTimestamp: nil,
			periodicEnabled:       false,
			expectedResult:        true,
			description:           "Should report when no timestamp exists regardless of other conditions",
		},
		{
			name: "failure acknowledged and terminated - skip report",
			request: &nvcav2beta1.ICMSRequest{Status: nvcav2beta1.ICMSRequestStatus{
				RequestStatus: nvcav2beta1.ICMSRequestStatusFailureAcknowledged,
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p1": {
						Status:             string(types.ICMSInstanceStarted),
						LastReportedStatus: string(types.ICMSInstanceStarted),
					},
				},
			}},
			currentStatus:         string(types.ICMSInstanceTerminated),
			lastReportedStatus:    string(types.ICMSInstanceTerminated),
			lastReportedTimestamp: &metav1.Time{Time: now},
			periodicEnabled:       true,
			expectedResult:        false,
			description:           "Should not report when request is failure acknowledged and instance is terminated",
		},
		{
			name: "periodic disabled - same status - skip report",
			request: &nvcav2beta1.ICMSRequest{Status: nvcav2beta1.ICMSRequestStatus{
				RequestStatus: nvcav2beta1.ICMSRequestStatusPending,
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p1": {
						Status:             string(types.ICMSInstanceStarted),
						LastReportedStatus: string(types.ICMSInstanceStarted),
					},
				},
			}},
			currentStatus:         string(types.ICMSInstanceStarted),
			lastReportedStatus:    string(types.ICMSInstanceStarted),
			lastReportedTimestamp: &metav1.Time{Time: now},
			periodicEnabled:       false,
			expectedResult:        false,
			description:           "Should not report when periodic updates disabled and status hasn't changed",
		},
		{
			name: "periodic enabled - interval not elapsed - skip report",
			request: &nvcav2beta1.ICMSRequest{Status: nvcav2beta1.ICMSRequestStatus{
				RequestStatus: nvcav2beta1.ICMSRequestStatusPending,
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p1": {
						Status:             string(types.ICMSInstanceStarted),
						LastReportedStatus: string(types.ICMSInstanceStarted),
					},
				},
			}},
			currentStatus:         string(types.ICMSInstanceStarted),
			lastReportedStatus:    string(types.ICMSInstanceStarted),
			lastReportedTimestamp: &metav1.Time{Time: now.Add(-30 * time.Second)},
			periodicEnabled:       true,
			expectedResult:        false,
			description:           "Should not report when periodic updates enabled but interval hasn't elapsed",
		},
		{
			name: "periodic enabled - interval elapsed - should report",
			request: &nvcav2beta1.ICMSRequest{Status: nvcav2beta1.ICMSRequestStatus{
				RequestStatus: nvcav2beta1.ICMSRequestStatusPending,
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p1": {
						Status:             string(types.ICMSInstanceStarted),
						LastReportedStatus: string(types.ICMSInstanceStarted),
					},
				},
			}},
			currentStatus:         string(types.ICMSInstanceStarted),
			lastReportedStatus:    string(types.ICMSInstanceStarted),
			lastReportedTimestamp: &metav1.Time{Time: now.Add(-2 * periodicInterval)},
			periodicEnabled:       true,
			expectedResult:        true,
			description:           "Should report when periodic updates enabled and interval has elapsed",
		},
		{
			name: "failure acknowledged but not terminated - should report",
			request: &nvcav2beta1.ICMSRequest{Status: nvcav2beta1.ICMSRequestStatus{
				RequestStatus: nvcav2beta1.ICMSRequestStatusFailureAcknowledged,
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"p1": {
						Status:             string(types.ICMSInstanceStarted),
						LastReportedStatus: string(types.ICMSInstanceStarted),
					},
				},
			}},
			currentStatus:         string(types.ICMSInstanceStarted),
			lastReportedStatus:    string(types.ICMSInstanceStarted),
			lastReportedTimestamp: &metav1.Time{Time: now.Add(-2 * periodicInterval)},
			periodicEnabled:       true,
			expectedResult:        true,
			description:           "Should report when failure acknowledged but instance not terminated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := &BackendK8sCache{
				periodicInstanceStatusUpdateInterval: periodicInterval,
				enablePeriodicInstanceStatusUpdate:   tt.periodicEnabled,
			}

			cache.icmsRequestHelper, _ = NewK8sComputeBackend(mockKubeClients(), cache)

			result := cache.shouldReportInstanceStatusHeartbeat(
				ctx,
				tt.request,
				instanceID,
				tt.currentStatus,
				tt.lastReportedStatus,
				tt.lastReportedTimestamp,
			)

			assert.Equal(t, tt.expectedResult, result, tt.description)
		})
	}
}

func Test_getICMSRequestIDLabelSelector(t *testing.T) {
	ctx := context.Background()
	lblSelector, err := getICMSRequestIDLabelSelector(ctx, "foo-bar", "foo-bar-batch-id")
	assert.NoError(t, err)
	assert.NotNil(t, lblSelector)

	inputLabels := labels.Set{
		nvcatypes.ICMSRequestIDKey: "foo-bar",
	}
	assert.False(t, lblSelector.Matches(inputLabels))

	inputLabels[nvcatypes.MessageBatchIDKey] = "foo-bar-batch-id"
	assert.True(t, lblSelector.Matches(inputLabels))

	inputLabels[nvcatypes.ICMSRequestIDKey] = "foo-bar-2"
	assert.False(t, lblSelector.Matches(inputLabels))
}

func TestNodeUpdates(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)
	var err error

	node1 := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-1",
			Labels: map[string]string{
				nodefeatures.UniformInstanceTypeLabelKey: "ON-PREM.GPU.A100",
				"nvidia.com/gpu.present":                 "true",
				"nvidia.com/gpu.family":                  "ampere",
				"nvidia.com/gpu.machine":                 "Google-Compute-Engine",
				"nvidia.com/gpu.memory":                  "40960",
				"nvidia.com/gpu.product":                 "A100-SXM4-40GB",
			},
		},
		Status: v1.NodeStatus{
			Conditions: []v1.NodeCondition{{
				Type:   v1.NodeReady,
				Status: v1.ConditionTrue,
			}},
			Capacity: v1.ResourceList{
				v1.ResourceCPU:              resource.MustParse("6000m"),
				v1.ResourceMemory:           resource.MustParse("32Gi"),
				nodefeatures.GPUResourceKey: resource.MustParse("1"),
			},
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:              resource.MustParse("6000m"),
				v1.ResourceMemory:           resource.MustParse("32Gi"),
				nodefeatures.GPUResourceKey: resource.MustParse("1"),
			},
		},
	}

	clients := mockKubeClientsDynamicGPUs(node1)
	inf := informers.NewSharedInformerFactoryWithOptions(
		clients.K8s,
		ResyncInterval,
	)
	ni := inf.Core().V1().Nodes()
	nodeInf := ni.Informer()
	nodeLister := ni.Lister()

	nodeIface := clients.K8s.CoreV1().Nodes()

	c := &BackendK8sCache{
		nodeUpdateWQ: workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
	}
	t.Cleanup(func() {
		c.nodeUpdateWQ.ShutDown()
	})

	_, err = addNodeEventHandler(ctx, c, nodeInf)
	require.NoError(t, err)

	nodeUpdater := nodefeatures.NewNodeUpdater(nodeIface, "ON-PREM")
	go func() {
		for c.processNodeWork(ctx, nodeIface, nodeUpdater) {
		}
	}()

	inf.Start(ctx.Done())

	// Node 1 already has the required label, should still be there.
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		node, err := nodeLister.Get(node1.Name)
		if assert.NoError(ct, err) && assert.NotNil(ct, node.Labels) {
			assert.Equal(ct, "ON-PREM.GPU.A100", node.Labels[nodefeatures.UniformInstanceTypeLabelKey])
		}
	}, 2*time.Second, 50*time.Millisecond)

	// New node, no instance label.
	node2 := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-2",
			Labels: map[string]string{
				"nvidia.com/gpu.present": "true",
				"nvidia.com/gpu.family":  "ampere",
				"nvidia.com/gpu.machine": "Google-Compute-Engine",
				"nvidia.com/gpu.memory":  "40960",
				"nvidia.com/gpu.product": "A100-SXM4-40GB",
			},
		},
		Status: v1.NodeStatus{
			Conditions: []v1.NodeCondition{{
				Type:   v1.NodeReady,
				Status: v1.ConditionTrue,
			}},
			Capacity: v1.ResourceList{
				v1.ResourceCPU:              resource.MustParse("6000m"),
				v1.ResourceMemory:           resource.MustParse("32Gi"),
				nodefeatures.GPUResourceKey: resource.MustParse("1"),
			},
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:              resource.MustParse("6000m"),
				v1.ResourceMemory:           resource.MustParse("32Gi"),
				nodefeatures.GPUResourceKey: resource.MustParse("1"),
			},
		},
	}
	_, err = nodeIface.Create(ctx, node2, metav1.CreateOptions{})
	require.NoError(t, err)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		node, err := nodeLister.Get(node2.Name)
		assert.NoError(ct, err)
		if assert.NoError(ct, err) && assert.NotNil(ct, node.Labels) {
			assert.Equal(ct, "ON-PREM.GPU.A100", node.Labels[nodefeatures.UniformInstanceTypeLabelKey])
		}
	}, 2*time.Second, 50*time.Millisecond)

	// Cordon node 2, label should be removed.
	node2.Spec.Unschedulable = true
	_, err = nodeIface.Update(ctx, node2, metav1.UpdateOptions{})
	require.NoError(t, err)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		node, err := nodeLister.Get(node2.Name)
		assert.NoError(ct, err)
		assert.NotContains(ct, node.Labels, nodefeatures.UniformInstanceTypeLabelKey)
	}, 2*time.Second, 50*time.Millisecond)

	// Uncordon node 2, label should be added.
	node2.Spec.Unschedulable = false
	_, err = nodeIface.Update(ctx, node2, metav1.UpdateOptions{})
	require.NoError(t, err)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		node, err := nodeLister.Get(node2.Name)
		assert.NoError(ct, err)
		if assert.NoError(ct, err) && assert.NotNil(ct, node.Labels) {
			assert.Equal(ct, "ON-PREM.GPU.A100", node.Labels[nodefeatures.UniformInstanceTypeLabelKey])
		}
	}, 2*time.Second, 50*time.Millisecond)
}

func TestReconcileSpanRequestStatus(t *testing.T) {
	ctx := context.Background()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	t.Cleanup(func() { tp.Shutdown(ctx) })
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.Baggage{}, propagation.TraceContext{}))
	tracer := tp.Tracer("default")

	tests := []struct {
		name            string
		prevReqStatus   nvcav2beta1.RequestStatus
		newReqStatus    nvcav2beta1.RequestStatus
		lastUpdatedTime metav1.Time
		sr              *nvcav2beta1.ICMSRequest
		srClient        *mockSRClient
		wantResult      bool
	}{
		{
			name:            "no state change",
			prevReqStatus:   nvcav2beta1.ICMSRequestStatusPending,
			newReqStatus:    nvcav2beta1.ICMSRequestStatusPending,
			lastUpdatedTime: metav1.Now(),
			sr:              &nvcav2beta1.ICMSRequest{},
			srClient:        &mockSRClient{},
			wantResult:      true,
		},
		{
			name:            "state change from empty to Pending",
			prevReqStatus:   "",
			newReqStatus:    nvcav2beta1.ICMSRequestStatusPending,
			lastUpdatedTime: metav1.Now(),
			sr: &nvcav2beta1.ICMSRequest{
				Status: nvcav2beta1.ICMSRequestStatus{
					RequestStatusTraceContexts: map[nvcav2beta1.RequestStatus]nvcav2beta1.ICMSRequestSpanContextConfig{},
				},
			},

			srClient:   &mockSRClient{},
			wantResult: true,
		},
		{
			name:            "state change from Pending to Running no previous stored span",
			prevReqStatus:   nvcav2beta1.ICMSRequestStatusPending,
			newReqStatus:    nvcav2beta1.ICMSRequestStatusInProgress,
			lastUpdatedTime: metav1.Now(),
			sr: &nvcav2beta1.ICMSRequest{
				Status: nvcav2beta1.ICMSRequestStatus{
					RequestStatusTraceContexts: map[nvcav2beta1.RequestStatus]nvcav2beta1.ICMSRequestSpanContextConfig{},
				},
			},
			srClient:   &mockSRClient{},
			wantResult: true,
		},
		{
			name:            "state change from Pending to Running",
			prevReqStatus:   nvcav2beta1.ICMSRequestStatusPending,
			newReqStatus:    nvcav2beta1.ICMSRequestStatusInProgress,
			lastUpdatedTime: metav1.Now(),
			sr: &nvcav2beta1.ICMSRequest{
				Status: nvcav2beta1.ICMSRequestStatus{
					RequestStatusTraceContexts: map[nvcav2beta1.RequestStatus]nvcav2beta1.ICMSRequestSpanContextConfig{
						nvcav2beta1.ICMSRequestStatusPending: {
							SpanName:  fmt.Sprintf("nvca.SPOTRequest.%s", nvcav2beta1.ICMSRequestStatusPending),
							StartTime: metav1.Time{Time: time.Now().Add(-5 * time.Minute)},
						},
					},
				},
			},
			srClient:   &mockSRClient{},
			wantResult: true,
		},
		{
			name:            "state change from Running to Completed",
			prevReqStatus:   nvcav2beta1.ICMSRequestStatusInProgress,
			newReqStatus:    nvcav2beta1.ICMSRequestStatusCompleted,
			lastUpdatedTime: metav1.Now(),
			sr:              &nvcav2beta1.ICMSRequest{},
			srClient:        &mockSRClient{},
			wantResult:      true,
		},
		{
			name:            "error updating SR status",
			prevReqStatus:   nvcav2beta1.ICMSRequestStatusInProgress,
			newReqStatus:    nvcav2beta1.ICMSRequestStatusCompleted,
			lastUpdatedTime: metav1.Now(),
			sr:              &nvcav2beta1.ICMSRequest{},
			srClient:        &mockSRClient{err: errors.New("test-error")},
			wantResult:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(func() {
				exp.Reset()
			})
			ctx := context.Background()
			result := reconcileSpanRequestStatus(ctx, tt.prevReqStatus, tt.newReqStatus, tt.lastUpdatedTime, tt.sr, tt.srClient, tracer)
			assert.Equal(t, tt.wantResult, result)
		})
	}
}

func TestSecretInformerSetup(t *testing.T) {
	ctx := newTestContext()

	// Create test namespaces
	sourceNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-source-ns",
		},
	}
	targetNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-target-ns",
			Labels: map[string]string{
				"kubernetes.io/metadata.name":       "test-target-ns",
				nvcatypes.WorkloadInstanceTypeLabel: "test",
			},
		},
	}

	// Define namespace labels for filtering
	namespaceLabels := labels.Set{
		nvcatypes.WorkloadInstanceTypeLabel: "test",
	}

	// Create test secret
	sourceSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: sourceNS.Name,
			Labels: map[string]string{
				"mirror": "true",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"key1": []byte("value1"),
			"key2": []byte("value2"),
		},
	}

	// Create fake clientset with test objects
	client := fakek8sclient.NewSimpleClientset(
		sourceNS,
		targetNS,
		sourceSecret,
		k8smock.NewNetworkPolicyConfigMap("nvca-system"),
	)

	// Create informer factory
	factory := informers.NewSharedInformerFactory(client, 5*time.Second)

	// Get informers
	nsInformer := factory.Core().V1().Namespaces()

	// Start informers
	stopCh := make(chan struct{})
	factory.Start(stopCh)
	factory.WaitForCacheSync(stopCh)

	// Add test data to informers
	nsInformer.Informer().GetStore().Add(sourceNS)
	nsInformer.Informer().GetStore().Add(targetNS)

	// Clean up after test
	t.Cleanup(func() {
		close(stopCh)
	})

	// Create test cache
	cache := &BackendK8sCache{
		clients:                     &kubeclients.KubeClients{K8s: client},
		systemNamespace:             "nvca-system",
		secretMirrorSourceNamespace: sourceNS.Name,
		secretMirrorLabelSelector:   "mirror=true",
		namespaceLabels:             namespaceLabels,
		instanceNamespaceLister:     nsInformer.Lister(),
	}

	// Call mirrorSecret directly
	err := cache.mirrorSecret(ctx, sourceSecret)
	require.NoError(t, err)

	// Verify secret was mirrored to target namespace
	mirroredSecret, err := client.CoreV1().Secrets(targetNS.Name).Get(ctx, sourceSecret.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, sourceSecret.Name, mirroredSecret.Name)
	assert.Equal(t, sourceSecret.Type, mirroredSecret.Type)
	assert.Equal(t, sourceSecret.Data, mirroredSecret.Data)
	assert.Equal(t, sourceNS.Name, mirroredSecret.Labels[SecretMirroredFromLabelKey])
}

func TestStartSecretMirroringInformer(t *testing.T) {
	ctx, cancel := context.WithTimeout(newTestContext(), 5*time.Second)
	defer cancel()

	tests := []struct {
		name                        string
		secretMirrorLabelSelector   string
		secretMirrorSourceNamespace string
		expectError                 bool
		errorContains               string
	}{
		{
			name:                        "valid label selector",
			secretMirrorLabelSelector:   "mirror=true",
			secretMirrorSourceNamespace: "test-source-ns",
			expectError:                 false,
		},
		{
			name:                        "invalid label selector - bad operator",
			secretMirrorLabelSelector:   "key===value",
			secretMirrorSourceNamespace: "test-source-ns",
			expectError:                 true,
			errorContains:               "invalid secret mirror label selector",
		},
		{
			name:                        "complex label selector",
			secretMirrorLabelSelector:   "mirror=true,env in (prod,staging)",
			secretMirrorSourceNamespace: "test-source-ns",
			expectError:                 false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test namespace
			sourceNS := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: tt.secretMirrorSourceNamespace,
				},
			}

			// Create fake clientset
			client := fakek8sclient.NewSimpleClientset(
				sourceNS,
				k8smock.NewNetworkPolicyConfigMap("nvca-system"),
			)

			// Create test cache
			cache := &BackendK8sCache{
				clients:                     &kubeclients.KubeClients{K8s: client},
				systemNamespace:             "nvca-system",
				secretMirrorSourceNamespace: tt.secretMirrorSourceNamespace,
				secretMirrorLabelSelector:   tt.secretMirrorLabelSelector,
				resyncPeriod:                5 * time.Second,
				syncedFuncs:                 []cache.InformerSynced{},
			}

			// Call startSecretMirroringInformer
			err := cache.startSecretMirroringInformer(ctx)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
				// Verify informer was set up correctly
				assert.NotNil(t, cache.secretLister, "secretLister should be initialized")
				assert.NotNil(t, cache.secretNamespaceLister, "secretNamespaceLister should be initialized")
				assert.Greater(t, len(cache.syncedFuncs), 0, "syncedFuncs should have at least one entry")
			}
		})
	}
}

func TestCustomAnnotationsInConfigMapInformer(t *testing.T) {
	ctx, cancel := context.WithTimeout(newTestContext(), 5*time.Second)
	defer cancel()

	// Create the custom annotations ConfigMap with proper format
	// The annotations should be in YAML format under the "annotations" key
	annotationsYAML := `custom-key-1: custom-value-1
custom-key-2: custom-value-2`

	customAnnotationsCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      k8sutil.CustomAnnotationsConfigMapName,
			Namespace: k8sutil.NVCASystemNamespace,
		},
		Data: map[string]string{
			k8sutil.CustomAnnotationsMapKey: annotationsYAML,
		},
	}

	// Create fake clientset with the ConfigMap
	client := fakek8sclient.NewSimpleClientset(
		customAnnotationsCM,
	)

	// Create namespace informer for instanceNamespaceLister (required by ConfigMap informer)
	nsInformerFactory := k8sinformers.NewSharedInformerFactoryWithOptions(
		client,
		5*time.Second,
	)
	nsInformer := nsInformerFactory.Core().V1().Namespaces()

	// Create test cache
	cache := &BackendK8sCache{
		clients:                 &kubeclients.KubeClients{K8s: client},
		systemNamespace:         "nvca-system",
		resyncPeriod:            5 * time.Second,
		syncedFuncs:             []cache.InformerSynced{},
		customAnnotations:       &sync.Map{},
		instanceNamespaceLister: nsInformer.Lister(),
	}
	cache.customAnnotations.Store("annotations", map[string]string{})

	// Start namespace informer
	nsInformerFactory.Start(ctx.Done())

	// Call addConfigMapInformers which now handles custom annotations
	err := addConfigMapInformers(ctx, cache)
	require.NoError(t, err)

	// Verify informer was set up correctly
	assert.Greater(t, len(cache.syncedFuncs), 0, "syncedFuncs should have at least one entry")

	// Wait for informer to sync
	require.Eventually(t, func() bool {
		for _, synced := range cache.syncedFuncs {
			if !synced() {
				return false
			}
		}
		return true
	}, 2*time.Second, 50*time.Millisecond, "informer should sync")

	// Wait a bit more for event handlers to process
	time.Sleep(200 * time.Millisecond)

	// Verify the custom annotations were cached
	cachedAnnotations, ok := cache.customAnnotations.Load("annotations")
	require.True(t, ok, "annotations key should exist in sync.Map")
	require.NotNil(t, cachedAnnotations, "cached annotations should not be nil")

	annotations, ok := cachedAnnotations.(map[string]string)
	require.True(t, ok, "cached annotations should be a map[string]string")
	assert.Equal(t, "custom-value-1", annotations["custom-key-1"])
	assert.Equal(t, "custom-value-2", annotations["custom-key-2"])
}

func TestInitCustomAnnotationsCache(t *testing.T) {
	cache := &BackendK8sCache{}

	// Call initCustomAnnotationsCache
	cache.initCustomAnnotationsCache()

	// Verify customAnnotations was initialized
	require.NotNil(t, cache.customAnnotations, "customAnnotations should be initialized")

	// Get cached annotations - should be empty map since we only initialize
	cachedAnnotations, ok := cache.customAnnotations.Load("annotations")
	require.True(t, ok, "annotations key should exist in sync.Map")
	require.NotNil(t, cachedAnnotations, "cached annotations should not be nil")

	annotations, ok := cachedAnnotations.(map[string]string)
	require.True(t, ok, "cached annotations should be a map[string]string")

	// Should be empty since initCustomAnnotationsCache only initializes the cache
	assert.Empty(t, annotations, "annotations should be empty after init")
}

func TestSecretMirroring(t *testing.T) {
	ctx := newTestContext()

	// Create test namespaces
	sourceNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-source-ns",
		},
	}
	targetNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-target-ns",
			Labels: map[string]string{
				"kubernetes.io/metadata.name":       "test-target-ns",
				nvcatypes.WorkloadInstanceTypeLabel: "test",
			},
		},
	}

	// Create test secrets
	sourceSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: sourceNS.Name,
			Labels: map[string]string{
				"mirror": "true",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"key1": []byte("value1"),
			"key2": []byte("value2"),
		},
	}

	// Create fake clientset
	client := fakek8sclient.NewSimpleClientset(
		sourceNS,
		targetNS,
		sourceSecret,
		k8smock.NewNetworkPolicyConfigMap("nvca-system"),
	)

	// Create informer factory
	factory := informers.NewSharedInformerFactory(client, 5*time.Second)

	// Get informers
	nsInformer := factory.Core().V1().Namespaces()
	secretInformer := factory.Core().V1().Secrets()

	// Start informers
	stopCh := make(chan struct{})
	factory.Start(stopCh)
	factory.WaitForCacheSync(stopCh)

	// Add test data to informers
	nsInformer.Informer().GetStore().Add(sourceNS)
	nsInformer.Informer().GetStore().Add(targetNS)
	secretInformer.Informer().GetStore().Add(sourceSecret)

	// Clean up after test
	t.Cleanup(func() {
		close(stopCh)
	})

	// Create test cache
	cache := &BackendK8sCache{
		clients:                     &kubeclients.KubeClients{K8s: client},
		systemNamespace:             "nvca-system",
		secretMirrorSourceNamespace: sourceNS.Name,
		secretMirrorLabelSelector:   "mirror=true",
		namespaceLabels:             labels.Set{nvcatypes.WorkloadInstanceTypeLabel: "test"},
		instanceNamespaceLister:     nsInformer.Lister(),
	}

	// Call mirrorSecret directly
	err := cache.mirrorSecret(ctx, sourceSecret)
	require.NoError(t, err)

	// Verify secret was mirrored to target namespace
	mirroredSecret, err := client.CoreV1().Secrets(targetNS.Name).Get(ctx, sourceSecret.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, sourceSecret.Name, mirroredSecret.Name)
	assert.Equal(t, sourceSecret.Type, mirroredSecret.Type)
	assert.Equal(t, sourceSecret.Data, mirroredSecret.Data)
	assert.Equal(t, sourceNS.Name, mirroredSecret.Labels[SecretMirroredFromLabelKey])

	// Test with non-matching label selector
	cache.secretMirrorLabelSelector = "mirror=false"
	err = cache.mirrorSecret(ctx, sourceSecret)
	require.NoError(t, err)
	// Delete the secret to simulate it not being mirrored
	err = client.CoreV1().Secrets(targetNS.Name).Delete(ctx, sourceSecret.Name, metav1.DeleteOptions{})
	require.NoError(t, err)
	// Verify the secret is not re-created
	_, err = client.CoreV1().Secrets(targetNS.Name).Get(ctx, sourceSecret.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "Secret should not exist")

	// Test with disabled mirroring
	cache.secretMirrorLabelSelector = "mirror=true"
	err = cache.mirrorSecret(ctx, sourceSecret)
	require.NoError(t, err)
	// Verify the secret is created
	sec, err := client.CoreV1().Secrets(targetNS.Name).Get(ctx, sourceSecret.Name, metav1.GetOptions{})
	assert.Nil(t, err)
	assert.NotNil(t, sec)
}

func TestSecretMirrorBuilderMethods(t *testing.T) {
	builder := NewBackendk8sCacheBuilder()

	// Test WithSecretMirrorSourceNamespace
	sourceNS := "test-source-ns"
	labelSelector := "app=test"
	b1 := builder.WithSecretMirrorConfig(sourceNS, labelSelector)
	assert.Equal(t, sourceNS, b1.secretMirrorSourceNamespace)
	assert.Equal(t, labelSelector, b1.secretMirrorLabelSelector)
}

func TestGetCurrentK8sVersion(t *testing.T) {
	tests := []struct {
		name          string
		serverVersion *version.Info
		expected      string
		expectError   bool
	}{
		{
			name:          "valid version",
			serverVersion: &version.Info{Major: "1", Minor: "20", GitVersion: "v1.20.0"},
			expected:      "v1.20.0",
			expectError:   false,
		},
		{
			name:          "error fetching version",
			serverVersion: nil,
			expected:      "",
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			serverVersionClient := &fakeServerVersionClient{
				serverVersion: tt.serverVersion,
			}

			version, err := getCurrentK8sVersion(ctx, serverVersionClient)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, version)
			}
		})
	}
}

type fakeServerVersionClient struct {
	serverVersion *version.Info
}

func (f *fakeServerVersionClient) ServerVersion() (*version.Info, error) {
	if f.serverVersion == nil {
		return nil, fmt.Errorf("failed to fetch server version")
	}
	return f.serverVersion, nil
}

type mockSRClient struct {
	err error
}

func (m *mockSRClient) Get(ctx context.Context, name string, options metav1.GetOptions) (*nvcav2beta1.ICMSRequest, error) {
	return &nvcav2beta1.ICMSRequest{}, nil
}

func (m *mockSRClient) UpdateStatus(ctx context.Context, sr *nvcav2beta1.ICMSRequest, options metav1.UpdateOptions) (*nvcav2beta1.ICMSRequest, error) {
	return &nvcav2beta1.ICMSRequest{}, m.err
}

func Test_newGPUAllocationGetter(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	node1 := functionNode.DeepCopy()
	node1.Name = "node-gpu-a100"
	node1.Labels[nodefeatures.UniformInstanceTypeLabelKey] = "DGX-CLOUD.GPU.A100"
	node2 := taskTestNode.DeepCopy()
	node2.Name = "node-gpu-l40"
	k8sClients := mockKubeClients(
		&v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: RequestsNamespace,
			},
		},
		&v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: SystemNamespace,
			},
		},
		node1, node2,
	)

	nodeInfFactory := informers.NewSharedInformerFactoryWithOptions(k8sClients.K8s, 1*time.Second,
		nodefeatures.NewNodeInformerOptions(nil)...,
	)
	ni := nodeInfFactory.Core().V1().Nodes()
	pi := nodeInfFactory.Core().V1().Pods()

	srInfFactory := nvcainformers.NewSharedInformerFactoryWithOptions(k8sClients.BART, 1*time.Second)
	icmsReqGenInf, err := srInfFactory.ForResource(nvcav2beta1.SchemeGroupVersion.WithResource("icmsrequests"))
	require.NoError(t, err)

	fff := &featureflagmock.Fetcher{
		EnabledFFs: []*featureflag.FeatureFlag{
			featureflag.MultiNodeWorkloads,
		},
	}
	bc := &BackendK8sCache{
		clients:              k8sClients,
		eventRecorder:        record.NewFakeRecorder(100),
		tracer:               noop.NewTracerProvider().Tracer("foo"),
		requestsNamespace:    RequestsNamespace,
		podInstanceNamespace: RequestsNamespace,
		systemNamespace:      SystemNamespace,
		icmsRequestLister:    nvcav2beta1listers.NewICMSRequestLister(icmsReqGenInf.Informer().GetIndexer()),
		podSpecLister:        pi.Lister().Pods(RequestsNamespace),
		nodeLister:           ni.Lister(),
		regITCache:           icms.NewRegistrationInstanceTypeCache(),
		featureFlagFetcher:   fff,
		infraOverheadGetter:  enforce.InfraOverheadGetterFunc(func(context.Context) (corev1.ResourceList, error) { return corev1.ResourceList{}, nil }),
		k8sTimeConfig: (&k8sutil.TimeConfig{
			MaxRunningTimeout: 1 * time.Minute,
		}).Complete(),
	}
	err = bc.initInstanceNamespaceInformer(ctx)
	require.NoError(t, err)

	srHelper, _ := NewK8sComputeBackend(k8sClients, bc)
	bc.icmsRequestHelper = srHelper
	ag := newGPUAllocationGetter(bc.icmsRequestLister, srHelper)
	bc.nfClient = nodefeatures.NewDynamicClient(ag, bc.nodeLister, "DGX-CLOUD", nodefeatures.DynamicClientOptions{
		MultipleGPUTypesAllowed: true,
		UniformInstanceLabels:   true,
	})

	nodeInfFactory.Start(ctx.Done())
	srInfFactory.Start(ctx.Done())
	syncCtx, syncCancel := context.WithTimeout(ctx, 10*time.Second)
	synced := cache.WaitForCacheSync(syncCtx.Done(),
		ni.Informer().HasSynced,
		pi.Informer().HasSynced,
		icmsReqGenInf.Informer().HasSynced,
	)
	syncCancel()
	if !synced {
		t.Skip("Cache sync did not complete within 10s (fake NVCA client/informers without WatchList semantics support)")
	}

	gotGPUCapA100, err := ag.GetForGPU(ctx, "A100")
	require.NoError(t, err)
	assert.EqualValues(t, 0, gotGPUCapA100)
	gotGPUCapL40, err := ag.GetForGPU(ctx, "L40")
	require.NoError(t, err)
	assert.EqualValues(t, 0, gotGPUCapL40)
	gotGPUUsage, err := bc.getGPUUsageStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[nvcatypes.GPUName]nvcatypes.GPUResource{
		"A100": nvcatypes.GPUResource{
			Capacity: 1,
		},
		"L40": nvcatypes.GPUResource{
			Capacity: 4,
		},
	}, gotGPUUsage)

	// Create ICMSRequest with a different GPU.
	icmsReq1L40 := &nvcav2beta1.ICMSRequest{}
	icmsReq1L40.Name = "sr-l40-1"
	icmsReq1L40.Namespace = bc.requestsNamespace
	icmsReq1L40.Spec.CreationMsgInfo.GPUType = "L40"
	icmsReq1L40.Spec.CreationMsgInfo.InstanceTypeName = "DGX-CLOUD.GPU.L40_1x"
	icmsReq1L40.Spec.CreationMsgInfo.InstanceTypeValue = "DGX-CLOUD.GPU.L40"
	icmsReq1L40.Spec.CreationMsgInfo.RequestedGPUCount = 1
	icmsReq1L40.Spec.CreationMsgInfo.InstanceCount = 1
	icmsReq1L40.Status.Instances = map[string]nvcav2beta1.InstanceStatus{
		"pod-instance": {
			Type:               nvcav2beta1.InstanceTypePod,
			Status:             string(nvcatypes.ICMSInstanceRunning),
			LastReportedStatus: string(nvcatypes.ICMSInstanceRunning),
		},
	}
	_, err = k8sClients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Create(ctx, icmsReq1L40, metav1.CreateOptions{})
	require.NoError(t, err)
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		icmsReqs, err := bc.icmsRequestLister.List(labels.Everything())
		assert.NoError(ct, err)
		assert.Len(ct, icmsReqs, 1)
	}, 2*time.Second, 50*time.Millisecond)

	gotGPUCapA100, err = ag.GetForGPU(ctx, "A100")
	require.NoError(t, err)
	assert.EqualValues(t, 0, gotGPUCapA100)
	gotGPUUsage, err = bc.getGPUUsageStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[nvcatypes.GPUName]nvcatypes.GPUResource{
		"A100": nvcatypes.GPUResource{
			Capacity: 1,
		},
		"L40": nvcatypes.GPUResource{
			Capacity:  4,
			Allocated: 1,
		},
	}, gotGPUUsage)

	// Create ICMSRequest with no GPU request.
	icmsReq1A100 := &nvcav2beta1.ICMSRequest{}
	icmsReq1A100.Name = "sr-a100-1"
	icmsReq1A100.Namespace = bc.requestsNamespace
	icmsReq1A100.Spec.CreationMsgInfo.GPUType = "A100"
	_, err = k8sClients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Create(ctx, icmsReq1A100, metav1.CreateOptions{})
	require.NoError(t, err)
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		icmsReqs, err := bc.icmsRequestLister.List(labels.Everything())
		assert.NoError(ct, err)
		assert.Len(ct, icmsReqs, 2)
	}, 2*time.Second, 50*time.Millisecond)

	gotGPUCapA100, err = ag.GetForGPU(ctx, "A100")
	require.NoError(t, err)
	assert.EqualValues(t, 0, gotGPUCapA100)
	gotGPUUsage, err = bc.getGPUUsageStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[nvcatypes.GPUName]nvcatypes.GPUResource{
		"A100": nvcatypes.GPUResource{
			Capacity: 1,
		},
		"L40": nvcatypes.GPUResource{
			Capacity:  4,
			Allocated: 1,
		},
	}, gotGPUUsage)

	// Update GPU request and instance count, should get allocation of 2.
	icmsReq1A100.Spec.CreationMsgInfo.InstanceTypeName = "DGX-CLOUD.GPU.A100_1x"
	icmsReq1A100.Spec.CreationMsgInfo.InstanceTypeValue = "DGX-CLOUD.GPU.A100"
	icmsReq1A100.Spec.CreationMsgInfo.RequestedGPUCount = 1
	icmsReq1A100.Spec.CreationMsgInfo.InstanceCount = 2
	_, err = k8sClients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Update(ctx, icmsReq1A100, metav1.UpdateOptions{})
	require.NoError(t, err)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotGPUCapA100, err = ag.GetForGPU(ctx, "A100")
		assert.NoError(ct, err)
		assert.EqualValues(ct, 2, gotGPUCapA100)
	}, 2*time.Second, 50*time.Millisecond)
	gotGPUUsage, err = bc.getGPUUsageStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[nvcatypes.GPUName]nvcatypes.GPUResource{
		"A100": nvcatypes.GPUResource{
			Capacity:  1,
			Allocated: 2,
		},
		"L40": nvcatypes.GPUResource{
			Capacity:  4,
			Allocated: 1,
		},
	}, gotGPUUsage)

	// Mark request failed but unreported, should get allocation of 2.
	icmsReq1A100.Status.Instances = map[string]nvcav2beta1.InstanceStatus{
		"pod-instance": {
			Type:               nvcav2beta1.InstanceTypePod,
			Status:             string(nvcatypes.ICMSInstanceTerminated),
			LastReportedStatus: string(nvcatypes.ICMSInstanceRunning),
		},
		"miniservice-instance": {
			Type:               nvcav2beta1.InstanceTypeMiniService,
			Status:             string(nvcatypes.ICMSInstanceTerminated),
			LastReportedStatus: string(nvcatypes.ICMSInstanceRunning),
		},
	}
	_, err = k8sClients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).UpdateStatus(ctx, icmsReq1A100, metav1.UpdateOptions{})
	require.NoError(t, err)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotGPUCapA100, err = ag.GetForGPU(ctx, "A100")
		assert.NoError(ct, err)
		assert.EqualValues(ct, 2, gotGPUCapA100)
	}, 2*time.Second, 50*time.Millisecond)

	// Mark request failed and reported, should get allocation of 0.
	icmsReq1A100.Status.Instances = map[string]nvcav2beta1.InstanceStatus{
		"pod-instance": {
			Type:               nvcav2beta1.InstanceTypePod,
			Status:             string(nvcatypes.ICMSInstanceTerminated),
			LastReportedStatus: string(nvcatypes.ICMSInstanceTerminated),
		},
		"miniservice-instance": {
			Type:               nvcav2beta1.InstanceTypeMiniService,
			Status:             string(nvcatypes.ICMSInstanceTerminated),
			LastReportedStatus: string(nvcatypes.ICMSInstanceTerminated),
		},
	}
	_, err = k8sClients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).UpdateStatus(ctx, icmsReq1A100, metav1.UpdateOptions{})
	require.NoError(t, err)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotGPUCapA100, err = ag.GetForGPU(ctx, "A100")
		assert.NoError(ct, err)
		assert.EqualValues(ct, 0, gotGPUCapA100)
	}, 2*time.Second, 50*time.Millisecond)
	gotGPUUsage, err = bc.getGPUUsageStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[nvcatypes.GPUName]nvcatypes.GPUResource{
		"A100": nvcatypes.GPUResource{
			Capacity: 1,
		},
		"L40": nvcatypes.GPUResource{
			Capacity:  4,
			Allocated: 1,
		},
	}, gotGPUUsage)

	// Multinode
	node3 := node1.DeepCopy()
	node3.Name = "node-gpu-a100-1"
	_, err = k8sClients.K8s.CoreV1().Nodes().Create(ctx, node3, metav1.CreateOptions{})
	require.NoError(t, err)

	icmsReq2A100 := icmsReq1A100.DeepCopy()
	icmsReq2A100.Name += "-multinode"
	icmsReq2A100.Spec.CreationMsgInfo.InstanceTypeName = "DGX-CLOUD.GPU.A100_1x.x2"
	icmsReq2A100.Spec.CreationMsgInfo.InstanceTypeValue = "DGX-CLOUD.GPU.A100"
	icmsReq2A100.Spec.CreationMsgInfo.RequestedGPUCount = 2
	icmsReq2A100.Spec.CreationMsgInfo.InstanceCount = 1
	icmsReq2A100.Status = nvcav2beta1.ICMSRequestStatus{}

	err = k8sClients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Delete(ctx, icmsReq1A100.Name, metav1.DeleteOptions{})
	require.NoError(t, err)
	_, err = k8sClients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Create(ctx, icmsReq2A100, metav1.CreateOptions{})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		nodes, err := bc.nodeLister.List(labels.Everything())
		if assert.NoError(ct, err) {
			assert.Len(ct, nodes, 3)
		}
	}, 5*time.Second, 100*time.Millisecond)
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		icmsReqs, err := bc.icmsRequestLister.List(labels.Everything())
		if assert.NoError(ct, err) {
			assert.Len(ct, icmsReqs, 2)
		}
	}, 2*time.Second, 50*time.Millisecond)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotGPUCapA100, err = ag.GetForGPU(ctx, "A100")
		assert.NoError(ct, err)
		assert.EqualValues(ct, 2, gotGPUCapA100)
	}, 2*time.Second, 50*time.Millisecond)
	gotGPUUsage, err = bc.getGPUUsageStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[nvcatypes.GPUName]nvcatypes.GPUResource{
		"A100": nvcatypes.GPUResource{
			Capacity:  2,
			Allocated: 2,
		},
		"L40": nvcatypes.GPUResource{
			Capacity:  4,
			Allocated: 1,
		},
	}, gotGPUUsage)
}

func TestBackendK8sCache_Start_FNDS(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClients(functionNode)
	b := NewBackendk8sCacheBuilder().
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithClusterProvider("ON-PREM").
		WithDynamicNodeFeatureClient(true, nodefeatures.DynamicClientOptions{}).
		WithClients(clients).
		WithFeatureFlagFetcher(&featureflagmock.Fetcher{
			EnabledFFs: []*featureflag.FeatureFlag{featureflag.UseFunctionDeploymentStages},
		})
	b.addSharedClusterNodePublisher = mockAddSharedClusterNodePublisherFunc

	bc, _, err := b.Start(ctx)
	require.NoError(t, err)

	// This will trigger the FNDS event handler.
	// No need to test the event handler logic here, it already has tests.
	podEvent := &v1.Event{}
	podEvent.Name = "foo"
	podEvent.InvolvedObject = v1.ObjectReference{
		APIVersion: "v1",
		Kind:       "Pod",
		Namespace:  bc.podInstanceNamespace,
		Name:       "bar",
	}
	podEvent.Message = "some message"
	podEvent.Reason = "GoodReason"
	_, err = clients.K8s.CoreV1().Events(bc.podInstanceNamespace).Create(ctx, podEvent, metav1.CreateOptions{})
	require.NoError(t, err)
}

func TestBackendK8sCache_ReconcileInstanceStatus(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name                   string
		inputState             types.ICMSServerInstanceState
		icmsRequests           []*nvcav2beta1.ICMSRequest
		icmsRequestListerError error
		labelRequirementError  bool
		expectedRequest        *nvcav2beta1.ICMSRequest
		expectedInstanceStatus nvcav2beta1.InstanceStatus
		expectedReconcileState ICMSInstanceReconcileState
		description            string
	}{
		{
			name: "no ICMS requests found",
			inputState: types.ICMSServerInstanceState{
				InstanceID:    "instance-1",
				RequestID:     "request-1",
				InstanceState: types.ICMSInstanceRunning,
			},
			icmsRequests:           []*nvcav2beta1.ICMSRequest{},
			expectedRequest:        nil,
			expectedInstanceStatus: nvcav2beta1.InstanceStatus{},
			expectedReconcileState: ICMSInstanceReconcileNoAction,
			description:            "Should return NoAction when no SPOTRequests found",
		},
		{
			name: "ICMS request lister error",
			inputState: types.ICMSServerInstanceState{
				InstanceID:    "instance-1",
				RequestID:     "request-1",
				InstanceState: types.ICMSInstanceRunning,
			},
			icmsRequests:           []*nvcav2beta1.ICMSRequest{},
			icmsRequestListerError: fmt.Errorf("lister error"),
			expectedRequest:        nil,
			expectedInstanceStatus: nvcav2beta1.InstanceStatus{},
			expectedReconcileState: ICMSInstanceReconcileNoAction,
			description:            "Should return NoAction when icmsRequestLister returns error",
		},
		{
			name: "instance not found in ICMS request",
			inputState: types.ICMSServerInstanceState{
				InstanceID:    "instance-1",
				RequestID:     "request-1",
				InstanceState: types.ICMSInstanceRunning,
			},
			icmsRequests: []*nvcav2beta1.ICMSRequest{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "icms-request-1",
					},
					Status: nvcav2beta1.ICMSRequestStatus{
						Instances: map[string]nvcav2beta1.InstanceStatus{
							"instance-2": {
								Status: string(types.ICMSInstanceRunning),
							},
						},
					},
				},
			},
			expectedRequest:        nil,
			expectedInstanceStatus: nvcav2beta1.InstanceStatus{},
			expectedReconcileState: ICMSInstanceReconcileTerminateAndUpdate,
			description:            "Should return TerminateAndUpdate when instance not found",
		},
		{
			name: "matching ICMS request with nil instances",
			inputState: types.ICMSServerInstanceState{
				InstanceID:    "instance-1",
				RequestID:     "request-1",
				InstanceState: types.ICMSInstanceRunning,
			},
			icmsRequests: []*nvcav2beta1.ICMSRequest{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "icms-request-1",
					},
					Status: nvcav2beta1.ICMSRequestStatus{
						Instances: nil,
					},
				},
			},
			expectedRequest:        nil,
			expectedInstanceStatus: nvcav2beta1.InstanceStatus{},
			expectedReconcileState: ICMSInstanceReconcileTerminateAndUpdate,
			description:            "Should return TerminateAndUpdate when instance not found",
		},
		{
			name: "instance found - states in sync (running/running)",
			inputState: types.ICMSServerInstanceState{
				InstanceID:    "instance-1",
				RequestID:     "request-1",
				InstanceState: types.ICMSInstanceRunning,
			},
			icmsRequests: []*nvcav2beta1.ICMSRequest{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "icms-request-1",
					},
					Status: nvcav2beta1.ICMSRequestStatus{
						Instances: map[string]nvcav2beta1.InstanceStatus{
							"instance-1": {
								Status: string(types.ICMSInstanceRunning),
							},
						},
					},
				},
			},
			expectedRequest: &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "icms-request-1",
				},
				Status: nvcav2beta1.ICMSRequestStatus{
					Instances: map[string]nvcav2beta1.InstanceStatus{
						"instance-1": {
							Status: string(types.ICMSInstanceRunning),
						},
					},
				},
			},
			expectedInstanceStatus: nvcav2beta1.InstanceStatus{
				Status: string(types.ICMSInstanceRunning),
			},
			expectedReconcileState: ICMSInstanceReconcileNoAction,
			description:            "Should return NoAction when states are in sync",
		},
		{
			name: "instance found - update only (started/running)",
			inputState: types.ICMSServerInstanceState{
				InstanceID:    "instance-1",
				RequestID:     "request-1",
				InstanceState: types.ICMSInstanceRunning,
			},
			icmsRequests: []*nvcav2beta1.ICMSRequest{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "icms-request-1",
					},
					Status: nvcav2beta1.ICMSRequestStatus{
						Instances: map[string]nvcav2beta1.InstanceStatus{
							"instance-1": {
								Status: string(types.ICMSInstanceStarted),
							},
						},
					},
				},
			},
			expectedRequest: &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "icms-request-1",
				},
				Status: nvcav2beta1.ICMSRequestStatus{
					Instances: map[string]nvcav2beta1.InstanceStatus{
						"instance-1": {
							Status: string(types.ICMSInstanceStarted),
						},
					},
				},
			},
			expectedInstanceStatus: nvcav2beta1.InstanceStatus{
				Status: string(types.ICMSInstanceStarted),
			},
			expectedReconcileState: ICMSInstanceReconcileUpdateOnly,
			description:            "Should return UpdateOnly when local state is newer",
		},
		{
			name: "instance found - terminate and update (shutting-down/running)",
			inputState: types.ICMSServerInstanceState{
				InstanceID:    "instance-1",
				RequestID:     "request-1",
				InstanceState: types.ICMSInstanceShuttingDown,
			},
			icmsRequests: []*nvcav2beta1.ICMSRequest{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "icms-request-1",
					},
					Status: nvcav2beta1.ICMSRequestStatus{
						Instances: map[string]nvcav2beta1.InstanceStatus{
							"instance-1": {
								Status: string(types.ICMSInstanceRunning),
							},
						},
					},
				},
			},
			expectedRequest: &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "icms-request-1",
				},
				Status: nvcav2beta1.ICMSRequestStatus{
					Instances: map[string]nvcav2beta1.InstanceStatus{
						"instance-1": {
							Status: string(types.ICMSInstanceRunning),
						},
					},
				},
			},
			expectedInstanceStatus: nvcav2beta1.InstanceStatus{
				Status: string(types.ICMSInstanceRunning),
			},
			expectedReconcileState: ICMSInstanceReconcileTerminateAndUpdate,
			description:            "Should return TerminateAndUpdate when input state is shutting down",
		},
		{
			name: "instance found - terminated states sync",
			inputState: types.ICMSServerInstanceState{
				InstanceID:    "instance-1",
				RequestID:     "request-1",
				InstanceState: types.ICMSInstanceTerminated,
			},
			icmsRequests: []*nvcav2beta1.ICMSRequest{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "icms-request-1",
					},
					Status: nvcav2beta1.ICMSRequestStatus{
						Instances: map[string]nvcav2beta1.InstanceStatus{
							"instance-1": {
								Status: string(types.ICMSInstanceTerminated),
							},
						},
					},
				},
			},
			expectedRequest: &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "icms-request-1",
				},
				Status: nvcav2beta1.ICMSRequestStatus{
					Instances: map[string]nvcav2beta1.InstanceStatus{
						"instance-1": {
							Status: string(types.ICMSInstanceTerminated),
						},
					},
				},
			},
			expectedInstanceStatus: nvcav2beta1.InstanceStatus{
				Status: string(types.ICMSInstanceTerminated),
			},
			expectedReconcileState: ICMSInstanceReconcileNoAction,
			description:            "Should return NoAction when both states are terminated",
		},
		{
			name: "multiple ICMS requests - instance found in second request",
			inputState: types.ICMSServerInstanceState{
				InstanceID:    "instance-1",
				RequestID:     "request-1",
				InstanceState: types.ICMSInstanceRunning,
			},
			icmsRequests: []*nvcav2beta1.ICMSRequest{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "icms-request-1",
					},
					Status: nvcav2beta1.ICMSRequestStatus{
						Instances: map[string]nvcav2beta1.InstanceStatus{
							"instance-2": {
								Status: string(types.ICMSInstanceRunning),
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "icms-request-2",
					},
					Status: nvcav2beta1.ICMSRequestStatus{
						Instances: map[string]nvcav2beta1.InstanceStatus{
							"instance-1": {
								Status: string(types.ICMSInstanceStarted),
							},
						},
					},
				},
			},
			expectedRequest: &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "icms-request-2",
				},
				Status: nvcav2beta1.ICMSRequestStatus{
					Instances: map[string]nvcav2beta1.InstanceStatus{
						"instance-1": {
							Status: string(types.ICMSInstanceStarted),
						},
					},
				},
			},
			expectedInstanceStatus: nvcav2beta1.InstanceStatus{
				Status: string(types.ICMSInstanceStarted),
			},
			expectedReconcileState: ICMSInstanceReconcileUpdateOnly,
			description:            "Should find instance in second request and return correct reconcile action",
		},
		{
			name: "instance with additional status fields",
			inputState: types.ICMSServerInstanceState{
				InstanceID:    "instance-1",
				RequestID:     "request-1",
				InstanceState: types.ICMSInstanceRunning,
			},
			icmsRequests: []*nvcav2beta1.ICMSRequest{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "icms-request-1",
					},
					Status: nvcav2beta1.ICMSRequestStatus{
						Instances: map[string]nvcav2beta1.InstanceStatus{
							"instance-1": {
								Status:                string(types.ICMSInstanceStarted),
								LastReportedStatus:    string(types.ICMSInstanceStateNoStatus),
								LastReportedTimestamp: &metav1.Time{Time: time.Date(2025, 7, 15, 12, 0, 0, 0, time.UTC)},
							},
						},
					},
				},
			},
			expectedRequest: &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "icms-request-1",
				},
				Status: nvcav2beta1.ICMSRequestStatus{
					Instances: map[string]nvcav2beta1.InstanceStatus{
						"instance-1": {
							Status:                string(types.ICMSInstanceStarted),
							LastReportedStatus:    string(types.ICMSInstanceStateNoStatus),
							LastReportedTimestamp: &metav1.Time{Time: time.Date(2025, 7, 15, 12, 0, 0, 0, time.UTC)},
						},
					},
				},
			},
			expectedInstanceStatus: nvcav2beta1.InstanceStatus{
				Status:                string(types.ICMSInstanceStarted),
				LastReportedStatus:    string(types.ICMSInstanceStateNoStatus),
				LastReportedTimestamp: &metav1.Time{Time: time.Date(2025, 7, 15, 12, 0, 0, 0, time.UTC)},
			},
			expectedReconcileState: ICMSInstanceReconcileUpdateOnly,
			description:            "Should handle instance with additional status fields correctly",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock ICMS request lister
			mockSpotRequestLister := &mockReconcileSpotRequestLister{
				icmsRequests: tt.icmsRequests,
				err:          tt.icmsRequestListerError,
			}

			// Create BackendK8sCache instance with mock lister
			bc := &BackendK8sCache{
				icmsRequestLister: mockSpotRequestLister,
			}

			// Call the function under test
			actualRequest, actualInstanceStatus, actualReconcileState, actualFound := bc.ReconcileInstanceStatus(ctx, tt.inputState)

			// Verify the results
			if tt.expectedRequest == nil {
				assert.Nil(t, actualRequest, "Expected request to be nil")
				assert.False(t, actualFound)
			} else {
				require.NotNil(t, actualRequest, "Expected request to be non-nil")
				assert.Equal(t, tt.expectedRequest.Name, actualRequest.Name, "Request name mismatch")
				assert.Equal(t, tt.expectedRequest.Status.Instances, actualRequest.Status.Instances, "Request instances mismatch")
				assert.True(t, actualFound)
			}

			// For instance status comparison, we need to handle timestamp comparison carefully
			if tt.expectedInstanceStatus.LastReportedTimestamp != nil {
				assert.NotNil(t, actualInstanceStatus.LastReportedTimestamp, "Expected LastReportedTimestamp to be non-nil")
				// We only check that timestamp exists, not the exact value since DeepCopy might affect it
				assert.Equal(t, tt.expectedInstanceStatus.Status, actualInstanceStatus.Status, "Instance status mismatch")
				assert.Equal(t, tt.expectedInstanceStatus.LastReportedStatus, actualInstanceStatus.LastReportedStatus, "Instance last reported status mismatch")
			} else {
				assert.Equal(t, tt.expectedInstanceStatus, actualInstanceStatus, "Instance status mismatch")
			}

			assert.Equal(t, tt.expectedReconcileState, actualReconcileState, "Reconcile state mismatch")
		})
	}
}

// Mock implementation of ICMSRequestLister for testing ReconcileInstanceStatus
type mockReconcileSpotRequestLister struct {
	icmsRequests []*nvcav2beta1.ICMSRequest
	err          error
}

func (m *mockReconcileSpotRequestLister) List(selector labels.Selector) ([]*nvcav2beta1.ICMSRequest, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.icmsRequests, nil
}

func (m *mockReconcileSpotRequestLister) ICMSRequests(namespace string) nvcav2beta1listers.ICMSRequestNamespaceLister {
	// Not needed for ReconcileInstanceStatus tests
	return nil
}

func TestInstanceStatusReconcileAction(t *testing.T) {
	tests := []struct {
		name           string
		spotState      types.ICMSInstanceState
		localState     types.ICMSInstanceState
		expectedAction ICMSInstanceReconcileState
	}{
		{
			name:           "started/started - no action",
			spotState:      types.ICMSInstanceStarted,
			localState:     types.ICMSInstanceStarted,
			expectedAction: ICMSInstanceReconcileNoAction,
		},
		{
			name:           "started/running - update only",
			spotState:      types.ICMSInstanceStarted,
			localState:     types.ICMSInstanceRunning,
			expectedAction: ICMSInstanceReconcileUpdateOnly,
		},
		{
			name:           "started/terminated - update only",
			spotState:      types.ICMSInstanceStarted,
			localState:     types.ICMSInstanceTerminated,
			expectedAction: ICMSInstanceReconcileUpdateOnly,
		},
		{
			name:           "running/started - update only",
			spotState:      types.ICMSInstanceRunning,
			localState:     types.ICMSInstanceStarted,
			expectedAction: ICMSInstanceReconcileUpdateOnly,
		},
		{
			name:           "running/running - no action",
			spotState:      types.ICMSInstanceRunning,
			localState:     types.ICMSInstanceRunning,
			expectedAction: ICMSInstanceReconcileNoAction,
		},
		{
			name:           "running/terminated - update only",
			spotState:      types.ICMSInstanceRunning,
			localState:     types.ICMSInstanceTerminated,
			expectedAction: ICMSInstanceReconcileUpdateOnly,
		},
		{
			name:           "terminated/started - update only",
			spotState:      types.ICMSInstanceTerminated,
			localState:     types.ICMSInstanceStarted,
			expectedAction: ICMSInstanceReconcileUpdateOnly,
		},
		{
			name:           "terminated/running - update only",
			spotState:      types.ICMSInstanceTerminated,
			localState:     types.ICMSInstanceRunning,
			expectedAction: ICMSInstanceReconcileUpdateOnly,
		},
		{
			name:           "terminated/terminated - no action",
			spotState:      types.ICMSInstanceTerminated,
			localState:     types.ICMSInstanceTerminated,
			expectedAction: ICMSInstanceReconcileNoAction,
		},
		{
			name:           "shutting-down/started - terminate and update",
			spotState:      types.ICMSInstanceShuttingDown,
			localState:     types.ICMSInstanceStarted,
			expectedAction: ICMSInstanceReconcileTerminateAndUpdate,
		},
		{
			name:           "shutting-down/running - terminate and update",
			spotState:      types.ICMSInstanceShuttingDown,
			localState:     types.ICMSInstanceRunning,
			expectedAction: ICMSInstanceReconcileTerminateAndUpdate,
		},
		{
			name:           "shutting-down/terminated - terminate and update",
			spotState:      types.ICMSInstanceShuttingDown,
			localState:     types.ICMSInstanceTerminated,
			expectedAction: ICMSInstanceReconcileTerminateAndUpdate,
		},
		{
			name:           "unknown state combination - no action",
			spotState:      types.ICMSInstanceState("unknown"),
			localState:     types.ICMSInstanceRunning,
			expectedAction: ICMSInstanceReconcileNoAction,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := instanceStatusReconcileAction(tt.spotState, tt.localState)
			assert.Equal(t, tt.expectedAction, result, "Reconcile action mismatch")
		})
	}
}

func mockAddSharedClusterNodePublisherFunc(context.Context, cache.SharedIndexInformer) (*atomic.Bool, cache.InformerSynced, error) {
	return &atomic.Bool{}, func() bool { return true }, nil
}

// noopICMSRequestHelper provides no-op implementations of every ICMSRequestHelper
// method. Embed it in test stubs so they only need to override the methods they
// actually exercise.
type noopICMSRequestHelper struct{}

func (noopICMSRequestHelper) ApplyCreationMessage(context.Context, *nvcav2beta1.ICMSRequest) error {
	return nil
}
func (noopICMSRequestHelper) ApplyTerminationMessage(context.Context, *nvcav2beta1.ICMSRequest) error {
	return nil
}
func (noopICMSRequestHelper) AggregateInstanceStatuses(context.Context, *nvcav2beta1.ICMSRequest) AggregatedInstanceStatus {
	return AggregatedInstanceStatusUnknown
}
func (noopICMSRequestHelper) GetICMSRequestStatusUpdatesForRequest(context.Context, *nvcav2beta1.ICMSRequest) ([]types.ICMSRequestUpdateInfo, error) {
	return nil, nil
}
func (noopICMSRequestHelper) GetICMSRequestUpdatesForTerminationRequest(context.Context, *nvcav2beta1.ICMSRequest) []types.ICMSRequestUpdateInfo {
	return nil
}
func (noopICMSRequestHelper) GetICMSRequestUpdatesForCreateRequest(context.Context, *nvcav2beta1.ICMSRequest) []types.ICMSRequestUpdateInfo {
	return nil
}
func (noopICMSRequestHelper) ComputeCleanupCacheReferences(context.Context, []string) error {
	return nil
}
func (noopICMSRequestHelper) AllInstancesTerminatedAndReported(context.Context, *nvcav2beta1.ICMSRequest) bool {
	return false
}
func (noopICMSRequestHelper) HandleInstanceStatusPreconditionFailure(context.Context, *nvcav2beta1.ICMSRequest, string) error {
	return nil
}
func (noopICMSRequestHelper) PurgeInstanceID(context.Context, *nvcav2beta1.ICMSRequest, map[string]nvcav2beta1.InstanceStatus, string) bool {
	return false
}
func (noopICMSRequestHelper) GetROSUpdatesForRequest(context.Context, *nvcav2beta1.ICMSRequest) ([]types.ROSUpdateInfo, error) {
	return nil, nil
}

// terminatedSet is a minimal ICMSRequestHelper for scheduler workload metric tests.
// It reports requests whose IDs are in the set as fully terminated.
type terminatedSet struct {
	noopICMSRequestHelper
	ids map[string]bool
}

func (ts terminatedSet) AllInstancesTerminatedAndReported(_ context.Context, req *nvcav2beta1.ICMSRequest) bool {
	return ts.ids[req.Spec.RequestID]
}

func TestUpdateSchedulerWorkloadMetrics(t *testing.T) {
	makeReq := func(requestID string, action common.MessageAction) *nvcav2beta1.ICMSRequest {
		return &nvcav2beta1.ICMSRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "sr-" + requestID,
				Namespace: RequestsNamespace,
			},
			Spec: nvcav2beta1.ICMSRequestSpec{
				RequestID: requestID,
				Action:    action,
			},
		}
	}

	makePod := func(name, namespace, requestID, schedulerName string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels:    map[string]string{nvcatypes.ICMSRequestIDKey: requestID},
			},
			Spec: corev1.PodSpec{
				SchedulerName: schedulerName,
				Containers:    []corev1.Container{{Name: "main", Image: "img"}},
			},
		}
	}

	type gaugeKey struct {
		scheduler    string
		workloadKind string
	}

	getGaugeValues := func(reg *prometheus.Registry) map[gaugeKey]float64 {
		mfs, err := reg.Gather()
		require.NoError(t, err)
		out := map[gaugeKey]float64{}
		for _, mf := range mfs {
			if *mf.Name != nvcametrics.SchedulerWorkloadCountMetricName {
				continue
			}
			for _, m := range mf.Metric {
				labels := map[string]string{}
				for _, lp := range m.Label {
					labels[*lp.Name] = *lp.Value
				}
				k := gaugeKey{
					scheduler:    labels[nvcametrics.SchedulerNameLabel],
					workloadKind: labels[nvcametrics.WorkloadKindLabel],
				}
				out[k] = *m.Gauge.Value
			}
		}
		return out
	}

	setupCache := func(t *testing.T, reqs []*nvcav2beta1.ICMSRequest, pods []*corev1.Pod,
		terminated terminatedSet, fff *featureflagmock.Fetcher,
	) (*BackendK8sCache, *prometheus.Registry, context.Context) {
		t.Helper()
		ctx := context.Background()
		ctx = core.WithDefaultLogger(ctx)
		reg := prometheus.NewRegistry()
		ctx = nvcametrics.WithDefaultMetrics(ctx, "test-nca", "test-cluster", "test-group", "v1",
			nvcametrics.WithRegisterer(reg))

		reqIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
		for _, r := range reqs {
			require.NoError(t, reqIndexer.Add(r))
		}

		podIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
		for _, p := range pods {
			require.NoError(t, podIndexer.Add(p))
		}

		bc := &BackendK8sCache{
			icmsRequestLister:  nvcav2beta1listers.NewICMSRequestLister(reqIndexer),
			podLister:          listersv1.NewPodLister(podIndexer),
			icmsRequestHelper:  terminated,
			featureFlagFetcher: fff,
		}
		return bc, reg, ctx
	}

	t.Run("mixed schedulers counted from actual pods", func(t *testing.T) {
		reqs := []*nvcav2beta1.ICMSRequest{
			makeReq("fn-default-1", common.FunctionCreationAction),
			makeReq("fn-kai-1", common.FunctionCreationAction),
			makeReq("task-default-1", common.TaskCreationAction),
			makeReq("task-kai-1", common.TaskCreationAction),
		}
		pods := []*corev1.Pod{
			makePod("pod-fn-d1", "ns1", "fn-default-1", "default-scheduler"),
			makePod("pod-fn-k1", "ns2", "fn-kai-1", "kai-scheduler"),
			makePod("pod-task-d1", "ns3", "task-default-1", "default-scheduler"),
			makePod("pod-task-k1", "ns4", "task-kai-1", "kai-scheduler"),
		}
		fff := &featureflagmock.Fetcher{}
		bc, reg, ctx := setupCache(t, reqs, pods, terminatedSet{}, fff)

		bc.UpdateSchedulerWorkloadMetrics(ctx)

		vals := getGaugeValues(reg)
		assert.Equal(t, float64(1), vals[gaugeKey{"default-scheduler", "function"}])
		assert.Equal(t, float64(1), vals[gaugeKey{"kai-scheduler", "function"}])
		assert.Equal(t, float64(1), vals[gaugeKey{"default-scheduler", "task"}])
		assert.Equal(t, float64(1), vals[gaugeKey{"kai-scheduler", "task"}])
	})

	t.Run("terminal requests excluded", func(t *testing.T) {
		reqs := []*nvcav2beta1.ICMSRequest{
			makeReq("active-fn", common.FunctionCreationAction),
			makeReq("terminated-fn", common.FunctionCreationAction),
		}
		pods := []*corev1.Pod{
			makePod("pod-active", "ns1", "active-fn", "kai-scheduler"),
			makePod("pod-terminated", "ns2", "terminated-fn", "kai-scheduler"),
		}
		term := terminatedSet{ids: map[string]bool{"terminated-fn": true}}
		fff := &featureflagmock.Fetcher{}
		bc, reg, ctx := setupCache(t, reqs, pods, term, fff)

		bc.UpdateSchedulerWorkloadMetrics(ctx)

		vals := getGaugeValues(reg)
		assert.Equal(t, float64(1), vals[gaugeKey{"kai-scheduler", "function"}])
		assert.Equal(t, float64(0), vals[gaugeKey{"default-scheduler", "function"}])
	})

	t.Run("empty schedulerName on pod defaults to default-scheduler", func(t *testing.T) {
		reqs := []*nvcav2beta1.ICMSRequest{
			makeReq("fn-empty", common.FunctionCreationAction),
		}
		pods := []*corev1.Pod{
			makePod("pod-empty", "ns1", "fn-empty", ""),
		}
		fff := &featureflagmock.Fetcher{}
		bc, reg, ctx := setupCache(t, reqs, pods, terminatedSet{}, fff)

		bc.UpdateSchedulerWorkloadMetrics(ctx)

		vals := getGaugeValues(reg)
		assert.Equal(t, float64(1), vals[gaugeKey{"default-scheduler", "function"}])
		assert.Equal(t, float64(0), vals[gaugeKey{"kai-scheduler", "function"}])
	})

	t.Run("request with no pods falls back to feature flag - KAI enabled", func(t *testing.T) {
		reqs := []*nvcav2beta1.ICMSRequest{
			makeReq("fn-no-pod", common.FunctionCreationAction),
		}
		fff := &featureflagmock.Fetcher{EnabledFFs: []*featureflag.FeatureFlag{featureflag.KAIScheduler}}
		bc, reg, ctx := setupCache(t, reqs, nil, terminatedSet{}, fff)

		bc.UpdateSchedulerWorkloadMetrics(ctx)

		vals := getGaugeValues(reg)
		assert.Equal(t, float64(1), vals[gaugeKey{"kai-scheduler", "function"}])
		assert.Equal(t, float64(0), vals[gaugeKey{"default-scheduler", "function"}])
	})

	t.Run("request with no pods falls back to feature flag - KAI disabled", func(t *testing.T) {
		reqs := []*nvcav2beta1.ICMSRequest{
			makeReq("fn-no-pod", common.FunctionCreationAction),
		}
		fff := &featureflagmock.Fetcher{}
		bc, reg, ctx := setupCache(t, reqs, nil, terminatedSet{}, fff)

		bc.UpdateSchedulerWorkloadMetrics(ctx)

		vals := getGaugeValues(reg)
		assert.Equal(t, float64(0), vals[gaugeKey{"kai-scheduler", "function"}])
		assert.Equal(t, float64(1), vals[gaugeKey{"default-scheduler", "function"}])
	})

	t.Run("mix of pods-present and no-pods requests", func(t *testing.T) {
		reqs := []*nvcav2beta1.ICMSRequest{
			makeReq("fn-has-pod", common.FunctionCreationAction),
			makeReq("fn-no-pod", common.FunctionCreationAction),
		}
		pods := []*corev1.Pod{
			makePod("pod-1", "ns1", "fn-has-pod", "default-scheduler"),
		}
		fff := &featureflagmock.Fetcher{EnabledFFs: []*featureflag.FeatureFlag{featureflag.KAIScheduler}}
		bc, reg, ctx := setupCache(t, reqs, pods, terminatedSet{}, fff)

		bc.UpdateSchedulerWorkloadMetrics(ctx)

		vals := getGaugeValues(reg)
		// fn-has-pod: actual pod says default-scheduler
		// fn-no-pod: no pod, KAI enabled → kai-scheduler
		assert.Equal(t, float64(1), vals[gaugeKey{"default-scheduler", "function"}])
		assert.Equal(t, float64(1), vals[gaugeKey{"kai-scheduler", "function"}])
	})

	t.Run("nil podLister falls back to feature flag for all", func(t *testing.T) {
		reqs := []*nvcav2beta1.ICMSRequest{
			makeReq("fn-1", common.FunctionCreationAction),
			makeReq("task-1", common.TaskCreationAction),
		}
		fff := &featureflagmock.Fetcher{EnabledFFs: []*featureflag.FeatureFlag{featureflag.KAIScheduler}}

		ctx := context.Background()
		ctx = core.WithDefaultLogger(ctx)
		reg := prometheus.NewRegistry()
		ctx = nvcametrics.WithDefaultMetrics(ctx, "test-nca", "test-cluster", "test-group", "v1",
			nvcametrics.WithRegisterer(reg))

		reqIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
		for _, r := range reqs {
			require.NoError(t, reqIndexer.Add(r))
		}

		bc := &BackendK8sCache{
			icmsRequestLister:  nvcav2beta1listers.NewICMSRequestLister(reqIndexer),
			podLister:          nil,
			icmsRequestHelper:  terminatedSet{},
			featureFlagFetcher: fff,
		}

		bc.UpdateSchedulerWorkloadMetrics(ctx)

		vals := getGaugeValues(reg)
		assert.Equal(t, float64(1), vals[gaugeKey{"kai-scheduler", "function"}])
		assert.Equal(t, float64(1), vals[gaugeKey{"kai-scheduler", "task"}])
		assert.Equal(t, float64(0), vals[gaugeKey{"default-scheduler", "function"}])
		assert.Equal(t, float64(0), vals[gaugeKey{"default-scheduler", "task"}])
	})

	t.Run("no active requests produces all zeros", func(t *testing.T) {
		fff := &featureflagmock.Fetcher{}
		bc, reg, ctx := setupCache(t, nil, nil, terminatedSet{}, fff)

		bc.UpdateSchedulerWorkloadMetrics(ctx)

		vals := getGaugeValues(reg)
		assert.Equal(t, float64(0), vals[gaugeKey{"default-scheduler", "function"}])
		assert.Equal(t, float64(0), vals[gaugeKey{"kai-scheduler", "function"}])
		assert.Equal(t, float64(0), vals[gaugeKey{"default-scheduler", "task"}])
		assert.Equal(t, float64(0), vals[gaugeKey{"kai-scheduler", "task"}])
	})

	t.Run("nil metrics context does not panic", func(t *testing.T) {
		ctx := context.Background()
		ctx = core.WithDefaultLogger(ctx)
		bc := &BackendK8sCache{
			featureFlagFetcher: &featureflagmock.Fetcher{},
		}
		assert.NotPanics(t, func() {
			bc.UpdateSchedulerWorkloadMetrics(ctx)
		})
	})

	t.Run("multiple pods per request only counted once", func(t *testing.T) {
		reqs := []*nvcav2beta1.ICMSRequest{
			makeReq("fn-multi", common.FunctionCreationAction),
		}
		pods := []*corev1.Pod{
			makePod("pod-1", "ns1", "fn-multi", "kai-scheduler"),
			makePod("pod-2", "ns1", "fn-multi", "kai-scheduler"),
			makePod("pod-3", "ns1", "fn-multi", "kai-scheduler"),
		}
		fff := &featureflagmock.Fetcher{}
		bc, reg, ctx := setupCache(t, reqs, pods, terminatedSet{}, fff)

		bc.UpdateSchedulerWorkloadMetrics(ctx)

		vals := getGaugeValues(reg)
		assert.Equal(t, float64(1), vals[gaugeKey{"kai-scheduler", "function"}])
	})

	t.Run("gauge decreases when workloads terminate", func(t *testing.T) {
		reqs := []*nvcav2beta1.ICMSRequest{
			makeReq("fn-1", common.FunctionCreationAction),
			makeReq("fn-2", common.FunctionCreationAction),
		}
		pods := []*corev1.Pod{
			makePod("pod-1", "ns1", "fn-1", "kai-scheduler"),
			makePod("pod-2", "ns2", "fn-2", "kai-scheduler"),
		}
		fff := &featureflagmock.Fetcher{}
		bc, reg, ctx := setupCache(t, reqs, pods, terminatedSet{}, fff)

		bc.UpdateSchedulerWorkloadMetrics(ctx)
		vals := getGaugeValues(reg)
		assert.Equal(t, float64(2), vals[gaugeKey{"kai-scheduler", "function"}])

		// Now mark fn-2 as terminated and re-run
		bc.icmsRequestHelper = terminatedSet{ids: map[string]bool{"fn-2": true}}
		bc.UpdateSchedulerWorkloadMetrics(ctx)
		vals = getGaugeValues(reg)
		assert.Equal(t, float64(1), vals[gaugeKey{"kai-scheduler", "function"}])
	})
}
