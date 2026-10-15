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
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// fileSink receives one logical file at a time. The directory and archive
// renderers each implement it so the dump layout is generated in exactly one
// place and the two output formats cannot drift apart.
type fileSink func(relPath string, body []byte) error

// truncationNotice is appended to a log body that was cut at the byte ceiling.
const truncationNotice = "\n... [truncated by cluster-dump --max-log-bytes]\n"

// writeDumpTree emits the full deep-dump layout to the sink:
//
//	dump.json            machine-readable summary + capture index (no raw bodies)
//	summary.txt          the human-readable health report (same as stdout)
//	upload.txt           summary + per-plane warnings collated for quick sharing
//	<plane>/namespaces/<ns>/<resource>/<name>.yaml
//	<plane>/namespaces/<ns>/pods/<pod>/<container>.log[.previous]
//	<plane>/warnings.txt
func writeDumpTree(d Dump, sink fileSink) error {
	jb, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal dump.json: %w", err)
	}
	if err := sink("dump.json", jb); err != nil {
		return err
	}

	var summary strings.Builder
	if err := RenderText(&summary, d); err != nil {
		return err
	}
	if err := sink("summary.txt", []byte(summary.String())); err != nil {
		return err
	}

	var planes []struct {
		dir string
		p   *PlaneSnapshot
	}
	if d.ControlPlane != nil {
		planes = append(planes, struct {
			dir string
			p   *PlaneSnapshot
		}{"control-plane", d.ControlPlane})
	}
	if d.ComputePlane != nil {
		planes = append(planes, struct {
			dir string
			p   *PlaneSnapshot
		}{"compute-plane", d.ComputePlane})
	}

	for _, pl := range planes {
		for _, r := range pl.p.Resources {
			rel := path.Join(pl.dir, "namespaces", r.Namespace, r.Resource, r.Name+".yaml")
			if err := sink(rel, r.YAML); err != nil {
				return err
			}
		}
		for _, lg := range pl.p.PodLogs {
			name := lg.Container + ".log"
			if lg.Previous {
				name = lg.Container + ".previous.log"
			}
			rel := path.Join(pl.dir, "namespaces", lg.Namespace, "pods", lg.Pod, name)
			body := lg.Body
			if lg.Truncated {
				body = append(append([]byte{}, body...), []byte(truncationNotice)...)
			}
			if err := sink(rel, body); err != nil {
				return err
			}
		}
		if len(pl.p.ICMSRequests) > 0 {
			if err := sink(path.Join(pl.dir, "icmsrequests-summary.txt"), []byte(renderICMSSummary(pl.p.ICMSRequests, false))); err != nil {
				return err
			}
			if failed := failedICMS(pl.p.ICMSRequests); len(failed) > 0 {
				if err := sink(path.Join(pl.dir, "icmsrequests-failed-summary.txt"), []byte(renderICMSSummary(failed, true))); err != nil {
					return err
				}
			}
		}
		if pl.p.GPU != nil {
			if err := sink(path.Join(pl.dir, "gpu-reconciliation.txt"), []byte(renderGPU(*pl.p.GPU))); err != nil {
				return err
			}
		}
		for _, sn := range pl.p.StuckNamespaces {
			if err := sink(path.Join(pl.dir, "stuck-namespaces", sn.Name+".txt"), []byte(renderStuckNamespace(sn))); err != nil {
				return err
			}
		}
		for _, h := range pl.p.HelmDetails {
			base := path.Join(pl.dir, "helm", h.Namespace, h.Name)
			if h.Manifest != "" {
				if err := sink(path.Join(base, "manifest.yaml"), []byte(h.Manifest)); err != nil {
					return err
				}
			}
			if len(h.Values) > 0 {
				if vb, err := yaml.Marshal(h.Values); err == nil {
					if err := sink(path.Join(base, "values.yaml"), vb); err != nil {
						return err
					}
				}
			}
		}
		if len(pl.p.Warnings) > 0 {
			rel := path.Join(pl.dir, "warnings.txt")
			if err := sink(rel, []byte(strings.Join(pl.p.Warnings, "\n")+"\n")); err != nil {
				return err
			}
		}
	}

	// upload.txt: a single file an operator can attach to a support ticket.
	var up strings.Builder
	fmt.Fprintf(&up, "NVCF Cluster Dump  %s\n\n", d.CollectedAt.UTC().Format(time.RFC3339))
	up.WriteString("===== summary.txt =====\n")
	up.WriteString(summary.String())
	for _, pl := range planes {
		if failed := failedICMS(pl.p.ICMSRequests); len(failed) > 0 {
			fmt.Fprintf(&up, "\n===== %s/icmsrequests-failed-summary.txt =====\n", pl.dir)
			up.WriteString(renderICMSSummary(failed, true))
		}
		if pl.p.GPU != nil && !pl.p.GPU.Match {
			fmt.Fprintf(&up, "\n===== %s/gpu-reconciliation.txt =====\n", pl.dir)
			up.WriteString(renderGPU(*pl.p.GPU))
		}
		if len(pl.p.Warnings) > 0 {
			fmt.Fprintf(&up, "\n===== %s/warnings.txt =====\n", pl.dir)
			up.WriteString(strings.Join(pl.p.Warnings, "\n"))
			up.WriteString("\n")
		}
	}
	return sink("upload.txt", []byte(up.String()))
}

