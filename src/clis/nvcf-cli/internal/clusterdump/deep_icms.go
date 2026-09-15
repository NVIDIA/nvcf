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

package clusterdump

import (
	"context"
	"fmt"
	"sort"

	"sigs.k8s.io/yaml"
)

// icmsFailedStatuses are the ICMSRequest requestStatus values that mark a failed
// function deployment, mirroring the QA cluster-dump triage.
var icmsFailedStatuses = map[string]bool{
	"RequestFailed":              true,
	"RequestFailureAcknowledged": true,
}

// ICMSRequestSummary is the per-request triage row written to the dump (and the
// failed-request summary). It mirrors what an operator reads from
// `kubectl get icmsrequest`.
type ICMSRequestSummary struct {
	Name              string `json:"name"`
	Namespace         string `json:"namespace"`
	Type              string `json:"type"` // container | helm
	FunctionID        string `json:"functionId,omitempty"`
	FunctionVersionID string `json:"functionVersionId,omitempty"`
	RequestStatus     string `json:"requestStatus"`
	Failed            bool   `json:"failed"`
}

// helmFunctionNamespaces returns the request-named namespaces for helm-function
// ICMSRequests; helm functions live in a namespace named after the request,
// while container functions live in nvcf-backend.
func helmFunctionNamespaces(summaries []ICMSRequestSummary) []string {
	var nss []string
	for _, s := range summaries {
		if s.Type == "helm" {
			nss = append(nss, s.Name)
		}
	}
	sort.Strings(nss)
	return nss
}

// collectICMSSummaries lists ICMSRequests and returns the triage summaries plus
// each request's CR as YAML. This is cheap (one paginated list) and is collected
// for every dump so the summaries show up in the stdout report; the CR YAML is
// only written to disk by the bundle renderer.
func (pc *PlaneCollector) collectICMSSummaries(ctx context.Context) ([]ICMSRequestSummary, []CapturedResource, []string) {
	items, err := pc.listResource(ctx, icmsRequestGVR, icmsBackendNamespace)
	if err != nil {
		return nil, nil, []string{fmt.Sprintf("icmsrequests: %v", err)}
	}

	var (
		summaries []ICMSRequestSummary
		crs       []CapturedResource
	)
	for i := range items {
		obj := items[i].Object
		name := items[i].GetName()
		status := nestedStr(obj, "status", "requestStatus")
		ftype := "container"
		ns := icmsBackendNamespace
		if icmsHasHelmChart(obj) {
			ftype = "helm"
			ns = name
		}
		summaries = append(summaries, ICMSRequestSummary{
			Name:              name,
			Namespace:         ns,
			Type:              ftype,
			FunctionID:        firstNonEmpty(nestedStr(obj, "spec", "functionDetails", "functionId"), nestedStr(obj, "spec", "functionId")),
			FunctionVersionID: firstNonEmpty(nestedStr(obj, "spec", "functionDetails", "functionVersionId"), nestedStr(obj, "spec", "functionVersionId")),
			RequestStatus:     status,
			Failed:            icmsFailedStatuses[status],
		})
		// The triage summary above is always collected, but the raw ICMSRequest
		// YAML is a heavy bundle artifact (function-deployment specs: helm chart
		// refs, GPU counts, function IDs). Only capture it when the operator
		// asked for resources (--include resources/all), matching the other
		// resource captures, so --include logs does not pull it in.
		if pc.Options.Has(SectionResources) {
			if body, err := yaml.Marshal(obj); err == nil {
				crs = append(crs, CapturedResource{
					Kind: "ICMSRequest", Resource: "icmsrequests",
					Namespace: icmsBackendNamespace, Name: name, YAML: body,
				})
			}
		}
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Name < summaries[j].Name })
	return summaries, crs, nil
}

// collectICMSHelmCaptures captures the resources and pod logs in each
// helm-function namespace. This is the heavier part (per-namespace lists and log
// streams) and is only run for the bundle.
func (pc *PlaneCollector) collectICMSHelmCaptures(ctx context.Context, summaries []ICMSRequestSummary) ([]CapturedResource, []PodLog, []string) {
	nss := helmFunctionNamespaces(summaries)
	if len(nss) == 0 {
		return nil, nil, nil
	}
	resources, warns := pc.collectResourcesIn(ctx, nss, pc.stackGVRs())
	logs, w2 := pc.collectLogsIn(ctx, nss)
	return resources, logs, append(warns, w2...)
}

// icmsHasHelmChart reports whether the request launches a helm function (vs a
// container function), matching the QA classification: a non-empty helmChart on
// either the function or task launch specification.
func icmsHasHelmChart(obj map[string]interface{}) bool {
	fn := nestedStr(obj, "spec", "creationMsgInfo", "functionLaunchSpecification", "helmChart")
	task := nestedStr(obj, "spec", "creationMsgInfo", "taskLaunchSpecification", "helmChart")
	return fn != "" || task != ""
}

// failedICMS returns just the failed request summaries.
func failedICMS(summaries []ICMSRequestSummary) []ICMSRequestSummary {
	var out []ICMSRequestSummary
	for _, s := range summaries {
		if s.Failed {
			out = append(out, s)
		}
	}
	return out
}
