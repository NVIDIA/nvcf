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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	nvresourcev1beta1 "github.com/NVIDIA/k8s-dra-driver-gpu/api/nvidia.com/resource/v1beta1"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/bombsimon/logrusr/v4"
	"github.com/google/go-cmp/cmp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	apischema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/icms"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/miniservice/chartcache"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/otel"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	k8smock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil/mock"
	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcfdra "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/dra"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	fakenodefeatures "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures/fake"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func newTestContext(level ...logrus.Level) context.Context {
	ctx := context.Background()
	ctx = core.WithDefaultLogger(ctx)
	log := core.GetLogger(ctx)
	if len(level) != 0 {
		log.Logger.SetLevel(level[0])
	}
	k8sLogger := logrusr.New(log, logrusr.WithReportCaller())
	ctx = ctrllog.IntoContext(ctx, k8sLogger)
	return ctx
}

func newTestRESTMapper(s *runtime.Scheme) meta.RESTMapper {
	gvs := s.PreferredVersionAllGroups()
	rm := meta.NewDefaultRESTMapper(gvs)
	for gvk := range s.AllKnownTypes() {
		scope := meta.RESTScopeNamespace
		switch gvk.Kind {
		case "MiniService":
			scope = meta.RESTScopeRoot
		}
		rm.Add(gvk, scope)
	}
	return rm
}

func newFakeClient(s *runtime.Scheme, objs ...client.Object) (client.Client, k8stesting.ObjectTracker) {
	tracker := k8stesting.NewObjectTracker(s, scheme.Codecs.UniversalDecoder())
	b := clientfake.NewClientBuilder().
		WithScheme(s).
		WithObjectTracker(tracker).
		WithRESTMapper(newTestRESTMapper(s)).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.MiniService{}).
		WithStatusSubresource(&nvcav1new.StorageRequest{}).
		WithStatusSubresource(&nvcav2beta1.StorageRequest{}).
		WithStatusSubresource(&nvcav2beta1.ICMSRequest{}).
		WithStatusSubresource(&nvresourcev1beta1.ComputeDomain{})

	for fieldPath, extractValues := range eventIndexSet {
		b.WithIndex(&corev1.Event{}, fieldPath, extractValues)
	}

	return b.Build(), tracker
}

func newFakeClientWithInterceptors(s *runtime.Scheme, funcs interceptor.Funcs, objs ...client.Object) (client.Client, k8stesting.ObjectTracker) {
	tracker := k8stesting.NewObjectTracker(s, scheme.Codecs.UniversalDecoder())
	b := clientfake.NewClientBuilder().
		WithScheme(s).
		WithObjectTracker(tracker).
		WithRESTMapper(newTestRESTMapper(s)).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.MiniService{}).
		WithStatusSubresource(&nvcav1new.StorageRequest{}).
		WithStatusSubresource(&nvcav2beta1.StorageRequest{}).
		WithStatusSubresource(&nvcav2beta1.ICMSRequest{}).
		WithStatusSubresource(&nvresourcev1beta1.ComputeDomain{}).
		WithInterceptorFuncs(funcs)

	for fieldPath, extractValues := range eventIndexSet {
		b.WithIndex(&corev1.Event{}, fieldPath, extractValues)
	}

	return b.Build(), tracker
}

// filterNVCFAnnotations filters out nvcf.nvidia.io/ annotations from an object's annotations.
func filterNVCFAnnotations(annotations map[string]string) map[string]string {
	result := make(map[string]string)
	for k, v := range annotations {
		// Skip NVCF annotations added by the workload environment mutator
		if !strings.HasPrefix(k, "nvcf.nvidia.io/") {
			result[k] = v
		}
	}
	return result
}

// filterNVCFEnvVars filters out NVCF_ and NVCT_ prefixed environment variables.
func filterNVCFEnvVars(envs []corev1.EnvVar) []corev1.EnvVar {
	result := make([]corev1.EnvVar, 0, len(envs))
	for _, env := range envs {
		// Skip NVCF environment variables added by the workload environment mutator
		if !strings.HasPrefix(env.Name, "NVCF_") && !strings.HasPrefix(env.Name, "NVCT_") {
			result = append(result, env)
		}
	}
	return result
}

