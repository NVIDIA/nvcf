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

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/otelconfig/backendconfig"
	otelconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/otelconfig/config"
)

func writeConfig(data []byte, outputPath string) error {
	// Write the rendered OpenTelemetry configuration to a file
	err := os.WriteFile(outputPath, data, 0644)
	if err != nil {
		return err
	}
	return nil
}

func main() {
	var inputFilePath, outputDir, backend, workloadType, computeType string
	// Accessing other arguments
	if len(os.Args) > 1 {
		inputFilePath = os.Args[1]
		outputDir = os.Args[2]
		backend = os.Args[3]      // vm (GFN) or k8s (non-GFN)
		workloadType = os.Args[4] // container or helm
		computeType = os.Args[5]  // functions or tasks
	} else {
		log.Fatalf("Please provide input file path")
	}

	// Read the JSON file with telemetry input
	inputData, err := os.ReadFile(inputFilePath)
	if err != nil {
		log.Fatalf("Error reading input file: %v", err)
	}

	// Render the OpenTelemetry configuration - returns data in yaml format
	// WorkloadType can be container or helm for any NVCF or NVCT
	// BackendType can be VM or K8s
	tcfg := backendconfig.TemplateConfig{
		BackendType:  backendconfig.BackendType(backend),
		WorkloadType: backendconfig.WorkloadType(workloadType),
		Namespace:    "sr-fake-namespace",
	}
	switch computeType {
	case "function":
		tcfg.FunctionID = "fake-function-id"
		tcfg.FunctionVersionID = "fake-function-version-id"
		tcfg.InstanceID = "fake-instance-id"
		tcfg.ZoneName = "fake-zone-name"
	case "task":
		tcfg.TaskID = "taskid123"
		tcfg.InstanceID = "fake-instance-id"
		tcfg.ZoneName = "fake-zone-name"
	default:
		log.Fatalf("expected function (container or helm) or task for last input arg, got %s", workloadType)
	}
	data, err := otelconfig.RenderOtelConfigFromBytes(inputData, tcfg)
	if err != nil {
		log.Fatalf("Error rendering OpenTelemetry configuration: %v", err)
	}

	configDir := filepath.Join(outputDir, "byoo-otel-collector")

	err = os.MkdirAll(configDir, 0755)
	if err != nil {
		log.Fatalf("Error creating config.yaml dir %v", err)
	}

	outputFilePath := fmt.Sprintf("%s/config.%s_%s_%s.yaml", configDir, computeType, backend, workloadType)
	err = writeConfig(data, outputFilePath)
	if err != nil {
		log.Fatalf("Error writing OpenTelemetry configuration: %v", err)
	}
}
