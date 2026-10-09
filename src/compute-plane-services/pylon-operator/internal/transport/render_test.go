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

package transport

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/config"
)

const (
	testNamespace     = "models"
	testName          = "llama"
	testUID           = types.UID("5f0c1e3a-llama")
	testService       = "llama-nim"
	testModel         = "meta/llama-3.1-8b-instruct"
	testHealthPath    = "/v1/health/ready"
	testImage         = "nvcr.io/nvidia/pylon:0.15.2"
	testRouter        = "llm-request-router.gateway.svc.cluster.local:50071"
	operatorNamespace = "pylon-operator"
)

func testConfig() config.Config {
	return config.Config{
		ClusterID:               "spark-berlin",
		RouterGRPCAddress:       testRouter,
		PylonImage:              testImage,
		TransportReplicas:       1,
		ClusterCredentialSecret: config.DefaultClusterCredentialSecret,
		OperatorNamespace:       operatorNamespace,
		InitialInputTPS:         config.DefaultInitialInputTPS,
	}
}

func endpoint() *pylonv1alpha1.InferenceEndpoint {
	return endpointNamed(testNamespace, testName, testUID)
}

func TestCanaryTimingIsScopedToEndpoint(t *testing.T) {
	cfg := testConfig()
	original := endpoint()
	base := Deployment(original, cfg, 1)
	custom := original.DeepCopy()
	custom.Spec.Canary = &pylonv1alpha1.CanarySpec{
		TimeoutSeconds:  ptr.To(int32(180)),
		IntervalSeconds: ptr.To(int32(60)),
	}
	args := Args(custom, cfg)
	assert.Contains(t, args, "--bringup-canary-timeout-ms=180000")
	assert.Contains(t, args, "--active-canary-interval-ms=60000")
	assert.NotEqual(t, base.Spec.Template.Annotations[SpecHashAnnotation], Deployment(custom, cfg, 1).Spec.Template.Annotations[SpecHashAnnotation])
	assert.Equal(t, base.Spec.Template, Deployment(original, cfg, 1).Spec.Template)
	custom.Spec.Canary = &pylonv1alpha1.CanarySpec{}
	assert.Equal(t, base.Spec.Template, Deployment(custom, cfg, 1).Spec.Template)
	custom.Spec.Canary.TimeoutSeconds = ptr.To(int32(30))
	assert.Contains(t, Args(custom, cfg), "--bringup-canary-timeout-ms=30000")
	assert.NotContains(t, strings.Join(Args(custom, cfg), " "), "--active-canary-interval-ms")
	custom.Spec.Canary = &pylonv1alpha1.CanarySpec{IntervalSeconds: ptr.To(int32(90))}
	assert.Contains(t, Args(custom, cfg), "--active-canary-interval-ms=90000")
	assert.NotContains(t, strings.Join(Args(custom, cfg), " "), "--bringup-canary-timeout-ms")
}

func endpointNamed(namespace, name string, uid types.UID) *pylonv1alpha1.InferenceEndpoint {
	return &pylonv1alpha1.InferenceEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: uid, Generation: 1},
		Spec: pylonv1alpha1.InferenceEndpointSpec{
			ModelName:          testModel,
			InferenceAPIFormat: pylonv1alpha1.InferenceAPIFormat{Type: pylonv1alpha1.InferenceAPIFormatChat},
			Service:            pylonv1alpha1.ServiceReference{Name: testService, Port: 8000},
			Health:             pylonv1alpha1.HealthCheck{Path: testHealthPath},
		},
	}
}

var minimalArgs = []string{
	"--upstream-http-base-url=http://llama-nim.models.svc.cluster.local:8000",
	"--stargate-address=llm-request-router.gateway.svc.cluster.local:50071",
	"--inference-server-id=spark-berlin.models.llama.$(POD_NAME)",
	"--cluster-id=spark-berlin",
	"--model-name=meta/llama-3.1-8b-instruct",
	"--auth-token-file=/var/run/pylon-operator/cluster-token",
	"--backend-connectivity=reverse",
	"--upstream-health-path=/v1/health/ready",
	"--wait-for-upstream",
	"--initial-input-tps=100",
}