func TestReconcile_Function(t *testing.T) {
	ctx := newTestContext()
	testScheme := mgrScheme

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

	r := &Reconciler{
		ControllerOptions: ControllerOptions{
			SystemNamespace:      "nvca-system",
			ICMSRequestNamespace: "nvcf-backend",
			K8sVersion:           "1.29.5",
			FeatureFlagFetcher: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{
					featureflag.HelmRBACEnforcement,
					featureflag.HelmResourceConstraints,
					featureflag.EnforceHelmFunctionResourceLimits,
					featureflag.InfraResourceOverhead,
					&featureflag.HelmSharedStorage.FeatureFlag,
					featureflag.BYOObservability,
					featureflag.MiniServiceRevisionHistory,
				},
			},
			K8sTimeConfig: (&k8sutil.TimeConfig{
				FailingObjectsBackoffTimeout:         1 * time.Millisecond, // Very short for tests
				FailingObjectsBackoffRequeueInterval: 1 * time.Millisecond, // Very short for tests
			}).Complete(),
			ImageCredentialHelperImage: "stg.nvcr.io/nv-cf/nvcf-core/image-credential-helper:latest",
			OverheadGetter:             enforce.NoOpInfraOverheadGetter,
			Metrics:                    metrics.NewDefaultMetrics("test-nca-id", "test-cluster", "test-group", "test-version", metrics.WithRegisterer(prometheus.NewRegistry())),
		},
		Decoder:               serializer.NewCodecFactory(testScheme).UniversalDeserializer(),
		NFClient:              nfClient,
		tracer:                otel.NewTracer(),
		eventRecorder:         record.NewFakeRecorder(10),
		chartCache:            chartcache.New(t.TempDir()),
		regITCache:            regITCache,
		now:                   time.Now,
		newPermissionsChecker: newFakePermissionsChecker,
	}
	r.statusCheckers = r.makeStatusCheckers()
	// Impersonation can't be mocked using the fake client.
	// Envtest handles this.
	r.newImpersonatingClient = func(_ string) (client.Client, error) {
		return r.Client, nil
	}

	configuredToleration := corev1.Toleration{
		Key:      "nvidia.com/test-workload",
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoSchedule,
	}
	r.cfg.Agent.SharedStorage.Server.Image = "smb:latest"
	exporterBatchMaxSizeBytes := int64(1000000)
	r.cfg.Agent.BYOOLogChunking = nvcaconfig.BYOOLogChunkingConfig{
		MaxBodyBytes:              983040,
		DryRun:                    true,
		ExporterBatchMaxSizeBytes: &exporterBatchMaxSizeBytes,
	}
	r.cfg.Agent.BYOOMetricSubset = nvcaconfig.BYOOMetricSubsetConfig{
		Enabled:      true,
		FilterConfig: "error_mode: ignore\nmetric_conditions:\n  - 'metric.name == \"drop\"'\n",
	}
	r.cfg.Agent.BYOOWorkloadMetrics = nvcaconfig.BYOOWorkloadMetricsConfig{
		DropLabels: []string{"metric_subset_enabled", "custom_label"},
	}
	err := k8sutil.SetConfigDefaultResources(&r.cfg)
	require.NoError(t, err)
	r.cfg.Workload.Tolerations = []corev1.Toleration{configuredToleration}

	err = r.chartCache.Start(ctx)
	require.NoError(t, err)

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
				Template: corev1.PodTemplateSpec{
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
		// SA's are not in the core set of allowed objects but NVCA leaves that decision
		// to the ReVal service and applies whatever it receives (within RBAC scopes).
		&corev1.ServiceAccount{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "ServiceAccount",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-sa",
			},
		},
	}
	objBytes, err := json.MarshalIndent(helmObjs, "", "  ")
	require.NoError(t, err)
	wantRevalErr := new(error)
	rvClient := newFakeReValClient(t, objBytes, wantRevalErr)
	r.ReValClient = rvClient

	envs := []corev1.EnvVar{
		{Name: "BYOO_OTEL_COLLECTOR_CONTAINER", Value: "registry.example.test/nvcf-core/byoo-otel-collector:1.2.3"},
		{Name: common.ContainerRegistriesCredentialsEnv, Value: "eyJrOHNTZWNyZXRzIjpbeyJhdXRocyI6eyJyZWdpc3RyeS5leGFtcGxlLnRlc3QiOnsiYXV0aCI6ImRHVnpkQzExYzJWeU9uUmxjM1F0Y0dGemN3PT0ifX19XX0="},
		{Name: "FUNCTION_NAME", Value: "my-func"},
		{Name: "HELM_CHART_INFERENCE_SERVICE_NAME", Value: "myservice"},
		{Name: common.HelmRegistriesCredentialsEnv, Value: "eyJrOHNTZWNyZXRzIjpbeyJhdXRocyI6eyJoZWxtLm5nYy5udmlkaWEuY29tIjp7ImF1dGgiOiJjM1JuTFdGaVl6RXlNem8yWmpZek5HUTROUzA1TXpGbExUUXhaall0WVRKbFl5MDJOMkk0TnpVd1pqRXlOMlU9In19fV19"},
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
		{Name: common.SidecarRegistryCredentialEnv, Value: "eyJhdXRocyI6eyJyZWdpc3RyeS5leGFtcGxlLnRlc3QiOnsiYXV0aCI6ImRHVnpkQzExYzJWeU9uUmxjM1F0Y0dGemN3PT0ifX19"},
		{Name: "TRACING_ACCESS_TOKEN", Value: "trace-tok-1"},
		{Name: "UTILS_CONTAINER", Value: "registry.example.test/nvcf-core/nvcf_worker_utils:2.21.4"},
	}

	telLaunchSpec := &common.TelemetriesLaunchSpecification{}
	telLaunchSpec.Telemetries.Logs = &common.Telemetry{
		Protocol: "http",
		Provider: "SPLUNK",
		Endpoint: "endpoint",
		Name:     "splunk-prd",
	}
	telLaunchSpec.Telemetries.Metrics = &common.Telemetry{
		Protocol: "http",
		Provider: "GRAFANA_CLOUD",
		Endpoint: "endpoint",
		Name:     "Grafana_prd",
	}
	telLaunchSpec.Telemetries.Traces = &common.Telemetry{
		Protocol: "http",
		Provider: "SERVICENOW",
		Endpoint: "endpoint:8323",
		Name:     "nv-lightstep-stg",
	}

	sr := &nvcav2beta1.ICMSRequest{}
	sr.Name = "sr-foo"
	sr.Namespace = r.ICMSRequestNamespace
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
					HelmChartURL: "https://helm.ngc.nvidia.com/myorg/myteam/charts/image-segmentation-1.0.3.tgz",
					Values:       []byte(`{"foo":{"bar":"baz"}}`),
				},
				CacheLaunchSpecification: &common.CacheLaunchSpecification{
					CacheArtifacts: true,
					CacheHandle:    "abc123handle",
					CacheSize:      262144000,
				},
				Telemetries: telLaunchSpec,
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
	translatedLabels := map[string]string{
		"ENVIRONMENT":                       launchSpec.ICMSEnvironment,
		nvcatypes.FunctionIDUpperKey:        sr.Spec.FunctionDetails.FunctionID,
		nvcatypes.FunctionVersionIDUpperKey: sr.Spec.FunctionDetails.FunctionVersionID,
		"GPU_COUNT":                         fmt.Sprint(sr.Spec.CreationMsgInfo.RequestedGPUCount),
		nvcatypes.NCAIDUpperKey:             nvcatypes.MakeNCAIDLabelValue(sr.Spec.NCAId),
		"environment":                       launchSpec.ICMSEnvironment,
		"performance_class":                 sr.Spec.CreationMsgInfo.InstanceTypeValue,
		nvcatypes.NCAIDKey:                  nvcatypes.MakeNCAIDLabelValue(sr.Spec.NCAId),
		nvcatypes.FunctionIDKey:             sr.Spec.FunctionDetails.FunctionID,
		nvcatypes.FunctionVersionIDKey:      sr.Spec.FunctionDetails.FunctionVersionID,
		nvcatypes.GPUNameKey:                sr.Spec.CreationMsgInfo.GPUType,
		nvcatypes.MessageBatchIDKey:         sr.Spec.MessageBatchID,
		nvcatypes.ICMSRequestIDKey:          sr.Spec.RequestID,
		miniserviceNameLabel:                msName,
	}
	translatedAnnotations := map[string]string{
		nvcatypes.NCAIDKey:                   sr.Spec.NCAId,
		nvcatypes.InstanceCountKey:           fmt.Sprint(sr.Spec.CreationMsgInfo.InstanceCount),
		nvcatypes.ClusterGroupKey:            sr.Spec.CreationMsgInfo.ClusterGroup,
		nvcatypes.ICMSRequestIDKey:           sr.Spec.RequestID,
		"nvcf.nvidia.io/backend":             "",
		"nvcf.nvidia.io/environment":         launchSpec.ICMSEnvironment,
		"nvcf.nvidia.io/instance-type-name":  sr.Spec.CreationMsgInfo.InstanceTypeName,
		"nvcf.nvidia.io/instance-type-value": sr.Spec.CreationMsgInfo.InstanceTypeValue,
		"nvcf.nvidia.io/region":              "",
		"FUNCTION_NAME":                      "my-func",
		"function-name":                      "my-func",
	}
	translatedInfraAnnotations := mergeMaps(translatedAnnotations, map[string]string{
		nvcatypes.InfraObjectAnnotationKey: "true",
	})

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
			ObjectMeta: metav1.ObjectMeta{Name: r.ICMSRequestNamespace},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: r.SystemNamespace},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: instanceRBACConfigMapName, Namespace: r.SystemNamespace},
			Data: map[string]string{
				instanceRoleName: `
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
rules:
- apiGroups: [""]
  apiVersions: ["v1"]
  resources: ["pods", "services", "secrets"]
  verbs: ["*"]
- apiGroups: ["apps"]
  apiVersions: ["v1"]
  resources: ["deployments"]
  verbs: ["*"]
- apiGroups: ["rbac.authorization.k8s.io"]
  apiVersions: ["v1"]
  resources: ["roles", "rolebindings"]
  verbs: ["*"]
`,
			},
		},
	}

	objs = append(objs, k8smock.NewNetworkPolicyConfigMap(r.SystemNamespace))

	crClient, tracker := newFakeClient(testScheme, append(objs, sr, ms)...)
	r.Client = crClient

	// Reval returns retryable error, reconciler should fail the miniservice.
	*wantRevalErr = fmt.Errorf("some internal error")
	req := reconcile.Request{}
	req.Name = ms.Name
	_, err = r.Reconcile(ctx, req)
	assert.EqualError(t, err, "unexpected HTTP status 500 Internal Server Error")

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)

	// Reval returns non-retryable error, reconciler should fail the miniservice.
	*wantRevalErr = errNonRetry
	_, err = r.Reconcile(ctx, req)
	assert.EqualError(t, err, "terminal error: unexpected HTTP status: 400 Bad Request")

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceInstallFailed, ms.Status.Phase)

	ms.Status.Phase = ""
	err = r.Client.Status().Update(ctx, ms)
	require.NoError(t, err)
	*wantRevalErr = nil

	req = reconcile.Request{}
	req.Name = ms.Name
	_, err = r.Reconcile(ctx, req)
	require.EqualError(t, err, "update default service account with worker image pull secrets: get instance service account: serviceaccounts \"default\" not found")

	createNamespaceDefaultSA(t, ctx, r, ms)

	req = reconcile.Request{}
	req.Name = ms.Name
	gotRes, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, reconcile.Result{}, gotRes)

	// Update image cred job's status as completed so the task can proceed.
	initImageCredsJob := &batchv1.Job{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: sr.Name + "-cred-init", Namespace: r.SystemNamespace}, initImageCredsJob)
	require.NoError(t, err)
	initImageCredsJob.Status = batchv1.JobStatus{
		CompletionTime: &metav1.Time{Time: r.now()},
		Succeeded:      1,
	}
	err = r.Client.Status().Update(ctx, initImageCredsJob)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, reconcile.Result{}, gotRes)

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)

	ns := &corev1.Namespace{}
	ns.Name = ms.Spec.Namespace
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ns), ns)
	require.NoError(t, err)
	// Namespace gets labels from GetLabelsForRequest, which includes function labels for function actions
	expectedNamespaceLabels := nvcatypes.GetLabelsForRequest(sr, r.FeatureFlagFetcher)
	expectedNamespaceLabels["nvca.nvcf.nvidia.io/workload-instance-type"] = "miniservice"
	expectedNamespaceLabels[miniserviceNameLabel] = msName
	assert.Equal(t, expectedNamespaceLabels, ns.Labels)
	assert.Equal(t, internalObjAnnotations, ns.Annotations)

	defaultSA := &corev1.ServiceAccount{}
	err = r.Client.Get(ctx, client.ObjectKey{Namespace: ms.Spec.Namespace, Name: defaultServiceAccountName}, defaultSA)
	require.NoError(t, err)
	if assert.Len(t, defaultSA.ImagePullSecrets, 1) {
		assert.Equal(t, "worker-sr-foo-regcred-0", defaultSA.ImagePullSecrets[0].Name)
	}
	helmSA := &corev1.ServiceAccount{}
	err = r.Client.Get(ctx, client.ObjectKey{Namespace: ms.Spec.Namespace, Name: serviceAccountName}, helmSA)
	require.NoError(t, err)
	if assert.Len(t, helmSA.ImagePullSecrets, 2) {
		assert.Equal(t, "worker-sr-foo-regcred-0", helmSA.ImagePullSecrets[0].Name)
		assert.Equal(t, "workload-sr-foo-regcred-0", helmSA.ImagePullSecrets[1].Name)
	}

	sharedStorageST := &nvcav2beta1.StorageRequest{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: "shared-storage", Namespace: ms.Spec.Namespace}, sharedStorageST)
	require.NoError(t, err)
	// StorageRequest must carry the general MiniService metadata so child events requeue correctly.
	assert.Equal(t, translatedLabels, sharedStorageST.Labels)
	assert.Equal(t, translatedInfraAnnotations, sharedStorageST.Annotations)
	assert.True(t, r.isOwner(ctx, ms, sharedStorageST))
	assert.Equal(t, "smb:latest", sharedStorageST.Spec.SharedStorage.SMBContainerImage)
	assert.Equal(t, "worker-sr-foo-regcred-0", sharedStorageST.Spec.SharedStorage.WorkerPullSecretName)
	assert.Equal(t, []string{
		"worker-sr-foo-regcred-0",
	}, sharedStorageST.Spec.SharedStorage.WorkerPullSecretNames)
	require.NotNil(t, sharedStorageST.Spec.SharedStorage.Server)
	assert.Contains(t, sharedStorageST.Spec.SharedStorage.Server.SMBServerPodTolerations, configuredToleration)

	sharedStorageST.Status.Phase = nvcav2beta1.StorageReady
	sharedStorageST.Status.SharedStorage = &nvcav2beta1.SharedStorageStatus{
		KNS: nvcav2beta1.SharedStorageTypeStatus{
			ReadOnlyPVCName:  "test-kns-ropvc",
			ReadWritePVCName: "test-kns-rwpvc",
		},
		Secrets: nvcav2beta1.SharedStorageTypeStatus{
			ReadOnlyPVCName:  "test-secrets-ropvc",
			ReadWritePVCName: "test-secrets-rwpvc",
		},
	}
	err = r.Client.Status().Update(ctx, sharedStorageST)
	require.NoError(t, err)

	// Storage is ready, reconcile should proceed.
	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, reconcile.Result{}, gotRes)

	// Check prereqs.
	utilsPod := &corev1.Pod{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: "utils", Namespace: ms.Spec.Namespace}, utilsPod)
	require.NoError(t, err)
	expEnvs := []corev1.EnvVar{
		{Name: nvcatypes.InstanceIDEnvKey, Value: ms.Name},
		{Name: InferenceNamespaceEnvKey, Value: ms.Spec.Namespace},
		{Name: nvcatypes.InferenceReadyTimeoutEnvKey, Value: "2h0m0s"},
	}
	for _, c := range utilsPod.Spec.InitContainers {
		assert.Subset(t, c.Env, expEnvs)
		if c.Name == common.InitContainerName {
			assert.Len(t, c.Resources.Limits, 2)
			assert.Len(t, c.Resources.Requests, 2)
		}
	}
	for _, c := range utilsPod.Spec.Containers {
		assert.Subset(t, c.Env, expEnvs)
		if c.Name == common.UtilsContainerName {
			assert.Len(t, c.Resources.Limits, 2)
			assert.Len(t, c.Resources.Requests, 2)
		}
	}
	assert.Contains(t, utilsPod.Spec.Tolerations, configuredToleration)
	assert.Equal(t, mergeMaps(translatedLabels, map[string]string{
		common.BYOOMetricsEgressTargetLabelKey: common.BYOOMetricsEgressTargetLabelValue,
	}), utilsPod.Labels)
	assert.Equal(t, mergeMaps(translatedInfraAnnotations, map[string]string{
		nvcastorage.HelmWebhookSharedStorageKNSReadWritePVCNameAnnotationKey:     "test-kns-rwpvc",
		nvcastorage.HelmWebhookSharedStorageSecretsReadWritePVCNameAnnotationKey: "test-secrets-rwpvc",
		nvcatypes.ICMSRequestIDKey: sr.Spec.RequestID,
	}), utilsPod.Annotations)

	byooSvc := &corev1.Service{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: common.ByooOTelCollectorPodNameBase, Namespace: ms.Spec.Namespace}, byooSvc)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		common.BYOOMetricsEgressTargetLabelKey: common.BYOOMetricsEgressTargetLabelValue,
	}, byooSvc.Spec.Selector)

	sa := &corev1.ServiceAccount{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: serviceAccountName, Namespace: ms.Spec.Namespace}, sa)
	require.NoError(t, err)

	role := &rbacv1.Role{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: serviceAccountName, Namespace: ms.Spec.Namespace}, role)
	require.NoError(t, err)

	roleBinding := &rbacv1.RoleBinding{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: serviceAccountName, Namespace: ms.Spec.Namespace}, roleBinding)
	require.NoError(t, err)

	netPolList := &netv1.NetworkPolicyList{}
	err = r.Client.List(ctx, netPolList, client.InNamespace(ms.Spec.Namespace))
	require.NoError(t, err)
	assert.Len(t, netPolList.Items, 7)

	// Check that initial resources were not created until after the utils pod's init containers complete.
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)
	gotDeployments := &appsv1.DeploymentList{}
	err = r.Client.List(ctx, gotDeployments, client.InNamespace(ms.Spec.Namespace))
	require.NoError(t, err)
	require.Len(t, gotDeployments.Items, 0)

	utilsPod.Status.Phase = corev1.PodPending
	utilsPod.Status.StartTime = &metav1.Time{Time: r.now()}
	utilsPod.Status.InitContainerStatuses = []corev1.ContainerStatus{
		{
			Name: common.InitContainerName,
			State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{},
			},
		},
		{
			Name: common.NewESSInitContainer("", nil).Name,
			State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{},
			},
		},
	}
	utilsPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: common.UtilsContainerName,
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{},
		},
	}}
	utilsPod.Status.Conditions = []corev1.PodCondition{
		{
			Type:   corev1.PodScheduled,
			Status: corev1.ConditionTrue,
		},
		{
			Type:   corev1.PodInitialized,
			Status: corev1.ConditionTrue,
		},
	}
	err = r.Client.Status().Update(ctx, utilsPod)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, reconcile.Result{}, gotRes)

	// Check that initial resources were created.
	err = r.Client.List(ctx, gotDeployments, client.InNamespace(ms.Spec.Namespace))
	require.NoError(t, err)
	require.Len(t, gotDeployments.Items, 1)
	gotDeployment := gotDeployments.Items[0]
	gotDeployment.ObjectMeta.Annotations = filterNVCFAnnotations(gotDeployment.ObjectMeta.Annotations)
	gotDeployment.Spec.Template.ObjectMeta.Annotations = filterNVCFAnnotations(gotDeployment.Spec.Template.ObjectMeta.Annotations)
	for i := range gotDeployment.Spec.Template.Spec.Containers {
		gotDeployment.Spec.Template.Spec.Containers[i].Env = filterNVCFEnvVars(gotDeployment.Spec.Template.Spec.Containers[i].Env)
	}

	expDeployment := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "foo",
			Namespace:       ms.Spec.Namespace,
			ResourceVersion: "1",
			Labels:          translatedLabels,
			Annotations:     filterNVCFAnnotations(translatedAnnotations),
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:    "test",
						Image:   "nvcr.io/foo/bar:baz",
						Command: []string{"yes"},
						Env:     []corev1.EnvVar{},
					}},
				},
			},
		},
	}
	assert.Equal(t, expDeployment, gotDeployment)

	serviceAccounts := &corev1.ServiceAccountList{}
	err = r.Client.List(ctx, serviceAccounts, client.InNamespace(ms.Spec.Namespace))
	require.NoError(t, err)
	require.Len(t, serviceAccounts.Items, 3)
	sort.Slice(serviceAccounts.Items, func(i, j int) bool {
		return serviceAccounts.Items[i].Name < serviceAccounts.Items[j].Name
	})
	gotSA := serviceAccounts.Items[2]
	gotSA.Annotations = filterNVCFAnnotations(gotSA.Annotations)
	assert.Equal(t, corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-sa", Namespace: ms.Spec.Namespace,
			ResourceVersion: "1",
			Labels:          translatedLabels,
			Annotations:     filterNVCFAnnotations(translatedAnnotations),
		},
	}, gotSA)

	// Verify the controller created the metadata ConfigMap with the expected data.
	metadataCM := &corev1.ConfigMap{}
	err = r.Client.Get(ctx, client.ObjectKey{
		Namespace: ms.Spec.Namespace,
		Name:      nvcatypes.MiniserviceMetadataConfigMapName,
	}, metadataCM)
	require.NoError(t, err, "metadata ConfigMap should exist")
	msMeta, err := nvcatypes.FromConfigMapData(metadataCM.Data)
	require.NoError(t, err, "metadata ConfigMap should deserialize")
	assert.Equal(t, common.FunctionCreationAction, msMeta.MessageAction)
	assert.Equal(t, serviceAccountName, msMeta.ServiceAccountName)
	assert.NotEmpty(t, msMeta.EnvVars, "metadata should include workload env vars")
	metaEnv := map[string]string{}
	for _, env := range msMeta.EnvVars {
		metaEnv[env.Name] = env.Value
	}
	assert.NotContains(t, metaEnv, nvcaconfig.BYOOLogChunkMaxBodyBytesEnv)
	assert.NotContains(t, metaEnv, nvcaconfig.BYOOMetricSubsetEnabledEnv)
	otelCollectorEnv := map[string]string{}
	for _, env := range msMeta.OTelCollectorEnvVars {
		otelCollectorEnv[env.Name] = env.Value
	}
	assert.Equal(t, "983040", otelCollectorEnv[nvcaconfig.BYOOLogChunkMaxBodyBytesEnv])
	assert.Equal(t, "true", otelCollectorEnv[nvcaconfig.BYOOLogChunkDryRunEnv])
	assert.Equal(t, "1000000", otelCollectorEnv[nvcaconfig.BYOOLogExporterBatchMaxSizeBytesEnv])
	assert.Equal(t, "true", otelCollectorEnv[nvcaconfig.BYOOMetricSubsetEnabledEnv])
	assert.Contains(t, otelCollectorEnv[nvcaconfig.BYOOMetricSubsetFilterConfigEnv], "metric.name")
	assert.Equal(t, "metric_subset_enabled,custom_label", otelCollectorEnv[nvcaconfig.BYOOWorkloadMetricsDropLabelsEnv])
	assert.Equal(t, nodefeatures.UniformInstanceTypeLabelKey, msMeta.NodeAffinityKey)
	assert.Equal(t, []corev1.Toleration{configuredToleration}, msMeta.Tolerations)

	gotSecrets := &corev1.SecretList{}
	err = r.Client.List(ctx, gotSecrets, client.InNamespace(ms.Spec.Namespace))
	require.NoError(t, err)
	require.Len(t, gotSecrets.Items, 4)
	sort.Slice(gotSecrets.Items, func(i, j int) bool {
		return gotSecrets.Items[i].Name < gotSecrets.Items[j].Name
	})
	assert.Equal(t, corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: common.HelmChartDownloadSecretName, Namespace: ms.Spec.Namespace,
			ResourceVersion: "1",
			Labels:          translatedLabels,
			Annotations:     translatedInfraAnnotations,
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"username": "stg-abc123",
			"password": "6f634d85-931e-41f6-a2ec-67b8750f127e",
		},
	}, gotSecrets.Items[0])
	assert.Equal(t, corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sr-foo-image-creds", Namespace: ms.Spec.Namespace,
			ResourceVersion: "1",
			Labels:          translatedLabels,
			Annotations:     translatedInfraAnnotations,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			common.ContainerRegistriesCredentialsEnv: []byte("eyJrOHNTZWNyZXRzIjpbeyJhdXRocyI6eyJyZWdpc3RyeS5leGFtcGxlLnRlc3QiOnsiYXV0aCI6ImRHVnpkQzExYzJWeU9uUmxjM1F0Y0dGemN3PT0ifX19XX0="),
			common.SidecarRegistryCredentialEnv:      []byte("eyJhdXRocyI6eyJyZWdpc3RyeS5leGFtcGxlLnRlc3QiOnsiYXV0aCI6ImRHVnpkQzExYzJWeU9uUmxjM1F0Y0dGemN3PT0ifX19"),
		},
	}, gotSecrets.Items[1])
	assert.Equal(t, corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-sr-foo-regcred-0", Namespace: ms.Spec.Namespace,
			ResourceVersion: "1",
			Labels:          translatedLabels,
			Annotations:     translatedInfraAnnotations,
		},
		StringData: map[string]string{
			corev1.DockerConfigJsonKey: `{"auths":{"registry.example.test":{"auth":"dGVzdC11c2VyOnRlc3QtcGFzcw=="}}}`,
		},
		Type: corev1.SecretTypeDockerConfigJson,
	}, gotSecrets.Items[2])
	assert.Equal(t, corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "workload-sr-foo-regcred-0", Namespace: ms.Spec.Namespace,
			ResourceVersion: "1",
			Labels:          translatedLabels,
			Annotations:     translatedInfraAnnotations,
		},
		StringData: map[string]string{
			corev1.DockerConfigJsonKey: `{"auths":{"registry.example.test":{"auth":"dGVzdC11c2VyOnRlc3QtcGFzcw=="}}}`,
		},
		Type: corev1.SecretTypeDockerConfigJson,
	}, gotSecrets.Items[3])

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceInstalled, ms.Status.Phase)
	assert.Equal(t, int64(0), ms.Status.Revision, "initial install should be revision 0")
	assert.Equal(t, ms.Generation, ms.Status.ObservedGeneration, "observedGeneration should match generation after install")

	revisionCM := &corev1.ConfigMap{}
	err = r.Client.Get(ctx, client.ObjectKey{Namespace: ms.Spec.Namespace, Name: "miniservice-revision-v0"}, revisionCM)
	require.NoError(t, err, "revision 0 ConfigMap should exist after install")
	assert.Equal(t, string(ms.Spec.HelmChartConfig.Values), revisionCM.Data["values"])
	assert.Equal(t, ms.Name, revisionCM.Labels[miniserviceNameLabel])

	gotData, found, err := r.getRenderedData(ctx, ms)
	require.NoError(t, err)
	if assert.True(t, found) {
		assert.JSONEq(t, string(objBytes), string(gotData))
	}

	// Deployment's replicas are not scheduled yet, should get phase -> Installed since pods are not scheduled.
	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{RequeueAfter: 10 * time.Minute}, gotRes)

	sms := &v1alpha1.MiniService{}
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), sms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceInstalled, sms.Status.Phase)
	if assert.Len(t, sms.Status.Conditions, 2) {
		cmpConditions(t, []metav1.Condition{
			{
				Reason: "AllObjectsApplied",
				Type:   v1alpha1.MiniServiceConditionInstallSuccessful,
				Status: metav1.ConditionTrue,
			},
			{
				Reason: v1alpha1.MiniServiceStatusReasonWaitingObjectReadiness,
				Type:   v1alpha1.MiniServiceConditionObjectsHealthy,
				Status: metav1.ConditionFalse,
				Message: `Some objects are not scheduled
apps/v1.Deployment foo
v1.Pod utils
`,
			},
		}, sms.Status.Conditions)
	}

	// Object statuses should still be pending, should get phase -> Starting now that pods are scheduled.
	depRS := &appsv1.ReplicaSet{}
	depRS.ObjectMeta.OwnerReferences = append(depRS.ObjectMeta.OwnerReferences, metav1.OwnerReference{
		Kind:       "Deployment",
		APIVersion: "apps/v1",
		Name:       gotDeployment.Name,
		Controller: newBool(true),
		UID:        gotDeployment.UID,
	})
	depRS.Name, depRS.Namespace = "dep-rs", ms.Spec.Namespace
	depRS.Spec.Template.Spec = gotDeployment.Spec.Template.Spec
	depRS.Status.Replicas = 1
	err = r.Client.Create(ctx, depRS)
	require.NoError(t, err)

	depPod := &corev1.Pod{}
	depPod.ObjectMeta.OwnerReferences = append(depPod.ObjectMeta.OwnerReferences, metav1.OwnerReference{
		Kind:       "ReplicaSet",
		APIVersion: "apps/v1",
		Name:       depRS.Name,
		Controller: newBool(true),
		UID:        depRS.UID,
	})
	depPod.Name, depPod.Namespace = "dep-pod", ms.Spec.Namespace
	depPod.Spec = gotDeployment.Spec.Template.Spec
	depPod.Status.Phase = corev1.PodPending
	depPod.Status.StartTime = &metav1.Time{Time: r.now()}
	depPod.Status.Conditions = []corev1.PodCondition{{
		Type:   corev1.PodScheduled,
		Status: corev1.ConditionTrue,
	}}
	err = r.Client.Create(ctx, depPod)
	require.NoError(t, err)

	gotDeployment.Status.Replicas = 1
	err = r.Client.Status().Update(ctx, &gotDeployment)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{RequeueAfter: 10 * time.Minute}, gotRes)

	sms = &v1alpha1.MiniService{}
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), sms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceStarting, sms.Status.Phase)
	if assert.Len(t, sms.Status.Conditions, 2) {
		cmpConditions(t, []metav1.Condition{
			{
				Reason: "AllObjectsApplied",
				Type:   v1alpha1.MiniServiceConditionInstallSuccessful,
				Status: metav1.ConditionTrue,
			},
			{
				Reason: v1alpha1.MiniServiceStatusReasonWaitingObjectReadiness,
				Type:   v1alpha1.MiniServiceConditionObjectsHealthy,
				Status: metav1.ConditionFalse,
				Message: `All objects are scheduled, waiting on readiness
apps/v1.Deployment foo
apps/v1.ReplicaSet dep-rs
v1.Pod dep-pod
v1.Pod utils
`,
			},
		}, sms.Status.Conditions)
	}

	// Set pod phase to running and update replicas, should get phase -> Running.
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
	err = r.Client.Status().Update(ctx, utilsPod)
	require.NoError(t, err)

	depRS.Status.ReadyReplicas = 1
	depRS.Status.AvailableReplicas = 1
	err = r.Client.Status().Update(ctx, depRS)
	require.NoError(t, err)

	depPod.Status.Phase = corev1.PodRunning
	depPod.Status.Conditions = []corev1.PodCondition{{
		Type:   corev1.PodReady,
		Status: corev1.ConditionTrue,
	}}
	err = r.Client.Status().Update(ctx, depPod)
	require.NoError(t, err)

	gotDeployment.Status.ReadyReplicas = 1
	gotDeployment.Status.AvailableReplicas = 1
	err = r.Client.Status().Update(ctx, &gotDeployment)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, gotRes)

	sms = &v1alpha1.MiniService{}
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), sms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceRunning, sms.Status.Phase)
	if assert.Len(t, sms.Status.Conditions, 2) {
		cmpConditions(t, []metav1.Condition{
			{
				Reason: "AllObjectsApplied",
				Type:   v1alpha1.MiniServiceConditionInstallSuccessful,
				Status: metav1.ConditionTrue,
			},
			{
				Reason: "ObjectsReady",
				Type:   v1alpha1.MiniServiceConditionObjectsHealthy,
				Status: metav1.ConditionTrue,
			},
		}, sms.Status.Conditions)
	}

	// Mark a pod as failed, ensure miniservice is also marked failed.
	gotDeployment.Status.ReadyReplicas = 0
	gotDeployment.Status.UnavailableReplicas = 1
	gotDeployment.Status.Conditions = []appsv1.DeploymentCondition{
		{
			Type:   appsv1.DeploymentReplicaFailure,
			Status: corev1.ConditionTrue,
			Reason: "SomeReplicaReason",
		},
		{
			Type:   appsv1.DeploymentProgressing,
			Status: corev1.ConditionFalse,
			Reason: "SomeProgressingReason",
		},
		{
			Type:   appsv1.DeploymentAvailable,
			Status: corev1.ConditionFalse,
			Reason: "ProgressDeadlineExceeded",
		},
	}
	err = r.Client.Status().Update(ctx, &gotDeployment)
	require.NoError(t, err)

	depRS.Status.ReadyReplicas = 0
	depRS.Status.AvailableReplicas = 1
	depRS.Status.Conditions = []appsv1.ReplicaSetCondition{
		{
			Type:   appsv1.ReplicaSetReplicaFailure,
			Status: corev1.ConditionTrue,
			Reason: "SomeReplicaReason",
		},
	}
	err = r.Client.Status().Update(ctx, depRS)
	require.NoError(t, err)

	depPod.Status.Phase = corev1.PodFailed
	depPod.Status.Reason = "SomeReason"
	depPod.Status.Conditions = []corev1.PodCondition{{
		Type:    corev1.PodReady,
		Status:  corev1.ConditionFalse,
		Message: "blah",
	}}
	err = r.Client.Status().Update(ctx, depPod)
	require.NoError(t, err)

	// Should result in status failure.
	exceededQuotaMsg := `exceeded quota: max-gpus, requested: requests.nvidia.com/gpu=1, used: requests.nvidia.com/gpu=1, limited: requests.nvidia.com/gpu=1`
	warnEvent := &corev1.Event{}
	warnEvent.Name, warnEvent.Namespace = "foo-warn", depPod.Namespace
	warnEvent.Reason = "FailedCreate"
	warnEvent.Message = `create Pod foo-xyz-abc in ReplicaSet foo-xyz failed error: ` +
		`pods "foo-xyz-abc" is forbidden: ` + exceededQuotaMsg
	warnEvent.InvolvedObject = corev1.ObjectReference{
		Name:       depPod.Name,
		Namespace:  depPod.Namespace,
		Kind:       "Pod",
		APIVersion: "v1",
	}
	warnEvent.Type = corev1.EventTypeWarning
	err = r.Client.Create(ctx, warnEvent)
	require.NoError(t, err)
	// Should be ignored.
	normalEvent := &corev1.Event{}
	normalEvent.Name, normalEvent.Namespace = "foo-normal", depPod.Namespace
	normalEvent.Message = "some non-issue"
	normalEvent.InvolvedObject = corev1.ObjectReference{
		Name:       depPod.Name,
		Namespace:  depPod.Namespace,
		Kind:       "Pod",
		APIVersion: "v1",
	}
	normalEvent.Type = corev1.EventTypeNormal
	err = r.Client.Create(ctx, normalEvent)
	require.NoError(t, err)
	// Should be ignored.
	kyvernoEvent := &corev1.Event{}
	kyvernoEvent.Name, kyvernoEvent.Namespace = "foo-kyverno", depPod.Namespace
	kyvernoEvent.Message = "some non-issue"
	kyvernoEvent.InvolvedObject = corev1.ObjectReference{
		Name:       depPod.Name,
		Namespace:  depPod.Namespace,
		Kind:       "Pod",
		APIVersion: "v1",
	}
	kyvernoEvent.Type = corev1.EventTypeWarning
	kyvernoEvent.Reason = "PolicyViolation"
	err = r.Client.Create(ctx, kyvernoEvent)
	require.NoError(t, err)

	// First reconcile will detect failures and enter backoff
	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err) // No error during backoff
	assert.Greater(t, gotRes.RequeueAfter, time.Duration(0), "Should requeue during backoff")

	// Wait for backoff to expire and reconcile should report terminal error
	assert.Eventually(t, func() bool {
		res, e := r.Reconcile(ctx, req)
		return e != nil && e.Error() == "terminal error: some objects failed" &&
			res.RequeueAfter == 0
	}, 100*time.Millisecond, 1*time.Millisecond, "Should eventually fail after backoff expires")

	expErrMsg := `apps/v1.Deployment foo: ProgressDeadlineExceeded,SomeProgressingReason,SomeReplicaReason
apps/v1.ReplicaSet dep-rs: SomeReplicaReason
v1.Pod dep-pod: SomeReason
events:
	FailedCreate: create Pod foo-xyz-abc in ReplicaSet foo-xyz failed error: pods "foo-xyz-abc" is forbidden: ` + exceededQuotaMsg + "\n\n"
	sms = &v1alpha1.MiniService{}
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), sms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceFailed, sms.Status.Phase)
	if assert.Len(t, sms.Status.Conditions, 2) {
		cmpConditions(t, []metav1.Condition{
			{
				Reason: "AllObjectsApplied",
				Type:   v1alpha1.MiniServiceConditionInstallSuccessful,
				Status: metav1.ConditionTrue,
			},
			{
				Reason:  v1alpha1.MiniServiceStatusReasonObjectsFailed,
				Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:  metav1.ConditionFalse,
				Message: expErrMsg,
			},
		}, sms.Status.Conditions)
	}

	// Set MiniService deletion timestamp but set namespace to stuck-terminating,
	// ensure resources are cleaned up but a failure is reported.
	sms.Finalizers = []string{finalizer}
	sms.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	err = tracker.Update(v1alpha1.SchemeGroupVersion.WithResource("miniservices"), sms, "")
	require.NoError(t, err)

	ns.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	ns.Finalizers = []string{"kubernetes"}
	ns.Status.Phase = corev1.NamespaceTerminating
	ns.Status.Conditions = []corev1.NamespaceCondition{{
		Type:   corev1.NamespaceDeletionDiscoveryFailure,
		Status: corev1.ConditionTrue,
	}}
	err = tracker.Update(corev1.SchemeGroupVersion.WithResource("namespaces"), ns, "")
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, gotRes)

	sms = &v1alpha1.MiniService{}
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), sms)
	require.True(t, apierrors.IsNotFound(err))
}

