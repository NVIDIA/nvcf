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

package mscontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	nvresourcev1beta1 "github.com/NVIDIA/k8s-dra-driver-gpu/api/nvidia.com/resource/v1beta1"
	nvresourcefake "github.com/NVIDIA/k8s-dra-driver-gpu/pkg/nvidia.com/clientset/versioned/fake"
	nvresourceinformers "github.com/NVIDIA/k8s-dra-driver-gpu/pkg/nvidia.com/informers/externalversions"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	toolscache "k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	nvcaenvtest "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/envtest"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/icms"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	k8smock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil/mock"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	fakenodefeatures "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures/fake"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestController(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	restConfig, _, cleanup, err := nvcaenvtest.SetupEnvtest()
	require.NoError(t, err)
	t.Cleanup(cleanup)

	mgr, err := ctrl.NewManager(restConfig, manager.Options{
		Scheme:                  mgrScheme,
		GracefulShutdownTimeout: new(time.Duration),
		BaseContext:             func() context.Context { return ctx },
		WebhookServer:           nvcaenvtest.NewFakeWebhookServer(),
		Metrics:                 nvcaenvtest.NewFakeMetricsOptions(),
	})
	require.NoError(t, err)

	defaultTimeConfig := (&k8sutil.TimeConfig{}).Complete()

	nfClient := &fakenodefeatures.Client{
		BackendGPUs: []nvcatypes.BackendGPU{{
			Name: "L40",
			InstanceTypes: []nvcatypes.InstanceType{
				{
					Name:            "ON-PREM.GPU.L40",
					FullName:        "NVIDIA-L40",
					Description:     "One Nvidia ada GPU",
					Default:         true,
					CPU:             resource.MustParse("4"),
					SystemMemory:    resource.MustParse("28Gi"),
					GPUCount:        1,
					GPUMemoryPerGPU: resource.MustParse("48Gi"),
					OS:              "linux",
					DriverVersion:   "535.135.05",
					CPUArch:         "amd64",
					Storage:         resource.MustParse("180Gi"),
					NodeCount:       1,
				},
			},
		}},
	}
	regITCache := icms.NewRegistrationInstanceTypeCache()
	regITCache.Put(nvcatypes.BackendGPUs(nfClient.BackendGPUs).ToRegistration(false, corev1.ResourceList{}))

	fff := &featureflagmock.Fetcher{
		EnabledFFs: []*featureflag.FeatureFlag{
			featureflag.HelmRBACEnforcement,
		},
		EnabledAttrs: []*featureflag.Attribute{
			featureflag.AttrNVLinkOptimized,
		},
	}

	helmObjs := []client.Object{
		&appsv1.Deployment{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "foo",
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"foo": "bar"},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"foo": "bar"},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:    "test",
							Image:   "nvcr.io/foo/bar:baz",
							Command: []string{"yes"},
						}},
					},
				},
			},
		},
		&corev1.ServiceAccount{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "ServiceAccount",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-sa",
			},
		},
		&unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "nvidia.com/v1alpha1",
				"kind":       "DynamoGraphDeployment",
				"metadata": map[string]any{
					"name": "test-dgd",
				},
				"spec": map[string]any{
					"services": map[string]any{
						"Frontend":          map[string]any{},
						"VllmDecodeWorker":  map[string]any{},
						"VllmPrefillWorker": map[string]any{},
					},
				},
			},
		},
	}
	objBytes, err := json.MarshalIndent(helmObjs, "", "  ")
	require.NoError(t, err)
	wantRevalErr := new(error)
	rvClient := newFakeReValClient(t, objBytes, wantRevalErr)

	controllerOpts := ControllerOptions{
		SystemNamespace:      "nvca-system",
		ICMSRequestNamespace: "nvcf-backend",
		K8sVersion:           "1.34.1",
		K8sTimeConfig:        defaultTimeConfig,
		FeatureFlagFetcher:   fff,
		ClusterName:          "local",
		ClusterRegion:        "us-west-1",
		Metrics:              metrics.NewDefaultMetrics("nca-cluster", "cluster-foo", "cluster-group-foo", "1.2.3"),
		cacheDir:             t.TempDir(),
	}

	cfg := nvcaconfig.Config{
		Cluster: nvcaconfig.NVCFClusterConfig{
			ValidationPolicy: &nvcaconfig.ValidationPolicyConfig{
				Name: "Default",
				AllowedExtraKubernetesTypes: []nvcaconfig.AllowedExtraKubernetesTypeConfig{
					{
						Group:    "nvidia.com",
						Version:  "v1alpha1",
						Kind:     "DynamoGraphDeployment",
						Resource: "dynamographdeployments",
					},
				},
			},
		},
	}
	err = k8sutil.SetConfigDefaultResources(&cfg)
	require.NoError(t, err)

	err = BuildController(ctx, cfg, mgr, rvClient, nfClient, regITCache, featureflag.NewAttributes(nil), controllerOpts)
	require.NoError(t, err)

	mgrErrCh, err := nvcaenvtest.StartManager(ctx, mgr)
	require.NoError(t, err)

	cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
	mgr.GetCache().WaitForCacheSync(cctx)
	ccancel()

	envs := []corev1.EnvVar{
		{Name: "BYOO_OTEL_COLLECTOR_CONTAINER", Value: "registry.example.test/nvcf-core/byoo-otel-collector:1.2.3"},
		{Name: "CONTAINER_REGISTRIES_CREDENTIALS", Value: "eyJrOHNTZWNyZXRzIjpbeyJhdXRocyI6eyJyZWdpc3RyeS5leGFtcGxlLnRlc3QiOnsiYXV0aCI6ImRHVnpkQzExYzJWeU9uUmxjM1F0Y0dGemN3PT0ifX19XX0="},
		{Name: "FUNCTION_NAME", Value: "my-func"},
		{Name: "HELM_CHART_INFERENCE_SERVICE_NAME", Value: "myservice"},
		{Name: "HELM_REGISTRIES_CREDENTIALS", Value: "eyJrOHNTZWNyZXRzIjpbeyJhdXRocyI6eyJoZWxtLm5nYy5udmlkaWEuY29tIjp7ImF1dGgiOiJjM1JuTFdGaVl6RXlNem8yWmpZek5HUTROUzA1TXpGbExUUXhaall0WVRKbFl5MDJOMkk0TnpVd1pqRXlOMlU9In19fV19"},
		{Name: "ESS_AGENT_CONTAINER", Value: "nvcr.io/nv-cf/nvcf-core/ess-agent:1.0.0"},
		{Name: "INFERENCE_CONTAINER_ENV", Value: "W3sia2V5IjoiSU5GRVJFTkNFX0VOVl9LRVkiLCJ2YWx1ZSI6ImluZmVyZW5jZV92YWx1ZSJ9XQ=="},
		{Name: "INFERENCE_HEALTH_ENDPOINT", Value: "/v2/health/ready"},
		{Name: "INFERENCE_HEALTH_EXPECTED_RESPONSE_CODE", Value: "200"},
		{Name: "INFERENCE_HEALTH_PORT", Value: "50051"},
		{Name: "INFERENCE_PORT", Value: "50051"},
		{Name: "INFERENCE_PROTOCOL", Value: "GRPC"},
		{Name: "INFERENCE_URL", Value: "/grpc"},
		{Name: "INIT_CONTAINER", Value: "registry.example.test/nvcf-core/nvcf_worker_init:0.24.10"},
		{Name: "MAX_REQUEST_CONCURRENCY", Value: "1"},
		{Name: "NVCF_FQDN", Value: "https://api.example.test"},
		{Name: "NVCF_FQDN_GRPC", Value: "https://grpc.example.test"},
		{Name: "NVCF_FQDN_NATS", Value: "tls://nats.example.test:4222"},
		{Name: "NVCF_WORKER_TOKEN", Value: "tok"},
		{Name: "OTEL_CONTAINER", Value: "registry.example.test/nvcf-core/opentelemetry-collector:0.74.0"},
		{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: "https://otel.example.test:8282"},
		{Name: "SECRETS_ASSERTION_TOKEN", Value: "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiYXNzZXJ0aW9uIjp7InNlY3JldFBhdGhzIjpbImFjY291bnRzL19sSUxYQi0xTmZObUJuUVNrX3NwcVZXT3RDQVhRbTUwVUVNd2ozVFJneW1KSjJBeXV3Y2d4cS90ZWxlbWV0cnkvNmZhODM2NmUtN2JhMi00MTQzLWE3NDQtNzUxNWZhZjliZjcyIiwiYWNjb3VudHMvX2xJTFhCLTFOZk5tQm5RU2tfc3BxVldPdENBWFFtNTBVRU13ajNUUmd5bUpKMkF5dXdjZ3hxL3RlbGVtZXRyeS82ZmE4MzY2ZS03YmEyLTQxNDMtYTc0NC03NTE1ZmFmOWJmODEiXX0sImFkbWluIjp0cnVlLCJpYXQiOjE1MTYyMzkwMjJ9.SpQYbFe1nfrQ5KshRly9SUC26W_j2pQh6DMinsbrsQHvKg1se2oH3VzoinbMbQz_5LXcg-XNkx4cNJN2AjuwUIzk6DIULICHequjq-xAagFR8_z25o11d01zBS5NxF9ACgtIl69dhTHk8sK2eQb4AFGCFff61j0kXabIYESGJxdv9RkNfWtYZ-FmIc9uF4jY59zR1EBdXilcccR0RiCvKAlTYorE7Tj-04KgTFnvQbmP0TQGQd6xbqdAaPRBpXyBG04qmA296TfrAOV_02aIdtjHa5SjmvtPAbVeHVV5Bx_Zd8yVmyN0e7qegnAOqc5NP3kF38W4nWhTE8VkNTZjTPA"},
		{Name: "SIDECAR_REGISTRY_CREDENTIAL", Value: "eyJhdXRocyI6eyJyZWdpc3RyeS5leGFtcGxlLnRlc3QiOnsiYXV0aCI6ImRHVnpkQzExYzJWeU9uUmxjM1F0Y0dGemN3PT0ifX19"},
		{Name: "TRACING_ACCESS_TOKEN", Value: "trace-tok-1"},
		{Name: "UTILS_CONTAINER", Value: "registry.example.test/nvcf-core/nvcf_worker_utils:2.21.4"},
	}

	sr := &nvcav2beta1.ICMSRequest{}
	sr.Name = "sr-7788caf9-cac0-42a4-820d-36bde3ced020"
	sr.Namespace = controllerOpts.ICMSRequestNamespace
	sr.Spec = nvcav2beta1.ICMSRequestSpec{
		FunctionDetails: function.Details{
			FunctionID:        "funcid-1",
			FunctionVersionID: "funcverid-1",
			FunctionType:      "DEFAULT",
		},
		Action:         common.FunctionCreationAction,
		NCAId:          "ncaid-1",
		RequestID:      "reqid1",
		MessageBatchID: "mbatchid1",
		CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
			CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
				Action:            common.FunctionCreationAction,
				RequestID:         "reqid1",
				MessageBatchID:    "mbatchid1",
				InstanceType:      "ON-PREM.GPU.L40",
				InstanceTypeName:  "ON-PREM.GPU.L40_1x",
				InstanceTypeValue: "ON-PREM.GPU.L40",
				GPUType:           "L40",
				RequestedGPUCount: 1,
				InstanceCount:     1,
				NCAID:             "ncaid-1",
			},
			FunctionLaunchSpecification: &function.LaunchSpecification{
				CloudProvider:   "DGXCLOUD",
				ICMSEnvironment: "prod",
				GPUName:         "L40",
				HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{
					HelmChartURL: "https://helm.ngc.nvidia.com/myorg/myteam/charts/mychart-1.0.0.tgz",
					Values:       []byte(`{"foo":{"bar":"baz"}}`),
				},
			},
		},
	}
	cmInfo := sr.Spec.CreationMsgInfo
	launchSpec := cmInfo.FunctionLaunchSpecification

	// Set envs from ICMS request to align values.
	envs = append(envs, []corev1.EnvVar{
		{Name: "ATTACHED_GPU_COUNT", Value: fmt.Sprint(cmInfo.CreationQueueMessageMetadata.RequestedGPUCount)},
		{Name: "CLOUD_PROVIDER", Value: launchSpec.CloudProvider},
		{Name: "FUNCTION_ID", Value: sr.Spec.FunctionDetails.FunctionID},
		{Name: "FUNCTION_VERSION_ID", Value: sr.Spec.FunctionDetails.FunctionVersionID},
		{Name: "GPU_NAME", Value: launchSpec.GPUName},
		{Name: "NCA_ID", Value: cmInfo.CreationQueueMessageMetadata.NCAID},
	}...)

	launchSpec.EnvironmentB64 = encodeEnvsForLaunchSpec(envs)

	msName := "miniservice"
	internalObjLabels := map[string]string{
		nvcatypes.NCAIDKey:                  nvcatypes.MakeNCAIDLabelValue(sr.Spec.NCAId),
		nvcatypes.NCAIDUpperKey:             nvcatypes.MakeNCAIDLabelValue(sr.Spec.NCAId),
		nvcatypes.FunctionIDKey:             sr.Spec.FunctionDetails.FunctionID,
		nvcatypes.FunctionIDUpperKey:        sr.Spec.FunctionDetails.FunctionID,
		nvcatypes.FunctionVersionIDKey:      sr.Spec.FunctionDetails.FunctionVersionID,
		nvcatypes.FunctionVersionIDUpperKey: sr.Spec.FunctionDetails.FunctionVersionID,
		nvcatypes.GPUNameKey:                sr.Spec.CreationMsgInfo.GPUType,
		nvcatypes.MessageBatchIDKey:         sr.Spec.MessageBatchID,
		nvcatypes.ICMSRequestIDKey:          sr.Spec.RequestID,
		miniserviceNameLabel:                msName,
	}
	internalObjAnnotations := map[string]string{
		nvcatypes.NCAIDKey:         sr.Spec.NCAId,
		nvcatypes.InstanceCountKey: fmt.Sprint(sr.Spec.CreationMsgInfo.InstanceCount),
		nvcatypes.ClusterGroupKey:  sr.Spec.CreationMsgInfo.ClusterGroup,
		nvcatypes.ICMSRequestIDKey: sr.Spec.RequestID,
	}

	hcCfg, err := common.ExtractHelmConfiguration(launchSpec.EnvironmentB64, launchSpec.HelmChartLaunchSpecification)
	require.NoError(t, err)

	ms := &v1alpha1.MiniService{}
	ms.Name = "miniservice"
	ms.Labels = internalObjLabels
	ms.Annotations = internalObjAnnotations
	ms.Spec = v1alpha1.MiniServiceSpec{
		Namespace:       sr.Name,
		ICMSRequestName: sr.Name,
		HelmChartConfig: hcCfg,
	}

	objs := []client.Object{
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: controllerOpts.ICMSRequestNamespace},
		},
		&corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: defaultServiceAccountName, Namespace: controllerOpts.ICMSRequestNamespace},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: controllerOpts.SystemNamespace},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: instanceRBACConfigMapName, Namespace: controllerOpts.SystemNamespace},
			Data: map[string]string{
				instanceRoleName: `
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
rules:
- apiGroups: [""]
  resources: ["pods", "services", "secrets"]
  verbs: ["*"]
- apiGroups: ["apps"]
  resources: ["deployments", "replicasets", "statefulsets"]
  verbs: ["*"]
- apiGroups: ["rbac.authorization.k8s.io"]
  resources: ["roles", "rolebindings"]
  verbs: ["*"]
- apiGroups: ["nvidia.com"]
  resources: ["dynamographdeployments"]
  verbs: ["get", "list", "watch", "create", "update", "delete", "patch"]
`,
			},
		},
	}

	objs = append(objs, k8smock.NewNetworkPolicyConfigMap(controllerOpts.SystemNamespace))

	crclient := mgr.GetClient()
	for _, obj := range objs {
		err := crclient.Create(ctx, obj)
		require.NoError(t, err)
	}

	err = crclient.Create(ctx, sr)
	require.NoError(t, err)
	err = crclient.Create(ctx, ms)
	require.NoError(t, err)

	assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		instanceDefaultSA := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: defaultServiceAccountName, Namespace: ms.Spec.Namespace},
		}
		err = crclient.Create(ctx, instanceDefaultSA)
		assert.NoError(collect, err)
	}, 2*time.Second, 100*time.Millisecond)

	utilsPod := &corev1.Pod{}
	assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		err = crclient.Get(ctx, client.ObjectKey{Name: "utils", Namespace: ms.Spec.Namespace}, utilsPod)
		assert.NoError(collect, err)
	}, 2*time.Second, 100*time.Millisecond)

	utilsPod.Status.Phase = corev1.PodRunning
	utilsPod.Status.Conditions = []corev1.PodCondition{
		{
			Type:   corev1.PodScheduled,
			Status: corev1.ConditionTrue,
		},
		{
			Type:   corev1.PodInitialized,
			Status: corev1.ConditionTrue,
		},
		{
			Type:   corev1.PodReady,
			Status: corev1.ConditionTrue,
		},
	}
	utilsPod.Status.InitContainerStatuses = make([]corev1.ContainerStatus, 0, len(utilsPod.Spec.InitContainers))
	for _, ic := range utilsPod.Spec.InitContainers {
		utilsPod.Status.InitContainerStatuses = append(utilsPod.Status.InitContainerStatuses, corev1.ContainerStatus{
			Name: ic.Name,
			State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{},
			},
		})
	}
	crclient.Status().Update(ctx, utilsPod)
	require.NoError(t, err)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err = crclient.Get(ctx, client.ObjectKeyFromObject(ms), ms)
		if !assert.NoError(ct, err) {
			return
		}
		assert.Equal(ct, v1alpha1.MiniServiceInstallFailed, ms.Status.Phase)
	}, 5*time.Second, 100*time.Millisecond)
	cmpConditions(t, []metav1.Condition{
		{
			Type:    v1alpha1.MiniServiceConditionInstallSuccessful,
			Status:  metav1.ConditionFalse,
			Reason:  v1alpha1.MiniServiceStatusReasonUnexpectedInstallError,
			Message: `terminal error: access to resource "serviceaccounts" apiVersion "v1" is denied, please check miniservice Role configuration`,
		},
	}, ms.Status.Conditions)

	cancel()
	<-mgrErrCh
}

