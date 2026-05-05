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

package otelconfig

import (
	"embed"
	"fmt"
	"io"
	"text/template"
)

var (
	//go:embed templates/*.tmpl
	templatesFS embed.FS
	templates   *template.Template
)

func init() {
	var err error
	if templates, err = template.ParseFS(templatesFS, "templates/*.tmpl"); err != nil {
		panic(err)
	}
}

type TemplateConfig struct {
	BackendType       BackendType
	WorkloadType      WorkloadType
	Namespace         string
	FunctionID        string
	FunctionVersionID string
	TaskID            string
	InstanceID        string
	ZoneName          string
}

func ExecuteTemplate(w io.Writer, tcfg TemplateConfig) error {
	var templateName string
	switch tcfg.BackendType {
	case VM:
		if tcfg.WorkloadType == Container {
			templateName = "config-vm-container.yaml.tmpl"
		} else {
			templateName = "config-vm-helm.yaml.tmpl"
		}
	case K8s:
		if tcfg.WorkloadType == Container {
			templateName = "config-k8s-container.yaml.tmpl"
		} else {
			templateName = "config-k8s-helm.yaml.tmpl"
		}
	default:
		return fmt.Errorf("unknown backend type: %s", tcfg.BackendType)
	}
	return templates.ExecuteTemplate(w, templateName, tcfg)
}