// TestReconcile_Function_SaveRevisionHistoryFails verifies that when saveRevisionHistory
// fails in doInstall (after all rendered objects are successfully applied), the reconcile
// returns an error and the MiniService phase does NOT advance to Installed.
func TestReconcile_Function_SaveRevisionHistoryFails(t *testing.T) {
	ctx := newTestContext()
	testScheme := mgrScheme

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

	r := &Reconciler{
		ControllerOptions: ControllerOptions{
			SystemNamespace:      "nvca-system",
			ICMSRequestNamespace: "nvcf-backend",
			K8sVersion:           "1.29.5",
			FeatureFlagFetcher: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{
					featureflag.HelmRBACEnforcement,
					featureflag.HelmResourceConstraints,
					featureflag.EnforceHelmFunctionResourceLimits,
					featureflag.InfraResourceOverhead,
					&featureflag.HelmSharedStorage.FeatureFlag,
					featureflag.BYOObservability,
					featureflag.MiniServiceRevisionHistory,
				},
			},
			K8sTimeConfig: (&k8sutil.TimeConfig{
				FailingObjectsBackoffTimeout:         1 * time.Millisecond,
				FailingObjectsBackoffRequeueInterval: 1 * time.Millisecond,
			}).Complete(),
			ImageCredentialHelperImage: "stg.nvcr.io/nv-cf/nvcf-core/image-credential-helper:latest",
			OverheadGetter:             enforce.NoOpInfraOverheadGetter,
			Metrics:                    metrics.NewDefaultMetrics("test-nca-id", "test-cluster", "test-group", "test-version", metrics.WithRegisterer(prometheus.NewRegistry())),
		},
		Decoder:               serializer.NewCodecFactory(testScheme).UniversalDeserializer(),
		NFClient:              nfClient,
		tracer:                otel.NewTracer(),
		eventRecorder:         record.NewFakeRecorder(10),
		chartCache:            chartcache.New(t.TempDir()),
		regITCache:            regITCache,
		now:                   time.Now,
		newPermissionsChecker: newFakePermissionsChecker,
	}
	r.statusCheckers = r.makeStatusCheckers()
	r.newImpersonatingClient = func(_ string) (client.Client, error) {
		return r.Client, nil
	}

	r.cfg.Agent.SharedStorage.Server.Image = "smb:latest"
	require.NoError(t, k8sutil.SetConfigDefaultResources(&r.cfg))

	require.NoError(t, r.chartCache.Start(ctx))

	helmObjs := []client.Object{
		&appsv1.Deployment{
			TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
			ObjectMeta: metav1.ObjectMeta{Name: "foo"},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name: "test", Image: "nvcr.io/foo/bar:baz", Command: []string{"yes"},
						}},
					},
				},
			},
		},
	}
	objBytes, err := json.MarshalIndent(helmObjs, "", "  ")
	require.NoError(t, err)
	wantRevalErr := new(error)
	r.ReValClient = newFakeReValClient(t, objBytes, wantRevalErr)

	envs := []corev1.EnvVar{
		{Name: common.ContainerRegistriesCredentialsEnv, Value: "eyJrOHNTZWNyZXRzIjpbeyJhdXRocyI6eyJyZWdpc3RyeS5leGFtcGxlLnRlc3QiOnsiYXV0aCI6ImRHVnpkQzExYzJWeU9uUmxjM1F0Y0dGemN3PT0ifX19XX0="},
		{Name: "FUNCTION_NAME", Value: "my-func"},
		{Name: "HELM_CHART_INFERENCE_SERVICE_NAME", Value: "myservice"},
		{Name: common.HelmRegistriesCredentialsEnv, Value: "eyJrOHNTZWNyZXRzIjpbeyJhdXRocyI6eyJoZWxtLm5nYy5udmlkaWEuY29tIjp7ImF1dGgiOiJjM1JuTFdGaVl6RXlNem8yWmpZek5HUTROUzA1TXpGbExUUXhaall0WVRKbFl5MDJOMkk0TnpVd1pqRXlOMlU9In19fV19"},
		{Name: "INFERENCE_HEALTH_ENDPOINT", Value: "/v2/health/ready"},
		{Name: "INFERENCE_HEALTH_EXPECTED_RESPONSE_CODE", Value: "200"},
		{Name: "INFERENCE_HEALTH_PORT", Value: "8080"},
		{Name: "INFERENCE_PORT", Value: "8080"},
		{Name: "INFERENCE_PROTOCOL", Value: "HTTP"},
		{Name: "INFERENCE_URL", Value: "/"},
		{Name: "INIT_CONTAINER", Value: "registry.example.test/nvcf-core/nvcf_worker_init:0.24.10"},
		{Name: "MAX_REQUEST_CONCURRENCY", Value: "1"},
		{Name: "NVCF_FQDN", Value: "https://api.example.test"},
		{Name: "NVCF_FQDN_GRPC", Value: "https://grpc.example.test"},
		{Name: "NVCF_FQDN_NATS", Value: "tls://nats.example.test:4222"},
		{Name: "NVCF_WORKER_TOKEN", Value: "tok"},
		{Name: "OTEL_CONTAINER", Value: "registry.example.test/nvcf-core/opentelemetry-collector:0.74.0"},
		{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: "https://otel.example.test:8282"},
		{Name: common.SidecarRegistryCredentialEnv, Value: "eyJhdXRocyI6eyJyZWdpc3RyeS5leGFtcGxlLnRlc3QiOnsiYXV0aCI6ImRHVnpkQzExYzJWeU9uUmxjM1F0Y0dGemN3PT0ifX19"},
		{Name: "UTILS_CONTAINER", Value: "registry.example.test/nvcf-core/nvcf_worker_utils:2.21.4"},
		{Name: "ESS_AGENT_CONTAINER", Value: "nvcr.io/nv-cf/nvcf-core/ess-agent:1.0.0"},
	}

	sr := &nvcav2beta1.ICMSRequest{}
	sr.Name = "sr-revfail"
	sr.Namespace = r.ICMSRequestNamespace
	sr.Spec = nvcav2beta1.ICMSRequestSpec{
		FunctionDetails: function.Details{
			FunctionID:        "funcid-revfail",
			FunctionVersionID: "funcverid-revfail",
			FunctionType:      "DEFAULT",
		},
		Action:         common.FunctionCreationAction,
		NCAId:          "ncaid-revfail",
		RequestID:      "reqid-revfail",
		MessageBatchID: "mbatchid-revfail",
		CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
			CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
				Action:            common.FunctionCreationAction,
				RequestID:         "reqid-revfail",
				MessageBatchID:    "mbatchid-revfail",
				InstanceType:      "ON-PREM.GPU.L40",
				InstanceTypeName:  "ON-PREM.GPU.L40_1x",
				InstanceTypeValue: "ON-PREM.GPU.L40",
				GPUType:           "L40",
				RequestedGPUCount: 1,
				InstanceCount:     1,
				NCAID:             "ncaid-revfail",
			},
			FunctionLaunchSpecification: &function.LaunchSpecification{
				CloudProvider:   "DGXCLOUD",
				ICMSEnvironment: "prod",
				GPUName:         "L40",
				HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{
					HelmChartURL: "https://helm.ngc.nvidia.com/myorg/myteam/charts/test-chart-1.0.0.tgz",
					Values:       []byte(`{"foo":"bar"}`),
				},
			},
		},
	}
	// Append launch-spec-derived envs and encode.
	cmInfo := sr.Spec.CreationMsgInfo
	funcLS := cmInfo.FunctionLaunchSpecification
	envs = append(envs,
		corev1.EnvVar{Name: "ATTACHED_GPU_COUNT", Value: fmt.Sprint(cmInfo.RequestedGPUCount)},
		corev1.EnvVar{Name: "CLOUD_PROVIDER", Value: funcLS.CloudProvider},
		corev1.EnvVar{Name: "FUNCTION_ID", Value: sr.Spec.FunctionDetails.FunctionID},
		corev1.EnvVar{Name: "FUNCTION_VERSION_ID", Value: sr.Spec.FunctionDetails.FunctionVersionID},
		corev1.EnvVar{Name: "GPU_NAME", Value: funcLS.GPUName},
		corev1.EnvVar{Name: "NCA_ID", Value: cmInfo.NCAID},
	)
	funcLS.EnvironmentB64 = encodeEnvsForLaunchSpec(envs)

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

	hcCfg, err := common.ExtractHelmConfiguration(funcLS.EnvironmentB64, funcLS.HelmChartLaunchSpecification)
	require.NoError(t, err)

	ms := &v1alpha1.MiniService{}
	ms.Name = msName
	ms.Labels = internalObjLabels
	ms.Annotations = internalObjAnnotations
	ms.Spec = v1alpha1.MiniServiceSpec{
		Namespace:       sr.Name,
		ICMSRequestName: sr.Name,
		HelmChartConfig: hcCfg,
	}

	objs := []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: r.ICMSRequestNamespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: r.SystemNamespace}},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: instanceRBACConfigMapName, Namespace: r.SystemNamespace},
			Data: map[string]string{
				instanceRoleName: `
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
rules:
- apiGroups: [""]
  apiVersions: ["v1"]
  resources: ["pods", "services", "secrets"]
  verbs: ["*"]
- apiGroups: ["apps"]
  apiVersions: ["v1"]
  resources: ["deployments"]
  verbs: ["*"]
- apiGroups: ["rbac.authorization.k8s.io"]
  apiVersions: ["v1"]
  resources: ["roles", "rolebindings"]
  verbs: ["*"]
`,
			},
		},
	}
	objs = append(objs, k8smock.NewNetworkPolicyConfigMap(r.SystemNamespace))

	// Use an interceptor that fails ConfigMap creation only for revision ConfigMaps.
	failRevisionSave := false
	injectedErr := fmt.Errorf("simulated etcd write failure")
	crClient, _ := newFakeClientWithInterceptors(testScheme,
		interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if failRevisionSave {
					if cm, ok := obj.(*corev1.ConfigMap); ok && strings.HasPrefix(cm.Name, revisionConfigMapPrefix) {
						return injectedErr
					}
				}
				return c.Create(ctx, obj, opts...)
			},
		},
		append(objs, sr, ms)...,
	)
	r.Client = crClient

	// Step through reconcile iterations to reach doInstall.
	req := reconcile.Request{NamespacedName: client.ObjectKey{Name: ms.Name}}

	// 1. First reconcile: creates namespace, SA, prereqs.
	_, err = r.Reconcile(ctx, req)
	require.EqualError(t, err,
		"update default service account with worker image pull secrets: get instance service account: serviceaccounts \"default\" not found")

	createNamespaceDefaultSA(t, ctx, r, ms)

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	// 2. Complete image credential init job.
	initImageCredsJob := &batchv1.Job{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: sr.Name + "-cred-init", Namespace: r.SystemNamespace}, initImageCredsJob)
	require.NoError(t, err)
	initImageCredsJob.Status = batchv1.JobStatus{CompletionTime: &metav1.Time{Time: r.now()}, Succeeded: 1}
	require.NoError(t, r.Client.Status().Update(ctx, initImageCredsJob))

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	// 3. Mark shared storage ready.
	sharedStorageST := &nvcav2beta1.StorageRequest{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: "shared-storage", Namespace: ms.Spec.Namespace}, sharedStorageST)
	require.NoError(t, err)
	sharedStorageST.Status.Phase = nvcav2beta1.StorageReady
	sharedStorageST.Status.SharedStorage = &nvcav2beta1.SharedStorageStatus{
		KNS:     nvcav2beta1.SharedStorageTypeStatus{ReadOnlyPVCName: "kns-ro", ReadWritePVCName: "kns-rw"},
		Secrets: nvcav2beta1.SharedStorageTypeStatus{ReadOnlyPVCName: "sec-ro", ReadWritePVCName: "sec-rw"},
	}
	require.NoError(t, r.Client.Status().Update(ctx, sharedStorageST))

	// Storage ready → creates infra objects including utils pod.
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	// 4. Mark utils pod init containers as complete.
	utilsPod := &corev1.Pod{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: "utils", Namespace: ms.Spec.Namespace}, utilsPod)
	require.NoError(t, err)
	utilsPod.Status.Phase = corev1.PodPending
	utilsPod.Status.StartTime = &metav1.Time{Time: r.now()}
	utilsPod.Status.InitContainerStatuses = []corev1.ContainerStatus{
		{Name: common.InitContainerName, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}},
		{Name: common.NewESSInitContainer("", nil).Name, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}},
	}
	utilsPod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: common.UtilsContainerName, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}},
	}
	utilsPod.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
		{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
	}
	require.NoError(t, r.Client.Status().Update(ctx, utilsPod))

	// 5. Activate the interceptor failure BEFORE the install reconcile.
	failRevisionSave = true

	_, err = r.Reconcile(ctx, req)
	require.Error(t, err, "reconcile should fail when saveRevisionHistory fails")
	assert.ErrorContains(t, err, "simulated etcd write failure")

	// Verify phase did NOT advance to Installed.
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase,
		"phase must remain Installing when saveRevisionHistory fails")

	// Verify the workload deployment WAS created (install succeeded up to saveRevisionHistory).
	gotDeployments := &appsv1.DeploymentList{}
	err = r.Client.List(ctx, gotDeployments, client.InNamespace(ms.Spec.Namespace))
	require.NoError(t, err)
	assert.Len(t, gotDeployments.Items, 1, "workload objects should exist despite saveRevisionHistory failure")

	// Verify no revision ConfigMap was created.
	revCM := &corev1.ConfigMap{}
	err = r.Client.Get(ctx, client.ObjectKey{Namespace: ms.Spec.Namespace, Name: "miniservice-revision-v0"}, revCM)
	assert.True(t, apierrors.IsNotFound(err), "revision ConfigMap should not exist")

	// Verify the InstallSuccessful condition was NOT set.
	installCond := meta.FindStatusCondition(ms.Status.Conditions, v1alpha1.MiniServiceConditionInstallSuccessful)
	assert.Nil(t, installCond, "InstallSuccessful condition should not be set when saveRevisionHistory fails")

	// 6. Disable the failure and retry — reconcile should now succeed.
	failRevisionSave = false

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceInstalled, ms.Status.Phase,
		"phase should advance to Installed after successful retry")

	err = r.Client.Get(ctx, client.ObjectKey{Namespace: ms.Spec.Namespace, Name: "miniservice-revision-v0"}, revCM)
	require.NoError(t, err, "revision ConfigMap should exist after successful retry")
	assert.Equal(t, string(ms.Spec.HelmChartConfig.Values), revCM.Data[revisionDataKeyValues])
}

func TestReconcile_Function_NVLinkOptimized(t *testing.T) {
	gpuLimits := corev1.ResourceList{
		"nvidia.com/gpu": resource.MustParse("2"),
	}

	t.Run("defaults", func(t *testing.T) {
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
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:    "test",
								Image:   "nvcr.io/foo/bar:baz",
								Command: []string{"yes"},
								Resources: corev1.ResourceRequirements{
									Limits: gpuLimits,
								},
							}},
						},
					},
				},
			},
		}
		// Workload objects are bare Helm renders; labels/annotations/pod-spec fields
		// (service account, affinity, DRA resource claims, NVLink labels) are injected
		// by the webhook at Pod admission time, not on Deployments.
		expDeployments := []appsv1.Deployment{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "foo",
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:    "test",
								Image:   "nvcr.io/foo/bar:baz",
								Command: []string{"yes"},
								Env:     []corev1.EnvVar{},
								Resources: corev1.ResourceRequirements{
									Limits: gpuLimits,
								},
							}},
						},
					},
				},
			},
		}
		testReconcileNVLinkOptimizedHelper(t, helmObjs, expDeployments)
	})
	t.Run("required domains", func(t *testing.T) {
		helmObjs := []client.Object{
			&appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "foo1",
					Annotations: map[string]string{
						nvcfdra.RequiredNVLinkDomainIndexAnnotation: "0",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:    "test",
								Image:   "nvcr.io/foo/bar:baz",
								Command: []string{"yes"},
								Resources: corev1.ResourceRequirements{
									Limits: gpuLimits,
								},
							}},
						},
					},
				},
			},
			&appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "foo2",
					Annotations: map[string]string{
						nvcfdra.RequiredNVLinkDomainIndexAnnotation: "1",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:    "test",
								Image:   "nvcr.io/foo/bar:baz",
								Command: []string{"yes"},
								Resources: corev1.ResourceRequirements{
									Limits: gpuLimits,
								},
							}},
						},
					},
				},
			},
		}
		// Workload objects are bare Helm renders; DRA resource claims, NVLink labels,
		// affinity, and service account are injected by the webhook at Pod admission.
		expDeployments := []appsv1.Deployment{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "foo1",
					Annotations: map[string]string{
						nvcfdra.RequiredNVLinkDomainIndexAnnotation: "0",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:    "test",
								Image:   "nvcr.io/foo/bar:baz",
								Command: []string{"yes"},
								Env:     []corev1.EnvVar{},
								Resources: corev1.ResourceRequirements{
									Limits: gpuLimits,
								},
							}},
						},
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "foo2",
					Annotations: map[string]string{
						nvcfdra.RequiredNVLinkDomainIndexAnnotation: "1",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:    "test",
								Image:   "nvcr.io/foo/bar:baz",
								Command: []string{"yes"},
								Env:     []corev1.EnvVar{},
								Resources: corev1.ResourceRequirements{
									Limits: gpuLimits,
								},
							}},
						},
					},
				},
			},
		}
		testReconcileNVLinkOptimizedHelper(t, helmObjs, expDeployments)
	})
}

func testReconcileNVLinkOptimizedHelper(t *testing.T, helmObjs []client.Object, expDeployments []appsv1.Deployment) {
	ctx := newTestContext()
	testScheme := mgrScheme

	nfClient := &fakenodefeatures.Client{
		BackendGPUs: []nvcatypes.BackendGPU{{
			Name: "GB200",
			InstanceTypes: []nvcatypes.InstanceType{
				{
					Name:            "AWS.GPU.GB200",
					FullName:        "NVIDIA-GB200",
					Description:     "One Nvidia ada GPU",
					Default:         true,
					CPU:             resource.MustParse("4"),
					SystemMemory:    resource.MustParse("28Gi"),
					GPUCount:        4,
					GPUMemoryPerGPU: resource.MustParse("48Gi"),
					OS:              "linux",
					DriverVersion:   "535.135.05",
					CPUArch:         "amd64",
					Storage:         resource.MustParse("180Gi"),
					NodeCount:       3,
				},
			},
		}},
	}
	regITCache := icms.NewRegistrationInstanceTypeCache()
	regITCache.Put(nvcatypes.BackendGPUs(nfClient.BackendGPUs).ToRegistration(true, corev1.ResourceList{}))

	r := &Reconciler{
		ControllerOptions: ControllerOptions{
			SystemNamespace:      "nvca-system",
			ICMSRequestNamespace: "nvcf-backend",
			K8sVersion:           "1.34.1",
			FeatureFlagFetcher: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{
					featureflag.HelmRBACEnforcement,
					featureflag.HelmResourceConstraints,
					featureflag.EnforceHelmFunctionResourceLimits,
					featureflag.InfraResourceOverhead,
					&featureflag.HelmSharedStorage.FeatureFlag,
					featureflag.MiniServiceRevisionHistory,
				},
				EnabledAttrs: []*featureflag.Attribute{
					featureflag.AttrNVLinkOptimized,
				},
			},
			K8sTimeConfig:  (&k8sutil.TimeConfig{}).Complete(),
			OverheadGetter: enforce.NoOpInfraOverheadGetter,
			Metrics:        metrics.NewDefaultMetrics("test-nca-id", "test-cluster", "test-group", "test-version", metrics.WithRegisterer(prometheus.NewRegistry())),
		},
		Decoder:               serializer.NewCodecFactory(testScheme).UniversalDeserializer(),
		NFClient:              nfClient,
		tracer:                otel.NewTracer(),
		eventRecorder:         record.NewFakeRecorder(10),
		chartCache:            chartcache.New(t.TempDir()),
		regITCache:            regITCache,
		now:                   time.Now,
		newPermissionsChecker: newFakePermissionsChecker,
	}
	r.statusCheckers = r.makeStatusCheckers()
	// Impersonation can't be mocked using the fake client.
	// Envtest handles this.
	r.newImpersonatingClient = func(_ string) (client.Client, error) {
		return r.Client, nil
	}

	r.cfg.Agent.SharedStorage.Server.Image = "smb:latest"
	err := k8sutil.SetConfigDefaultResources(&r.cfg)
	require.NoError(t, err)

	err = r.chartCache.Start(ctx)
	require.NoError(t, err)

	objBytes, err := json.MarshalIndent(helmObjs, "", "  ")
	require.NoError(t, err)
	wantRevalErr := new(error)
	rvClient := newFakeReValClient(t, objBytes, wantRevalErr)
	r.ReValClient = rvClient

	envs := []corev1.EnvVar{
		{Name: "BYOO_OTEL_COLLECTOR_CONTAINER", Value: "registry.example.test/nvcf-core/byoo-otel-collector:1.2.3"},
		{Name: common.ContainerRegistriesCredentialsEnv, Value: "eyJrOHNTZWNyZXRzIjpbeyJhdXRocyI6eyJyZWdpc3RyeS5leGFtcGxlLnRlc3QiOnsiYXV0aCI6ImRHVnpkQzExYzJWeU9uUmxjM1F0Y0dGemN3PT0ifX19XX0="},
		{Name: "FUNCTION_NAME", Value: "my-func"},
		{Name: "HELM_CHART_INFERENCE_SERVICE_NAME", Value: "myservice"},
		{Name: common.HelmRegistriesCredentialsEnv, Value: "eyJrOHNTZWNyZXRzIjpbeyJhdXRocyI6eyJoZWxtLm5nYy5udmlkaWEuY29tIjp7ImF1dGgiOiJjM1JuTFdGaVl6RXlNem8yWmpZek5HUTROUzA1TXpGbExUUXhaall0WVRKbFl5MDJOMkk0TnpVd1pqRXlOMlU9In19fV19"},
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
		{Name: common.SidecarRegistryCredentialEnv, Value: "eyJhdXRocyI6eyJyZWdpc3RyeS5leGFtcGxlLnRlc3QiOnsiYXV0aCI6ImRHVnpkQzExYzJWeU9uUmxjM1F0Y0dGemN3PT0ifX19"},
		{Name: "TRACING_ACCESS_TOKEN", Value: "trace-tok-1"},
		{Name: "UTILS_CONTAINER", Value: "registry.example.test/nvcf-core/nvcf_worker_utils:2.21.4"},
	}

	sr := &nvcav2beta1.ICMSRequest{}
	sr.Name = "sr-foo"
	sr.Namespace = r.ICMSRequestNamespace
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
				InstanceType:      "AWS.GPU.GB200",
				InstanceTypeName:  "AWS.GPU.GB200_1x",
				InstanceTypeValue: "AWS.GPU.GB200",
				GPUType:           "GB200",
				RequestedGPUCount: 1,
				InstanceCount:     1,
				NCAID:             "ncaid-1",
			},
			FunctionLaunchSpecification: &function.LaunchSpecification{
				CloudProvider:   "AWS",
				ICMSEnvironment: "prod",
				GPUName:         "GB200",
				HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{
					HelmChartURL: "https://helm.ngc.nvidia.com/myorg/myteam/charts/image-segmentation-1.0.3.tgz",
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
			ObjectMeta: metav1.ObjectMeta{Name: r.ICMSRequestNamespace},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: r.SystemNamespace},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: instanceRBACConfigMapName, Namespace: r.SystemNamespace},
			Data: map[string]string{
				instanceRoleName: `
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
rules:
- apiGroups: [""]
  apiVersions: ["v1"]
  resources: ["pods", "services", "secrets"]
  verbs: ["*"]
- apiGroups: ["apps"]
  apiVersions: ["v1"]
  resources: ["deployments"]
  verbs: ["*"]
- apiGroups: ["rbac.authorization.k8s.io"]
  apiVersions: ["v1"]
  resources: ["roles", "rolebindings"]
  verbs: ["*"]
`,
			},
		},
	}

	objs = append(objs, k8smock.NewNetworkPolicyConfigMap(r.SystemNamespace))

	crClient, _ := newFakeClient(testScheme, append(objs, sr, ms)...)
	r.Client = crClient

	createNamespaceDefaultSA(t, ctx, r, ms)

	req := reconcile.Request{}
	req.Name = ms.Name
	gotRes, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, reconcile.Result{}, gotRes)

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)

	sharedStorageST := &nvcav2beta1.StorageRequest{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: "shared-storage", Namespace: ms.Spec.Namespace}, sharedStorageST)
	require.NoError(t, err)
	sharedStorageST.Status.Phase = nvcav2beta1.StorageReady
	sharedStorageST.Status.SharedStorage = &nvcav2beta1.SharedStorageStatus{
		KNS: nvcav2beta1.SharedStorageTypeStatus{
			ReadOnlyPVCName:  "test-kns-ropvc",
			ReadWritePVCName: "test-kns-rwpvc",
		},
		Secrets: nvcav2beta1.SharedStorageTypeStatus{
			ReadOnlyPVCName:  "test-secrets-ropvc",
			ReadWritePVCName: "test-secrets-rwpvc",
		},
	}
	err = r.Client.Status().Update(ctx, sharedStorageST)
	require.NoError(t, err)

	// Storage is ready, reconcile should proceed.
	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, reconcile.Result{}, gotRes)

	// Check that initial resources were not created until after the utils pod's init containers complete.
	utilsPod := &corev1.Pod{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: "utils", Namespace: ms.Spec.Namespace}, utilsPod)
	require.NoError(t, err)
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)
	gotDeployments := &appsv1.DeploymentList{}
	err = r.Client.List(ctx, gotDeployments, client.InNamespace(ms.Spec.Namespace))
	require.NoError(t, err)
	require.Len(t, gotDeployments.Items, 0)

	utilsPod.Status.Phase = corev1.PodPending
	utilsPod.Status.StartTime = &metav1.Time{Time: r.now()}
	utilsPod.Status.InitContainerStatuses = []corev1.ContainerStatus{
		{
			Name: common.InitContainerName,
			State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{},
			},
		},
		{
			Name: common.NewESSInitContainer("", nil).Name,
			State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{},
			},
		},
	}
	utilsPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: common.UtilsContainerName,
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{},
		},
	}}
	utilsPod.Status.Conditions = []corev1.PodCondition{
		{
			Type:   corev1.PodScheduled,
			Status: corev1.ConditionTrue,
		},
		{
			Type:   corev1.PodInitialized,
			Status: corev1.ConditionTrue,
		},
	}
	err = r.Client.Status().Update(ctx, utilsPod)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, reconcile.Result{}, gotRes)

	// Check that initial resources were created.
	err = r.Client.List(ctx, gotDeployments, client.InNamespace(ms.Spec.Namespace))
	require.NoError(t, err)
	require.Len(t, gotDeployments.Items, len(expDeployments))
	sort.Slice(gotDeployments.Items, func(i, j int) bool {
		return gotDeployments.Items[i].Name < gotDeployments.Items[j].Name
	})
	workloadLabels := map[string]string{
		"ENVIRONMENT":                       launchSpec.ICMSEnvironment,
		nvcatypes.FunctionIDUpperKey:        sr.Spec.FunctionDetails.FunctionID,
		nvcatypes.FunctionVersionIDUpperKey: sr.Spec.FunctionDetails.FunctionVersionID,
		"GPU_COUNT":                         fmt.Sprint(sr.Spec.CreationMsgInfo.RequestedGPUCount),
		nvcatypes.NCAIDUpperKey:             nvcatypes.MakeNCAIDLabelValue(sr.Spec.NCAId),
		"environment":                       launchSpec.ICMSEnvironment,
		"performance_class":                 sr.Spec.CreationMsgInfo.InstanceTypeValue,
		nvcatypes.NCAIDKey:                  nvcatypes.MakeNCAIDLabelValue(sr.Spec.NCAId),
		nvcatypes.FunctionIDKey:             sr.Spec.FunctionDetails.FunctionID,
		nvcatypes.FunctionVersionIDKey:      sr.Spec.FunctionDetails.FunctionVersionID,
		nvcatypes.GPUNameKey:                sr.Spec.CreationMsgInfo.GPUType,
		nvcatypes.MessageBatchIDKey:         sr.Spec.MessageBatchID,
		nvcatypes.ICMSRequestIDKey:          sr.Spec.RequestID,
		miniserviceNameLabel:                msName,
	}
	workloadAnnotationsFiltered := filterNVCFAnnotations(map[string]string{
		nvcatypes.NCAIDKey:         sr.Spec.NCAId,
		nvcatypes.InstanceCountKey: fmt.Sprint(sr.Spec.CreationMsgInfo.InstanceCount),
		nvcatypes.ClusterGroupKey:  sr.Spec.CreationMsgInfo.ClusterGroup,
		nvcatypes.ICMSRequestIDKey: sr.Spec.RequestID,
		"FUNCTION_NAME":            "my-func",
		"function-name":            "my-func",
	})

	for i, gotDeployment := range gotDeployments.Items {
		gotDeployment.Annotations = filterNVCFAnnotations(gotDeployment.ObjectMeta.Annotations)
		gotDeployment.Spec.Template.ObjectMeta.Annotations = filterNVCFAnnotations(gotDeployment.Spec.Template.ObjectMeta.Annotations)
		for i := range gotDeployment.Spec.Template.Spec.Containers {
			gotDeployment.Spec.Template.Spec.Containers[i].Env = filterNVCFEnvVars(gotDeployment.Spec.Template.Spec.Containers[i].Env)
		}
		gotDeployment.Namespace = ""
		gotDeployment.ResourceVersion = ""

		expDeployment := expDeployments[i]
		expDeployment.Labels = mergeMaps(expDeployment.Labels, workloadLabels)
		expDeployment.Annotations = mergeMaps(filterNVCFAnnotations(expDeployment.Annotations), workloadAnnotationsFiltered)
		expDeployment.Spec.Template.Annotations = filterNVCFAnnotations(expDeployment.Spec.Template.Annotations)
		assert.Equal(t, expDeployment, gotDeployment)
	}

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceInstalled, ms.Status.Phase)
	assert.Equal(t, int64(0), ms.Status.Revision, "initial install should be revision 0")
	assert.Equal(t, ms.Generation, ms.Status.ObservedGeneration, "observedGeneration should match generation after install")
	if assert.Len(t, ms.Status.Conditions, 1) {
		cmpConditions(t, []metav1.Condition{
			{
				Reason: "AllObjectsApplied",
				Type:   v1alpha1.MiniServiceConditionInstallSuccessful,
				Status: metav1.ConditionTrue,
			},
		}, ms.Status.Conditions)
	}
}

