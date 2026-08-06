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

package operator

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"strings"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/transporttls"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	yamlv3 "gopkg.in/yaml.v3"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
)

// nvcaOperatorConfigDTO is the chart-owned configuration stored in
// nvca-operator-config. It is intentionally distinct from nvcaconfig.Config:
// the former selects Kubernetes sources, while the latter is written for NVCA.
type nvcaOperatorConfigDTO struct {
	Workload *nvcaOperatorWorkloadConfigDTO `yaml:"workload"`
}

type nvcaOperatorWorkloadConfigDTO struct {
	TransportTLS *nvcaOperatorTransportTLSConfigDTO `yaml:"transportTLS"`
}

type nvcaOperatorTransportTLSConfigDTO struct {
	TrustBundle              *nvcaOperatorTrustBundleConfigDTO `yaml:"trustBundle"`
	Fingerprint              string                            `yaml:"fingerprint"`
	InstalledBundleMountPath string                            `yaml:"installedBundleMountPath"`
}

type nvcaOperatorTrustBundleConfigDTO struct {
	SecretKeyRef *nvcaOperatorSecretKeyRefDTO `yaml:"secretKeyRef"`
}

type nvcaOperatorSecretKeyRefDTO struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key"`
}

// nvcaOperatorConfigMapper maps the operator's ConfigMap DTO into the NVCA
// configuration resource. A mapper reports false when its source is disabled.
type nvcaOperatorConfigMapper func(context.Context, *nvcaOperatorConfigDTO, *nvcaconfig.Config) (bool, error)

// nvcaOperatorConfigMapMapper follows the same decode-then-map pattern as the
// cluster ConfigMap client. It keeps Kubernetes-specific source resolution out
// of the generated NVCA configuration path.
type nvcaOperatorConfigMapMapper struct {
	configMaps corev1client.ConfigMapInterface
	mappers    []nvcaOperatorConfigMapper
}

func newNVCAOperatorConfigMapMapper(
	namespace string,
	configMaps corev1client.ConfigMapInterface,
	secrets corev1client.SecretInterface,
) *nvcaOperatorConfigMapMapper {
	return &nvcaOperatorConfigMapMapper{
		configMaps: configMaps,
		mappers: []nvcaOperatorConfigMapper{
			withSecretBackedTransportTLSMapper(namespace, secrets),
		},
	}
}

// getConfig resolves the chart-owned ConfigMap into an NVCA config overlay.
// A missing ConfigMap or an empty source is intentionally a no-op.
func (m *nvcaOperatorConfigMapMapper) getConfig(ctx context.Context) (nvcaconfig.Config, bool, error) {
	cm, err := m.configMaps.Get(ctx, nvcaOperatorConfigMapName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nvcaconfig.Config{}, false, nil
		}
		return nvcaconfig.Config{}, false, fmt.Errorf("read %s ConfigMap: %w", nvcaOperatorConfigMapName, err)
	}

	raw := strings.TrimSpace(cm.Data[agentConfigFile])
	if raw == "" {
		return nvcaconfig.Config{}, false, nil
	}

	source, err := decodeNVCAOperatorConfig([]byte(raw))
	if err != nil {
		return nvcaconfig.Config{}, false, fmt.Errorf("parse %s %s: %w", nvcaOperatorConfigMapName, agentConfigFile, err)
	}

	var destination nvcaconfig.Config
	found := false
	for _, mapper := range m.mappers {
		mapped, err := mapper(ctx, &source, &destination)
		if err != nil {
			return nvcaconfig.Config{}, false, err
		}
		found = found || mapped
	}
	return destination, found, nil
}

func (bc *BackendK8sCache) getNVCAAgentConfig(ctx context.Context) (nvcaconfig.Config, bool, error) {
	mapper := newNVCAOperatorConfigMapMapper(
		bc.operatorNamespace,
		bc.clients.K8s.CoreV1().ConfigMaps(bc.operatorNamespace),
		bc.clients.K8s.CoreV1().Secrets(bc.operatorNamespace),
	)
	return mapper.getConfig(ctx)
}