func TestArgs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*pylonv1alpha1.InferenceEndpoint, *config.Config)
		want   []string
	}{
		{name: "required flags only", want: minimalArgs},
		{
			name: "every optional flag",
			mutate: func(ep *pylonv1alpha1.InferenceEndpoint, cfg *config.Config) {
				ep.Spec.MaxEngineConcurrency = ptr.To[int32](8)
				cfg.DevInsecureTransport = true
				cfg.InitialInputTPS = 2200.5
			},
			want: append(append([]string{}, minimalArgs[:9]...),
				"--max-engine-concurrency=8",
				"--initial-input-tps=2200.5",
				"--quic-insecure",
			),
		},
		{
			name: "GPU type is not passed to Pylon",
			mutate: func(ep *pylonv1alpha1.InferenceEndpoint, _ *config.Config) {
				ep.Status.GPU = &pylonv1alpha1.GPUStatus{Product: "NVIDIA-GB10", Source: pylonv1alpha1.GPUSourceNodeLabels}
			},
			want: minimalArgs,
		},
		{
			name: "dollar signs are escaped from $(VAR) expansion",
			mutate: func(ep *pylonv1alpha1.InferenceEndpoint, _ *config.Config) {
				ep.Spec.ModelName = "org/$(HOME)-model"
				ep.Spec.Health.Path = "/health$"
			},
			want: func() []string {
				a := append([]string{}, minimalArgs...)
				a[4] = "--model-name=org/$$(HOME)-model"
				a[7] = "--upstream-health-path=/health$$"
				return a
			}(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep, cfg := endpoint(), testConfig()
			if tt.mutate != nil {
				tt.mutate(ep, &cfg)
			}
			assert.Equal(t, tt.want, Args(ep, cfg))
		})
	}
}

// TestDeploymentGolden pins the whole rendered object, with every optional
// part enabled. A known GPU type renders nothing.
func TestDeploymentGolden(t *testing.T) {
	ep := endpoint()
	ep.Spec.MaxEngineConcurrency = ptr.To[int32](4)
	ep.Status.GPU = &pylonv1alpha1.GPUStatus{Product: "NVIDIA-GB10"}
	cfg := testConfig()
	cfg.RouterGRPCAddress = "https://" + testRouter
	cfg.PylonImagePullPolicy = "IfNotPresent"
	cfg.TrustBundleConfigMap = "router-ca"
	cfg.DevInsecureTransport = true
	cfg.PylonImagePullSecrets = []string{"ngc-pull", "team-pull"}

	got := Deployment(ep, cfg, 2)
	hash := got.Spec.Template.Annotations[SpecHashAnnotation]
	assert.Len(t, hash, 16)
	assert.Empty(t, strings.Trim(hash, "0123456789abcdef"), "hex")

	labels := map[string]string{
		"app.kubernetes.io/name":       "pylon",
		"app.kubernetes.io/managed-by": "pylon-operator",
		"pylon.nvidia.com/endpoint":    "llama",
	}
	want := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pylon-llama",
			Namespace: testNamespace,
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "pylon.nvidia.com/v1alpha1",
				Kind:               "InferenceEndpoint",
				Name:               testName,
				UID:                testUID,
				Controller:         ptr.To(true),
				BlockOwnerDeletion: ptr.To(true),
			}},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](2),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{
				"app.kubernetes.io/name":    "pylon",
				"pylon.nvidia.com/endpoint": "llama",
			}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: map[string]string{"pylon.nvidia.com/transport-spec-hash": hash},
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(false),
					EnableServiceLinks:           ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   ptr.To(true),
						RunAsUser:      ptr.To[int64](65532),
						RunAsGroup:     ptr.To[int64](65532),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					ImagePullSecrets: []corev1.LocalObjectReference{{Name: "ngc-pull"}, {Name: "team-pull"}},
					Containers: []corev1.Container{{
						Name:            "pylon",
						Image:           testImage,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Args: append(append([]string{minimalArgs[0], "--stargate-address=https://" + testRouter}, minimalArgs[2:9]...),
							"--max-engine-concurrency=4",
							"--initial-input-tps=100",
							"--quic-insecure",
						),
						Env: []corev1.EnvVar{
							{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.name"}}},
							{Name: "STARGATE_TLS_CERT_PATH", Value: "/etc/pylon-operator/tls/ca.crt"},
							{Name: "STARGATE_GRPC_TLS_CA_CERT_PATH", Value: "/etc/pylon-operator/tls/ca.crt"},
						},
						Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9089, Protocol: corev1.ProtocolTCP}},
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						}},
						SecurityContext: &corev1.SecurityContext{
							RunAsNonRoot:             ptr.To(true),
							ReadOnlyRootFilesystem:   ptr.To(true),
							AllowPrivilegeEscalation: ptr.To(false),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "cluster-credential", MountPath: "/var/run/pylon-operator", ReadOnly: true},
							{Name: "trust-bundle", MountPath: "/etc/pylon-operator/tls", ReadOnly: true},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "cluster-credential", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
							SecretName:  "pylon-operator-cluster-credential",
							Items:       []corev1.KeyToPath{{Key: "cluster-token", Path: "cluster-token"}},
							DefaultMode: ptr.To[int32](0o444),
						}}},
						{Name: "trust-bundle", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: "router-ca"},
							DefaultMode:          ptr.To[int32](0o444),
						}}},
					},
				},
			},
		},
	}
	assert.Equal(t, want, got)
}