func TestReconcile_Task(t *testing.T) {
	ctx := newTestContext(logrus.DebugLevel)
	testScheme := mgrScheme

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

	r := &Reconciler{
		ControllerOptions: ControllerOptions{
			SystemNamespace:      "nvca-system",
			ICMSRequestNamespace: "nvcf-backend",
			K8sVersion:           "1.29.5",
			FeatureFlagFetcher: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{
					featureflag.HelmRBACEnforcement,
					featureflag.HelmResourceConstraints,
					featureflag.EnforceHelmTaskResourceLimits,
					featureflag.InfraResourceOverhead,
					&featureflag.HelmSharedStorage.FeatureFlag,
					featureflag.BYOObservability,
				},
			},
			K8sTimeConfig:              (&k8sutil.TimeConfig{}).Complete(),
			ImageCredentialHelperImage: "stg.nvcr.io/nv-cf/nvcf-core/image-credential-helper:latest",
			OverheadGetter:             enforce.NoOpInfraOverheadGetter,
			Metrics:                    metrics.NewDefaultMetrics("test-nca-id", "test-cluster", "test-group", "test-version", metrics.WithRegisterer(prometheus.NewRegistry())),
		},
		Decoder:               serializer.NewCodecFactory(testScheme).UniversalDeserializer(),
		NFClient:              nfClient,
		tracer:                otel.NewTracer(),
		eventRecorder:         record.NewFakeRecorder(10),
		chartCache:            chartcache.New(t.TempDir()),
		regITCache:            regITCache,
		now:                   time.Now,
		newPermissionsChecker: newFakePermissionsChecker,
	}
	r.statusCheckers = r.makeStatusCheckers()
	// Impersonation can't be mocked using the fake client.
	// Envtest handles this.
	r.newImpersonatingClient = func(_ string) (client.Client, error) {
		return r.Client, nil
	}

	r.cfg.Agent.SharedStorage.Server.Image = "smb:latest"
	err := k8sutil.SetConfigDefaultResources(&r.cfg)
	require.NoError(t, err)

	err = r.chartCache.Start(ctx)
	require.NoError(t, err)

	helmObjs := []client.Object{
		&batchv1.Job{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "batch/v1",
				Kind:       "Job",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "foo",
			},
			Spec: batchv1.JobSpec{
				Template: corev1.PodTemplateSpec{
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
	}
	objBytes, err := json.MarshalIndent(helmObjs, "", "  ")
	require.NoError(t, err)
	var wantRevalErr *error = nil
	rvClient := newFakeReValClient(t, objBytes, wantRevalErr)
	r.ReValClient = rvClient

	envs := []corev1.EnvVar{
		{Name: common.ContainerRegistriesCredentialsEnv, Value: "eyJrOHNTZWNyZXRzIjpbeyJhdXRocyI6eyJyZWdpc3RyeS5leGFtcGxlLnRlc3QiOnsiYXV0aCI6ImRHVnpkQzExYzJWeU9uUmxjM1F0Y0dGemN3PT0ifX19XX0="},
		{Name: common.HelmRegistriesCredentialsEnv, Value: "eyJrOHNTZWNyZXRzIjpbeyJhdXRocyI6eyJoZWxtLm5nYy5udmlkaWEuY29tIjp7ImF1dGgiOiJjM1JuTFdGaVl6RXlNem8yWmpZek5HUTROUzA1TXpGbExUUXhaall0WVRKbFl5MDJOMkk0TnpVd1pqRXlOMlU9In19fV19"},
		{Name: "INIT_CONTAINER", Value: "registry.example.test/nvcf-core/nvcf_worker_init:0.24.10"},
		{Name: "MAX_REQUEST_CONCURRENCY", Value: "1"},
		{Name: "NVCF_FQDN", Value: "https://api.example.test"},
		{Name: "NVCF_FQDN_GRPC", Value: "https://grpc.example.test"},
		{Name: "NVCF_FQDN_NATS", Value: "tls://nats.example.test:4222"},
		{Name: "NVCF_WORKER_TOKEN", Value: "tok"},
		{Name: "OTEL_CONTAINER", Value: "registry.example.test/nvcf-core/opentelemetry-collector:0.74.0"},
		{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: "https://otel.example.test:8282"},
		{Name: common.SidecarRegistryCredentialEnv, Value: "eyJhdXRocyI6eyJyZWdpc3RyeS5leGFtcGxlLnRlc3QiOnsiYXV0aCI6ImRHVnpkQzExYzJWeU9uUmxjM1F0Y0dGemN3PT0ifX19"},
		{Name: "TASK_CONTAINER", Value: "nvcr.io/myorg/gpt-3.5-turbo-fine-tune:1.0.0"},
		{Name: "TASK_CONTAINER_ARGS", Value: "-arg1=test1 arg2=test2"},
		{Name: "TASK_CONTAINER_ENV", Value: "W3sia2V5IjoiVEFTS19FTlZfS0VZIiwidmFsdWUiOiJ0YXNrX3ZhbHVlIn1d"},
		{Name: "TASK_HEALTH_ENDPOINT", Value: "/v2/health/ready"},
		{Name: "TASK_HEALTH_EXPECTED_RESPONSE_CODE", Value: "200"},
		{Name: "TASK_HEALTH_PORT", Value: "50051"},
		{Name: "TASK_NAME", Value: "my-task"},
		{Name: "TASK_PORT", Value: "50051"},
		{Name: "TASK_PROTOCOL", Value: "GRPC"},
		{Name: "TASK_URL", Value: "/grpc"},
		{Name: "TERMINATION_GRACE_PERIOD", Value: "PT2H"},
		{Name: "TRACING_ACCESS_TOKEN", Value: "trace-tok-1"},
		{Name: "UTILS_CONTAINER", Value: "registry.example.test/nvcf-core/nvcf_worker_utils:2.21.4"},
	}

	telLaunchSpec := &common.TelemetriesLaunchSpecification{}
	telLaunchSpec.Telemetries.Logs = &common.Telemetry{
		Protocol: "http",
		Provider: "SPLUNK",
		Endpoint: "endpoint",
		Name:     "splunk-prd",
	}
	telLaunchSpec.Telemetries.Metrics = &common.Telemetry{
		Protocol: "http",
		Provider: "GRAFANA_CLOUD",
		Endpoint: "endpoint",
		Name:     "Grafana_prd",
	}
	telLaunchSpec.Telemetries.Traces = &common.Telemetry{
		Protocol: "http",
		Provider: "SERVICENOW",
		Endpoint: "endpoint:8323",
		Name:     "nv-lightstep-stg",
	}

	sr := &nvcav2beta1.ICMSRequest{}
	sr.Name = "sr-bar"
	sr.Namespace = r.ICMSRequestNamespace
	sr.Spec = nvcav2beta1.ICMSRequestSpec{
		TaskDetails: task.Details{
			TaskID:   "taskid-1",
			TaskType: "HELMCHART",
		},
		Action:         common.TaskCreationAction,
		NCAId:          "ncaid-1",
		RequestID:      "reqid1",
		MessageBatchID: "mbatchid1",
		CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
			CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
				Action:            common.TaskCreationAction,
				RequestID:         "reqid1",
				MessageBatchID:    "mbatchid1",
				InstanceType:      "ON-PREM.GPU.L40",
				InstanceTypeName:  "ON-PREM.GPU.L40_1x",
				InstanceTypeValue: "ON-PREM.GPU.L40",
				GPUType:           "L40",
				RequestedGPUCount: 1,
				InstanceCount:     1,
				NCAID:             "ncaid-1",
				AccountName:       "account1",
			},
			TaskLaunchSpecification: &task.LaunchSpecification{
				CloudProvider:                  "DGXCLOUD",
				ICMSEnvironment:                "prod",
				ResultHandlingStrategy:         "UPLOAD",
				TerminationGracePeriodDuration: "PT10M",
				MaxRuntimeDuration:             "PT2H",
				MaxQueuedDuration:              "PT2M",
				HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{
					HelmChartURL: "https://helm.ngc.nvidia.com/myorg/myteam/charts/image-segmentation-1.0.3.tgz",
					Values:       []byte(`{"foo":{"bar":"baz"}}`),
				},
				CacheLaunchSpecification: &common.CacheLaunchSpecification{
					CacheArtifacts: true,
					CacheHandle:    "abc123handle",
					CacheSize:      262144000,
				},
				Telemetries: telLaunchSpec,
			},
		},
	}
	cmInfo := sr.Spec.CreationMsgInfo
	launchSpec := cmInfo.TaskLaunchSpecification

	// Set envs from ICMS request to align values.
	envs = append(envs, []corev1.EnvVar{
		{Name: "ATTACHED_GPU_COUNT", Value: fmt.Sprint(cmInfo.CreationQueueMessageMetadata.RequestedGPUCount)},
		{Name: "CLOUD_PROVIDER", Value: launchSpec.CloudProvider},
		{Name: "TASK_ID", Value: sr.Spec.TaskDetails.TaskID},
		{Name: "GPU_NAME", Value: cmInfo.GPUType},
		{Name: "NCA_ID", Value: cmInfo.CreationQueueMessageMetadata.NCAID},
	}...)

	launchSpec.EnvironmentB64 = encodeEnvsForLaunchSpec(envs)

	msName := "miniservice"
	internalObjLabels := map[string]string{
		nvcatypes.NCAIDKey:          nvcatypes.MakeNCAIDLabelValue(sr.Spec.NCAId),
		nvcatypes.NCAIDUpperKey:     nvcatypes.MakeNCAIDLabelValue(sr.Spec.NCAId),
		nvcatypes.TaskIDKey:         sr.Spec.TaskDetails.TaskID,
		nvcatypes.TaskIDUpperKey:    sr.Spec.TaskDetails.TaskID,
		nvcatypes.GPUNameKey:        sr.Spec.CreationMsgInfo.GPUType,
		nvcatypes.MessageBatchIDKey: sr.Spec.MessageBatchID,
		nvcatypes.ICMSRequestIDKey:  sr.Spec.RequestID,
		miniserviceNameLabel:        msName,
	}
	internalObjAnnotations := map[string]string{
		nvcatypes.NCAIDKey:         sr.Spec.NCAId,
		nvcatypes.InstanceCountKey: fmt.Sprint(sr.Spec.CreationMsgInfo.InstanceCount),
		nvcatypes.ClusterGroupKey:  sr.Spec.CreationMsgInfo.ClusterGroup,
		nvcatypes.ICMSRequestIDKey: sr.Spec.RequestID,
	}
	translatedLabels := map[string]string{
		"ENVIRONMENT":               launchSpec.ICMSEnvironment,
		nvcatypes.TaskIDUpperKey:    sr.Spec.TaskDetails.TaskID,
		"GPU_COUNT":                 fmt.Sprint(sr.Spec.CreationMsgInfo.RequestedGPUCount),
		nvcatypes.NCAIDUpperKey:     nvcatypes.MakeNCAIDLabelValue(sr.Spec.NCAId),
		"environment":               launchSpec.ICMSEnvironment,
		"performance_class":         sr.Spec.CreationMsgInfo.InstanceTypeValue,
		nvcatypes.NCAIDKey:          nvcatypes.MakeNCAIDLabelValue(sr.Spec.NCAId),
		nvcatypes.TaskIDKey:         sr.Spec.TaskDetails.TaskID,
		nvcatypes.GPUNameKey:        sr.Spec.CreationMsgInfo.GPUType,
		nvcatypes.MessageBatchIDKey: sr.Spec.MessageBatchID,
		nvcatypes.ICMSRequestIDKey:  sr.Spec.RequestID,
		miniserviceNameLabel:        msName,
	}
	translatedAnnotations := map[string]string{
		nvcatypes.NCAIDKey:                   sr.Spec.NCAId,
		nvcatypes.InstanceCountKey:           fmt.Sprint(sr.Spec.CreationMsgInfo.InstanceCount),
		nvcatypes.ClusterGroupKey:            sr.Spec.CreationMsgInfo.ClusterGroup,
		nvcatypes.ICMSRequestIDKey:           sr.Spec.RequestID,
		"nvcf.nvidia.io/backend":             "",
		"nvcf.nvidia.io/environment":         launchSpec.ICMSEnvironment,
		"nvcf.nvidia.io/instance-type-name":  sr.Spec.CreationMsgInfo.InstanceTypeName,
		"nvcf.nvidia.io/instance-type-value": sr.Spec.CreationMsgInfo.InstanceTypeValue,
		"nvcf.nvidia.io/region":              "",
		"TASK_NAME":                          "my-task",
		"task-name":                          "my-task",
	}
	translatedInfraAnnotations := mergeMaps(translatedAnnotations, map[string]string{
		nvcatypes.InfraObjectAnnotationKey: "true",
	})

	hcCfg, err := common.ExtractHelmConfiguration(launchSpec.EnvironmentB64, launchSpec.HelmChartLaunchSpecification)
	require.NoError(t, err)

	ms := &v1alpha1.MiniService{}
	ms.Name = msName
	ms.Labels = internalObjLabels
	ms.Annotations = internalObjAnnotations
	ms.Spec = v1alpha1.MiniServiceSpec{
		Namespace:       sr.Name,
		ICMSRequestName: sr.Name,
		HelmChartConfig: hcCfg,
	}

	objs := []client.Object{
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: r.ICMSRequestNamespace},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: r.SystemNamespace},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: instanceRBACConfigMapName, Namespace: r.SystemNamespace},
			Data: map[string]string{
				instanceRoleName: `
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
rules:
- apiGroups: [""]
  apiVersions: ["v1"]
  resources: ["pods", "services", "secrets"]
  verbs: ["*"]
- apiGroups: ["apps"]
  apiVersions: ["v1"]
  resources: ["deployments"]
  verbs: ["*"]
- apiGroups: ["rbac.authorization.k8s.io"]
  apiVersions: ["v1"]
  resources: ["roles", "rolebindings"]
  verbs: ["*"]
`,
			},
		},
	}

	objs = append(objs, k8smock.NewNetworkPolicyConfigMap(r.SystemNamespace))

	crClient, tracker := newFakeClient(testScheme, append(objs, sr, ms)...)
	r.Client = crClient

	req := reconcile.Request{}
	req.Name = ms.Name
	_, err = r.Reconcile(ctx, req)
	require.EqualError(t, err, "update default service account with worker image pull secrets: get instance service account: serviceaccounts \"default\" not found")

	createNamespaceDefaultSA(t, ctx, r, ms)

	req = reconcile.Request{}
	req.Name = ms.Name
	gotRes, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, reconcile.Result{}, gotRes)

	// Update image cred job's status as completed so the task can proceed.
	initImageCredsJob := &batchv1.Job{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: sr.Name + "-cred-init", Namespace: r.SystemNamespace}, initImageCredsJob)
	require.NoError(t, err)
	initImageCredsJob.Status = batchv1.JobStatus{
		CompletionTime: &metav1.Time{Time: r.now()},
		Succeeded:      1,
	}
	err = r.Client.Status().Update(ctx, initImageCredsJob)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, reconcile.Result{}, gotRes)

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)
	ns := &corev1.Namespace{}
	ns.Name = ms.Spec.Namespace
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ns), ns)
	require.NoError(t, err)
	// Namespace gets labels from GetLabelsForRequest, which includes task labels for task actions
	expectedNamespaceLabels := nvcatypes.GetLabelsForRequest(sr, r.FeatureFlagFetcher)
	expectedNamespaceLabels["nvca.nvcf.nvidia.io/workload-instance-type"] = "miniservice"
	expectedNamespaceLabels[miniserviceNameLabel] = msName
	assert.Equal(t, expectedNamespaceLabels, ns.Labels)
	assert.Equal(t, internalObjAnnotations, ns.Annotations)

	defaultSA := &corev1.ServiceAccount{}
	err = r.Client.Get(ctx, client.ObjectKey{Namespace: ms.Spec.Namespace, Name: defaultServiceAccountName}, defaultSA)
	require.NoError(t, err)
	if assert.Len(t, defaultSA.ImagePullSecrets, 1) {
		assert.Equal(t, "worker-sr-bar-regcred-0", defaultSA.ImagePullSecrets[0].Name)
	}
	helmSA := &corev1.ServiceAccount{}
	err = r.Client.Get(ctx, client.ObjectKey{Namespace: ms.Spec.Namespace, Name: serviceAccountName}, helmSA)
	require.NoError(t, err)
	if assert.Len(t, helmSA.ImagePullSecrets, 2) {
		assert.Equal(t, "worker-sr-bar-regcred-0", helmSA.ImagePullSecrets[0].Name)
		assert.Equal(t, "workload-sr-bar-regcred-0", helmSA.ImagePullSecrets[1].Name)
	}

	sharedStorageST := &nvcav2beta1.StorageRequest{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: "shared-storage", Namespace: ms.Spec.Namespace}, sharedStorageST)
	require.NoError(t, err)
	// StorageRequest metadata includes translated task labels and infra annotations.
	assert.Equal(t, translatedLabels, sharedStorageST.Labels)
	assert.Equal(t, translatedInfraAnnotations, sharedStorageST.Annotations)
	assert.Equal(t, "smb:latest", sharedStorageST.Spec.SharedStorage.SMBContainerImage)
	assert.Equal(t, "worker-sr-bar-regcred-0", sharedStorageST.Spec.SharedStorage.WorkerPullSecretName)
	assert.Equal(t, []string{
		"worker-sr-bar-regcred-0",
	}, sharedStorageST.Spec.SharedStorage.WorkerPullSecretNames)

	sharedStorageST.Status.Phase = nvcav2beta1.StorageReady
	sharedStorageST.Status.SharedStorage = &nvcav2beta1.SharedStorageStatus{
		KNS: nvcav2beta1.SharedStorageTypeStatus{
			ReadOnlyPVCName:  "test-kns-ropvc",
			ReadWritePVCName: "test-kns-rwpvc",
		},
		Secrets: nvcav2beta1.SharedStorageTypeStatus{
			ReadOnlyPVCName:  "test-secrets-ropvc",
			ReadWritePVCName: "test-secrets-rwpvc",
		},
	}
	err = r.Client.Status().Update(ctx, sharedStorageST)
	require.NoError(t, err)

	// Storage is ready, reconcile should proceed.
	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, reconcile.Result{}, gotRes)

	// Check prereqs.
	utilsPod := &corev1.Pod{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: "utils", Namespace: ms.Spec.Namespace}, utilsPod)
	require.NoError(t, err)
	expEnvs := []corev1.EnvVar{
		{Name: nvcatypes.InstanceIDEnvKey, Value: ms.Name},
		{Name: InferenceNamespaceEnvKey, Value: ms.Spec.Namespace},
		{Name: nvcatypes.InferenceReadyTimeoutEnvKey, Value: "2h0m0s"},
	}
	for _, c := range utilsPod.Spec.InitContainers {
		assert.Subset(t, c.Env, expEnvs)
	}
	for _, c := range utilsPod.Spec.Containers {
		assert.Subset(t, c.Env, expEnvs)
		if c.Name == common.UtilsContainerName {
			assert.Subset(t, c.Env, []corev1.EnvVar{
				{Name: "POLL_PROGRESS", Value: "true"},
			})
		}
	}
	assert.Equal(t, mergeMaps(translatedLabels, map[string]string{
		common.BYOOMetricsEgressTargetLabelKey: common.BYOOMetricsEgressTargetLabelValue,
	}), utilsPod.Labels)
	assert.Equal(t, mergeMaps(translatedInfraAnnotations, map[string]string{
		nvcastorage.HelmWebhookSharedStorageKNSReadWritePVCNameAnnotationKey:      "test-kns-rwpvc",
		nvcastorage.HelmWebhookSharedStorageSecretsReadWritePVCNameAnnotationKey:  "test-secrets-rwpvc",
		nvcastorage.HelmWebhookSharedStorageTaskDataReadWritePVCNameAnnotationKey: nvcastorage.SharedStorageTaskDataReadWritePVCName,
	}), utilsPod.Annotations)

	utilsPod.CreationTimestamp = metav1.Time{Time: time.Now().Add(-1 * time.Minute)}
	err = tracker.Update(corev1.SchemeGroupVersion.WithResource("pods"), utilsPod, ms.Spec.Namespace)
	require.NoError(t, err)

	byooSvc := &corev1.Service{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: common.ByooOTelCollectorPodNameBase, Namespace: ms.Spec.Namespace}, byooSvc)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		common.BYOOMetricsEgressTargetLabelKey: common.BYOOMetricsEgressTargetLabelValue,
	}, byooSvc.Spec.Selector)

	sa := &corev1.ServiceAccount{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: serviceAccountName, Namespace: ms.Spec.Namespace}, sa)
	require.NoError(t, err)

	role := &rbacv1.Role{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: serviceAccountName, Namespace: ms.Spec.Namespace}, role)
	require.NoError(t, err)

	roleBinding := &rbacv1.RoleBinding{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: serviceAccountName, Namespace: ms.Spec.Namespace}, roleBinding)
	require.NoError(t, err)

	netPolList := &netv1.NetworkPolicyList{}
	err = r.Client.List(ctx, netPolList, client.InNamespace(ms.Spec.Namespace))
	require.NoError(t, err)
	assert.Len(t, netPolList.Items, 7)

	// Check that initial resources were not created until after the utils pod's init containers complete.
	gotJob := &batchv1.Job{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: "foo", Namespace: ms.Spec.Namespace}, gotJob)
	require.True(t, apierrors.IsNotFound(err))

	utilsPod.Status.Phase = corev1.PodPending
	utilsPod.Status.StartTime = &metav1.Time{Time: r.now()}
	utilsPod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
		Name: common.InitContainerName,
		State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{},
		},
	}}
	utilsPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: common.UtilsContainerName,
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{},
		},
	}}
	utilsPod.Status.Conditions = []corev1.PodCondition{
		{
			Type:   corev1.PodScheduled,
			Status: corev1.ConditionTrue,
		},
		{
			Type:   corev1.PodInitialized,
			Status: corev1.ConditionTrue,
		},
	}
	err = r.Client.Status().Update(ctx, utilsPod)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, reconcile.Result{}, gotRes)

	// Check that initial resources were created.
	err = r.Client.Get(ctx, client.ObjectKey{Name: "foo", Namespace: ms.Spec.Namespace}, gotJob)
	require.NoError(t, err)

	gotJob.Annotations = filterNVCFAnnotations(gotJob.Annotations)
	expJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "foo",
			Namespace:       ms.Spec.Namespace,
			ResourceVersion: "1",
			Labels:          translatedLabels,
			Annotations:     filterNVCFAnnotations(translatedAnnotations),
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:    "test",
						Image:   "nvcr.io/foo/bar:baz",
						Command: []string{"yes"},
					}},
				},
			},
		},
	}
	assert.Equal(t, expJob, gotJob)

	// Verify the controller created the metadata ConfigMap with the expected data.
	metadataCM := &corev1.ConfigMap{}
	err = r.Client.Get(ctx, client.ObjectKey{
		Namespace: ms.Spec.Namespace,
		Name:      nvcatypes.MiniserviceMetadataConfigMapName,
	}, metadataCM)
	require.NoError(t, err, "metadata ConfigMap should exist")
	msMeta, err := nvcatypes.FromConfigMapData(metadataCM.Data)
	require.NoError(t, err, "metadata ConfigMap should deserialize")
	assert.Equal(t, common.TaskCreationAction, msMeta.MessageAction)
	assert.Equal(t, serviceAccountName, msMeta.ServiceAccountName)
	assert.NotEmpty(t, msMeta.EnvVars, "metadata should include workload env vars")
	assert.NotNil(t, msMeta.TerminationGracePeriodSeconds)

	gotSecrets := &corev1.SecretList{}
	err = r.Client.List(ctx, gotSecrets, client.InNamespace(ms.Spec.Namespace))
	require.NoError(t, err)
	require.Len(t, gotSecrets.Items, 4)
	sort.Slice(gotSecrets.Items, func(i, j int) bool {
		return gotSecrets.Items[i].Name < gotSecrets.Items[j].Name
	})
	assert.Equal(t, corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: common.HelmChartDownloadSecretName, Namespace: ms.Spec.Namespace,
			ResourceVersion: "1",
			Labels:          translatedLabels,
			Annotations:     translatedInfraAnnotations,
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"username": "stg-abc123",
			"password": "6f634d85-931e-41f6-a2ec-67b8750f127e",
		},
	}, gotSecrets.Items[0])
	assert.Equal(t, corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sr-bar-image-creds", Namespace: ms.Spec.Namespace,
			ResourceVersion: "1",
			Labels:          translatedLabels,
			Annotations:     translatedInfraAnnotations,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			common.ContainerRegistriesCredentialsEnv: []byte("eyJrOHNTZWNyZXRzIjpbeyJhdXRocyI6eyJyZWdpc3RyeS5leGFtcGxlLnRlc3QiOnsiYXV0aCI6ImRHVnpkQzExYzJWeU9uUmxjM1F0Y0dGemN3PT0ifX19XX0="),
			common.SidecarRegistryCredentialEnv:      []byte("eyJhdXRocyI6eyJyZWdpc3RyeS5leGFtcGxlLnRlc3QiOnsiYXV0aCI6ImRHVnpkQzExYzJWeU9uUmxjM1F0Y0dGemN3PT0ifX19"),
		},
	}, gotSecrets.Items[1])
	assert.Equal(t, corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-sr-bar-regcred-0", Namespace: ms.Spec.Namespace,
			ResourceVersion: "1",
			Labels:          translatedLabels,
			Annotations:     translatedInfraAnnotations,
		},
		StringData: map[string]string{
			corev1.DockerConfigJsonKey: `{"auths":{"registry.example.test":{"auth":"dGVzdC11c2VyOnRlc3QtcGFzcw=="}}}`,
		},
		Type: corev1.SecretTypeDockerConfigJson,
	}, gotSecrets.Items[2])
	assert.Equal(t, corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "workload-sr-bar-regcred-0", Namespace: ms.Spec.Namespace,
			ResourceVersion: "1",
			Labels:          translatedLabels,
			Annotations:     translatedInfraAnnotations,
		},
		StringData: map[string]string{
			corev1.DockerConfigJsonKey: `{"auths":{"registry.example.test":{"auth":"dGVzdC11c2VyOnRlc3QtcGFzcw=="}}}`,
		},
		Type: corev1.SecretTypeDockerConfigJson,
	}, gotSecrets.Items[3])

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceInstalled, ms.Status.Phase)
	assert.Equal(t, int64(0), ms.Status.Revision, "initial install should be revision 0")
	assert.Equal(t, ms.Generation, ms.Status.ObservedGeneration, "observedGeneration should match generation after install")

	gotData, found, err := r.getRenderedData(ctx, ms)
	require.NoError(t, err)
	if assert.True(t, found) {
		assert.JSONEq(t, string(objBytes), string(gotData))
	}

	// Create job pods and set phase to running, ensure status checkers run.
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
	err = r.Client.Status().Update(ctx, utilsPod)
	require.NoError(t, err)

	jobPod := &corev1.Pod{}
	jobPod.ObjectMeta.OwnerReferences = append(jobPod.ObjectMeta.OwnerReferences, metav1.OwnerReference{
		Kind:       "Job",
		APIVersion: "batch/v1",
		Name:       gotJob.Name,
		Controller: newBool(true),
		UID:        gotJob.UID,
	})
	jobPod.Name, jobPod.Namespace = "job-pod", ms.Spec.Namespace
	jobPod.Spec = gotJob.Spec.Template.Spec
	jobPod.Status.Phase = corev1.PodRunning
	jobPod.Status.StartTime = &metav1.Time{Time: r.now()}
	jobPod.Status.Conditions = []corev1.PodCondition{{
		Type:   corev1.PodReady,
		Status: corev1.ConditionTrue,
	}}
	err = r.Client.Create(ctx, jobPod)
	require.NoError(t, err)

	gotJob.Status.Active++
	err = r.Client.Status().Update(ctx, gotJob)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, gotRes)

	sms := &v1alpha1.MiniService{}
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), sms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceRunning, sms.Status.Phase)
	if assert.Len(t, sms.Status.Conditions, 2) {
		cmpConditions(t, []metav1.Condition{
			{
				Reason: "AllObjectsApplied",
				Type:   v1alpha1.MiniServiceConditionInstallSuccessful,
				Status: metav1.ConditionTrue,
			},
			{
				Reason: "ObjectsReady",
				Type:   v1alpha1.MiniServiceConditionObjectsHealthy,
				Status: metav1.ConditionTrue,
			},
		}, sms.Status.Conditions)
	}

	// Set deletion timestamp on miniservice, should be cleaned up and finalizer remains until namespace is deleted.
	sms.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	sms.Finalizers = []string{finalizer}
	err = tracker.Update(v1alpha1.SchemeGroupVersion.WithResource("miniservices"), sms, "")
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, gotRes)

	sms = &v1alpha1.MiniService{}
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), sms)
	require.NoError(t, err)
	assert.Equal(t, []string{finalizer}, sms.Finalizers)

	if assert.Len(t, sms.Status.Conditions, 3) {
		cmpConditions(t, []metav1.Condition{
			{
				Reason: "AllObjectsApplied",
				Type:   v1alpha1.MiniServiceConditionInstallSuccessful,
				Status: metav1.ConditionTrue,
			},
			{
				Reason: "ObjectsReady",
				Type:   v1alpha1.MiniServiceConditionObjectsHealthy,
				Status: metav1.ConditionTrue,
			},
			{
				Type:    v1alpha1.MiniServiceConditionCleanupSuccessful,
				Status:  metav1.ConditionFalse,
				Reason:  "SomeObjectsPendingDeletion",
				Message: `Objects: "/v1, Kind=Pod, job-pod;"`,
			},
		}, sms.Status.Conditions)
	}

	// Controller should have deleted the pod, even if listed in message condition.
	err = r.Client.Delete(ctx, jobPod)
	require.True(t, apierrors.IsNotFound(err))

	gotRes, err = r.Reconcile(ctx, req)
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, gotRes)

	sms = &v1alpha1.MiniService{}
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), sms)
	require.NoError(t, err)
	assert.Equal(t, []string{finalizer}, sms.Finalizers)

	if assert.Len(t, sms.Status.Conditions, 3) {
		cmpConditions(t, []metav1.Condition{
			{
				Reason: "AllObjectsApplied",
				Type:   v1alpha1.MiniServiceConditionInstallSuccessful,
				Status: metav1.ConditionTrue,
			},
			{
				Reason: "ObjectsReady",
				Type:   v1alpha1.MiniServiceConditionObjectsHealthy,
				Status: metav1.ConditionTrue,
			},
			{
				Type:    v1alpha1.MiniServiceConditionCleanupSuccessful,
				Status:  metav1.ConditionFalse,
				Reason:  "SomeObjectsPendingDeletion",
				Message: "StorageRequests: shared-storage",
			},
		}, sms.Status.Conditions)
	}

	// Reconcile again with namespace/shared storage gone, miniservice should be cleaned up and finalizer removed.
	err = r.Client.Delete(ctx, sharedStorageST)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, gotRes)

	sms = &v1alpha1.MiniService{}
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), sms)
	require.True(t, apierrors.IsNotFound(err))
}

