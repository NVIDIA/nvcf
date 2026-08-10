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

// Package deploy applies the rendered BYOO collector to a cluster (k3d or
// remote), fronts it with a harness OTLP Service, waits for readiness, and
// tears it down. Load generation and measurement land in a later milestone.
package deploy

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/render"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/spec"
)

const (
	// partOf tags every suite-created object so cleanup can scope deletions.
	partOfLabelKey   = "app.kubernetes.io/part-of"
	partOfLabelValue = "byoo-perf"
	// instance identifies one workload so its harness Service selects only its pod.
	instanceLabelKey = "app.kubernetes.io/instance"
	// managedBy marks ownership.
	managedByLabelKey   = "app.kubernetes.io/managed-by"
	managedByLabelValue = "byoo-perf"

	servicePrefix = "byoo-perf-otlp"
)

// Client wraps a Kubernetes clientset with the operations the suite needs.
type Client struct {
	cs kubernetes.Interface
}

// Deployed describes the resources created for a single workload shape and the
// in-cluster endpoints load generators use to reach the collector.
type Deployed struct {
	Namespace   string
	PodName     string
	ServiceName string
	// Endpoints maps a collector port name (e.g. "otlp-grpc") to its
	// in-cluster address (service DNS:port).
	Endpoints map[string]string
}

// RestConfig builds a *rest.Config: in-cluster when kubeconfig and contextName
// are empty, otherwise the default kubeconfig rules (contextName selects a
// context).
func RestConfig(kubeconfig, contextName string) (*rest.Config, error) {
	if kubeconfig == "" && contextName == "" {
		if cfg, err := rest.InClusterConfig(); err == nil {
			return cfg, nil
		}
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if contextName != "" {
		overrides.CurrentContext = contextName
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kube config: %w", err)
	}
	return cfg, nil
}

// NewClient builds a Client from a kubeconfig path and context; both may be
// empty to use the ambient config.
func NewClient(kubeconfig, contextName string) (*Client, error) {
	cfg, err := RestConfig(kubeconfig, contextName)
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build clientset: %w", err)
	}
	return &Client{cs: cs}, nil
}

// NewClientForClientset builds a Client from an existing clientset, so tests
// can inject a fake.
func NewClientForClientset(cs kubernetes.Interface) *Client {
	return &Client{cs: cs}
}

// EnsureNamespace creates the namespace if it does not already exist.
func (c *Client) EnsureNamespace(ctx context.Context, namespace string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   namespace,
			Labels: map[string]string{partOfLabelKey: partOfLabelValue, managedByLabelKey: managedByLabelValue},
		},
	}
	_, err := c.cs.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensure namespace %q: %w", namespace, err)
	}
	return nil
}

// Deploy applies the collector pod plus a harness OTLP Service into the
// namespace, replacing existing objects so runs are repeatable.
func (c *Client) Deploy(ctx context.Context, namespace string, res *render.Result) (*Deployed, error) {
	if err := c.EnsureNamespace(ctx, namespace); err != nil {
		return nil, err
	}

	pod := res.BenchPod(namespace)
	instance := pod.Name
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Labels[partOfLabelKey] = partOfLabelValue
	pod.Labels[managedByLabelKey] = managedByLabelValue
	pod.Labels[instanceLabelKey] = instance

	if err := c.applyPod(ctx, namespace, pod); err != nil {
		return nil, err
	}

	svc := harnessService(namespace, instance, res.Shape, pod.Spec.Containers[0].Ports)
	if err := c.applyService(ctx, namespace, svc); err != nil {
		return nil, err
	}

	deployed := &Deployed{
		Namespace:   namespace,
		PodName:     pod.Name,
		ServiceName: svc.Name,
		Endpoints:   map[string]string{},
	}
	for _, p := range svc.Spec.Ports {
		deployed.Endpoints[p.Name] = fmt.Sprintf("%s.%s.svc.cluster.local:%d", svc.Name, namespace, p.Port)
	}
	return deployed, nil
}

func (c *Client) applyPod(ctx context.Context, namespace string, pod *corev1.Pod) error {
	pods := c.cs.CoreV1().Pods(namespace)
	err := pods.Delete(ctx, pod.Name, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete existing pod %q: %w", pod.Name, err)
	}
	if err == nil {
		// Wait for the old pod to be gone so the create doesn't race deletion.
		if werr := c.waitPodDeleted(ctx, namespace, pod.Name, 60*time.Second); werr != nil {
			return fmt.Errorf("wait for existing pod %q deletion: %w", pod.Name, werr)
		}
	}
	if _, err := pods.Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create pod %q: %w", pod.Name, err)
	}
	return nil
}

