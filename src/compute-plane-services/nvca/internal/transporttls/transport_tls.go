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

package transporttls

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/transporttls/trustbundle"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
)

const (
	TrustModeSystem = nvcaconfig.TrustModeSystem
	TrustModeBundle = nvcaconfig.TrustModeBundle

	DefaultTrustBundleConfigMapName = "nvcf-transport-trust-bundle"
	DefaultTrustBundleKey           = "nvcf-ca-bundle.pem"
	TrustBundleFingerprintKey       = "fingerprint"

	TrustBundleVolumeName = "nvcf-transport-trust-bundle"
	MergedCertsVolumeName = "nvcf-trust-merged-certs"
	InstallContainerName  = "nvcf-trust-bundle-install"
	InstallCommandPath    = "/usr/bin/nvcf-trust-bundle-install"

	TrustBundleMountPath = "/nvcf/trust"
	MergedCertsMountPath = "/merged-certs"
	MergedCertsFile      = "/merged-certs/ca-certificates.crt"
	SystemCertDir        = "/etc/ssl/certs"
	SystemCertFile       = "/etc/ssl/certs/ca-certificates.crt"
	CertPathEnv          = "STARGATE_TLS_CERT_PATH"
)

var fingerprintPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func NormalizeConfig(cfg nvcaconfig.TransportTLSConfig) nvcaconfig.TransportTLSConfig {
	if strings.TrimSpace(string(cfg.TrustMode)) == "" {
		cfg.TrustMode = TrustModeSystem
	}
	if cfg.TrustBundleConfigMapName == "" {
		cfg.TrustBundleConfigMapName = DefaultTrustBundleConfigMapName
	}
	if cfg.TrustBundleKey == "" {
		cfg.TrustBundleKey = DefaultTrustBundleKey
	}
	cfg.TrustBundleFingerprint = strings.ToLower(strings.TrimSpace(cfg.TrustBundleFingerprint))
	return cfg
}

func ValidateConfig(cfg nvcaconfig.TransportTLSConfig) error {
	switch cfg.TrustMode {
	case TrustModeSystem:
		return nil
	case TrustModeBundle:
		if errs := validation.IsDNS1123Subdomain(cfg.TrustBundleConfigMapName); len(errs) > 0 {
			return fmt.Errorf("transportTls.trustBundleConfigMapName is invalid: %s", strings.Join(errs, "; "))
		}
		if errs := validation.IsConfigMapKey(cfg.TrustBundleKey); len(errs) > 0 {
			return fmt.Errorf("transportTls.trustBundleKey is invalid: %s", strings.Join(errs, "; "))
		}
		if cfg.TrustBundleKey == TrustBundleFingerprintKey {
			return fmt.Errorf("transportTls.trustBundleKey must not use reserved key %q", TrustBundleFingerprintKey)
		}
		if strings.TrimSpace(cfg.TrustBundlePEM) == "" {
			return fmt.Errorf("transportTls.trustBundlePem is required when trustMode=bundle")
		}
		if !fingerprintPattern.MatchString(cfg.TrustBundleFingerprint) {
			return fmt.Errorf("transportTls.trustBundleFingerprint must match sha256:<64 lowercase hex characters>")
		}
		computed, err := FingerprintTrustBundle(cfg.TrustBundlePEM)
		if err != nil {
			return fmt.Errorf("transportTls.trustBundlePem is invalid: %w", err)
		}
		if computed != cfg.TrustBundleFingerprint {
			return fmt.Errorf("transportTls.trustBundleFingerprint does not match transportTls.trustBundlePem")
		}
		return nil
	default:
		return fmt.Errorf("unsupported transportTls.trustMode %q", cfg.TrustMode)
	}
}

func FingerprintTrustBundle(trustBundlePEM string) (string, error) {
	return trustbundle.FingerprintPEM(trustBundlePEM)
}

func DesiredConfigMapData(cfg nvcaconfig.TransportTLSConfig) map[string]string {
	return map[string]string{
		cfg.TrustBundleKey:        cfg.TrustBundlePEM,
		TrustBundleFingerprintKey: cfg.TrustBundleFingerprint,
	}
}

func PodSpecHasLLMWorker(podSpec *corev1.PodSpec) bool {
	return findContainerIndex(podSpec, function.LLMWorkerContainerName) >= 0
}