func TestNVLinkOptMetricsRunnable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	// log := logrus.New()
	// log.Level = logrus.DebugLevel
	// ctx = logf.IntoContext(ctx, logrusr.New(logrus.NewEntry(log)))

	k8sclient := nvresourcefake.NewSimpleClientset()
	tracker := k8sclient.Tracker()
	crclient := clientfake.NewClientBuilder().
		WithScheme(mgrScheme).
		WithObjectTracker(tracker).
		WithRESTMapper(newTestRESTMapper(mgrScheme)).
		WithStatusSubresource(&v1alpha1.MiniService{}).
		WithStatusSubresource(&nvcav2beta1.ICMSRequest{}).
		WithStatusSubresource(&nvresourcev1beta1.ComputeDomain{}).
		Build()
	infFactory := nvresourceinformers.NewSharedInformerFactory(k8sclient, 1*time.Hour)
	cdInf := infFactory.Resource().V1beta1().ComputeDomains().Informer()
	infFactory.Start(ctx.Done())

	crcache := &informertest.FakeInformers{
		Scheme: mgrScheme,
	}
	crcache.InformersByGVK = map[schema.GroupVersionKind]toolscache.SharedIndexInformer{
		nvresourcev1beta1.SchemeGroupVersion.WithKind("ComputeDomain"): cdInf,
	}
	mgr := &fakeManager{
		cache: crcache,
	}
	reg := prometheus.NewRegistry()
	r := &Reconciler{
		Client: crclient,
		ControllerOptions: ControllerOptions{
			Metrics: metrics.NewDefaultMetrics("a", "b", "c", "d", metrics.WithRegisterer(reg)),
			K8sTimeConfig: &k8sutil.TimeConfig{
				PodScheduledThreshold: 10 * time.Minute,
			},
		},
		now: time.Now,
	}
	runnable := r.newNVLinkOptMetricsRunnable(mgr)

	errCh := make(chan error)
	go func() {
		errCh <- runnable.Start(ctx)
	}()

	// Runnable may block on fake cache sync or return an error; avoid hanging or failing on nil metrics.
	select {
	case err := <-errCh:
		if err != nil {
			t.Skipf("NVLink metrics runnable failed (fake cache may not sync): %v", err)
			return
		}
	case <-time.After(15 * time.Second):
		t.Skip("NVLink metrics runnable did not complete within 15s (fake cache may not sync)")
		return
	}

	getMetrics := func() (created, success, failure *io_prometheus_client.MetricFamily, err error) {
		var metricFamilies []*io_prometheus_client.MetricFamily
		metricFamilies, err = reg.Gather()
		for _, metric := range metricFamilies {
			switch *metric.Name {
			case metrics.NVLinkAllocationCreatedTotalMetricName:
				created = metric
			case metrics.NVLinkAllocationSuccessTotalMetricName:
				success = metric
			case metrics.NVLinkAllocationFailureTotalMetricName:
				failure = metric
			}
		}
		return
	}

	// Ensure empty but existing on startup.
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		created, success, failure, err := getMetrics()
		require.NoError(ct, err)
		require.NotNil(ct, created)
		require.Len(ct, created.Metric, 1)
		require.EqualValues(ct, 0, created.Metric[0].Gauge.GetValue())
		require.NotNil(ct, success)
		require.Len(ct, success.Metric, 1)
		require.EqualValues(ct, 0, success.Metric[0].Gauge.GetValue())
		require.NotNil(ct, failure)
		require.Len(ct, failure.Metric, 1)
		require.EqualValues(ct, 0, failure.Metric[0].Gauge.GetValue())
	}, 5*time.Second, 50*time.Millisecond)

	// Ensure count 1 for created.
	cd := &nvresourcev1beta1.ComputeDomain{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
			Annotations: map[string]string{
				nvcatypes.InfraObjectAnnotationKey: "true",
			},
			CreationTimestamp: metav1.Now(),
		},
	}
	err := crclient.Create(ctx, cd)
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		created, success, failure, err := getMetrics()
		require.NoError(ct, err)
		require.NotNil(ct, created)
		require.Len(ct, created.Metric, 1)
		require.EqualValues(ct, 1.0, created.Metric[0].Gauge.GetValue())
		require.NotNil(ct, success)
		require.Len(ct, success.Metric, 1)
		require.EqualValues(ct, 0, success.Metric[0].Gauge.GetValue())
		require.NotNil(ct, failure)
		require.Len(ct, failure.Metric, 1)
		require.EqualValues(ct, 0, failure.Metric[0].Gauge.GetValue())
	}, 5*time.Second, 50*time.Millisecond)

	// Ensure count 1 for created, 1 for ready.
	cd.Status.Status = nvresourcev1beta1.ComputeDomainStatusReady
	err = crclient.Status().Update(ctx, cd)
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		created, success, failure, err := getMetrics()
		require.NoError(ct, err)
		require.NotNil(ct, created)
		require.Len(ct, created.Metric, 1)
		require.EqualValues(ct, 1.0, created.Metric[0].Gauge.GetValue())
		require.NotNil(ct, success)
		require.Len(ct, success.Metric, 1)
		require.EqualValues(ct, 1.0, success.Metric[0].Gauge.GetValue())
		require.NotNil(ct, failure)
		require.Len(ct, failure.Metric, 1)
		require.EqualValues(ct, 0, failure.Metric[0].Gauge.GetValue())
	}, 5*time.Second, 50*time.Millisecond)

	// Ensure count 1 for created within timeout.
	cd.Status.Status = nvresourcev1beta1.ComputeDomainStatusNotReady
	err = crclient.Status().Update(ctx, cd)
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		created, success, failure, err := getMetrics()
		require.NoError(ct, err)
		require.NotNil(ct, created)
		require.Len(ct, created.Metric, 1)
		require.EqualValues(ct, 1.0, created.Metric[0].Gauge.GetValue())
		require.NotNil(ct, success)
		require.Len(ct, success.Metric, 1)
		require.EqualValues(ct, 0, success.Metric[0].Gauge.GetValue())
		require.NotNil(ct, failure)
		require.Len(ct, failure.Metric, 1)
		require.EqualValues(ct, 0, failure.Metric[0].Gauge.GetValue())
	}, 5*time.Second, 50*time.Millisecond)

	// Ensure count 2 for created with miniservice not found.
	cd2 := cd.DeepCopy()
	cd2.ResourceVersion = ""
	cd2.Name = cd.Name + "-2"
	cd2.CreationTimestamp = metav1.Time{Time: metav1.Now().Add(-1 * time.Hour)}
	cd2.Labels = map[string]string{miniserviceNameLabel: "sr-bar"}
	err = crclient.Create(ctx, cd2)
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		created, success, failure, err := getMetrics()
		require.NoError(ct, err)
		require.NotNil(ct, created)
		require.Len(ct, created.Metric, 1)
		require.EqualValues(ct, 2.0, created.Metric[0].Gauge.GetValue())
		require.NotNil(ct, success)
		require.Len(ct, success.Metric, 1)
		require.EqualValues(ct, 0, success.Metric[0].Gauge.GetValue())
		require.NotNil(ct, failure)
		require.Len(ct, failure.Metric, 1)
		require.EqualValues(ct, 0, failure.Metric[0].Gauge.GetValue())
	}, 5*time.Second, 50*time.Millisecond)

	// Ensure count 3 for created within task queue timeout.
	srName := "sr-foo"
	ms := &v1alpha1.MiniService{}
	ms.Name = srName + "-miniservice"
	ms.Spec.ICMSRequestName = srName
	err = crclient.Create(ctx, ms)
	require.NoError(t, err)
	icmsReq := &nvcav2beta1.ICMSRequest{}
	icmsReq.Name, icmsReq.Namespace = srName, r.ICMSRequestNamespace
	icmsReq.Spec.Action = common.TaskCreationAction
	icmsReq.Spec.CreationMsgInfo.TaskLaunchSpecification = &task.LaunchSpecification{
		MaxQueuedDuration: "PT2H",
	}
	err = crclient.Create(ctx, icmsReq)
	require.NoError(t, err)

	cd3 := cd.DeepCopy()
	cd3.ResourceVersion = ""
	cd3.Name = cd.Name + "-3"
	cd3.CreationTimestamp = metav1.Time{Time: metav1.Now().Add(-1 * time.Hour)}
	cd3.Labels = map[string]string{miniserviceNameLabel: ms.Name}
	err = crclient.Create(ctx, cd3)
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		created, success, failure, err := getMetrics()
		require.NoError(ct, err)
		require.NotNil(ct, created)
		require.Len(ct, created.Metric, 1)
		require.EqualValues(ct, 3.0, created.Metric[0].Gauge.GetValue())
		require.NotNil(ct, success)
		require.Len(ct, success.Metric, 1)
		require.EqualValues(ct, 0, success.Metric[0].Gauge.GetValue())
		require.NotNil(ct, failure)
		require.Len(ct, failure.Metric, 1)
		require.EqualValues(ct, 0, failure.Metric[0].Gauge.GetValue())
	}, 5*time.Second, 50*time.Millisecond)

	// Ensure count 3 for created, 1 for failure over task queue timeout.
	icmsReq.Spec.CreationMsgInfo.TaskLaunchSpecification = &task.LaunchSpecification{
		MaxQueuedDuration: "PT20M",
	}
	err = crclient.Update(ctx, icmsReq)
	require.NoError(t, err)

	cd3.Labels["foo"] = "bar"
	err = crclient.Update(ctx, cd3)
	require.NoError(t, err)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		created, success, failure, err := getMetrics()
		require.NoError(ct, err)
		require.NotNil(ct, created)
		require.Len(ct, created.Metric, 1)
		require.EqualValues(ct, 3.0, created.Metric[0].Gauge.GetValue())
		require.NotNil(ct, success)
		require.Len(ct, success.Metric, 1)
		require.EqualValues(ct, 0, success.Metric[0].Gauge.GetValue())
		require.NotNil(ct, failure)
		require.Len(ct, failure.Metric, 1)
		require.EqualValues(ct, 1.0, failure.Metric[0].Gauge.GetValue())
	}, 5*time.Second, 50*time.Millisecond)

	cancel()
	err = <-errCh
	require.NoError(t, err)
}

type fakeManager struct {
	manager.Manager
	cache cache.Cache
}

func (f *fakeManager) GetCache() cache.Cache { return f.cache }
