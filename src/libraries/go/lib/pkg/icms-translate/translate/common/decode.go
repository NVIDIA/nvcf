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

package common

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

type KeyValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func EnvDecoderJSON(envBytes []byte) ([]KeyValue, error) {
	kvs := []KeyValue{}
	err := json.Unmarshal(envBytes, &kvs)
	return kvs, err
}

func EnvDecoderText(envBytes []byte) ([]KeyValue, error) {
	kvs := []KeyValue{}
	scanner := bufio.NewScanner(bytes.NewReader(envBytes))
	for scanner.Scan() {
		line := scanner.Text()
		trimmedLine := strings.TrimSpace(line)

		// Skip empty lines
		if trimmedLine == "" {
			continue
		}

		parts := strings.SplitN(trimmedLine, "=", 2)
		kv := KeyValue{Key: parts[0]}
		if len(parts) > 1 {
			if val, err := strconv.Unquote(parts[1]); err == nil {
				kv.Value = val
			} else {
				kv.Value = parts[1]
			}
		}

		kvs = append(kvs, kv)
	}
	return kvs, scanner.Err()
}

func DecodeEnvironmentB64(envB64 string, dec func([]byte) ([]KeyValue, error)) (map[string]string, error) {
	envBytes, err := base64.StdEncoding.DecodeString(envB64)
	if err != nil {
		return nil, err
	}

	kvs, err := dec(envBytes)
	if err != nil {
		return nil, err
	}

	envs := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		envs[kv.Key] = kv.Value
	}

	return envs, nil
}

func MapToEnv(m map[string]string) []corev1.EnvVar {
	envs := make([]corev1.EnvVar, len(m))
	i := 0
	for k, v := range m {
		envs[i] = corev1.EnvVar{Name: k, Value: v}
		i++
	}
	return envs
}

func DecodeB64ToJSON(listB64 string, out any) error {
	b, err := base64.StdEncoding.DecodeString(listB64)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &out)
}

func SortEnvs(envs []corev1.EnvVar) []corev1.EnvVar {
	sort.Slice(envs, func(i, j int) bool { return envs[i].Name < envs[j].Name })
	return envs
}

// HelmConfig contains all data needed to create a HelmRelease or MiniService object.
// +k8s:deepcopy-gen=true
type HelmConfig struct {
	// Full Helm chart URL.
	URL string `json:"url"`
	// Service name, if any.
	ServiceName string `json:"serviceName,omitempty"`
	// Service port, if any.
	ServicePort *int32 `json:"servicePort,omitempty"`
	// Helm values as JSON bytes.
	Values json.RawMessage `json:"values,omitempty"`
	// Registry authn/z configuration.
	AuthConfig HelmAuthConfig `json:"authConfig,omitempty"`
}

// HelmAuthConfig holds a variety of fields that indicate how a client should
// authenticate with a Helm registry.
// +k8s:deepcopy-gen=true
type HelmAuthConfig struct {
	// K8sSecrets contains a list of image pull secret-formatted auths.
	// that may be used to pull a chart.
	// NOTE: for now this only has one registry entry with one auth entry.
	K8sSecrets []RegistryAuthSecret `json:"k8sSecrets"`
}

// ExtractHelmConfiguration parses data in launchSpec to construct Helm function or task configuration.
func ExtractHelmConfiguration(
	envB64 string,
	hcLaunchSpec *HelmChartLaunchSpecification,
) (HelmConfig, error) {
	if hcLaunchSpec == nil {
		return HelmConfig{}, fmt.Errorf("empty Helm chart launch specification")
	}
	allEnvSet, err := DecodeEnvironmentB64(envB64, EnvDecoderText)
	if err != nil {
		return HelmConfig{}, fmt.Errorf("decode worker environment: %v", err)
	}

	var inferencePort *int32
	if portStr := allEnvSet["INFERENCE_PORT"]; portStr != "" {
		port, err := strconv.ParseInt(portStr, 10, 32)
		if err != nil {
			return HelmConfig{}, fmt.Errorf("invalid inference port: %w", err)
		}
		inferencePort = new(int32)
		*inferencePort = int32(port)
	}

	authCfg, _, err := ParseHelmWorkloadAuthConfig(hcLaunchSpec.HelmChartURL, allEnvSet)
	if err != nil {
		return HelmConfig{}, fmt.Errorf("parse helm registry auth config: %v", err)
	}

	return HelmConfig{
		URL:         hcLaunchSpec.HelmChartURL,
		ServiceName: allEnvSet["HELM_CHART_INFERENCE_SERVICE_NAME"],
		ServicePort: inferencePort,
		Values:      hcLaunchSpec.Values,
		AuthConfig:  authCfg,
	}, nil
}