func InjectIntoPodSpec(podSpec *corev1.PodSpec, cfg nvcaconfig.TransportTLSConfig) error {
	llmWorkerIdx := findContainerIndex(podSpec, function.LLMWorkerContainerName)
	if llmWorkerIdx < 0 {
		return nil
	}

	installImage, installImagePullPolicy, err := resolveInstallContainerImage(podSpec, cfg)
	if err != nil {
		return err
	}
	upsertVolumes(podSpec, cfg)
	upsertInstallContainer(podSpec, installImage, installImagePullPolicy, cfg)

	llmWorker := &podSpec.Containers[llmWorkerIdx]
	upsertVolumeMount(&llmWorker.VolumeMounts, corev1.VolumeMount{
		Name:      MergedCertsVolumeName,
		MountPath: SystemCertDir,
		ReadOnly:  true,
	})
	k8sutil.AddEnvsToContainer(llmWorker, corev1.EnvVar{
		Name:  CertPathEnv,
		Value: SystemCertFile,
	})
	return nil
}

func resolveInstallContainerImage(
	podSpec *corev1.PodSpec,
	cfg nvcaconfig.TransportTLSConfig,
) (string, corev1.PullPolicy, error) {
	if image := strings.TrimSpace(cfg.InstallerImage); image != "" {
		return image, corev1.PullIfNotPresent, nil
	}
	for _, c := range podSpec.InitContainers {
		if c.Name == InstallContainerName && c.Image != "" {
			return c.Image, c.ImagePullPolicy, nil
		}
	}
	return "", "", fmt.Errorf("transportTls.installerImage is required when injecting the transport trust bundle")
}

func upsertVolumes(podSpec *corev1.PodSpec, cfg nvcaconfig.TransportTLSConfig) {
	upsertVolume(&podSpec.Volumes, corev1.Volume{
		Name: TrustBundleVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: cfg.TrustBundleConfigMapName},
				Optional:             ptr.To(false),
				Items: []corev1.KeyToPath{{
					Key:  cfg.TrustBundleKey,
					Path: cfg.TrustBundleKey,
				}},
			},
		},
	})
	upsertVolume(&podSpec.Volumes, corev1.Volume{
		Name:         MergedCertsVolumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}},
	})
}

func upsertInstallContainer(
	podSpec *corev1.PodSpec,
	image string,
	imagePullPolicy corev1.PullPolicy,
	cfg nvcaconfig.TransportTLSConfig,
) {
	upsertContainer(&podSpec.InitContainers, corev1.Container{
		Name:            InstallContainerName,
		Image:           image,
		ImagePullPolicy: imagePullPolicy,
		Command:         []string{InstallCommandPath},
		Args: []string{
			"--system-bundle", SystemCertFile,
			"--trust-bundle", TrustBundleMountPath + "/" + cfg.TrustBundleKey,
			"--output-bundle", MergedCertsFile,
			"--expected-fingerprint", cfg.TrustBundleFingerprint,
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: TrustBundleVolumeName, MountPath: TrustBundleMountPath, ReadOnly: true},
			{Name: MergedCertsVolumeName, MountPath: MergedCertsMountPath},
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsUser:    ptr.To[int64](0),
			RunAsNonRoot: ptr.To(false),
		},
	})
}

func findContainerIndex(podSpec *corev1.PodSpec, name string) int {
	for i := range podSpec.Containers {
		if podSpec.Containers[i].Name == name {
			return i
		}
	}
	return -1
}

func upsertVolume(volumes *[]corev1.Volume, volume corev1.Volume) {
	for i := range *volumes {
		if (*volumes)[i].Name == volume.Name {
			(*volumes)[i] = volume
			return
		}
	}
	*volumes = append(*volumes, volume)
}

func upsertContainer(containers *[]corev1.Container, container corev1.Container) {
	for i := range *containers {
		if (*containers)[i].Name == container.Name {
			(*containers)[i] = container
			return
		}
	}
	*containers = append(*containers, container)
}

func upsertVolumeMount(mounts *[]corev1.VolumeMount, mount corev1.VolumeMount) {
	for i := range *mounts {
		if (*mounts)[i].Name == mount.Name {
			(*mounts)[i] = mount
			return
		}
	}
	*mounts = append(*mounts, mount)
}