func TestDeploymentWithoutTrustBundle(t *testing.T) {
	spec := Deployment(endpoint(), testConfig(), 1).Spec.Template.Spec
	require.Len(t, spec.Containers, 1)
	c := spec.Containers[0]
	assert.Equal(t, []corev1.EnvVar{{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.name"}}}}, c.Env)
	assert.Equal(t, []corev1.VolumeMount{{Name: "cluster-credential", MountPath: "/var/run/pylon-operator", ReadOnly: true}}, c.VolumeMounts)
	require.Len(t, spec.Volumes, 1)
	assert.Equal(t, "cluster-credential", spec.Volumes[0].Name)
	assert.Equal(t, minimalArgs, c.Args)
	for _, v := range spec.Volumes {
		assert.Nil(t, v.Projected, "no ServiceAccount token volume")
	}
	assert.Nil(t, spec.ImagePullSecrets, "no pull Secrets unless configured")
}

// TestImagePullSecrets checks that --pylon-image-pull-secrets reaches the pod
// spec in order, and that without it the template's JSON form, and so its
// spec hash, has no imagePullSecrets field: an operator upgrade that adds the
// flag does not roll transport pods that do not use it.
func TestImagePullSecrets(t *testing.T) {
	cfg := testConfig()
	cfg.PylonImagePullSecrets = []string{"ngc-pull", "team-pull"}
	spec := Deployment(endpoint(), cfg, 1).Spec.Template.Spec
	assert.Equal(t, []corev1.LocalObjectReference{{Name: "ngc-pull"}, {Name: "team-pull"}}, spec.ImagePullSecrets)

	for name, secrets := range map[string][]string{"unset": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig()
			cfg.PylonImagePullSecrets = secrets
			template := Deployment(endpoint(), cfg, 1).Spec.Template
			assert.Nil(t, template.Spec.ImagePullSecrets)
			delete(template.Annotations, SpecHashAnnotation)
			data, err := json.Marshal(template)
			require.NoError(t, err)
			assert.NotContains(t, string(data), "imagePullSecrets")
		})
	}
}

// TestTrustBundleWithPlaintextRouter checks that a trust bundle with a
// plaintext registration address sets only the QUIC trust: Pylon refuses a
// gRPC CA for an address that is not https://.
func TestTrustBundleWithPlaintextRouter(t *testing.T) {
	for _, router := range []string{testRouter, "http://" + testRouter} {
		t.Run(router, func(t *testing.T) {
			cfg := testConfig()
			cfg.RouterGRPCAddress = router
			cfg.TrustBundleConfigMap = "router-ca"
			spec := Deployment(endpoint(), cfg, 1).Spec.Template.Spec
			require.Len(t, spec.Containers, 1)
			c := spec.Containers[0]
			assert.Equal(t, []corev1.EnvVar{
				{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.name"}}},
				{Name: "STARGATE_TLS_CERT_PATH", Value: "/etc/pylon-operator/tls/ca.crt"},
			}, c.Env)
			assert.Contains(t, c.VolumeMounts, corev1.VolumeMount{Name: "trust-bundle", MountPath: "/etc/pylon-operator/tls", ReadOnly: true})
			assert.Empty(t, c.ImagePullPolicy, "the Kubernetes default applies when no pull policy is configured")
		})
	}
}

func TestRouterUsesTLS(t *testing.T) {
	for addr, want := range map[string]bool{
		"router.ns.svc:50071":          false,
		"http://router.ns.svc:50071":   false,
		"https://router.ns.svc:50071":  true,
		" https://router.ns.svc:50071": true,
		"HTTPS://router.ns.svc:50071":  false,
	} {
		assert.Equal(t, want, RouterUsesTLS(addr), addr)
	}
}

func TestNames(t *testing.T) {
	long := strings.Repeat("a", 60)
	tests := []struct {
		name       string
		endpoint   string
		wantName   string
		wantLabel  string
		wantHashed bool
	}{
		{name: "short", endpoint: "llama", wantName: "pylon-llama", wantLabel: "llama"},
		{name: "exactly 63 with the prefix", endpoint: strings.Repeat("b", 57), wantName: "pylon-" + strings.Repeat("b", 57), wantLabel: strings.Repeat("b", 57)},
		{name: "64 with the prefix", endpoint: strings.Repeat("c", 58), wantHashed: true, wantLabel: strings.Repeat("c", 58)},
		{name: "longer than a label value", endpoint: long + "-" + long, wantHashed: true},
		{name: "dots are not DNS labels", endpoint: "llama-3.1", wantHashed: true, wantLabel: "llama-3.1"},
		{name: "cut right after a dash", endpoint: strings.Repeat("d", 47) + "-" + strings.Repeat("e", 20), wantHashed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep := endpointNamed(testNamespace, tt.endpoint, testUID)
			name := DeploymentName(ep)
			label := PodLabels(ep)[EndpointLabel]
			assert.Empty(t, validation.IsDNS1123Label(name), name)
			assert.Empty(t, validation.IsValidLabelValue(label), label)
			assert.LessOrEqual(t, len(name), 63)
			assert.Equal(t, name, DeploymentName(endpointNamed("other", tt.endpoint, "other-uid")), "stable")
			if tt.wantHashed {
				assert.True(t, strings.HasPrefix(name, "pylon-"), name)
				assert.Regexp(t, `-[0-9a-f]{8}$`, name)
				assert.NotContains(t, name, "--", "no double dash before the hash")
			} else {
				assert.Equal(t, tt.wantName, name)
			}
			if tt.wantLabel != "" {
				assert.Equal(t, tt.wantLabel, label)
			} else {
				assert.Len(t, label, 63)
				assert.Regexp(t, `-[0-9a-f]{8}$`, label)
			}
		})
	}

	a := DeploymentName(endpointNamed(testNamespace, long+"-one", testUID))
	b := DeploymentName(endpointNamed(testNamespace, long+"-two", testUID))
	assert.Len(t, a, 63)
	assert.NotEqual(t, a, b, "names sharing a long prefix stay distinct")
}

func TestSpecHash(t *testing.T) {
	base := Deployment(endpoint(), testConfig(), 1).Spec.Template.Annotations[SpecHashAnnotation]
	assert.Equal(t, base, Deployment(endpoint(), testConfig(), 1).Spec.Template.Annotations[SpecHashAnnotation], "deterministic")

	rolls := []struct {
		name   string
		mutate func(*pylonv1alpha1.InferenceEndpoint, *config.Config)
	}{
		{"service", func(ep *pylonv1alpha1.InferenceEndpoint, _ *config.Config) { ep.Spec.Service.Name = "other" }},
		{"port", func(ep *pylonv1alpha1.InferenceEndpoint, _ *config.Config) { ep.Spec.Service.Port = 9000 }},
		{"model name", func(ep *pylonv1alpha1.InferenceEndpoint, _ *config.Config) { ep.Spec.ModelName = "other" }},
		{"health path", func(ep *pylonv1alpha1.InferenceEndpoint, _ *config.Config) { ep.Spec.Health.Path = "/health" }},
		{"max engine concurrency", func(ep *pylonv1alpha1.InferenceEndpoint, _ *config.Config) {
			ep.Spec.MaxEngineConcurrency = ptr.To[int32](2)
		}},
		{"image", func(_ *pylonv1alpha1.InferenceEndpoint, c *config.Config) { c.PylonImage = "pylon:next" }},
		{"image pull policy", func(_ *pylonv1alpha1.InferenceEndpoint, c *config.Config) { c.PylonImagePullPolicy = "Always" }},
		{"image pull secrets", func(_ *pylonv1alpha1.InferenceEndpoint, c *config.Config) {
			c.PylonImagePullSecrets = []string{"ngc-pull"}
		}},
		{"router address", func(_ *pylonv1alpha1.InferenceEndpoint, c *config.Config) { c.RouterGRPCAddress = "router:1" }},
		{"cluster id", func(_ *pylonv1alpha1.InferenceEndpoint, c *config.Config) { c.ClusterID = "other" }},
		{"insecure transport", func(_ *pylonv1alpha1.InferenceEndpoint, c *config.Config) { c.DevInsecureTransport = true }},
		{"initial input tps", func(_ *pylonv1alpha1.InferenceEndpoint, c *config.Config) { c.InitialInputTPS = 50 }},
		{"trust bundle", func(_ *pylonv1alpha1.InferenceEndpoint, c *config.Config) { c.TrustBundleConfigMap = "ca" }},
		{"credential secret", func(_ *pylonv1alpha1.InferenceEndpoint, c *config.Config) { c.ClusterCredentialSecret = "cred" }},
	}
	for _, tt := range rolls {
		t.Run(tt.name+" rolls", func(t *testing.T) {
			ep, cfg := endpoint(), testConfig()
			tt.mutate(ep, &cfg)
			assert.NotEqual(t, base, Deployment(ep, cfg, 1).Spec.Template.Annotations[SpecHashAnnotation])
		})
	}

	keeps := []struct {
		name   string
		mutate func(*pylonv1alpha1.InferenceEndpoint, *config.Config) int32
	}{
		{"replicas", func(*pylonv1alpha1.InferenceEndpoint, *config.Config) int32 { return 0 }},
		{"conditions", func(ep *pylonv1alpha1.InferenceEndpoint, _ *config.Config) int32 {
			ep.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: "X"}}
			return 1
		}},
		{"GPU type", func(ep *pylonv1alpha1.InferenceEndpoint, _ *config.Config) int32 {
			ep.Status.GPU = &pylonv1alpha1.GPUStatus{Product: "NVIDIA-GB10", Source: pylonv1alpha1.GPUSourceNodeLabels}
			return 1
		}},
		{"GPU source only", func(ep *pylonv1alpha1.InferenceEndpoint, _ *config.Config) int32 {
			ep.Status.GPU = &pylonv1alpha1.GPUStatus{Source: pylonv1alpha1.GPUSourceUnknown}
			return 1
		}},
		{"generation", func(ep *pylonv1alpha1.InferenceEndpoint, _ *config.Config) int32 { ep.Generation = 7; return 1 }},
		{"probe interval", func(_ *pylonv1alpha1.InferenceEndpoint, c *config.Config) int32 { c.ProbeInterval = 99; return 1 }},
	}
	for _, tt := range keeps {
		t.Run(tt.name+" keeps", func(t *testing.T) {
			ep, cfg := endpoint(), testConfig()
			replicas := tt.mutate(ep, &cfg)
			assert.Equal(t, base, Deployment(ep, cfg, replicas).Spec.Template.Annotations[SpecHashAnnotation])
		})
	}
}
