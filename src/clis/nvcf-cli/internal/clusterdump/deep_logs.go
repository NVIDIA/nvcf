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
	"io"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	defaultLogTail     int64 = 2000
	defaultMaxLogBytes int   = 1 << 20 // 1 MiB
)

// PodLog is one container's captured log (current or previous instance). The
// body is written to a file and excluded from dump.json.
type PodLog struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Previous  bool   `json:"previous"`
	Truncated bool   `json:"truncated"`
	Body      []byte `json:"-"`
}

// collectPodLogs fetches bounded logs for every container of every pod in the
// probed namespaces. It also grabs the previous instance's logs for any
// container that has restarted, which is usually where a crash cause lives.
// Per-container failures degrade to warnings.
func (pc *PlaneCollector) collectPodLogs(ctx context.Context) ([]PodLog, []string, error) {
	out, warns := pc.collectLogsIn(ctx, pc.Namespaces)
	return out, warns, nil
}

// collectLogsIn fetches bounded logs for every container of every pod in the
// given namespaces. Shared by the fixed-namespace probe and the per-request
// (helm-function namespace) capture.
func (pc *PlaneCollector) collectLogsIn(ctx context.Context, namespaces []string) ([]PodLog, []string) {
	tail := pc.Options.LogTail
	if tail <= 0 {
		tail = defaultLogTail
	}
	maxBytes := pc.Options.MaxLogBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxLogBytes
	}

	var (
		out   []PodLog
		warns []string
	)
	for _, ns := range namespaces {
		pods, err := pc.Kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			warns = append(warns, fmt.Sprintf("list pods in %s: %v", ns, err))
			continue
		}
		for i := range pods.Items {
			p := &pods.Items[i]
			containers := make([]string, 0, len(p.Spec.InitContainers)+len(p.Spec.Containers))
			for _, c := range p.Spec.InitContainers {
				containers = append(containers, c.Name)
			}
			for _, c := range p.Spec.Containers {
				containers = append(containers, c.Name)
			}
			for _, c := range containers {
				if body, trunc, err := pc.fetchLog(ctx, ns, p.Name, c, false, tail, maxBytes); err != nil {
					warns = append(warns, fmt.Sprintf("logs %s/%s[%s]: %v", ns, p.Name, c, err))
				} else if len(body) > 0 {
					out = append(out, PodLog{Namespace: ns, Pod: p.Name, Container: c, Body: body, Truncated: trunc})
				}
				if containerRestarted(p, c) {
					if body, trunc, err := pc.fetchLog(ctx, ns, p.Name, c, true, tail, maxBytes); err == nil && len(body) > 0 {
						out = append(out, PodLog{Namespace: ns, Pod: p.Name, Container: c, Previous: true, Body: body, Truncated: trunc})
					}
				}
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		if out[i].Pod != out[j].Pod {
			return out[i].Pod < out[j].Pod
		}
		if out[i].Container != out[j].Container {
			return out[i].Container < out[j].Container
		}
		return !out[i].Previous && out[j].Previous
	})
	return out, warns
}

func (pc *PlaneCollector) fetchLog(ctx context.Context, ns, pod, container string, previous bool, tail int64, maxBytes int) ([]byte, bool, error) {
	req := pc.Kube.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{
		Container: container,
		Previous:  previous,
		TailLines: &tail,
	})
	rc, err := req.Stream(ctx)
	if err != nil {
		return nil, false, err
	}
	defer rc.Close()

	// Read one byte past the ceiling so we can detect truncation.
	b, err := io.ReadAll(io.LimitReader(rc, int64(maxBytes)+1))
	if err != nil {
		return nil, false, err
	}
	if len(b) > maxBytes {
		return b[:maxBytes], true, nil
	}
	return b, false, nil
}

func containerRestarted(p *corev1.Pod, container string) bool {
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Name == container {
			return cs.RestartCount > 0
		}
	}
	for _, cs := range p.Status.InitContainerStatuses {
		if cs.Name == container {
			return cs.RestartCount > 0
		}
	}
	return false
}
