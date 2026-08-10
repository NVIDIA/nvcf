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

package ratelimiter

import (
	"context"
	"errors"
	"log"
	"os"

	"github.com/olric-data/olric/pkg/service_discovery"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var _ service_discovery.ServiceDiscovery = &k8sDiscovery{}

type k8sDiscovery struct {
	c             context.Context
	namespace     string
	labelSelector string
	clientset     *kubernetes.Clientset
}

// NewK8sDiscovery We are not using olric-cloud-plugin because there are some compile issues because of dependency like people pointed out online
// This file is from an existing sample code which uses k8s/client-go
func NewK8sDiscovery(c context.Context) (*k8sDiscovery, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	// Retrieve the namespace from the environment variable
	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		return nil, errors.New("POD_NAMESPACE environment variable is not set")
	}

	// Scope peer discovery to this service's pods. Empty = all pods in the
	// namespace (back-compat, e.g. single-tenant managed namespace); set it in
	// shared namespaces to avoid merging with other Olric services.
	labelSelector := os.Getenv("OLRIC_K8S_LABEL_SELECTOR")
	if labelSelector == "" {
		log.Printf("OLRIC_K8S_LABEL_SELECTOR unset; discovering all pods in namespace %q", namespace)
	}

	return &k8sDiscovery{
		c:             c,
		namespace:     namespace,
		labelSelector: labelSelector,
		clientset:     clientset,
	}, nil
}

func (d *k8sDiscovery) DiscoverPeers() ([]string, error) {
	pods, err := d.clientset.CoreV1().Pods(d.namespace).List(d.c, metav1.ListOptions{
		LabelSelector: d.labelSelector,
	})
	if err != nil {
		return nil, err
	}

	addrs, err := podAddrs(pods)
	if err != nil {
		return nil, err
	}

	if len(addrs) == 0 {
		return nil, errors.New("no peers found")
	}

	return addrs, nil
}

// podAddrs returns join-seed IPs of Running, non-terminating pods.
// Not gated on Ready: /health starts only after olric joins, so peers are
// never Ready during the cold-start join window. memberlist SWIM handles
// runtime liveness.
func podAddrs(pods *corev1.PodList) ([]string, error) {
	var addrs []string

	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}

		// Skip pods being torn down so we don't seed a leaving member.
		if pod.DeletionTimestamp != nil {
			continue
		}

		// Get the IP address that we will join.
		addr := pod.Status.PodIP

		if addr == "" {
			// This can be empty according to the API docs, so we protect that.
			continue
		}

		addrs = append(addrs, addr)
	}

	return addrs, nil
}

func (d *k8sDiscovery) Initialize() error { return nil }

func (d *k8sDiscovery) SetLogger(l *log.Logger) {}

func (d *k8sDiscovery) SetConfig(cfg map[string]interface{}) error { return nil }

func (d *k8sDiscovery) Register() error { return nil }

func (d *k8sDiscovery) Deregister() error { return nil }

func (d *k8sDiscovery) Close() error { return nil }