func TestReconcile_TaskStatus(t *testing.T) {
	ctx := newTestContext()
	testScheme := mgrScheme

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

	r := &Reconciler{
		ControllerOptions: ControllerOptions{
			SystemNamespace:      "nvca-system",
			ICMSRequestNamespace: "nvcf-backend",
			K8sVersion:           "1.29.5",
			FeatureFlagFetcher: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{
					featureflag.HelmRBACEnforcement,
					featureflag.HelmResourceConstraints,
					featureflag.EnforceHelmTaskResourceLimits,
					featureflag.InfraResourceOverhead,
					&featureflag.HelmSharedStorage.FeatureFlag,
					featureflag.BYOObservability,
				},
			},
			K8sTimeConfig: (&k8sutil.TimeConfig{
				FailingObjectsBackoffTimeout:         1 * time.Millisecond, // Very short for tests
				FailingObjectsBackoffRequeueInterval: 1 * time.Millisecond, // Very short for tests
			}).Complete(),
			ImageCredentialHelperImage: "stg.nvcr.io/nv-cf/nvcf-core/image-credential-helper:latest",
			OverheadGetter:             enforce.NoOpInfraOverheadGetter,
			Metrics:                    metrics.NewDefaultMetrics("test-nca-id", "test-cluster", "test-group", "test-version", metrics.WithRegisterer(prometheus.NewRegistry())),
		},
		Decoder:               serializer.NewCodecFactory(testScheme).UniversalDeserializer(),
		NFClient:              nfClient,
		tracer:                otel.NewTracer(),
		eventRecorder:         record.NewFakeRecorder(10),
		chartCache:            chartcache.New(t.TempDir()),
		regITCache:            regITCache,
		now:                   time.Now,
		newPermissionsChecker: newFakePermissionsChecker,
	}
	r.statusCheckers = r.makeStatusCheckers()
	// Impersonation can't be mocked using the fake client.
	// Envtest handles this.
	r.newImpersonatingClient = func(_ string) (client.Client, error) {
		return r.Client, nil
	}

	r.cfg.Agent.SharedStorage.Server.Image = "smb:latest"
	err := k8sutil.SetConfigDefaultResources(&r.cfg)
	require.NoError(t, err)

	err = r.chartCache.Start(ctx)
	require.NoError(t, err)

	tgps := int64((2 * time.Hour).Seconds())
	helmObjs := []client.Object{
		&batchv1.Job{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "batch/v1",
				Kind:       "Job",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "foo",
			},
			Spec: batchv1.JobSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						RestartPolicy:                 corev1.RestartPolicyNever,
						TerminationGracePeriodSeconds: &tgps,
						Containers: []corev1.Container{{
							Name:    "test",
							Image:   "nvcr.io/foo/bar:baz",
							Command: []string{"yes"},
						}},
					},
				},
			},
		},
	}
	objBytes, err := json.MarshalIndent(helmObjs, "", "  ")
	require.NoError(t, err)
	var wantRevalErr *error = nil
	rvClient := newFakeReValClient(t, objBytes, wantRevalErr)
	r.ReValClient = rvClient

	envs := []corev1.EnvVar{
		{Name: common.ContainerRegistriesCredentialsEnv, Value: "eyJrOHNTZWNyZXRzIjpbeyJhdXRocyI6eyJyZWdpc3RyeS5leGFtcGxlLnRlc3QiOnsiYXV0aCI6ImRHVnpkQzExYzJWeU9uUmxjM1F0Y0dGemN3PT0ifX19XX0="},
		{Name: common.HelmRegistriesCredentialsEnv, Value: "eyJrOHNTZWNyZXRzIjpbeyJhdXRocyI6eyJoZWxtLm5nYy5udmlkaWEuY29tIjp7ImF1dGgiOiJjM1JuTFdGaVl6RXlNem8yWmpZek5HUTROUzA1TXpGbExUUXhaall0WVRKbFl5MDJOMkk0TnpVd1pqRXlOMlU9In19fV19"},
		{Name: "INIT_CONTAINER", Value: "registry.example.test/nvcf-core/nvcf_worker_init:0.24.10"},
		{Name: "MAX_REQUEST_CONCURRENCY", Value: "1"},
		{Name: "NVCF_FQDN", Value: "https://api.example.test"},
		{Name: "NVCF_FQDN_GRPC", Value: "https://grpc.example.test"},
		{Name: "NVCF_FQDN_NATS", Value: "tls://nats.example.test:4222"},
		{Name: "NVCF_WORKER_TOKEN", Value: "tok"},
		{Name: "OTEL_CONTAINER", Value: "registry.example.test/nvcf-core/opentelemetry-collector:0.74.0"},
		{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: "https://otel.example.test:8282"},
		{Name: common.SidecarRegistryCredentialEnv, Value: "eyJhdXRocyI6eyJyZWdpc3RyeS5leGFtcGxlLnRlc3QiOnsiYXV0aCI6ImRHVnpkQzExYzJWeU9uUmxjM1F0Y0dGemN3PT0ifX19"},
		{Name: "TASK_CONTAINER", Value: "nvcr.io/myorg/gpt-3.5-turbo-fine-tune:1.0.0"},
		{Name: "TASK_CONTAINER_ARGS", Value: "-arg1=test1 arg2=test2"},
		{Name: "TASK_CONTAINER_ENV", Value: "W3sia2V5IjoiVEFTS19FTlZfS0VZIiwidmFsdWUiOiJ0YXNrX3ZhbHVlIn1d"},
		{Name: "TASK_HEALTH_ENDPOINT", Value: "/v2/health/ready"},
		{Name: "TASK_HEALTH_EXPECTED_RESPONSE_CODE", Value: "200"},
		{Name: "TASK_HEALTH_PORT", Value: "50051"},
		{Name: "TASK_NAME", Value: "my-task"},
		{Name: "TASK_PORT", Value: "50051"},
		{Name: "TASK_PROTOCOL", Value: "GRPC"},
		{Name: "TASK_URL", Value: "/grpc"},
		{Name: "TERMINATION_GRACE_PERIOD", Value: "PT2H"},
		{Name: "TRACING_ACCESS_TOKEN", Value: "trace-tok-1"},
		{Name: "UTILS_CONTAINER", Value: "registry.example.test/nvcf-core/nvcf_worker_utils:2.21.4"},
	}

	sr := &nvcav2beta1.ICMSRequest{}
	sr.Name = "sr-baz"
	sr.Namespace = r.ICMSRequestNamespace
	sr.Spec = nvcav2beta1.ICMSRequestSpec{
		TaskDetails: task.Details{
			TaskID:   "taskid-1",
			TaskType: "HELMCHART",
		},
		Action:         common.TaskCreationAction,
		NCAId:          "ncaid-1",
		RequestID:      "reqid1",
		MessageBatchID: "mbatchid1",
		CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
			CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
				Action:            common.TaskCreationAction,
				RequestID:         "reqid1",
				MessageBatchID:    "mbatchid1",
				InstanceType:      "ON-PREM.GPU.L40",
				InstanceTypeName:  "ON-PREM.GPU.L40_1x",
				InstanceTypeValue: "ON-PREM.GPU.L40",
				GPUType:           "L40",
				RequestedGPUCount: 1,
				InstanceCount:     1,
				NCAID:             "ncaid-1",
				AccountName:       "account1",
			},
			TaskLaunchSpecification: &task.LaunchSpecification{
				CloudProvider:                  "DGXCLOUD",
				ICMSEnvironment:                "prod",
				ResultHandlingStrategy:         "UPLOAD",
				TerminationGracePeriodDuration: "PT10M",
				MaxRuntimeDuration:             "PT2H",
				MaxQueuedDuration:              "PT2M",
				HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{
					HelmChartURL: "https://helm.ngc.nvidia.com/myorg/myteam/charts/image-segmentation-1.0.3.tgz",
					Values:       []byte(`{"foo":{"bar":"baz"}}`),
				},
			},
		},
	}
	cmInfo := sr.Spec.CreationMsgInfo
	launchSpec := cmInfo.TaskLaunchSpecification

	// Set envs from ICMS request to align values.
	envs = append(envs, []corev1.EnvVar{
		{Name: "ATTACHED_GPU_COUNT", Value: fmt.Sprint(cmInfo.CreationQueueMessageMetadata.RequestedGPUCount)},
		{Name: "CLOUD_PROVIDER", Value: launchSpec.CloudProvider},
		{Name: "TASK_ID", Value: sr.Spec.TaskDetails.TaskID},
		{Name: "GPU_NAME", Value: cmInfo.GPUType},
		{Name: "NCA_ID", Value: cmInfo.CreationQueueMessageMetadata.NCAID},
	}...)

	launchSpec.EnvironmentB64 = encodeEnvsForLaunchSpec(envs)

	msName := "miniservice"
	internalObjLabels := map[string]string{
		nvcatypes.NCAIDKey:          nvcatypes.MakeNCAIDLabelValue(sr.Spec.NCAId),
		nvcatypes.NCAIDUpperKey:     nvcatypes.MakeNCAIDLabelValue(sr.Spec.NCAId),
		nvcatypes.TaskIDKey:         sr.Spec.TaskDetails.TaskID,
		nvcatypes.TaskIDUpperKey:    sr.Spec.TaskDetails.TaskID,
		nvcatypes.GPUNameKey:        sr.Spec.CreationMsgInfo.GPUType,
		nvcatypes.MessageBatchIDKey: sr.Spec.MessageBatchID,
		nvcatypes.ICMSRequestIDKey:  sr.Spec.RequestID,
		miniserviceNameLabel:        msName,
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
	ms.Name = msName
	ms.Labels = internalObjLabels
	ms.Annotations = internalObjAnnotations
	ms.Spec = v1alpha1.MiniServiceSpec{
		Namespace:       sr.Name,
		ICMSRequestName: sr.Name,
		HelmChartConfig: hcCfg,
	}

	objs := []client.Object{
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: r.ICMSRequestNamespace},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: r.SystemNamespace},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: instanceRBACConfigMapName, Namespace: r.SystemNamespace},
			Data: map[string]string{
				instanceRoleName: `
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
rules:
- apiGroups: [""]
  apiVersions: ["v1"]
  resources: ["pods", "services", "secrets"]
  verbs: ["*"]
- apiGroups: ["apps"]
  apiVersions: ["v1"]
  resources: ["deployments"]
  verbs: ["*"]
- apiGroups: ["rbac.authorization.k8s.io"]
  apiVersions: ["v1"]
  resources: ["roles", "rolebindings"]
  verbs: ["*"]
`,
			},
		},
	}

	objs = append(objs, k8smock.NewNetworkPolicyConfigMap(r.SystemNamespace))

	crClient, tracker := newFakeClient(testScheme, append(objs, sr, ms)...)
	r.Client = crClient

	// Ensure the case where no utils pod is present results in an error.
	statusMS := ms.DeepCopy()
	_, err = r.doStatus(ctx, statusMS, sr)
	require.EqualError(t, err, "terminal error: utils pod not found")
	if assert.Len(t, statusMS.Status.Conditions, 1) {
		cmpConditions(t, []metav1.Condition{
			{
				Reason:  v1alpha1.MiniServiceStatusReasonObjectsFailed,
				Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:  metav1.ConditionFalse,
				Message: "Infrastructure objects not found",
			},
		}, statusMS.Status.Conditions)
	}

	req := reconcile.Request{}
	req.Name = ms.Name
	_, err = r.Reconcile(ctx, req)
	require.EqualError(t, err, "update default service account with worker image pull secrets: get instance service account: serviceaccounts \"default\" not found")

	createNamespaceDefaultSA(t, ctx, r, ms)

	req = reconcile.Request{}
	req.Name = ms.Name
	gotRes, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, reconcile.Result{}, gotRes)

	// Update image cred job's status as completed so the task can proceed.
	initImageCredsJob := &batchv1.Job{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: sr.Name + "-cred-init", Namespace: r.SystemNamespace}, initImageCredsJob)
	require.NoError(t, err)
	initImageCredsJob.Status = batchv1.JobStatus{
		CompletionTime: &metav1.Time{Time: r.now()},
		Succeeded:      1,
	}
	err = r.Client.Status().Update(ctx, initImageCredsJob)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, reconcile.Result{}, gotRes)

	sharedStorageST := &nvcav2beta1.StorageRequest{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: "shared-storage", Namespace: ms.Spec.Namespace}, sharedStorageST)
	require.NoError(t, err)

	sharedStorageST.Status.Phase = nvcav2beta1.StorageReady
	sharedStorageST.Status.SharedStorage = &nvcav2beta1.SharedStorageStatus{
		KNS: nvcav2beta1.SharedStorageTypeStatus{
			ReadOnlyPVCName:  "test-kns-ropvc",
			ReadWritePVCName: "test-kns-rwpvc",
		},
		Secrets: nvcav2beta1.SharedStorageTypeStatus{
			ReadOnlyPVCName:  "test-secrets-ropvc",
			ReadWritePVCName: "test-secrets-rwpvc",
		},
	}
	err = r.Client.Status().Update(ctx, sharedStorageST)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, gotRes)

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)

	// Set utils pod phase to pending, ensure status checkers run
	// and mark the miniservice as Installing since utils init container is still running.
	utilsPod := &corev1.Pod{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: "utils", Namespace: ms.Spec.Namespace}, utilsPod)
	require.NoError(t, err)

	utilsPod.Status.Phase = corev1.PodPending
	utilsPod.Status.StartTime = &metav1.Time{Time: r.now()}
	utilsPod.Status.InitContainerStatuses = []corev1.ContainerStatus{
		{
			Name: common.InitContainerName,
			State: corev1.ContainerState{
				Running: &corev1.ContainerStateRunning{},
			},
		},
	}
	utilsPod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name: "utils",
			State: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{},
			},
		},
	}
	utilsPod.Status.Conditions = []corev1.PodCondition{
		{
			Type:   corev1.PodScheduled,
			Status: corev1.ConditionTrue,
		},
		{
			Type:   corev1.PodInitialized,
			Status: corev1.ConditionTrue,
		},
	}
	err = r.Client.Status().Update(ctx, utilsPod)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, gotRes)

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)

	job := helmObjs[0].(*batchv1.Job)
	err = r.Client.Get(ctx, client.ObjectKey{Name: job.Name, Namespace: ms.Spec.Namespace}, job)
	require.True(t, apierrors.IsNotFound(err))

	// Create job pods and set utils pod phase to running, ensure status checkers run
	// and mark the miniservice as Installed since job/pods are not scheduled.
	utilsPod.Status.Phase = corev1.PodRunning
	utilsPod.Status.InitContainerStatuses = []corev1.ContainerStatus{
		{
			Name: common.InitContainerName,
			State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{},
			},
		},
	}
	utilsPod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name: "utils",
			State: corev1.ContainerState{
				Running: &corev1.ContainerStateRunning{},
			},
		},
	}
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
	err = r.Client.Status().Update(ctx, utilsPod)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, gotRes)

	err = r.Client.Get(ctx, client.ObjectKey{Name: job.Name, Namespace: ms.Spec.Namespace}, job)
	require.NoError(t, err)

	jobPod := &corev1.Pod{}
	jobPod.ObjectMeta.OwnerReferences = append(jobPod.ObjectMeta.OwnerReferences, metav1.OwnerReference{
		Kind:       "Job",
		APIVersion: "batch/v1",
		Name:       job.Name,
		Controller: newBool(true),
		UID:        job.UID,
	})
	jobPod.Name, jobPod.Namespace = "job-pod", ms.Spec.Namespace
	jobPod.Spec = job.Spec.Template.Spec
	jobPod.Status.Phase = corev1.PodPending
	jobPod.Status.Conditions = []corev1.PodCondition{{
		Type:   corev1.PodScheduled,
		Status: corev1.ConditionFalse,
	}}
	err = r.Client.Create(ctx, jobPod)
	require.NoError(t, err)

	job.Status.StartTime = &metav1.Time{Time: r.now()}
	err = r.Client.Status().Update(ctx, job)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	// The job's pod has no startTime or "scheduled=true", should requeue after 2 minutes (max queue duration).
	assert.Equal(t, reconcile.Result{RequeueAfter: 2 * time.Minute}, gotRes)

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceInstalled, ms.Status.Phase)
	assert.Equal(t, int64(0), ms.Status.Revision, "initial install should be revision 0")
	assert.Equal(t, ms.Generation, ms.Status.ObservedGeneration, "observedGeneration should match generation after install")
	if assert.Len(t, ms.Status.Conditions, 2) {
		cmpConditions(t, []metav1.Condition{
			{
				Reason: "AllObjectsApplied",
				Type:   v1alpha1.MiniServiceConditionInstallSuccessful,
				Status: metav1.ConditionTrue,
			},
			{
				Reason:  v1alpha1.MiniServiceStatusReasonWaitingObjectReadiness,
				Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:  metav1.ConditionFalse,
				Message: "Some objects are not scheduled\nbatch/v1.Job foo\nv1.Pod job-pod\n",
			},
		}, ms.Status.Conditions)
	}

	// Pod is scheduled and containers are initializing, should see phase -> Running
	// since task pods do not wait for completion to be marked running.
	jobPod.Status.Phase = corev1.PodPending
	jobPod.Status.Conditions = []corev1.PodCondition{
		{
			Type:   corev1.PodScheduled,
			Status: corev1.ConditionTrue,
		},
		{
			Type:   corev1.PodReady,
			Status: corev1.ConditionFalse,
		},
		{
			Type:   corev1.ContainersReady,
			Status: corev1.ConditionFalse,
		},
	}
	jobPod.Status.StartTime = &metav1.Time{Time: r.now()}
	err = r.Client.Status().Update(ctx, jobPod)
	require.NoError(t, err)
	job.Status.Active++
	err = r.Client.Status().Update(ctx, job)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, gotRes)

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceRunning, ms.Status.Phase)
	if assert.Len(t, ms.Status.Conditions, 2) {
		cmpConditions(t, []metav1.Condition{
			{
				Reason: "AllObjectsApplied",
				Type:   v1alpha1.MiniServiceConditionInstallSuccessful,
				Status: metav1.ConditionTrue,
			},
			{
				Reason: "ObjectsReady",
				Type:   v1alpha1.MiniServiceConditionObjectsHealthy,
				Status: metav1.ConditionTrue,
			},
		}, ms.Status.Conditions)
	}

	// Pod is scheduled and containers are running, should see phase still Running.
	jobPod.Status.Phase = corev1.PodPending
	jobPod.Status.Conditions = []corev1.PodCondition{
		{
			Type:   corev1.PodScheduled,
			Status: corev1.ConditionTrue,
		},
		{
			Type:   corev1.PodReady,
			Status: corev1.ConditionTrue,
		},
		{
			Type:   corev1.ContainersReady,
			Status: corev1.ConditionTrue,
		},
	}
	err = r.Client.Status().Update(ctx, jobPod)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, gotRes)

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceRunning, ms.Status.Phase)
	if assert.Len(t, ms.Status.Conditions, 2) {
		cmpConditions(t, []metav1.Condition{
			{
				Reason: "AllObjectsApplied",
				Type:   v1alpha1.MiniServiceConditionInstallSuccessful,
				Status: metav1.ConditionTrue,
			},
			{
				Reason: "ObjectsReady",
				Type:   v1alpha1.MiniServiceConditionObjectsHealthy,
				Status: metav1.ConditionTrue,
			},
		}, ms.Status.Conditions)
	}

	// Mark a pod as failed but utils succeeded, ensure miniservice is marked as task failed.
	jobPod.Status.Phase = corev1.PodRunning
	jobPod.Status.Reason = "SomePodReason"
	jobPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "foo",
		State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 1,
				Reason:   "Exited",
			},
		},
	}}
	jobPod.Status.Conditions = []corev1.PodCondition{
		{
			Type:   corev1.PodReady,
			Status: corev1.ConditionFalse,
			Reason: "SomePodNotReadyReason",
		},
		{
			Type:   corev1.ContainersReady,
			Status: corev1.ConditionFalse,
			Reason: "SomeContainersNotReadyReason",
		},
		{
			Type:   corev1.PodInitialized,
			Status: corev1.ConditionTrue,
		},
	}
	err = r.Client.Status().Update(ctx, jobPod)
	require.NoError(t, err)

	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
		Type:    batchv1.JobFailed,
		Status:  corev1.ConditionTrue,
		Message: "Job has reached the specified backoff limit",
		Reason:  batchv1.JobReasonBackoffLimitExceeded,
	})
	err = r.Client.Status().Update(ctx, job)
	require.NoError(t, err)

	// Ensure non-exited utils pod before max runtime duration ignores status update.
	utilsPod.CreationTimestamp = metav1.Time{Time: time.Now().Add(-1 * time.Hour)}
	utilsPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "utils",
		State: corev1.ContainerState{
			Running: &corev1.ContainerStateRunning{},
		},
	}}
	err = tracker.Update(corev1.SchemeGroupVersion.WithResource("pods"), utilsPod, ms.Spec.Namespace)
	require.NoError(t, err)

	// First reconcile - objects fail, enters backoff mode
	gotRes, err = r.Reconcile(ctx, req)
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{Requeue: false, RequeueAfter: 1 * time.Millisecond}, gotRes)

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceRunning, ms.Status.Phase)
	if assert.Len(t, ms.Status.Conditions, 2) {
		cmpConditions(t, []metav1.Condition{
			{
				Reason: "AllObjectsApplied",
				Type:   v1alpha1.MiniServiceConditionInstallSuccessful,
				Status: metav1.ConditionTrue,
			},
			{
				Reason:  v1alpha1.MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout,
				Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:  metav1.ConditionFalse,
				Message: "batch/v1.Job foo: BackoffLimitExceeded\nv1.Pod job-pod: SomeContainersNotReadyReason\n",
			},
		}, ms.Status.Conditions)
	}

	// Sleep to allow backoff timeout to elapse (configured to 1ms in test)
	time.Sleep(5 * time.Millisecond)

	// Second reconcile - backoff timeout elapsed, now should fail terminally
	gotRes, err = r.Reconcile(ctx, req)
	assert.EqualError(t, err, "terminal error: some objects failed")
	assert.Equal(t, reconcile.Result{}, gotRes)

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceFailed, ms.Status.Phase)
	if assert.Len(t, ms.Status.Conditions, 2) {
		cmpConditions(t, []metav1.Condition{
			{
				Reason: "AllObjectsApplied",
				Type:   v1alpha1.MiniServiceConditionInstallSuccessful,
				Status: metav1.ConditionTrue,
			},
			{
				Reason:  v1alpha1.MiniServiceStatusReasonObjectsFailed,
				Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:  metav1.ConditionFalse,
				Message: "batch/v1.Job foo: BackoffLimitExceeded\nv1.Pod job-pod: SomeContainersNotReadyReason\n",
			},
		}, ms.Status.Conditions)
	}

	// Ensure exited utils pod results in task completion.
	ms.Status.Phase = v1alpha1.MiniServiceRunning
	err = r.Client.Status().Update(ctx, ms)
	require.NoError(t, err)

	utilsPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "utils",
		State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 0,
			},
		},
	}}
	err = r.Client.Status().Update(ctx, utilsPod)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, gotRes)

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceCompleted, ms.Status.Phase)
	if assert.Len(t, ms.Status.Conditions, 2) {
		cmpConditions(t, []metav1.Condition{
			{
				Reason: "AllObjectsApplied",
				Type:   v1alpha1.MiniServiceConditionInstallSuccessful,
				Status: metav1.ConditionTrue,
			},
			{
				Reason:  "UtilsPodCompletedSuccessfully",
				Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:  metav1.ConditionTrue,
				Message: "Task completed successfully",
			},
		}, ms.Status.Conditions)
	}

	// Since utils completed successfully, no explicit pod cleanup should occur.
	podList := &corev1.PodList{}
	err = r.Client.List(ctx, podList, client.InNamespace(ms.Spec.Namespace))
	require.NoError(t, err)
	gotTaskPodsToDelete, gotForceDelete := getTaskPodsToDelete(ms, podList)
	assert.Len(t, gotTaskPodsToDelete, 1)
	assert.False(t, gotForceDelete)

	// Mark utils pod as running over max runtime duration, ensure miniservice is marked failed.
	ms.Status.Phase = v1alpha1.MiniServiceRunning
	err = r.Client.Status().Update(ctx, ms)
	require.NoError(t, err)

	utilsPod.CreationTimestamp = metav1.Time{Time: time.Now().Add(-3 * time.Hour)}
	utilsPod.Status.StartTime = &metav1.Time{Time: time.Now().Add(-3*time.Hour + 5*time.Second)}
	utilsPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "utils",
		State: corev1.ContainerState{
			Running: &corev1.ContainerStateRunning{},
		},
	}}
	err = tracker.Update(corev1.SchemeGroupVersion.WithResource("pods"), utilsPod, ms.Spec.Namespace)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	assert.EqualError(t, err, "terminal error: task max runtime duration of 2h0m0s has been exceeded")
	assert.Equal(t, reconcile.Result{}, gotRes)

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceFailed, ms.Status.Phase)
	if assert.Len(t, ms.Status.Conditions, 2) {
		cmpConditions(t, []metav1.Condition{
			{
				Reason: "AllObjectsApplied",
				Type:   v1alpha1.MiniServiceConditionInstallSuccessful,
				Status: metav1.ConditionTrue,
			},
			{
				Reason:  v1alpha1.MiniServiceStatusReasonObjectsFailed,
				Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
				Status:  metav1.ConditionFalse,
				Message: "max runtime duration 2h0m0s has been exceeded",
			},
		}, ms.Status.Conditions)
	}

	// Mark utils pod as failed, ensure miniservice is marked failed.
	ms.Status.Phase = v1alpha1.MiniServiceRunning
	err = r.Client.Status().Update(ctx, ms)
	require.NoError(t, err)

	utilsPod.CreationTimestamp = metav1.Time{Time: time.Now().Add(-1 * time.Hour)}
	utilsPod.Status.StartTime = &metav1.Time{Time: time.Now().Add(-1*time.Hour + 5*time.Second)}
	utilsPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "utils",
		State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 1,
			},
		},
	}}
	err = tracker.Update(corev1.SchemeGroupVersion.WithResource("pods"), utilsPod, ms.Spec.Namespace)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	assert.EqualError(t, err, "terminal error: task utils container has exited non-zero")
	assert.Equal(t, reconcile.Result{}, gotRes)

	err = r.Client.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.MiniServiceFailed, ms.Status.Phase)
	if assert.Len(t, ms.Status.Conditions, 2) {
		cmpConditions(t, []metav1.Condition{
			{
				Reason: "AllObjectsApplied",
				Type:   v1alpha1.MiniServiceConditionInstallSuccessful,
				Status: metav1.ConditionTrue,
			},
			{
				Reason: v1alpha1.MiniServiceStatusReasonObjectsFailed,
				Type:   v1alpha1.MiniServiceConditionObjectsHealthy,
				Status: metav1.ConditionFalse,
				Message: "task infrastructure detected an error in the task, check task logs for more information\n" +
					"batch/v1.Job foo: BackoffLimitExceeded\nv1.Pod job-pod: SomeContainersNotReadyReason\n",
			},
		}, ms.Status.Conditions)
	}

	// Attempt cleanup on failed task, expect expclicit deletion.
	podList = &corev1.PodList{}
	err = r.Client.List(ctx, podList, client.InNamespace(ms.Spec.Namespace))
	require.NoError(t, err)
	// Ensure SMB pod is skipped.
	podList.Items = append(podList.Items, corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: nvcastorage.SMBServerPodName}})
	gotTaskPodsToDelete, gotForceDelete = getTaskPodsToDelete(ms, podList)
	assert.True(t, gotForceDelete)
	if assert.Len(t, gotTaskPodsToDelete, 1) {
		assert.Equal(t, jobPod.Name, gotTaskPodsToDelete[0].Name)
	}

	// Attempt cleanup on failed task with cleanup already run, expect explicit deletion.
	cleanupMS := ms.DeepCopy()
	cleanupMS.Status.Conditions = append(cleanupMS.Status.Conditions,
		metav1.Condition{
			Type:   v1alpha1.MiniServiceConditionCleanupSuccessful,
			Status: metav1.ConditionFalse,
			Reason: "SomeObjectsPendingDeletion",
		},
	)
	gotTaskPodsToDelete, gotForceDelete = getTaskPodsToDelete(cleanupMS, podList)
	assert.True(t, gotForceDelete)
	if assert.Len(t, gotTaskPodsToDelete, 1) {
		assert.Equal(t, jobPod.Name, gotTaskPodsToDelete[0].Name)
	}

	err = r.Client.Delete(ctx, ms)
	require.NoError(t, err)

	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, gotRes)

	podList = &corev1.PodList{}
	err = r.Client.List(ctx, podList, client.InNamespace(ms.Spec.Namespace))
	require.NoError(t, err)
	// Utils pod is not included in taskPodsToDelete (handled by namespace deletion),
	// so it may still be present until the namespace is fully deleted.
	assert.LessOrEqual(t, len(podList.Items), 1)

	gotRes, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, gotRes)
}