func withSecretBackedTransportTLSMapper(namespace string, secrets corev1client.SecretInterface) nvcaOperatorConfigMapper {
	return func(ctx context.Context, source *nvcaOperatorConfigDTO, destination *nvcaconfig.Config) (bool, error) {
		if source.Workload == nil || source.Workload.TransportTLS == nil ||
			source.Workload.TransportTLS.TrustBundle == nil ||
			source.Workload.TransportTLS.TrustBundle.SecretKeyRef == nil {
			return false, nil
		}

		selector := source.Workload.TransportTLS.TrustBundle.SecretKeyRef
		secretName := strings.TrimSpace(selector.Name)
		if secretName == "" {
			return false, nil
		}
		secretKey := strings.TrimSpace(selector.Key)
		if secretKey == "" {
			return false, fmt.Errorf("%s workload.transportTLS.trustBundle.secretKeyRef.key is required", nvcaOperatorConfigMapName)
		}

		secret, err := secrets.Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("read transport TLS Secret %s/%s: %w", namespace, secretName, err)
		}
		pemData, found := secret.Data[secretKey]
		if !found || len(pemData) == 0 {
			return false, fmt.Errorf("transport TLS Secret %s/%s has no %q data", namespace, secretName, secretKey)
		}
		if err := validateCertificateOnlyPEM(pemData); err != nil {
			return false, fmt.Errorf("validate transport TLS Secret %s/%s key %q: %w", namespace, secretName, secretKey, err)
		}

		fingerprint, err := transporttls.FingerprintTrustBundle(string(pemData))
		if err != nil {
			return false, fmt.Errorf("fingerprint transport TLS Secret %s/%s key %q: %w", namespace, secretName, secretKey, err)
		}
		if supplied := strings.TrimSpace(source.Workload.TransportTLS.Fingerprint); supplied != "" && supplied != fingerprint {
			return false, fmt.Errorf("transport TLS fingerprint %q does not match computed fingerprint %q", supplied, fingerprint)
		}

		transportTLSConfig := nvcaconfig.TransportTLSConfig{
			TrustMode:                nvcaconfig.TrustModeBundle,
			TrustBundleFingerprint:   fingerprint,
			TrustBundlePEM:           string(pemData),
			InstalledBundleMountPath: source.Workload.TransportTLS.InstalledBundleMountPath,
		}
		if err := transporttls.ValidateConfig(transporttls.NormalizeConfig(transportTLSConfig)); err != nil {
			return false, fmt.Errorf("validate transport TLS configuration: %w", err)
		}
		destination.Workload.TransportTLS = &transportTLSConfig
		return true, nil
	}
}

func decodeNVCAOperatorConfig(data []byte) (nvcaOperatorConfigDTO, error) {
	decoder := yamlv3.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	var config nvcaOperatorConfigDTO
	if err := decoder.Decode(&config); err != nil {
		return nvcaOperatorConfigDTO{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nvcaOperatorConfigDTO{}, fmt.Errorf("expected a single YAML document")
		}
		return nvcaOperatorConfigDTO{}, err
	}
	return config, nil
}

func validateCertificateOnlyPEM(data []byte) error {
	const (
		certificateBegin = "-----BEGIN CERTIFICATE-----"
		certificateEnd   = "-----END CERTIFICATE-----"
	)

	remaining := bytes.TrimSpace(data)
	if len(remaining) == 0 {
		return fmt.Errorf("contains no certificates")
	}
	certificates := 0
	for len(remaining) > 0 {
		if !bytes.HasPrefix(remaining, []byte(certificateBegin)) ||
			!isCompletePEMLine(remaining, len(certificateBegin)) {
			return fmt.Errorf("contains non-PEM data")
		}
		end := findCertificatePEMEnd(remaining, certificateEnd)
		if end == -1 || bytes.Contains(remaining[len(certificateBegin):end], []byte("\n-----BEGIN")) {
			return fmt.Errorf("contains malformed certificate PEM")
		}
		pemBlock := remaining[:end+len("\n"+certificateEnd)]
		block, rest := pem.Decode(pemBlock)
		if block == nil || len(bytes.TrimSpace(rest)) != 0 {
			return fmt.Errorf("contains malformed certificate PEM")
		}
		if block.Type != "CERTIFICATE" {
			return fmt.Errorf("contains %q PEM block", block.Type)
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return fmt.Errorf("parse certificate: %w", err)
		}
		certificates++
		remaining = bytes.TrimSpace(remaining[len(pemBlock):])
	}
	if certificates == 0 {
		return fmt.Errorf("contains no certificates")
	}
	return nil
}

func findCertificatePEMEnd(data []byte, certificateEnd string) int {
	endMarker := []byte("\n" + certificateEnd)
	for searchOffset := 0; searchOffset < len(data); {
		endOffset := bytes.Index(data[searchOffset:], endMarker)
		if endOffset == -1 {
			return -1
		}
		endOffset += searchOffset
		if isCompletePEMLine(data, endOffset+len(endMarker)) {
			return endOffset
		}
		searchOffset = endOffset + 1
	}
	return -1
}

func isCompletePEMLine(data []byte, lineEnd int) bool {
	return lineEnd == len(data) || data[lineEnd] == '\n' ||
		(data[lineEnd] == '\r' && lineEnd+1 < len(data) && data[lineEnd+1] == '\n')
}