// renderICMSSummary formats the ICMSRequest triage table.
func renderICMSSummary(rows []ICMSRequestSummary, failedOnly bool) string {
	var b strings.Builder
	if failedOnly {
		b.WriteString("Failed ICMSRequests\n")
	} else {
		b.WriteString("ICMSRequests\n")
	}
	fmt.Fprintf(&b, "%-52s %-10s %-28s %-38s %s\n", "NAME", "TYPE", "STATUS", "FUNCTION-ID", "NAMESPACE")
	for _, r := range rows {
		fmt.Fprintf(&b, "%-52s %-10s %-28s %-38s %s\n", r.Name, r.Type, r.RequestStatus, r.FunctionID, r.Namespace)
	}
	return b.String()
}

// renderGPU formats the GPU reconciliation report.
func renderGPU(g GPUReconciliation) string {
	var b strings.Builder
	b.WriteString("GPU Reconciliation (NVCFBackend vs Kubernetes nodes)\n")
	fmt.Fprintf(&b, "  Node capacity:           %d\n", g.NodeCapacity)
	fmt.Fprintf(&b, "  Node allocated (pods):   %d\n", g.NodeAllocatedByPods)
	fmt.Fprintf(&b, "  MiniService reserved:    %d\n", g.MiniServiceReserved)
	fmt.Fprintf(&b, "  NVCFBackend capacity:    %d\n", g.BackendCapacity)
	fmt.Fprintf(&b, "  NVCFBackend allocated:   %d\n", g.BackendAllocated)
	fmt.Fprintf(&b, "  NVCFBackend available:   %d\n", g.BackendAvailable)
	if g.Match {
		b.WriteString("  Status: MATCH\n")
	} else {
		b.WriteString("  Status: MISMATCH\n")
		for _, n := range g.Notes {
			fmt.Fprintf(&b, "    - %s\n", n)
		}
	}
	if len(g.Cliques) > 0 {
		b.WriteString("\n  Per-clique:\n")
		fmt.Fprintf(&b, "  %-30s %8s %10s %10s %6s\n", "CLIQUE", "CAPACITY", "ALLOCATED", "AVAILABLE", "NODES")
		for _, c := range g.Cliques {
			fmt.Fprintf(&b, "  %-30s %8d %10d %10d %6d\n", c.Clique, c.Capacity, c.Allocated, c.Available, c.Nodes)
		}
	}
	return b.String()
}
