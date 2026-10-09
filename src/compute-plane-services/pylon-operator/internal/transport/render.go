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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/config"
)

const (
	// ContainerName is the Pylon container of a transport pod.
	ContainerName = "pylon"
	// MetricsPort is the port of Pylon's /metrics on every transport pod.
	MetricsPort int32 = 9089
	// MetricsPortName names MetricsPort in the pod spec.
	MetricsPortName = "metrics"

	// CredentialKey is the cluster credential's key in the credential Secret
	// and its file name under CredentialMountPath.
	CredentialKey = "cluster-token"
	// CredentialMountPath is where the credential Secret is mounted.
	CredentialMountPath = "/var/run/pylon-operator"
	// TrustBundleMountPath is where the trust bundle ConfigMap is mounted.
	TrustBundleMountPath = "/etc/pylon-operator/tls"
	// TrustBundleCAKey is the CA bundle's key in the trust bundle ConfigMap.
	TrustBundleCAKey = "ca.crt"

	// EnvPodName carries the pod name from the downward API. The inference
	// server id refers to it as $(POD_NAME).
	EnvPodName = "POD_NAME"
	// EnvTLSCertPath points Pylon at the router CA for the QUIC tunnel.
	EnvTLSCertPath = "STARGATE_TLS_CERT_PATH"
	// EnvGRPCTLSCACertPath points Pylon at the router CA for the gRPC
	// registration stream. It is set only for an https:// router address:
	// Pylon refuses a gRPC CA for a plaintext registration address.
	EnvGRPCTLSCACertPath = "STARGATE_GRPC_TLS_CA_CERT_PATH"

	// NameLabel and NameLabelValue identify transport pods.
	NameLabel = "app.kubernetes.io/name"
	// NameLabelValue is the NameLabel value of transport pods.
	NameLabelValue = "pylon"
	// EndpointLabel carries the InferenceEndpoint name, shortened with a
	// hash suffix when it is longer than a label value may be.
	EndpointLabel = "pylon.nvidia.com/endpoint"
	// SpecHashAnnotation on the pod template is a hash of the rendered
	// template. The transport step rewrites the template only when it
	// differs, so an unchanged spec never rolls the pods.
	SpecHashAnnotation = "pylon.nvidia.com/transport-spec-hash"

	// transportUser is the UID and GID of the Pylon process, the distroless
	// nonroot user. It is set explicitly so runAsNonRoot holds whatever USER
	// the image declares.
	transportUser int64 = 65532
	// readOnlyFileMode is the mode of the mounted credential and CA files.
	readOnlyFileMode int32 = 0o444

	endpointKind      = "InferenceEndpoint"
	credentialVolume  = "cluster-credential"
	trustBundleVolume = "trust-bundle"
	namePrefix        = "pylon-"
	nameHashLength    = 8
	specHashLength    = 16
)

// DeploymentName is pylon-<endpoint name>. When that is not a DNS label,
// because it is longer than 63 characters or contains a dot, dots become
// dashes, the name is cut to fit and a hash of the endpoint name is appended,
// so it stays unique and stable.
func DeploymentName(ep *pylonv1alpha1.InferenceEndpoint) string {
	return bounded(namePrefix+ep.Name, ep.Name, validation.IsDNS1123Label)
}

// PodLabels are the labels of the transport pods of ep and the Deployment's
// selector. The registration observer lists the pods with them.
func PodLabels(ep *pylonv1alpha1.InferenceEndpoint) map[string]string {
	return map[string]string{
		NameLabel:     NameLabelValue,
		EndpointLabel: bounded(ep.Name, ep.Name, validation.IsValidLabelValue),
	}
}

// objectLabels are the labels of the Deployment and its pods.
func objectLabels(ep *pylonv1alpha1.InferenceEndpoint) map[string]string {
	l := PodLabels(ep)
	l[config.ManagedByLabel] = config.ManagedBy
	return l
}

