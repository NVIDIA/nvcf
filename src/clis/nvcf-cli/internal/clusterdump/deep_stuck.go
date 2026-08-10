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
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// StuckObject is one resource still present in a Terminating namespace, with the
// finalizers blocking its (and therefore the namespace's) deletion.
type StuckObject struct {
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	Finalizers []string `json:"finalizers,omitempty"`
}

// StuckNamespace diagnoses a namespace stuck in Terminating: a namespace cannot
// finish deleting while any contained object still carries a finalizer whose
// controller has not removed it. This probe is strictly read-only: it reports
// the suggested manual remedy but never strips a finalizer.
type StuckNamespace struct {
	Name              string        `json:"name"`
	DeletionTimestamp string        `json:"deletionTimestamp,omitempty"`
	Conditions        []string      `json:"conditions,omitempty"`
	RemainingObjects  []StuckObject `json:"remainingObjects,omitempty"`
}

// collectStuckNamespaces finds Terminating namespaces and, for each, lists the
// objects (across the captured kinds) that still hold finalizers.
func (pc *PlaneCollector) collectStuckNamespaces(ctx context.Context) ([]StuckNamespace, []string) {
	var warns []string
	nss, err := pc.Kube.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, []string{fmt.Sprintf("stuck-namespaces: %v", err)}
	}

	var out []StuckNamespace
	for i := range nss.Items {
		ns := &nss.Items[i]
		if ns.Status.Phase != corev1.NamespaceTerminating {
			continue
		}
		sn := StuckNamespace{Name: ns.Name}
		if ns.DeletionTimestamp != nil {
			sn.DeletionTimestamp = ns.DeletionTimestamp.UTC().Format("2006-01-02T15:04:05Z")
		}
		for _, c := range ns.Status.Conditions {
			if msg := strings.TrimSpace(c.Message); msg != "" {
				sn.Conditions = append(sn.Conditions, fmt.Sprintf("%s: %s", c.Type, msg))
			}
		}
		// Probe the captured kinds for objects still holding finalizers.
		if pc.Dynamic != nil {
			for _, gvr := range pc.resolveGVRs(pc.stackGVRs()) {
				items, err := pc.listResource(ctx, gvr, ns.Name)
				if err != nil {
					continue
				}
				for j := range items {
					if fins := items[j].GetFinalizers(); len(fins) > 0 {
						sn.RemainingObjects = append(sn.RemainingObjects, StuckObject{
							Kind:       items[j].GetKind(),
							Name:       items[j].GetName(),
							Finalizers: fins,
						})
					}
				}
			}
			sort.Slice(sn.RemainingObjects, func(a, b int) bool {
				if sn.RemainingObjects[a].Kind != sn.RemainingObjects[b].Kind {
					return sn.RemainingObjects[a].Kind < sn.RemainingObjects[b].Kind
				}
				return sn.RemainingObjects[a].Name < sn.RemainingObjects[b].Name
			})
		}
		out = append(out, sn)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out, warns
}

// renderStuckNamespace formats the read-only diagnosis, including the manual
// remedy (which the tool never executes).
func renderStuckNamespace(sn StuckNamespace) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Stuck namespace: %s\n", sn.Name)
	if sn.DeletionTimestamp != "" {
		fmt.Fprintf(&b, "  deletionTimestamp: %s\n", sn.DeletionTimestamp)
	}
	if len(sn.Conditions) > 0 {
		b.WriteString("  conditions:\n")
		for _, c := range sn.Conditions {
			fmt.Fprintf(&b, "    - %s\n", c)
		}
	}
	if len(sn.RemainingObjects) == 0 {
		b.WriteString("  no captured objects hold finalizers; the namespace's own finalizer or an\n")
		b.WriteString("  uncaptured resource kind may be blocking. Check: kubectl get ns " + sn.Name + " -o jsonpath='{.spec.finalizers}'\n")
		return b.String()
	}
	b.WriteString("  objects still holding finalizers (these block deletion):\n")
	for _, o := range sn.RemainingObjects {
		fmt.Fprintf(&b, "    - %s/%s  finalizers=%s\n", o.Kind, o.Name, strings.Join(o.Finalizers, ","))
	}
	b.WriteString("\n  Remedy (manual, NOT performed by this tool):\n")
	b.WriteString("    1. Preferred: restart the owning controller so it re-reconciles and removes the finalizer.\n")
	b.WriteString("    2. Last resort, only after confirming the backing resource is gone:\n")
	b.WriteString("       kubectl patch <kind>/<name> -n " + sn.Name + " --type merge -p '{\"metadata\":{\"finalizers\":[]}}'\n")
	return b.String()
}
