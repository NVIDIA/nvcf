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

package bdd_tmp

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type documentedResource struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name        string            `yaml:"name"`
		Namespace   string            `yaml:"namespace"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
	Spec struct {
		ControllerName string `yaml:"controllerName"`
		ParametersRef  struct {
			Group     string `yaml:"group"`
			Kind      string `yaml:"kind"`
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"parametersRef"`
		Provider struct {
			Kubernetes struct {
				EnvoyService struct {
					Annotations map[string]string `yaml:"annotations"`
				} `yaml:"envoyService"`
			} `yaml:"kubernetes"`
		} `yaml:"provider"`
	} `yaml:"spec"`
}

func TestRemoteGatewayExamplesConfigureAWSNLBOnEnvoyService(t *testing.T) {
	examples := []struct {
		name string
		path string
	}{
		{
			name: "gateway routing guide",
			path: filepath.Join("..", "..", "docs", "user", "gateway-routing.md"),
		},
		{
			name: "CLI install prompt",
			path: filepath.Join(
				"..", "..", "ai-tooling", "user", "skills", "nvcf-self-managed-cli",
				"prompts", "install-from-scratch.md",
			),
		},
	}

	wantAnnotations := map[string]string{
		"service.beta.kubernetes.io/aws-load-balancer-type":            "external",
		"service.beta.kubernetes.io/aws-load-balancer-nlb-target-type": "instance",
		"service.beta.kubernetes.io/aws-load-balancer-scheme":          "internet-facing",
	}

	for _, example := range examples {
		t.Run(example.name, func(t *testing.T) {
			body, err := os.ReadFile(example.path)
			if err != nil {
				t.Fatalf("read %s: %v", example.path, err)
			}

			resources, err := documentedApplyResources(body)
			if err != nil {
				t.Fatalf("parse applied resources from %s: %v", example.path, err)
			}

			envoyProxy := findDocumentedResource(resources, "EnvoyProxy", "eg")
			if envoyProxy == nil {
				t.Fatalf("EnvoyProxy eg not found in an applied manifest")
			}
			for annotation, want := range wantAnnotations {
				if got := envoyProxy.Spec.Provider.Kubernetes.EnvoyService.Annotations[annotation]; got != want {
					t.Errorf("EnvoyProxy envoyService annotation %q = %q, want %q", annotation, got, want)
				}
			}

			gatewayClass := findDocumentedResource(resources, "GatewayClass", "eg")
			if gatewayClass == nil {
				t.Fatalf("GatewayClass eg not found in an applied manifest")
			}
			gatewayClassFields := []struct {
				name string
				got  string
				want string
			}{
				{
					name: "controllerName",
					got:  gatewayClass.Spec.ControllerName,
					want: "gateway.envoyproxy.io/gatewayclass-controller",
				},
				{
					name: "parametersRef.group",
					got:  gatewayClass.Spec.ParametersRef.Group,
					want: "gateway.envoyproxy.io",
				},
				{
					name: "parametersRef.kind",
					got:  gatewayClass.Spec.ParametersRef.Kind,
					want: envoyProxy.Kind,
				},
				{
					name: "parametersRef.name",
					got:  gatewayClass.Spec.ParametersRef.Name,
					want: envoyProxy.Metadata.Name,
				},
				{
					name: "parametersRef.namespace",
					got:  gatewayClass.Spec.ParametersRef.Namespace,
					want: envoyProxy.Metadata.Namespace,
				},
			}
			for _, field := range gatewayClassFields {
				if field.got != field.want {
					t.Errorf("GatewayClass %s = %q, want %q", field.name, field.got, field.want)
				}
			}

			gateway := findDocumentedResource(resources, "Gateway", "nvcf-gateway")
			if gateway == nil {
				t.Fatalf("Gateway nvcf-gateway not found in an applied manifest")
			}
			for i := range resources {
				if resources[i].Kind != "Gateway" {
					continue
				}
				for annotation := range resources[i].Metadata.Annotations {
					if strings.HasPrefix(annotation, "service.beta.kubernetes.io/") {
						t.Errorf(
							"Gateway %s metadata contains Service annotation %q; configure the generated Service through EnvoyProxy",
							resources[i].Metadata.Name,
							annotation,
						)
					}
				}
			}
		})
	}
}

func documentedApplyResources(markdown []byte) ([]documentedResource, error) {
	scanner := bufio.NewScanner(bytes.NewReader(markdown))
	var resources []documentedResource
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "kubectl apply -f - <<EOF" {
			continue
		}

		var manifest strings.Builder
		foundEnd := false
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == "EOF" {
				foundEnd = true
				break
			}
			manifest.WriteString(scanner.Text())
			manifest.WriteByte('\n')
		}
		if !foundEnd {
			return nil, fmt.Errorf("kubectl apply heredoc has no EOF terminator")
		}

		decoder := yaml.NewDecoder(strings.NewReader(dedent(manifest.String())))
		for {
			var resource documentedResource
			if err := decoder.Decode(&resource); err != nil {
				if err == io.EOF {
					break
				}
				return nil, fmt.Errorf("decode kubectl apply manifest: %w", err)
			}
			if resource.Kind != "" {
				resources = append(resources, resource)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan Markdown: %w", err)
	}
	return resources, nil
}

func findDocumentedResource(resources []documentedResource, kind, name string) *documentedResource {
	for i := range resources {
		if resources[i].Kind == kind && resources[i].Metadata.Name == name {
			return &resources[i]
		}
	}
	return nil
}

func dedent(body string) string {
	lines := strings.Split(body, "\n")
	commonIndent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if commonIndent == -1 || indent < commonIndent {
			commonIndent = indent
		}
	}
	if commonIndent <= 0 {
		return body
	}
	for i, line := range lines {
		if len(line) >= commonIndent {
			lines[i] = line[commonIndent:]
		}
	}
	return strings.Join(lines, "\n")
}