func newBool(v bool) *bool { return &v }

var errNonRetry = fmt.Errorf("not retryable")

func newFakeReValClient(t *testing.T, objBytes []byte, wantErr *error) ReValClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/render", func(w http.ResponseWriter, r *http.Request) {
		if wantErr != nil && *wantErr != nil {
			if errors.Is(*wantErr, errNonRetry) {
				w.WriteHeader(http.StatusBadRequest)
			} else {
				w.WriteHeader(http.StatusInternalServerError)
			}
			w.Write([]byte(fmt.Sprintf(`{"errors":["%s"]}`, strconv.Quote((*wantErr).Error()))))
			return
		}
		b, err := json.Marshal(HelmReValRenderOutput{
			Valid:  newBool(true),
			Output: objBytes,
		})
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(fmt.Sprintf(`{"errors":["%s"]}`, strconv.Quote(err.Error()))))
			return
		}
		w.Write(b)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	rvClient := NewReValClient(srv.URL, testTokenFetcher{}, srv.Client(), nil)
	return rvClient
}

type testTokenFetcher struct{}

func (f testTokenFetcher) FetchToken(ctx context.Context) (string, error) {
	return "testtoken", nil
}

func encodeEnvsForLaunchSpec(envs []corev1.EnvVar) string {
	envsSB := &bytes.Buffer{}
	for _, env := range envs {
		envsSB.WriteString(env.Name)
		envsSB.WriteByte('=')
		if _, err := strconv.ParseInt(env.Value, 10, 64); err == nil {
			envsSB.WriteByte('"')
			envsSB.WriteString(env.Value)
			envsSB.WriteByte('"')
		} else {
			envsSB.WriteString(env.Value)
		}
		envsSB.WriteByte('\n')
	}
	return base64.StdEncoding.EncodeToString(envsSB.Bytes())
}

// The k8s namespace controller will always create the serviceaccount "default"
// in a new namespace, which the reconciler expects to exist
func createNamespaceDefaultSA(t *testing.T, ctx context.Context, r *Reconciler, ms *v1alpha1.MiniService) {
	t.Helper()
	sa := &corev1.ServiceAccount{}
	sa.Name = defaultServiceAccountName
	sa.Namespace = ms.Spec.Namespace
	err := r.Client.Create(ctx, sa)
	require.NoError(t, err)
}

var newFakePermissionsChecker = func(map[apischema.GroupVersionKind]error, []string) permissionCheckerFunc {
	return func(context.Context, client.Client, apischema.GroupVersionKind, string) error {
		return nil
	}
}

func TestGetFunctionNameAndTaskName(t *testing.T) {
	tests := []struct {
		name           string
		fnLaunchSpec   *function.LaunchSpecification
		taskLaunchSpec *task.LaunchSpecification
		wantFnName     string
		wantTaskName   string
		wantErr        bool
	}{
		{
			name: "valid function name",
			fnLaunchSpec: &function.LaunchSpecification{
				EnvironmentB64: base64.StdEncoding.EncodeToString([]byte("FUNCTION_NAME=test-function")),
			},
			taskLaunchSpec: nil,
			wantFnName:     "test-function",
			wantTaskName:   "",
			wantErr:        false,
		},
		{
			name:         "valid task name",
			fnLaunchSpec: nil,
			taskLaunchSpec: &task.LaunchSpecification{
				EnvironmentB64: base64.StdEncoding.EncodeToString([]byte("TASK_NAME=test-task")),
			},
			wantFnName:   "",
			wantTaskName: "test-task",
			wantErr:      false,
		},
		{
			name: "invalid base64 in function spec",
			fnLaunchSpec: &function.LaunchSpecification{
				EnvironmentB64: "invalid-base64",
			},
			taskLaunchSpec: nil,
			wantFnName:     "",
			wantTaskName:   "",
			wantErr:        true,
		},
		{
			name:         "invalid base64 in task spec",
			fnLaunchSpec: nil,
			taskLaunchSpec: &task.LaunchSpecification{
				EnvironmentB64: "invalid-base64",
			},
			wantFnName:   "",
			wantTaskName: "",
			wantErr:      true,
		},
		{
			name:           "no launch specs",
			fnLaunchSpec:   nil,
			taskLaunchSpec: nil,
			wantFnName:     "",
			wantTaskName:   "",
			wantErr:        false,
		},
		{
			name: "multiple env vars in function spec",
			fnLaunchSpec: &function.LaunchSpecification{
				EnvironmentB64: base64.StdEncoding.EncodeToString([]byte("FUNCTION_NAME=test-function\nOTHER_VAR=value")),
			},
			taskLaunchSpec: nil,
			wantFnName:     "test-function",
			wantTaskName:   "",
			wantErr:        false,
		},
		{
			name:         "multiple env vars in task spec",
			fnLaunchSpec: nil,
			taskLaunchSpec: &task.LaunchSpecification{
				EnvironmentB64: base64.StdEncoding.EncodeToString([]byte("TASK_NAME=test-task\nOTHER_VAR=value")),
			},
			wantFnName:   "",
			wantTaskName: "test-task",
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotFnName, gotTaskName, err := getFunctionNameAndTaskName(tt.fnLaunchSpec, tt.taskLaunchSpec)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.wantFnName, gotFnName)
			assert.Equal(t, tt.wantTaskName, gotTaskName)
		})
	}
}