// bounded returns s when validate accepts it, and otherwise a shortened form
// with a hash of hashInput appended that fits a DNS label.
func bounded(s, hashInput string, validate func(string) []string) string {
	if len(validate(s)) == 0 {
		return s
	}
	sum := sha256.Sum256([]byte(hashInput))
	suffix := hex.EncodeToString(sum[:])[:nameHashLength]
	s = strings.ReplaceAll(s, ".", "-")
	if limit := validation.DNS1123LabelMaxLength - 1 - nameHashLength; len(s) > limit {
		s = s[:limit]
	}
	return strings.TrimRight(s, "-") + "-" + suffix
}

// RouterUsesTLS reports whether Pylon dials the router's gRPC registration
// address with TLS. Pylon treats only an https:// address as TLS; a bare
// host:port or an http:// address is plaintext.
func RouterUsesTLS(routerGRPCAddress string) bool {
	return strings.HasPrefix(strings.TrimSpace(routerGRPCAddress), "https://")
}

// UpstreamURL is the in-cluster URL of the endpoint's Service port.
func UpstreamURL(ep *pylonv1alpha1.InferenceEndpoint) string {
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", ep.Spec.Service.Name, ep.Namespace, ep.Spec.Service.Port)
}

// InferenceServerID is the --inference-server-id of the transport pods of ep:
// <clusterId>.<namespace>.<name>.$(POD_NAME). Kubernetes substitutes the pod
// name, so every replica registers under its own frozen identity.
func InferenceServerID(ep *pylonv1alpha1.InferenceEndpoint, clusterID string) string {
	return fmt.Sprintf("%s.%s.%s.$(%s)", clusterID, ep.Namespace, ep.Name, EnvPodName)
}

// Args are Pylon's arguments, in the order of the design. Optional flags are
// present only when their value is: --max-engine-concurrency when the spec
// sets it and --quic-insecure with --dev-insecure-transport. status.gpu is
// not passed to Pylon, so a GPU type change never rolls the transport.
func Args(ep *pylonv1alpha1.InferenceEndpoint, cfg config.Config) []string {
	args := []string{
		"--upstream-http-base-url=" + UpstreamURL(ep),
		"--stargate-address=" + escape(cfg.RouterGRPCAddress),
		"--inference-server-id=" + InferenceServerID(ep, cfg.ClusterID),
		"--cluster-id=" + cfg.ClusterID,
		"--model-name=" + escape(ep.Spec.ModelName),
		"--auth-token-file=" + path.Join(CredentialMountPath, CredentialKey),
		"--backend-connectivity=reverse",
		"--upstream-health-path=" + escape(ep.Spec.Health.Path),
		"--wait-for-upstream",
	}
	if ep.Spec.MaxEngineConcurrency != nil {
		args = append(args, fmt.Sprintf("--max-engine-concurrency=%d", *ep.Spec.MaxEngineConcurrency))
	}
	if ep.Spec.Canary != nil {
		if timeout := ep.Spec.Canary.TimeoutSeconds; timeout != nil {
			args = append(args, fmt.Sprintf("--bringup-canary-timeout-ms=%d", int64(*timeout)*1000))
		}
		if interval := ep.Spec.Canary.IntervalSeconds; interval != nil {
			args = append(args, fmt.Sprintf("--active-canary-interval-ms=%d", int64(*interval)*1000))
		}
	}
	args = append(args, "--initial-input-tps="+strconv.FormatFloat(cfg.InitialInputTPS, 'f', -1, 64))
	if cfg.DevInsecureTransport {
		args = append(args, "--quic-insecure")
	}
	return args
}

// escape protects a literal value from Kubernetes' $(VAR) expansion in
// container arguments, where $$ stands for $.
func escape(s string) string {
	return strings.ReplaceAll(s, "$", "$$")
}

// Deployment renders the transport Deployment of ep with the given replica
// count. The pod template carries SpecHashAnnotation; replicas are not part
// of the hash, so scaling never rolls the pods.
func Deployment(ep *pylonv1alpha1.InferenceEndpoint, cfg config.Config, replicas int32) *appsv1.Deployment {
	template := podTemplate(ep, cfg)
	template.Annotations = map[string]string{SpecHashAnnotation: specHash(template)}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            DeploymentName(ep),
			Namespace:       ep.Namespace,
			Labels:          objectLabels(ep),
			OwnerReferences: []metav1.OwnerReference{*controllerRef(ep)},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: PodLabels(ep)},
			Template: template,
		},
	}
}

