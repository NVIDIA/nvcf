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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// resolveGVRs rewrites each GVR's version to the one the API server actually
// serves, discovered at runtime. This makes capture robust to version skew
// (the DRA and Flux CRDs change group-version across releases). With no
// discovery client, or if discovery returns nothing, the supplied default
// versions are used unchanged, so the authoritative NVCA GVRs are always tried.
func (pc *PlaneCollector) resolveGVRs(gvrs []schema.GroupVersionResource) []schema.GroupVersionResource {
	if pc.Discovery == nil {
		return gvrs
	}
	pc.servedOnce.Do(func() { pc.served = pc.computeServedVersions() })
	if len(pc.served) == 0 {
		return gvrs
	}
	out := make([]schema.GroupVersionResource, len(gvrs))
	for i, gvr := range gvrs {
		out[i] = gvr
		if v, ok := pc.served[schema.GroupResource{Group: gvr.Group, Resource: gvr.Resource}]; ok && v != "" {
			out[i].Version = v
		}
	}
	return out
}

// computeServedVersions builds a group/resource -> preferred-version map from
// the API server's discovery document. Partial discovery failures (a flaky
// aggregated API group) still yield a usable map.
func (pc *PlaneCollector) computeServedVersions() map[schema.GroupResource]string {
	m := map[schema.GroupResource]string{}
	// ServerPreferredResources gives the server's preferred version per group;
	// fall back to the full served set if it is unavailable.
	lists, _ := pc.Discovery.ServerPreferredResources()
	if len(lists) == 0 {
		_, lists, _ = pc.Discovery.ServerGroupsAndResources()
	}
	for _, rl := range lists {
		gv, err := schema.ParseGroupVersion(rl.GroupVersion)
		if err != nil {
			continue
		}
		for _, r := range rl.APIResources {
			if strings.Contains(r.Name, "/") { // skip subresources (e.g. pods/log)
				continue
			}
			gr := schema.GroupResource{Group: gv.Group, Resource: r.Name}
			if _, ok := m[gr]; !ok {
				m[gr] = gv.Version
			}
		}
	}
	return m
}

// CapturedResource is one Kubernetes object captured for the dump tree. The YAML
// body is written to a file and is deliberately excluded from dump.json (which
// keeps only the lightweight index of what was captured).
type CapturedResource struct {
	Kind      string `json:"kind"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	YAML      []byte `json:"-"`
}

// stackGVRs returns the resource kinds captured for this plane. The compute
// plane adds the NVCA / function-deployment CRDs.
func (pc *PlaneCollector) stackGVRs() []schema.GroupVersionResource {
	if pc.Role == RoleComputePlane {
		out := make([]schema.GroupVersionResource, 0, len(coreStackGVRs)+len(nvcaStackGVRs))
		out = append(out, coreStackGVRs...)
		return append(out, nvcaStackGVRs...)
	}
	return coreStackGVRs
}

// collectStackResources captures the configured resource kinds in each probed
// namespace for this plane.
func (pc *PlaneCollector) collectStackResources(ctx context.Context) ([]CapturedResource, []string, error) {
	if pc.Dynamic == nil {
		return nil, nil, fmt.Errorf("dynamic client unavailable")
	}
	out, warns := pc.collectResourcesIn(ctx, pc.Namespaces, pc.stackGVRs())
	return out, warns, nil
}

// collectResourcesIn lists each GVR in each namespace via the dynamic client and
// captures every object as redacted YAML. A kind that is absent (NotFound) or
// not permitted (Forbidden) is skipped quietly; other list errors become
// warnings. Results are sorted for deterministic output.
func (pc *PlaneCollector) collectResourcesIn(ctx context.Context, namespaces []string, gvrs []schema.GroupVersionResource) ([]CapturedResource, []string) {
	gvrs = pc.resolveGVRs(gvrs)
	var (
		out   []CapturedResource
		warns []string
	)
	for _, ns := range namespaces {
		for _, gvr := range gvrs {
			items, err := pc.listResource(ctx, gvr, ns)
			if err != nil {
				switch {
				case apierrors.IsNotFound(err):
					// CRD/kind not installed on this cluster; skip quietly.
				case apierrors.IsForbidden(err):
					// The resource exists but RBAC blocked it; the operator should
					// know it was not captured rather than assume it was absent.
					warns = append(warns, fmt.Sprintf("%s in %s: not permitted (RBAC), skipped", gvr.Resource, ns))
				default:
					warns = append(warns, fmt.Sprintf("%s in %s: %v", gvr.Resource, ns, err))
				}
				continue
			}
			for i := range items {
				item := items[i]
				redactObject(&item, pc.Options)
				body, err := yaml.Marshal(item.Object)
				if err != nil {
					warns = append(warns, fmt.Sprintf("marshal %s/%s: %v", gvr.Resource, item.GetName(), err))
					continue
				}
				out = append(out, CapturedResource{
					Kind:      item.GetKind(),
					Resource:  gvr.Resource,
					Namespace: ns,
					Name:      item.GetName(),
					YAML:      body,
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		if out[i].Resource != out[j].Resource {
			return out[i].Resource < out[j].Resource
		}
		return out[i].Name < out[j].Name
	})
	return out, warns
}

// listResource returns every object of a GVR in a namespace, following the
// List pagination continue token.
func (pc *PlaneCollector) listResource(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]unstructured.Unstructured, error) {
	var items []unstructured.Unstructured
	cont := ""
	for {
		ri := pc.Dynamic.Resource(gvr)
		var (
			list *unstructured.UnstructuredList
			err  error
		)
		opts := metav1.ListOptions{Limit: stackListPageSize, Continue: cont}
		if namespace == "" {
			list, err = ri.List(ctx, opts)
		} else {
			list, err = ri.Namespace(namespace).List(ctx, opts)
		}
		if err != nil {
			return nil, err
		}
		items = append(items, list.Items...)
		cont = list.GetContinue()
		if cont == "" {
			return items, nil
		}
	}
}