func TestDoUpdateWorkload(t *testing.T) {
	ctx := newTestContext()
	testScheme := mgrScheme

	const (
		testNamespace      = "ms-update-ns"
		workloadObjectName = "updated-workload-cm"
	)

	newMiniService := func() *v1alpha1.MiniService {
		return &v1alpha1.MiniService{
			ObjectMeta: metav1.ObjectMeta{
				Name: "miniservice-update",
				UID:  "miniservice-update-uid",
			},
			Spec: v1alpha1.MiniServiceSpec{
				Namespace:       testNamespace,
				ICMSRequestName: "icms-request-update",
				HelmChartConfig: common.HelmConfig{
					URL:    "https://helm.ngc.nvidia.com/myorg/charts/foo-1.0.0.tgz",
					Values: []byte(`{"foo":"bar"}`),
				},
			},
			Status: v1alpha1.MiniServiceStatus{
				Phase:    v1alpha1.MiniServiceInstalling,
				Revision: 2,
			},
		}
	}

	newICMSRequest := func() *nvcav2beta1.ICMSRequest {
		return &nvcav2beta1.ICMSRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "icms-request-update",
				Namespace: "nvcf-backend",
			},
			Spec: nvcav2beta1.ICMSRequestSpec{
				RequestID:      "request-1",
				MessageBatchID: "batch-1",
				NCAId:          "ncaid-1",
				Action:         common.TaskCreationAction,
				TaskDetails: task.Details{
					TaskID: "task-1",
				},
				CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
					CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
						Action:            common.TaskCreationAction,
						RequestID:         "request-1",
						MessageBatchID:    "batch-1",
						NCAID:             "ncaid-1",
						AccountName:       "acct",
						GPUType:           "L40",
						RequestedGPUCount: 1,
						InstanceCount:     1,
						InstanceTypeValue: "ON-PREM.GPU.L40",
					},
					TaskLaunchSpecification: &task.LaunchSpecification{
						EnvironmentB64: encodeEnvsForLaunchSpec([]corev1.EnvVar{
							{Name: common.TaskNameEncodedEnvKey, Value: "my-task"},
						}),
						ICMSEnvironment:   "prod",
						MaxQueuedDuration: "PT2M",
					},
				},
			},
		}
	}

	newRenderedObjectsData := func(t *testing.T) []byte {
		t.Helper()

		obj := &corev1.ConfigMap{
			TypeMeta: metav1.TypeMeta{
				APIVersion: corev1.SchemeGroupVersion.String(),
				Kind:       "ConfigMap",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: workloadObjectName,
			},
			Data: map[string]string{"key": "value"},
		}
		rawObj, err := json.Marshal(obj)
		require.NoError(t, err)

		objsData, err := json.Marshal([]json.RawMessage{rawObj})
		require.NoError(t, err)

		return objsData
	}

	newReconciler := func(t *testing.T, c client.Client) *Reconciler {
		t.Helper()

		r := &Reconciler{
			ControllerOptions: ControllerOptions{
				FeatureFlagFetcher: &featureflagmock.Fetcher{},
				K8sTimeConfig:      (&k8sutil.TimeConfig{}).Complete(),
			},
			Client:                c,
			Decoder:               serializer.NewCodecFactory(testScheme).UniversalDeserializer(),
			chartCache:            chartcache.New(t.TempDir()),
			newPermissionsChecker: newFakePermissionsChecker,
			now:                   time.Now,
		}
		require.NoError(t, r.chartCache.Start(ctx))
		return r
	}

	setup := func(t *testing.T, c client.Client) (*Reconciler, *v1alpha1.MiniService, *nvcav2beta1.ICMSRequest) {
		t.Helper()

		r := newReconciler(t, c)
		ms := newMiniService()
		icmsReq := newICMSRequest()
		require.NoError(t, r.saveRenderedData(ctx, ms, newRenderedObjectsData(t)))

		return r, ms, icmsReq
	}

	t.Run("updates workload when utils pod is ready", func(t *testing.T) {
		patchCalls := 0
		utilsPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      common.UtilsPodName,
				Namespace: testNamespace,
			},
			Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{Name: common.InitContainerName},
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
					{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
					{Type: corev1.PodReady, Status: corev1.ConditionTrue},
				},
				InitContainerStatuses: []corev1.ContainerStatus{
					{
						Name: common.InitContainerName,
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{ExitCode: 0},
						},
					},
				},
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name: common.UtilsContainerName,
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{},
						},
					},
				},
			},
		}

		c, _ := newFakeClientWithInterceptors(testScheme,
			interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == workloadObjectName {
						patchCalls++
					}
					return c.Patch(ctx, obj, patch, opts...)
				},
			},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}},
			&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workloadObjectName,
					Namespace: testNamespace,
				},
				Data: map[string]string{"key": "old"},
			},
			utilsPod,
		)

		r, ms, icmsReq := setup(t, c)

		gotRes, err := r.doUpdateWorkload(ctx, ms, icmsReq)
		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{}, gotRes)
		assert.Equal(t, 1, patchCalls, "expected one server-side apply patch for rendered workload object")

		assert.Equal(t, v1alpha1.MiniServiceInstalled, ms.Status.Phase)
		cond := meta.FindStatusCondition(ms.Status.Conditions, v1alpha1.MiniServiceConditionInstallSuccessful)
		if assert.NotNil(t, cond) {
			assert.Equal(t, metav1.ConditionTrue, cond.Status)
			assert.Equal(t, "WorkloadObjectsUpdated", cond.Reason)
			assert.Equal(t, "Workload update to revision 2 completed successfully", cond.Message)
		}

		require.NotNil(t, ms.Status.RenderDetails)
		if assert.Len(t, ms.Status.RenderDetails.Resources, 1) {
			assert.Equal(t, 1, ms.Status.RenderDetails.Resources[0].Count)
			assert.Equal(t, []string{workloadObjectName}, ms.Status.RenderDetails.Resources[0].Names)
		}
	})

	t.Run("waits when utils pod init containers are incomplete", func(t *testing.T) {
		patchCalls := 0
		utilsPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      common.UtilsPodName,
				Namespace: testNamespace,
			},
			Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{Name: common.InitContainerName},
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
					{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
					{Type: corev1.PodReady, Status: corev1.ConditionFalse},
				},
				InitContainerStatuses: []corev1.ContainerStatus{
					{
						Name: common.InitContainerName,
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{},
						},
					},
				},
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name: common.UtilsContainerName,
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{},
						},
					},
				},
			},
		}

		c, _ := newFakeClientWithInterceptors(testScheme,
			interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == workloadObjectName {
						patchCalls++
					}
					return c.Patch(ctx, obj, patch, opts...)
				},
			},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}},
			utilsPod,
		)

		r, ms, icmsReq := setup(t, c)

		gotRes, err := r.doUpdateWorkload(ctx, ms, icmsReq)
		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{}, gotRes)
		assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)
		assert.Equal(t, 0, patchCalls, "expected no server-side apply patch while waiting on utils pod init containers")
	})

	t.Run("returns terminal error when utils pod is not found", func(t *testing.T) {
		patchCalls := 0
		c, _ := newFakeClientWithInterceptors(testScheme,
			interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == workloadObjectName {
						patchCalls++
					}
					return c.Patch(ctx, obj, patch, opts...)
				},
			},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}},
		)

		r, ms, icmsReq := setup(t, c)

		gotRes, err := r.doUpdateWorkload(ctx, ms, icmsReq)
		require.Error(t, err)
		assert.Equal(t, reconcile.Result{}, gotRes)
		assert.ErrorContains(t, err, "terminal error")
		assert.ErrorContains(t, err, "pods \"utils\" not found")
		assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)
		assert.Equal(t, 0, patchCalls, "expected no server-side apply patch when utils pod is missing")
	})

	t.Run("returns terminal error when utils pod status is terminal bad", func(t *testing.T) {
		patchCalls := 0
		utilsPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      common.UtilsPodName,
				Namespace: testNamespace,
			},
			Status: corev1.PodStatus{
				Phase:  corev1.PodFailed,
				Reason: "boom",
			},
		}

		c, _ := newFakeClientWithInterceptors(testScheme,
			interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == workloadObjectName {
						patchCalls++
					}
					return c.Patch(ctx, obj, patch, opts...)
				},
			},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}},
			utilsPod,
		)

		r, ms, icmsReq := setup(t, c)

		gotRes, err := r.doUpdateWorkload(ctx, ms, icmsReq)
		require.Error(t, err)
		assert.Equal(t, reconcile.Result{}, gotRes)
		assert.ErrorContains(t, err, "terminal error")
		assert.ErrorContains(t, err, "utils pod is failed: boom")
		assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)
		assert.Equal(t, 0, patchCalls, "expected no server-side apply patch when utils pod is terminal bad")
	})

	t.Run("requeues on transient utils pod get errors", func(t *testing.T) {
		patchCalls := 0
		c, _ := newFakeClientWithInterceptors(testScheme,
			interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if key == (client.ObjectKey{Name: common.UtilsPodName, Namespace: testNamespace}) {
						if _, ok := obj.(*corev1.Pod); ok {
							return apierrors.NewTooManyRequests("try again later", 1)
						}
					}
					return c.Get(ctx, key, obj, opts...)
				},
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == workloadObjectName {
						patchCalls++
					}
					return c.Patch(ctx, obj, patch, opts...)
				},
			},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}},
		)

		r, ms, icmsReq := setup(t, c)

		gotRes, err := r.doUpdateWorkload(ctx, ms, icmsReq)
		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{Requeue: true}, gotRes)
		assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)
		assert.Equal(t, 0, patchCalls, "expected no server-side apply patch when utils pod lookup is transiently failing")
	})
}

func Test_dedupeWorkloadImagePullSecrets(t *testing.T) {
	// With filtering.
	wlObjs := []client.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod"},
			Spec: corev1.PodSpec{
				Containers:     []corev1.Container{{Image: "nvcr.io/foo:latest"}},
				InitContainers: []corev1.Container{{Image: "ghcr.io/foo:latest"}},
			},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "not-pull-secret"},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "not-workload-secret"},
			Type:       corev1.SecretTypeDockerConfigJson,
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "workload-sr-foo-regcred-1"},
			Type:       corev1.SecretTypeDockerConfigJson,
			StringData: map[string]string{
				corev1.DockerConfigJsonKey: "eyJhdXRocyI6eyJnaGNyLmlvIjp7ImF1dGgiOiJmb29iYXIifX19",
			},
		},
	}
	wlPullSecrets := []*corev1.Secret{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "workload-sr-foo-regcred-0"},
			Type:       corev1.SecretTypeDockerConfigJson,
			Data: map[string][]byte{
				corev1.DockerConfigJsonKey: []byte("eyJhdXRocyI6eyJudmNyLmlvIjp7ImF1dGgiOiJmb29iYXIifX19"),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "workload-sr-foo-regcred-1"},
			Type:       corev1.SecretTypeDockerConfigJson,
			StringData: map[string]string{
				corev1.DockerConfigJsonKey: "eyJhdXRocyI6eyJnaGNyLmlvIjp7ImF1dGgiOiJmb29iYXIifX19",
			},
		},
	}
	filtered, err := dedupeWorkloadImagePullSecrets(wlObjs, wlPullSecrets)
	require.NoError(t, err)
	assert.ElementsMatch(t, []*corev1.Secret{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "workload-sr-foo-regcred-1"},
			Type:       corev1.SecretTypeDockerConfigJson,
			StringData: map[string]string{
				corev1.DockerConfigJsonKey: "eyJhdXRocyI6eyJnaGNyLmlvIjp7ImF1dGgiOiJmb29iYXIifX19",
			},
		},
	}, filtered)

	// Without filtering.
	wlObjs = []client.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod"},
			Spec: corev1.PodSpec{
				Containers:     []corev1.Container{{Image: "nvcr.io/foo:latest"}},
				InitContainers: []corev1.Container{{Image: "blah.io/foo:latest"}},
			},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "not-pull-secret"},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "not-workload-secret"},
			Type:       corev1.SecretTypeDockerConfigJson,
		},
	}
	wlPullSecrets = []*corev1.Secret{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "workload-sr-foo-regcred-0"},
			Type:       corev1.SecretTypeDockerConfigJson,
			Data: map[string][]byte{
				corev1.DockerConfigJsonKey: []byte("eyJhdXRocyI6eyJudmNyLmlvIjp7ImF1dGgiOiJmb29iYXIifX19"),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "workload-sr-foo-regcred-1"},
			Type:       corev1.SecretTypeDockerConfigJson,
			StringData: map[string]string{
				corev1.DockerConfigJsonKey: "eyJhdXRocyI6eyJnaGNyLmlvIjp7ImF1dGgiOiJmb29iYXIifX19",
			},
		},
	}
	filtered, err = dedupeWorkloadImagePullSecrets(wlObjs, wlPullSecrets)
	require.NoError(t, err)
	assert.ElementsMatch(t, []*corev1.Secret{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "workload-sr-foo-regcred-0"},
			Type:       corev1.SecretTypeDockerConfigJson,
			Data: map[string][]byte{
				corev1.DockerConfigJsonKey: []byte("eyJhdXRocyI6eyJudmNyLmlvIjp7ImF1dGgiOiJmb29iYXIifX19"),
			},
		},
	}, filtered)
}

// A minimal runtime.Object that does NOT implement client.Object
type notClientObject struct {
	metav1.TypeMeta `json:",inline"`
}

func (o *notClientObject) DeepCopyObject() runtime.Object {
	cpy := *o
	return &cpy
}

func TestDecodeObjects_Success(t *testing.T) {
	ctx := newTestContext()
	s := mgrScheme
	decoder := serializer.NewCodecFactory(s).UniversalDeserializer()

	helmObjs := []client.Object{
		&corev1.ConfigMap{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
			ObjectMeta: metav1.ObjectMeta{
				Name: "cm-1",
			},
		},
		&appsv1.Deployment{
			TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
			ObjectMeta: metav1.ObjectMeta{
				Name: "dep-1",
			},
		},
	}

	data, err := json.Marshal(helmObjs)
	require.NoError(t, err)

	objs, resources, derr := decodeObjects(ctx, decoder, data)
	require.NoError(t, derr)
	require.Len(t, objs, 2)
	require.Len(t, resources, 2)

	// Object assertions
	if cm, ok := objs[0].(*corev1.ConfigMap); assert.True(t, ok) {
		assert.Equal(t, "cm-1", cm.Name)
	}
	if dep, ok := objs[1].(*appsv1.Deployment); assert.True(t, ok) {
		assert.Equal(t, "dep-1", dep.Name)
	}

	// Resource status assertions
	assert.Contains(t, resources, v1alpha1.ResourceStatus{
		GVK:   "/v1, Kind=ConfigMap",
		Names: []string{"cm-1"},
		Count: 1,
	})
	assert.Contains(t, resources, v1alpha1.ResourceStatus{
		GVK:   "apps/v1, Kind=Deployment",
		Names: []string{"dep-1"},
		Count: 1,
	})
}

func TestDecodeObjects_JSONUnmarshalError(t *testing.T) {
	ctx := newTestContext()
	s := mgrScheme
	decoder := serializer.NewCodecFactory(s).UniversalDeserializer()

	// Invalid JSON
	data := []byte("not-json")

	objs, resources, err := decodeObjects(ctx, decoder, data)
	assert.Error(t, err)
	assert.Nil(t, objs)
	assert.Nil(t, resources)
}

func TestDecodeObjects_NonClientObject(t *testing.T) {
	ctx := newTestContext()
	s := runtime.NewScheme()
	gv := apischema.GroupVersion{Group: "x.test", Version: "v1"}
	s.AddKnownTypes(gv, &notClientObject{})
	metav1.AddToGroupVersion(s, gv)
	decoder := serializer.NewCodecFactory(s).UniversalDeserializer()

	objs := []notClientObject{{
		TypeMeta: metav1.TypeMeta{APIVersion: gv.String(), Kind: "notClientObject"},
	}}
	data, err := json.Marshal(objs)
	require.NoError(t, err)

	gotObjs, gotResources, derr := decodeObjects(ctx, decoder, data)
	assert.EqualError(t, derr, "terminal error: bad object type")
	assert.Nil(t, gotObjs)
	assert.Nil(t, gotResources)
}

var cmpConditions = assert.ComparisonAssertionFunc(func(tt assert.TestingT, i1, i2 interface{}, args ...interface{}) bool {
	if t, ok := tt.(interface{ Helper() }); ok {
		t.Helper()
	}
	c1, c2 := i1.([]metav1.Condition), i2.([]metav1.Condition)
	if len(c1) != len(c2) {
		assert.Failf(tt, "Unequal length", "%d != %d", len(c1), len(c2))
		return false
	}
	for i := range len(c1) {
		if c1[i].Reason != c2[i].Reason || c1[i].Type != c2[i].Type ||
			c1[i].Status != c2[i].Status || c1[i].Message != c2[i].Message {
			c1[i].LastTransitionTime = metav1.Time{}
			c2[i].LastTransitionTime = metav1.Time{}
			assert.Fail(tt, fmt.Sprintf("Status condition %d unequal:\nexpected: %s\nactual:   %s\n", i, c1[i].String(), c2[i].String()), args...)
			return false
		}
	}
	return true
})

func TestApplyCustomAnnotations(t *testing.T) {
	tests := []struct {
		name                string
		pod                 *corev1.Pod
		customAnnotations   *sync.Map
		expectedAnnotations map[string]string
		updateAnnotations   map[string]string // If set, will update the sync.Map and verify again
		expectedAfterUpdate map[string]string
	}{
		{
			name: "nil custom annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
				},
			},
			customAnnotations:   nil,
			expectedAnnotations: nil,
		},
		{
			name: "empty annotations map",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
				},
			},
			customAnnotations: func() *sync.Map {
				sm := &sync.Map{}
				sm.Store("annotations", map[string]string{})
				return sm
			}(),
			expectedAnnotations: nil,
		},
		{
			name: "apply custom annotations to pod without annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
				},
			},
			customAnnotations: func() *sync.Map {
				sm := &sync.Map{}
				sm.Store("annotations", map[string]string{
					"custom.annotation/key1": "value1",
					"custom.annotation/key2": "value2",
				})
				return sm
			}(),
			expectedAnnotations: map[string]string{
				"custom.annotation/key1": "value1",
				"custom.annotation/key2": "value2",
			},
		},
		{
			name: "merge custom annotations with existing annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Annotations: map[string]string{
						"existing.annotation/key1": "existing-value1",
					},
				},
			},
			customAnnotations: func() *sync.Map {
				sm := &sync.Map{}
				sm.Store("annotations", map[string]string{
					"custom.annotation/key1": "value1",
					"custom.annotation/key2": "value2",
				})
				return sm
			}(),
			expectedAnnotations: map[string]string{
				"existing.annotation/key1": "existing-value1",
				"custom.annotation/key1":   "value1",
				"custom.annotation/key2":   "value2",
			},
		},
		{
			name: "custom annotations override existing annotations with same key",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Annotations: map[string]string{
						"shared.annotation/key": "original-value",
					},
				},
			},
			customAnnotations: func() *sync.Map {
				sm := &sync.Map{}
				sm.Store("annotations", map[string]string{
					"shared.annotation/key": "custom-value",
				})
				return sm
			}(),
			expectedAnnotations: map[string]string{
				"shared.annotation/key": "custom-value",
			},
		},
		{
			name: "sync.Map with nil content",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
				},
			},
			customAnnotations: func() *sync.Map {
				sm := &sync.Map{}
				// Don't store anything - Load() will return nil
				return sm
			}(),
			expectedAnnotations: nil,
		},
		{
			name: "annotations update dynamically",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
				},
			},
			customAnnotations: func() *sync.Map {
				sm := &sync.Map{}
				sm.Store("annotations", map[string]string{
					"custom.annotation/key1": "initial-value1",
				})
				return sm
			}(),
			expectedAnnotations: map[string]string{
				"custom.annotation/key1": "initial-value1",
			},
			updateAnnotations: map[string]string{
				"custom.annotation/key1": "updated-value1",
				"custom.annotation/key2": "updated-value2",
			},
			expectedAfterUpdate: map[string]string{
				"custom.annotation/key1": "updated-value1",
				"custom.annotation/key2": "updated-value2",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Apply custom annotations
			k8sutil.ApplyCustomAnnotations(tt.pod, tt.customAnnotations)

			// Verify initial annotations
			if tt.expectedAnnotations == nil {
				assert.Nil(t, tt.pod.Annotations, "Pod annotations should be nil")
			} else {
				assert.Equal(t, tt.expectedAnnotations, tt.pod.Annotations, "Pod annotations mismatch")
			}

			// If we have an update test, update the sync.Map and verify again
			if tt.updateAnnotations != nil {
				// Update the sync.Map
				tt.customAnnotations.Store("annotations", tt.updateAnnotations)

				// Create a new pod and apply again
				newPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pod-2",
					},
				}
				k8sutil.ApplyCustomAnnotations(newPod, tt.customAnnotations)

				// Verify updated annotations
				assert.Equal(t, tt.expectedAfterUpdate, newPod.Annotations, "Pod annotations after update mismatch")
			}
		})
	}
}