// controllerRef makes ep the controller of an object, so deleting ep
// garbage-collects it.
func controllerRef(ep *pylonv1alpha1.InferenceEndpoint) *metav1.OwnerReference {
	return metav1.NewControllerRef(ep, pylonv1alpha1.GroupVersion.WithKind(endpointKind))
}

func podTemplate(ep *pylonv1alpha1.InferenceEndpoint, cfg config.Config) corev1.PodTemplateSpec {
	env := []corev1.EnvVar{{
		Name: EnvPodName,
		ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.name"},
		},
	}}
	mounts := []corev1.VolumeMount{{Name: credentialVolume, MountPath: CredentialMountPath, ReadOnly: true}}
	volumes := []corev1.Volume{{
		Name: credentialVolume,
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName:  cfg.ClusterCredentialSecret,
			Items:       []corev1.KeyToPath{{Key: CredentialKey, Path: CredentialKey}},
			DefaultMode: ptr.To(readOnlyFileMode),
		}},
	}}
	if cfg.TrustBundleConfigMap != "" {
		ca := path.Join(TrustBundleMountPath, TrustBundleCAKey)
		env = append(env, corev1.EnvVar{Name: EnvTLSCertPath, Value: ca})
		if RouterUsesTLS(cfg.RouterGRPCAddress) {
			env = append(env, corev1.EnvVar{Name: EnvGRPCTLSCACertPath, Value: ca})
		}
		mounts = append(mounts, corev1.VolumeMount{Name: trustBundleVolume, MountPath: TrustBundleMountPath, ReadOnly: true})
		volumes = append(volumes, corev1.Volume{
			Name: trustBundleVolume,
			VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: cfg.TrustBundleConfigMap},
				DefaultMode:          ptr.To(readOnlyFileMode),
			}},
		})
	}

	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: objectLabels(ep)},
		Spec: corev1.PodSpec{
			// Pylon never calls the Kubernetes API: no ServiceAccount token,
			// and no Service environment variables that could shadow the
			// STARGATE_* variables Pylon reads.
			AutomountServiceAccountToken: ptr.To(false),
			EnableServiceLinks:           ptr.To(false),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   ptr.To(true),
				RunAsUser:      ptr.To(transportUser),
				RunAsGroup:     ptr.To(transportUser),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			ImagePullSecrets: imagePullSecrets(cfg.PylonImagePullSecrets),
			Containers: []corev1.Container{{
				Name:            ContainerName,
				Image:           cfg.PylonImage,
				ImagePullPolicy: corev1.PullPolicy(cfg.PylonImagePullPolicy),
				Args:            Args(ep, cfg),
				Env:             env,
				Ports:           []corev1.ContainerPort{{Name: MetricsPortName, ContainerPort: MetricsPort, Protocol: corev1.ProtocolTCP}},
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
				VolumeMounts: mounts,
			}},
			Volumes: volumes,
		},
	}
}

// imagePullSecrets references the configured pull Secrets in the endpoint's
// namespace. With none it returns nil, which the JSON form omits, so the spec
// hash of a template without pull Secrets is the same as before they were
// configurable and an operator upgrade does not roll the transport pods.
func imagePullSecrets(names []string) []corev1.LocalObjectReference {
	if len(names) == 0 {
		return nil
	}
	refs := make([]corev1.LocalObjectReference, 0, len(names))
	for _, name := range names {
		refs = append(refs, corev1.LocalObjectReference{Name: name})
	}
	return refs
}

// specHash hashes the JSON form of the template. encoding/json writes struct
// fields in declaration order and map keys sorted, so equal templates hash
// equally.
func specHash(template corev1.PodTemplateSpec) string {
	h := sha256.New()
	// Encoding a PodTemplateSpec cannot fail: it has no channels, functions
	// or custom marshalers that return errors for valid values.
	_ = json.NewEncoder(h).Encode(template)
	return hex.EncodeToString(h.Sum(nil))[:specHashLength]
}