func (c *Client) applyService(ctx context.Context, namespace string, svc *corev1.Service) error {
	svcs := c.cs.CoreV1().Services(namespace)
	err := svcs.Delete(ctx, svc.Name, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete existing service %q: %w", svc.Name, err)
	}
	if _, err := svcs.Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create service %q: %w", svc.Name, err)
	}
	return nil
}

// WaitPodReady blocks until the pod is ready, the timeout elapses, or the pod
// hits a terminal failure.
func (c *Client) WaitPodReady(ctx context.Context, namespace, podName string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		pod, err := c.cs.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		if pod.Status.Phase == corev1.PodFailed {
			return false, fmt.Errorf("pod %q failed: %s", podName, pod.Status.Reason)
		}
		if reason, ok := terminalContainerFailure(pod); ok {
			return false, fmt.Errorf("pod %q not schedulable/ready: %s", podName, reason)
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
}

func (c *Client) waitPodDeleted(ctx context.Context, namespace, podName string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := c.cs.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, fmt.Errorf("get pod %q: %w", podName, err)
		}
		return false, nil
	})
}

// Cleanup deletes the suite's pods and services in the namespace, scoped by the
// part-of label so unrelated resources are left alone.
func (c *Client) Cleanup(ctx context.Context, namespace string) error {
	listOpts := metav1.ListOptions{LabelSelector: fmt.Sprintf("%s=%s", partOfLabelKey, partOfLabelValue)}
	delOpts := metav1.DeleteOptions{}

	pods, err := c.cs.CoreV1().Pods(namespace).List(ctx, listOpts)
	if err != nil {
		return fmt.Errorf("list pods in %q: %w", namespace, err)
	}
	for i := range pods.Items {
		name := pods.Items[i].Name
		if err := c.cs.CoreV1().Pods(namespace).Delete(ctx, name, delOpts); err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("delete pod %q: %w", name, err)
		}
	}

	svcs, err := c.cs.CoreV1().Services(namespace).List(ctx, listOpts)
	if err != nil {
		return fmt.Errorf("list services in %q: %w", namespace, err)
	}
	for i := range svcs.Items {
		name := svcs.Items[i].Name
		if err := c.cs.CoreV1().Services(namespace).Delete(ctx, name, delOpts); err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("delete service %q: %w", name, err)
		}
	}
	return nil
}

// harnessService builds a ClusterIP Service exposing the collector's named
// ports, giving load generators a stable in-cluster address. It mirrors the
// production Service for the Helm shape; for the container (sidecar) shape it is
// a harness-only addition that never alters the collector spec under test.
func harnessService(namespace, instance string, shape spec.Shape, ports []corev1.ContainerPort) *corev1.Service {
	svcPorts := make([]corev1.ServicePort, 0, len(ports))
	for _, p := range ports {
		svcPorts = append(svcPorts, corev1.ServicePort{
			Name:       p.Name,
			Port:       p.ContainerPort,
			TargetPort: intstr.FromString(p.Name),
			Protocol:   corev1.ProtocolTCP,
		})
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", servicePrefix, shape),
			Namespace: namespace,
			Labels: map[string]string{
				partOfLabelKey:    partOfLabelValue,
				managedByLabelKey: managedByLabelValue,
				instanceLabelKey:  instance,
			},
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: map[string]string{instanceLabelKey: instance},
			Ports:    svcPorts,
		},
	}
}

// terminalContainerFailure reports a reason when a container is stuck in a
// non-recoverable waiting state, so WaitPodReady can fail fast.
func terminalContainerFailure(pod *corev1.Pod) (string, bool) {
	for _, cs := range pod.Status.ContainerStatuses {
		w := cs.State.Waiting
		if w == nil {
			continue
		}
		switch w.Reason {
		case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "CreateContainerConfigError", "InvalidImageName":
			msg := w.Reason
			if w.Message != "" {
				msg = fmt.Sprintf("%s: %s", w.Reason, w.Message)
			}
			return fmt.Sprintf("container %q %s", cs.Name, msg), true
		}
	}
	return "", false
}