func TestApplyCustomAnnotations_Concurrent(t *testing.T) {
	// Test that concurrent reads work correctly while the sync.Map is being updated
	sm := &sync.Map{}
	sm.Store("annotations", map[string]string{
		"custom.annotation/key1": "value1",
	})

	// Channel to signal test completion
	done := make(chan bool)
	errChan := make(chan error, 100)

	// Start multiple goroutines that apply annotations
	for i := 0; i < 50; i++ {
		go func(id int) {
			for j := 0; j < 10; j++ {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: fmt.Sprintf("test-pod-%d-%d", id, j),
					},
				}
				k8sutil.ApplyCustomAnnotations(pod, sm)

				// Verify that we got some annotations (either the old or new value)
				if pod.Annotations == nil {
					errChan <- fmt.Errorf("pod %s: expected annotations but got nil", pod.Name)
					return
				}
				if len(pod.Annotations) == 0 {
					errChan <- fmt.Errorf("pod %s: expected non-empty annotations", pod.Name)
					return
				}
			}
			done <- true
		}(i)
	}

	// Concurrently update the sync.Map
	go func() {
		for i := 0; i < 20; i++ {
			sm.Store("annotations", map[string]string{
				"custom.annotation/key1": fmt.Sprintf("value-iteration-%d", i),
				"custom.annotation/key2": fmt.Sprintf("value2-iteration-%d", i),
			})
			time.Sleep(1 * time.Millisecond)
		}
	}()

	// Wait for all goroutines to complete
	for i := 0; i < 50; i++ {
		select {
		case <-done:
			// Goroutine completed successfully
		case err := <-errChan:
			t.Fatalf("Concurrent test failed: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("Test timeout")
		}
	}

	// Verify no errors occurred
	select {
	case err := <-errChan:
		t.Fatalf("Error occurred during concurrent test: %v", err)
	default:
		// No errors
	}
}

func Test_newSelfSubjectAccessReviewPermissionsChecker(t *testing.T) {
	ctx := t.Context()
	ssarGVR := authorizationv1.SchemeGroupVersion.WithResource("selfsubjectaccessreviews")
	tracker := k8stesting.NewObjectTracker(mgrScheme, scheme.Codecs.UniversalDecoder())
	c := clientfake.NewClientBuilder().
		WithScheme(mgrScheme).
		WithObjectTracker(tracker).
		WithRESTMapper(newTestRESTMapper(mgrScheme)).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				ssar := obj.(*authorizationv1.SelfSubjectAccessReview)
				if ssar.Name == "" {
					verb := ssar.Spec.ResourceAttributes.Verb
					resource := ssar.Spec.ResourceAttributes.Resource
					ssar.Name = fmt.Sprintf("%s-%s", verb, resource)
				}
				if existing, err := tracker.Get(ssarGVR, ssar.Namespace, ssar.Name); err == nil {
					*ssar = *(existing).(*authorizationv1.SelfSubjectAccessReview)
					return nil
				}
				return tracker.Create(ssarGVR, obj, ssar.Namespace)
			},
		}).
		Build()

	caniCache := map[apischema.GroupVersionKind]error{}
	checkPerms := newSelfSubjectAccessReviewPermissionsChecker(caniCache, requiredRBACVerbsWrite)

	podGVK := apischema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	podTermErr := reconcile.TerminalError(resourceAccesssDeniedError{
		gvr: apischema.GroupVersionResource{Version: "v1", Resource: "pods"},
	})
	err := checkPerms(ctx, c, podGVK, "default")
	assert.EqualError(t, err, podTermErr.Error())
	if assert.Contains(t, caniCache, podGVK) {
		assert.Equal(t, podTermErr, caniCache[podGVK])
	}
	for _, verb := range requiredRBACVerbsWrite {
		err = c.Delete(ctx, &authorizationv1.SelfSubjectAccessReview{ObjectMeta: metav1.ObjectMeta{Name: verb + "-pods"}})
		require.NoError(t, err)
	}

	err = c.Create(ctx, &authorizationv1.SelfSubjectAccessReview{
		ObjectMeta: metav1.ObjectMeta{Name: "get-pods"},
		Status: authorizationv1.SubjectAccessReviewStatus{
			Allowed: true,
		},
	})
	require.NoError(t, err)
	delete(caniCache, podGVK)
	err = checkPerms(ctx, c, podGVK, "default")
	assert.EqualError(t, err, podTermErr.Error())
	if assert.Contains(t, caniCache, podGVK) {
		assert.Equal(t, podTermErr, caniCache[podGVK])
	}
	for _, verb := range requiredRBACVerbsWrite {
		err = c.Delete(ctx, &authorizationv1.SelfSubjectAccessReview{ObjectMeta: metav1.ObjectMeta{Name: verb + "-pods"}})
		require.NoError(t, err)
	}

	for _, verb := range requiredRBACVerbsWrite {
		err := c.Create(ctx, &authorizationv1.SelfSubjectAccessReview{
			ObjectMeta: metav1.ObjectMeta{Name: verb + "-pods"},
			Status: authorizationv1.SubjectAccessReviewStatus{
				Allowed: true,
			},
		})
		require.NoError(t, err)
	}

	delete(caniCache, podGVK)
	err = checkPerms(ctx, c, podGVK, "default")
	assert.NoError(t, err)
	if assert.Contains(t, caniCache, podGVK) {
		assert.Nil(t, caniCache[podGVK])
	}
}

// TestEnsureInstanceNamespace_TerminatingRequeues verifies that when a namespace
// is in Terminating phase, the function returns nil (causing a requeue) instead of
// an error (which would count as a reconcile failure).
func TestEnsureInstanceNamespace_TerminatingRequeues(t *testing.T) {
	ctx := newTestContext()
	testScheme := mgrScheme

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "terminating-ns",
		},
		Status: corev1.NamespaceStatus{
			Phase: corev1.NamespaceTerminating,
		},
	}

	c, _ := newFakeClient(testScheme, ns)

	r := &Reconciler{
		Client: c,
	}

	ms := &v1alpha1.MiniService{
		Spec: v1alpha1.MiniServiceSpec{
			Namespace: "terminating-ns",
		},
	}

	icmsReq := &nvcav2beta1.ICMSRequest{}
	err := r.ensureInstanceNamespace(ctx, ms, icmsReq)

	// Should return nil (for requeue), not an error
	assert.NoError(t, err, "Terminating namespace should not return an error")
}

// TestGetCacheKey_NamespaceIncluded verifies that the cache key includes the
// namespace, so two MiniServices with different namespaces produce different
// cache keys. This prevents rendered output from namespace A being incorrectly
// returned for namespace B when deployments happen close together.
func TestGetCacheKey_NamespaceIncluded(t *testing.T) {
	var servicePort int32 = 8080

	// Create two MiniServices with DIFFERENT namespaces but SAME chart configuration
	msNamespaceA := &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "miniservice-a",
		},
		Spec: v1alpha1.MiniServiceSpec{
			Namespace:       "sr-9dee7e5a-843d-44ae-bcec-1b4b23dea297", // Namespace A
			ICMSRequestName: "request-a",
			HelmChartConfig: common.HelmConfig{
				URL:         "oci://helm.ngc.nvidia.com/org/team/srtx-benchmarks:0.0.8",
				ServicePort: &servicePort,
				ServiceName: "nvcf-service",
				Values:      []byte(`{"global":{"replicaCount":3}}`),
			},
		},
	}

	msNamespaceB := &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "miniservice-b",
		},
		Spec: v1alpha1.MiniServiceSpec{
			Namespace:       "sr-bb09f1cb-3277-4ce1-b9a1-a904a88bc900", // Namespace B (DIFFERENT!)
			ICMSRequestName: "request-b",
			HelmChartConfig: common.HelmConfig{
				URL:         "oci://helm.ngc.nvidia.com/org/team/srtx-benchmarks:0.0.8", // Same chart
				ServicePort: &servicePort,                                               // Same port
				ServiceName: "nvcf-service",                                             // Same service
				Values:      []byte(`{"global":{"replicaCount":3}}`),                    // Same values
			},
		},
	}

	// Get cache keys for both MiniServices
	cacheKeyA := getCacheKey(msNamespaceA)
	cacheKeyB := getCacheKey(msNamespaceB)

	t.Run("different namespaces produce different cache keys", func(t *testing.T) {
		// Verify the namespaces are actually different (test setup validation)
		require.NotEqual(t, msNamespaceA.Spec.Namespace, msNamespaceB.Spec.Namespace,
			"Test setup error: namespaces should be different")

		// FIXED: Different namespaces now produce different cache keys
		assert.NotEqual(t, cacheKeyA, cacheKeyB,
			"MiniServices with different namespaces (%s vs %s) should produce different cache keys. "+
				"This ensures rendered output with .Release.Namespace from namespace A is not "+
				"incorrectly returned for namespace B.",
			msNamespaceA.Spec.Namespace, msNamespaceB.Spec.Namespace)

		// Verify namespace is included in the cache input
		assert.Equal(t, msNamespaceA.Spec.Namespace, cacheKeyA.Namespace,
			"Cache key should include the namespace")
		assert.Equal(t, msNamespaceB.Spec.Namespace, cacheKeyB.Namespace,
			"Cache key should include the namespace")
	})

	t.Run("same namespace and config produces same cache key", func(t *testing.T) {
		// Two MiniServices with the same namespace and config should share cache
		msSameNamespace := &v1alpha1.MiniService{
			ObjectMeta: metav1.ObjectMeta{
				Name: "miniservice-c",
			},
			Spec: v1alpha1.MiniServiceSpec{
				Namespace:       msNamespaceA.Spec.Namespace, // Same namespace as A
				ICMSRequestName: "request-c",
				HelmChartConfig: common.HelmConfig{
					URL:         "oci://helm.ngc.nvidia.com/org/team/srtx-benchmarks:0.0.8",
					ServicePort: &servicePort,
					ServiceName: "nvcf-service",
					Values:      []byte(`{"global":{"replicaCount":3}}`),
				},
			},
		}

		cacheKeySameNS := getCacheKey(msSameNamespace)
		assert.Equal(t, cacheKeyA, cacheKeySameNS,
			"MiniServices with the same namespace and config should produce the same cache key")
	})
}

func TestUtilsPodGXCacheSkipAnnotation(t *testing.T) {
	tests := []struct {
		name               string
		gxCacheEnabled     bool
		existingAnnotation map[string]string
		expectAnnotation   bool
	}{
		{
			name:             "GXCache enabled - annotation added",
			gxCacheEnabled:   true,
			expectAnnotation: true,
		},
		{
			name:             "GXCache disabled - no annotation",
			gxCacheEnabled:   false,
			expectAnnotation: false,
		},
		{
			name:               "GXCache enabled with existing annotations - annotation added",
			gxCacheEnabled:     true,
			existingAnnotation: map[string]string{"existing": "value"},
			expectAnnotation:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the annotation logic from doInstall
			utilsPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "utils",
					Annotations: tt.existingAnnotation,
				},
			}

			// This mirrors the logic in reconcile.go doInstall
			if utilsPod.Annotations == nil {
				utilsPod.Annotations = map[string]string{}
			}

			fff := &featureflagmock.Fetcher{}
			if tt.gxCacheEnabled {
				fff.EnabledFFs = []*featureflag.FeatureFlag{featureflag.GXCache}
			}

			if fff.IsFeatureFlagEnabled(featureflag.GXCache) {
				utilsPod.Annotations[nvcatypes.GXCacheSkipInjectionAnnotationKey] = nvcatypes.GXCacheSkipInjectionAnnotationValue
			}

			if tt.expectAnnotation {
				assert.Equal(t, nvcatypes.GXCacheSkipInjectionAnnotationValue,
					utilsPod.Annotations[nvcatypes.GXCacheSkipInjectionAnnotationKey],
					"expected GXCache skip annotation to be set")
			} else {
				_, exists := utilsPod.Annotations[nvcatypes.GXCacheSkipInjectionAnnotationKey]
				assert.False(t, exists, "expected GXCache skip annotation to not be set")
			}

			// Verify existing annotations are preserved
			if tt.existingAnnotation != nil {
				for k, v := range tt.existingAnnotation {
					assert.Equal(t, v, utilsPod.Annotations[k], "existing annotation should be preserved")
				}
			}
		})
	}
}

func TestGVKCache(t *testing.T) {
	t.Run("Get returns GVK for known type", func(t *testing.T) {
		cache := newGVKCache(mgrScheme)

		pod := &corev1.Pod{}
		gvk, err := cache.Get(pod)
		require.NoError(t, err)
		assert.Equal(t, corev1.SchemeGroupVersion.WithKind("Pod"), gvk)
	})

	t.Run("Get caches result", func(t *testing.T) {
		cache := newGVKCache(mgrScheme)

		pod := &corev1.Pod{}
		gvk1, err := cache.Get(pod)
		require.NoError(t, err)

		gvk2, err := cache.Get(pod)
		require.NoError(t, err)

		assert.Equal(t, gvk1, gvk2)
		assert.Len(t, cache.cache, 1)
	})

	t.Run("Get returns GVK already set on object", func(t *testing.T) {
		cache := newGVKCache(mgrScheme)

		customGVK := apischema.GroupVersionKind{Group: "custom", Version: "v1", Kind: "Thing"}
		pod := &corev1.Pod{}
		pod.SetGroupVersionKind(customGVK)

		gvk, err := cache.Get(pod)
		require.NoError(t, err)
		assert.Equal(t, customGVK, gvk)
		assert.Len(t, cache.cache, 0)
	})

	t.Run("PrePopulate adds GVK to cache", func(t *testing.T) {
		cache := newGVKCache(mgrScheme)

		cache.PrePopulate(&corev1.Pod{}, podGVK)
		cache.PrePopulate(&appsv1.Deployment{}, deploymentGVK)

		assert.Len(t, cache.cache, 2)

		gvk, err := cache.Get(&corev1.Pod{})
		require.NoError(t, err)
		assert.Equal(t, podGVK, gvk)
	})

	t.Run("Get is concurrent safe", func(t *testing.T) {
		cache := newGVKCache(mgrScheme)

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := cache.Get(&corev1.Pod{})
				assert.NoError(t, err)
			}()
		}
		wg.Wait()
	})

	t.Run("Get returns error for unregistered type", func(t *testing.T) {
		emptyScheme := runtime.NewScheme()
		cache := newGVKCache(emptyScheme)

		pod := &corev1.Pod{}
		_, err := cache.Get(pod)
		assert.Error(t, err)
	})
}

func TestReconciler_list(t *testing.T) {
	ctx := newTestContext()
	msName := "test-ms"
	nsName := "test-ns"
	ms := &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{Name: msName},
		Spec:       v1alpha1.MiniServiceSpec{Namespace: nsName},
	}

	configMapGVK := apischema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
	customGVK := apischema.GroupVersionKind{Group: "example.io", Version: "v1", Kind: "Widget"}

	t.Run("structured namespaced objects are converted to typed", func(t *testing.T) {
		cm1 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "cm-1", Namespace: nsName},
			Data:       map[string]string{"key": "val1"},
		}
		cm2 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "cm-2", Namespace: nsName},
			Data:       map[string]string{"key": "val2"},
		}
		otherNS := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "cm-other", Namespace: "other-ns"},
		}
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		otherNSObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other-ns"}}

		c, _ := newFakeClient(mgrScheme, ns, otherNSObj, cm1, cm2, otherNS)
		r := &Reconciler{Client: c}

		objs, err := r.list(ctx, ms, configMapGVK)
		require.NoError(t, err)
		require.Len(t, objs, 2)

		for _, obj := range objs {
			typedCM, ok := obj.(*corev1.ConfigMap)
			require.True(t, ok, "expected *corev1.ConfigMap, got %T", obj)
			assert.Equal(t, nsName, typedCM.Namespace)
			assert.Equal(t, configMapGVK, obj.GetObjectKind().GroupVersionKind())
		}
	})

	t.Run("unstructured namespaced objects stay unstructured", func(t *testing.T) {
		u1 := &unstructured.Unstructured{}
		u1.SetGroupVersionKind(customGVK)
		u1.SetName("widget-1")
		u1.SetNamespace(nsName)
		u2 := &unstructured.Unstructured{}
		u2.SetGroupVersionKind(customGVK)
		u2.SetName("widget-2")
		u2.SetNamespace(nsName)

		rm := meta.NewDefaultRESTMapper(mgrScheme.PreferredVersionAllGroups())
		for gvk := range mgrScheme.AllKnownTypes() {
			scope := meta.RESTScopeNamespace
			if gvk.Kind == "MiniService" {
				scope = meta.RESTScopeRoot
			}
			rm.Add(gvk, scope)
		}
		rm.Add(customGVK, meta.RESTScopeNamespace)

		tracker := k8stesting.NewObjectTracker(mgrScheme, scheme.Codecs.UniversalDecoder())
		c := clientfake.NewClientBuilder().
			WithScheme(mgrScheme).
			WithObjectTracker(tracker).
			WithRESTMapper(rm).
			WithObjects(u1, u2).
			Build()

		r := &Reconciler{Client: c}

		objs, err := r.list(ctx, ms, customGVK)
		require.NoError(t, err)
		require.Len(t, objs, 2)

		for _, obj := range objs {
			_, ok := obj.(*unstructured.Unstructured)
			require.True(t, ok, "expected *unstructured.Unstructured, got %T", obj)
			assert.Equal(t, customGVK, obj.GetObjectKind().GroupVersionKind())
			assert.Equal(t, nsName, obj.GetNamespace())
		}
	})

	t.Run("cluster-scoped objects list by label selector", func(t *testing.T) {
		matchingMS := &v1alpha1.MiniService{
			ObjectMeta: metav1.ObjectMeta{
				Name: "matching-ms",
				Labels: map[string]string{
					miniserviceNameLabel: msName,
				},
			},
			Spec: v1alpha1.MiniServiceSpec{Namespace: "some-ns"},
		}
		nonMatchingMS := &v1alpha1.MiniService{
			ObjectMeta: metav1.ObjectMeta{
				Name: "non-matching-ms",
				Labels: map[string]string{
					miniserviceNameLabel: "other",
				},
			},
			Spec: v1alpha1.MiniServiceSpec{Namespace: "some-ns"},
		}

		c, _ := newFakeClient(mgrScheme, matchingMS, nonMatchingMS)
		r := &Reconciler{Client: c}

		msGVK := apischema.GroupVersionKind{Group: v1alpha1.SchemeGroupVersion.Group, Version: v1alpha1.SchemeGroupVersion.Version, Kind: "MiniService"}
		objs, err := r.list(ctx, ms, msGVK)
		require.NoError(t, err)
		require.Len(t, objs, 1)
		assert.Equal(t, "matching-ms", objs[0].GetName())
	})

	t.Run("empty result returns nil slice", func(t *testing.T) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		c, _ := newFakeClient(mgrScheme, ns)
		r := &Reconciler{Client: c}

		objs, err := r.list(ctx, ms, configMapGVK)
		require.NoError(t, err)
		assert.Nil(t, objs)
	})

	t.Run("list error is propagated", func(t *testing.T) {
		injectedErr := fmt.Errorf("synthetic list failure")
		c, _ := newFakeClientWithInterceptors(mgrScheme,
			interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					return injectedErr
				},
			},
		)
		r := &Reconciler{Client: c}

		_, err := r.list(ctx, ms, configMapGVK)
		require.ErrorIs(t, err, injectedErr)
	})
}

func baseMiniServiceForPatchTest() *v1alpha1.MiniService {
	return &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "patch-test-ms",
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "external-party",
				"custom-label":                 "keep-me",
			},
			Annotations: map[string]string{
				"external-party/annotation": "do-not-touch",
			},
		},
		Spec: v1alpha1.MiniServiceSpec{
			Namespace:       "workload-ns",
			ICMSRequestName: "icms-req-1",
			HelmChartConfig: testHelmConfig("https://example.com/chart.tgz", `{"replicas": 3}`),
		},
	}
}

func TestPatchMiniService(t *testing.T) {
	ctx := newTestContext()

	tests := []struct {
		name            string
		prepareStored   func(*v1alpha1.MiniService)
		setupNew        func(*v1alpha1.MiniService)
		wantFinalizers  []string
		wantStatusPhase v1alpha1.MiniServicePhase
	}{
		{
			name: "updates status without modifying spec or metadata",
			setupNew: func(newObj *v1alpha1.MiniService) {
				newObj.Status.Phase = v1alpha1.MiniServiceInstalling
				newObj.Status.ObservedGeneration = 1
			},
			wantFinalizers:  nil,
			wantStatusPhase: v1alpha1.MiniServiceInstalling,
		},
		{
			name: "updates finalizers without modifying spec or metadata",
			setupNew: func(newObj *v1alpha1.MiniService) {
				newObj.Finalizers = []string{finalizer}
				newObj.Status.Phase = v1alpha1.MiniServiceInstalled
			},
			wantFinalizers:  []string{finalizer},
			wantStatusPhase: v1alpha1.MiniServiceInstalled,
		},
		{
			name: "removes finalizers without modifying spec or metadata",
			prepareStored: func(stored *v1alpha1.MiniService) {
				stored.Finalizers = []string{finalizer}
			},
			setupNew: func(newObj *v1alpha1.MiniService) {
				newObj.Finalizers = nil
				newObj.Status.Phase = v1alpha1.MiniServiceCompleted
			},
			wantFinalizers:  nil,
			wantStatusPhase: v1alpha1.MiniServiceCompleted,
		},
		{
			name: "does not persist spec changes present in newObj",
			setupNew: func(newObj *v1alpha1.MiniService) {
				newObj.Spec.Namespace = "attacker-ns"
				newObj.Spec.ICMSRequestName = "attacker-req"
				newObj.Spec.HelmChartConfig = testHelmConfig("https://evil.example/chart.tgz", `{"replicas": 999}`)
				newObj.Labels["custom-label"] = "changed"
				newObj.Annotations["external-party/annotation"] = "changed"
				newObj.Status.Phase = v1alpha1.MiniServiceInstallFailed
			},
			wantFinalizers:  nil,
			wantStatusPhase: v1alpha1.MiniServiceInstallFailed,
		},
		{
			name: "skips finalizer patch when unchanged",
			prepareStored: func(stored *v1alpha1.MiniService) {
				stored.Finalizers = []string{finalizer}
			},
			setupNew: func(newObj *v1alpha1.MiniService) {
				newObj.Finalizers = []string{finalizer}
				newObj.Status.Phase = v1alpha1.MiniServiceRunning
			},
			wantFinalizers:  []string{finalizer},
			wantStatusPhase: v1alpha1.MiniServiceRunning,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stored := baseMiniServiceForPatchTest()
			if tt.prepareStored != nil {
				tt.prepareStored(stored)
			}

			crClient, _ := newFakeClient(mgrScheme, stored)
			r := &Reconciler{Client: crClient}

			oldObj := &v1alpha1.MiniService{}
			require.NoError(t, crClient.Get(ctx, client.ObjectKeyFromObject(stored), oldObj))

			wantSpec := oldObj.Spec
			wantLabels := maps.Clone(oldObj.Labels)
			wantAnnotations := maps.Clone(oldObj.Annotations)

			newObj := oldObj.DeepCopy()
			tt.setupNew(newObj)

			require.NoError(t, r.patchMiniService(ctx, oldObj, newObj))

			got := &v1alpha1.MiniService{}
			require.NoError(t, crClient.Get(ctx, client.ObjectKeyFromObject(stored), got))

			assert.Empty(t, cmp.Diff(wantSpec, got.Spec), "spec must not be modified by patchMiniService")
			assert.Equal(t, wantLabels, got.Labels, "labels must not be modified by patchMiniService")
			assert.Equal(t, wantAnnotations, got.Annotations, "annotations must not be modified by patchMiniService")
			assert.Equal(t, tt.wantStatusPhase, got.Status.Phase)
			assert.Equal(t, tt.wantFinalizers, got.Finalizers)
		})
	}
}
