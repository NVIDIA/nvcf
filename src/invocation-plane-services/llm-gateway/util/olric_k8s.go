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

package util

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/olric-data/olric/pkg/service_discovery"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// PodNamespaceEnv is the Downward API env var the in-cluster service discovery
// plugin uses to locate its namespace. Set it via:
//
//	env:
//	  - name: POD_NAMESPACE
//	    valueFrom:
//	      fieldRef:
//	        fieldPath: metadata.namespace
const PodNamespaceEnv = "POD_NAMESPACE"

var _ service_discovery.ServiceDiscovery = (*k8sDiscovery)(nil)

// k8sDiscovery is an Olric ServiceDiscovery plugin that finds peers by listing
// pods in the current Kubernetes namespace and returning those that match a
// label selector and are in Running+Ready state. It relies on an in-cluster
// ServiceAccount with `get,list,watch` on `pods`.
//
// Adapted from nvcf-ratelimiter/olric_k8s.go, with two changes:
//   - LabelSelector is required (empty would list every pod in the namespace,
//     which is never what we want).
//   - podAddrs is exposed as a package-private function for unit testing
//     without a real Kubernetes API.
type k8sDiscovery struct {
	ctx           context.Context
	namespace     string
	labelSelector string
	clientset     kubernetes.Interface
}

// NewK8sDiscovery builds a ServiceDiscovery plugin that looks up Olric peers
// via the Kubernetes pod API. The caller owns ctx; shutting the context down
// will abort in-flight DiscoverPeers calls.
//
// labelSelector must be non-empty and is passed verbatim to the
// Pods().List(... LabelSelector: ...) request (e.g.
// "app.kubernetes.io/part-of=llm-api-gateway").
func NewK8sDiscovery(ctx context.Context, labelSelector string) (*k8sDiscovery, error) {
	if labelSelector == "" {
		return nil, errors.New("k8s discovery: labelSelector must not be empty")
	}

	namespace := os.Getenv(PodNamespaceEnv)
	if namespace == "" {
		return nil, fmt.Errorf("k8s discovery: %s environment variable is not set", PodNamespaceEnv)
	}

	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}

	return &k8sDiscovery{
		ctx:           ctx,
		namespace:     namespace,
		labelSelector: labelSelector,
		clientset:     clientset,
	}, nil
}

// DiscoverPeers implements service_discovery.ServiceDiscovery.
//
// It returns the IP addresses of all pods in the configured namespace that
// match the label selector and are Running+Ready. An empty result is treated
// as an error so Olric retries instead of joining a single-node cluster by
// accident during a rolling restart.
func (d *k8sDiscovery) DiscoverPeers() ([]string, error) {
	pods, err := d.clientset.CoreV1().Pods(d.namespace).List(d.ctx, metav1.ListOptions{
		LabelSelector: d.labelSelector,
	})
	if err != nil {
		return nil, err
	}

	addrs := podAddrs(pods)
	if len(addrs) == 0 {
		return nil, fmt.Errorf("k8s discovery: no ready peers matched selector %q", d.labelSelector)
	}
	return addrs, nil
}

// podAddrs returns the pod IP of every Running+Ready pod in the list. A pod
// with no Ready condition is accepted (this matches the upstream behavior from
// hashicorp/go-discover); a pod whose Ready condition is explicitly false is
// skipped. Pods without an assigned IP are skipped as well.
//
// Adapted from https://github.com/hashicorp/go-discover/blob/49f60c093101c9c5f6b04d5b1c80164251a761a6/provider/k8s/k8s_discover.go#L122-L183
func podAddrs(pods *corev1.PodList) []string {
	if pods == nil {
		return nil
	}

	addrs := make([]string, 0, len(pods.Items))

PodLoop:
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status != corev1.ConditionTrue {
				continue PodLoop
			}
		}
		if pod.Status.PodIP == "" {
			continue
		}
		addrs = append(addrs, pod.Status.PodIP)
	}
	return addrs
}

func (d *k8sDiscovery) Initialize() error                  { return nil }
func (d *k8sDiscovery) SetLogger(l *log.Logger)            {}
func (d *k8sDiscovery) SetConfig(cfg map[string]any) error { return nil }
func (d *k8sDiscovery) Register() error                    { return nil }
func (d *k8sDiscovery) Deregister() error                  { return nil }
func (d *k8sDiscovery) Close() error                       { return nil }
