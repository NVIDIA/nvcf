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
	"encoding/json"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/labels"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/render"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/report"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/sink"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/spec"
)

// Label keys/values are shared with the sink and loadgen packages via
// pkg/labels so cleanup (scoped by partOfLabelKey) matches every object the
// suite creates.
const (
	partOfLabelKey      = labels.PartOf
	partOfLabelValue    = labels.PartOfValue
	instanceLabelKey    = labels.Instance
	managedByLabelKey   = labels.ManagedBy
	managedByLabelValue = labels.ManagedByValue

	servicePrefix = "byoo-perf-otlp"

	// accountsSecretsMountPath is where the BYOO collector's secrets-extractor
	// reads its input accounts-secrets.json. The extractor flattens that JSON
	// into one file per key under the output secrets dir, which the generated
	// exporter config then references via ${file:...}. The translator mounts an
	// emptyDir here; we back it with a Secret so the extractor can start and
	// generate the per-signal token files (leaving the output dir writable).
	accountsSecretsMountPath = "/var/secrets"
	// accountsSecretsFile is the input file name the extractor waits for.
	accountsSecretsFile = "accounts-secrets.json"
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

// deploySettings holds optional behavior for Deploy.
type deploySettings struct {
	// exportCredentials maps a credential file name to its content. When set,
	// Deploy writes them into a Secret and mounts it over the collector's
	// secrets volume so the generated exporter config can resolve the
	// ${file:...} references it needs to start.
	exportCredentials map[string]string
}

// DeployOption customizes Deploy.
type DeployOption func(*deploySettings)

// WithExportCredentials backs the collector's accounts-secrets input with a
// Secret whose accounts-secrets.json holds the given name -> token map. The
// collector's secrets-extractor flattens it into the per-signal token files the
// exporter config references, so the collector can start and export to the
// in-cluster sink instead of the unreachable placeholder endpoints used for
// rendering.
func WithExportCredentials(creds map[string]string) DeployOption {
	return func(s *deploySettings) { s.exportCredentials = creds }
}

// Deploy applies the rendered workload for the shape into the namespace: the
// authentic collector pod plus a harness OTLP Service that targets it. Existing
// objects with the same names are replaced so runs are repeatable.
func (c *Client) Deploy(ctx context.Context, namespace string, res *render.Result, opts ...DeployOption) (*Deployed, error) {
	var settings deploySettings
	for _, o := range opts {
		o(&settings)
	}

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

	if len(settings.exportCredentials) > 0 {
		secretName := instance + "-export-creds"
		// The extractor consumes a single accounts-secrets.json whose keys become
		// the per-signal token files, so encode the credential map as that file.
		payload, err := json.Marshal(settings.exportCredentials)
		if err != nil {
			return nil, fmt.Errorf("encode accounts secrets: %w", err)
		}
		if err := c.applyCredentialsSecret(ctx, namespace, secretName, instance, map[string]string{accountsSecretsFile: string(payload)}); err != nil {
			return nil, err
		}
		if !mountSecretOverPath(pod, accountsSecretsMountPath, secretName) {
			return nil, fmt.Errorf("collector container does not mount %q; cannot inject accounts secrets", accountsSecretsMountPath)
		}
	}

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

// mountSecretOverPath finds the volume backing the collector volumeMount at
// mountPath and repoints it at the given Secret. It reports whether a matching
// mount was found.
func mountSecretOverPath(pod *corev1.Pod, mountPath, secretName string) bool {
	var volName string
	for _, ct := range pod.Spec.Containers {
		for _, vm := range ct.VolumeMounts {
			if vm.MountPath == mountPath {
				volName = vm.Name
				break
			}
		}
	}
	if volName == "" {
		return false
	}
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == volName {
			pod.Spec.Volumes[i].VolumeSource = corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: secretName},
			}
			return true
		}
	}
	// The mount exists but no matching volume was declared; add one.
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: volName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: secretName},
		},
	})
	return true
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

func (c *Client) applyConfigMap(ctx context.Context, namespace string, cm *corev1.ConfigMap) error {
	cms := c.cs.CoreV1().ConfigMaps(namespace)
	err := cms.Delete(ctx, cm.Name, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete existing configmap %q: %w", cm.Name, err)
	}
	if _, err := cms.Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create configmap %q: %w", cm.Name, err)
	}
	return nil
}

func (c *Client) applyCredentialsSecret(ctx context.Context, namespace, name, instance string, data map[string]string) error {
	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{Kind: "Secret", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				partOfLabelKey:    partOfLabelValue,
				managedByLabelKey: managedByLabelValue,
				instanceLabelKey:  instance,
			},
		},
		StringData: data,
		Type:       corev1.SecretTypeOpaque,
	}
	secrets := c.cs.CoreV1().Secrets(namespace)
	err := secrets.Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete existing secret %q: %w", name, err)
	}
	if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create secret %q: %w", name, err)
	}
	return nil
}

// SinkDeployed describes the in-cluster OTLP sink and the endpoints the
// collector under test exports to.
type SinkDeployed struct {
	PodName         string
	ServiceName     string
	GRPCEndpoint    string
	HTTPEndpoint    string
	MetricsEndpoint string
}

// DeploySink applies the in-cluster OTLP sink (config map, pod, and service)
// into the namespace, replacing any existing sink so runs are repeatable.
func (c *Client) DeploySink(ctx context.Context, namespace string, opts sink.Options) (*SinkDeployed, error) {
	if err := c.EnsureNamespace(ctx, namespace); err != nil {
		return nil, err
	}
	if err := c.applyConfigMap(ctx, namespace, sink.ConfigMap(namespace)); err != nil {
		return nil, err
	}
	if err := c.applyPod(ctx, namespace, sink.Pod(namespace, opts)); err != nil {
		return nil, err
	}
	svc := sink.Service(namespace)
	if err := c.applyService(ctx, namespace, svc); err != nil {
		return nil, err
	}
	return &SinkDeployed{
		PodName:         sink.Name,
		ServiceName:     svc.Name,
		GRPCEndpoint:    sink.GRPCEndpoint(namespace),
		HTTPEndpoint:    sink.HTTPEndpoint(namespace),
		MetricsEndpoint: sink.MetricsEndpoint(namespace),
	}, nil
}

// StartLoad creates the telemetrygen Jobs without waiting. Existing Jobs with
// the same names are replaced first so a rerun does not stack load. Splitting
// start from wait lets the caller sample metrics while load is in flight.
func (c *Client) StartLoad(ctx context.Context, namespace string, jobs []*batchv1.Job) error {
	for _, j := range jobs {
		if err := c.applyJob(ctx, namespace, j); err != nil {
			return err
		}
	}
	return nil
}

// WaitLoadStarted blocks until every load Job has a pod that has started
// (Running, or already Succeeded for very short jobs) or the timeout elapses.
// Waiting for generators to start before the measurement window keeps pod
// scheduling and image-pull latency out of the sampled throughput.
func (c *Client) WaitLoadStarted(ctx context.Context, namespace string, jobs []*batchv1.Job, timeout time.Duration) error {
	for _, j := range jobs {
		if err := c.waitJobPodStarted(ctx, namespace, j.Name, timeout); err != nil {
			return err
		}
	}
	return nil
}

// loadStartPollInterval is the poll cadence for waiting on load-generator pods
// to start. It is a variable so tests can shorten it.
var loadStartPollInterval = time.Second

// jobPodLabelKeys are the labels the Job controller stamps on its pods. The
// modern key is batch.kubernetes.io/job-name; the unprefixed job-name is kept
// for backward compatibility. Matching either keeps the readiness wait working
// across cluster versions.
var jobPodLabelKeys = []string{"batch.kubernetes.io/job-name", "job-name"}

func (c *Client) waitJobPodStarted(ctx context.Context, namespace, jobName string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, loadStartPollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		// Resolve the current Job's UID so we only observe its pods. applyJob
		// replaces the previous run's Job with background propagation, so a
		// Failed pod from a prior repetition can briefly linger under the same
		// name; without this filter it would wrongly abort the new run.
		job, err := c.cs.BatchV1().Jobs(namespace).Get(ctx, jobName, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				return false, nil
			}
			return false, fmt.Errorf("get job %q: %w", jobName, err)
		}
		for _, key := range jobPodLabelKeys {
			pods, err := c.cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: key + "=" + jobName})
			if err != nil {
				return false, fmt.Errorf("list pods for job %q: %w", jobName, err)
			}
			for _, p := range pods.Items {
				if !ownedBy(p.OwnerReferences, job.UID) {
					continue
				}
				switch p.Status.Phase {
				case corev1.PodRunning, corev1.PodSucceeded:
					return true, nil
				case corev1.PodFailed:
					return false, fmt.Errorf("load generator job %q pod %q failed", jobName, p.Name)
				}
			}
		}
		return false, nil
	})
}

// ownedBy reports whether any owner reference matches the given UID.
func ownedBy(owners []metav1.OwnerReference, uid types.UID) bool {
	for _, o := range owners {
		if o.UID == uid {
			return true
		}
	}
	return false
}

// WaitLoad blocks until every load Job completes or the timeout elapses.
func (c *Client) WaitLoad(ctx context.Context, namespace string, jobs []*batchv1.Job, timeout time.Duration) error {
	for _, j := range jobs {
		if err := c.waitJobComplete(ctx, namespace, j.Name, timeout); err != nil {
			return err
		}
	}
	return nil
}

// RunLoad starts the load Jobs and blocks until they all complete.
func (c *Client) RunLoad(ctx context.Context, namespace string, jobs []*batchv1.Job, timeout time.Duration) error {
	if err := c.StartLoad(ctx, namespace, jobs); err != nil {
		return err
	}
	return c.WaitLoad(ctx, namespace, jobs, timeout)
}

// scrapeTimeout bounds a single metric scrape so an unresponsive pod or API
// proxy cannot hang the whole run; a timeout yields an error the caller treats
// as a missing (best-effort) sample. It is a variable so tests can shorten it.
var scrapeTimeout = 15 * time.Second

// proxyGet performs the low-level API-proxy fetch. It is a seam so tests can
// exercise the timeout path without a live cluster.
var proxyGet = func(ctx context.Context, cs kubernetes.Interface, namespace, pod, port, path string) ([]byte, error) {
	return cs.CoreV1().Pods(namespace).ProxyGet("http", pod, port, path, nil).DoRaw(ctx)
}

// ScrapePodMetrics fetches a pod's Prometheus endpoint through the API server
// proxy. This works without a metrics-server, an ingress, or port-forwarding,
// and is the only cross-namespace-safe way to read in-cluster endpoints from
// outside the cluster. Each scrape is bounded by scrapeTimeout.
func (c *Client) ScrapePodMetrics(ctx context.Context, namespace, pod, port, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, scrapeTimeout)
	defer cancel()
	raw, err := proxyGet(ctx, c.cs, namespace, pod, port, path)
	if err != nil {
		return nil, fmt.Errorf("scrape %s/%s:%s%s: %w", namespace, pod, port, path, err)
	}
	return raw, nil
}

// PodHealth reports the collector pod's phase, aggregate restart count, and
// whether any container was OOM killed.
func (c *Client) PodHealth(ctx context.Context, namespace, pod string) (report.PodHealth, error) {
	p, err := c.cs.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return report.PodHealth{}, fmt.Errorf("get pod %q: %w", pod, err)
	}
	h := report.PodHealth{Phase: string(p.Status.Phase)}
	for _, cs := range p.Status.ContainerStatuses {
		h.Restarts += cs.RestartCount
		if term := cs.LastTerminationState.Terminated; term != nil && term.Reason == "OOMKilled" {
			h.OOMKilled = true
		}
		if term := cs.State.Terminated; term != nil && term.Reason == "OOMKilled" {
			h.OOMKilled = true
		}
	}
	return h, nil
}

func (c *Client) applyJob(ctx context.Context, namespace string, job *batchv1.Job) error {
	jobs := c.cs.BatchV1().Jobs(namespace)
	policy := metav1.DeletePropagationBackground
	err := jobs.Delete(ctx, job.Name, metav1.DeleteOptions{PropagationPolicy: &policy})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete existing job %q: %w", job.Name, err)
	}
	if _, err := jobs.Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create job %q: %w", job.Name, err)
	}
	return nil
}

func (c *Client) waitJobComplete(ctx context.Context, namespace, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		job, err := c.cs.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		for _, cond := range job.Status.Conditions {
			if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
				return true, nil
			}
			if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
				return false, fmt.Errorf("load generator job %q failed: %s", name, cond.Message)
			}
		}
		return false, nil
	})
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

// Cleanup deletes every resource the suite created in the namespace (pods,
// services, jobs, config maps, and secrets), scoped by the part-of label so it
// never removes anything the suite did not create.
func (c *Client) Cleanup(ctx context.Context, namespace string) error {
	listOpts := metav1.ListOptions{LabelSelector: fmt.Sprintf("%s=%s", partOfLabelKey, partOfLabelValue)}
	delOpts := metav1.DeleteOptions{}
	background := metav1.DeletePropagationBackground
	jobDelOpts := metav1.DeleteOptions{PropagationPolicy: &background}

	// Delete Jobs first (with background propagation) so their pods are torn
	// down by the Job controller rather than lingering.
	jobs, err := c.cs.BatchV1().Jobs(namespace).List(ctx, listOpts)
	if err != nil {
		return fmt.Errorf("list jobs in %q: %w", namespace, err)
	}
	for i := range jobs.Items {
		name := jobs.Items[i].Name
		if err := c.cs.BatchV1().Jobs(namespace).Delete(ctx, name, jobDelOpts); err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("delete job %q: %w", name, err)
		}
	}

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

	cms, err := c.cs.CoreV1().ConfigMaps(namespace).List(ctx, listOpts)
	if err != nil {
		return fmt.Errorf("list configmaps in %q: %w", namespace, err)
	}
	for i := range cms.Items {
		name := cms.Items[i].Name
		if err := c.cs.CoreV1().ConfigMaps(namespace).Delete(ctx, name, delOpts); err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("delete configmap %q: %w", name, err)
		}
	}

	secrets, err := c.cs.CoreV1().Secrets(namespace).List(ctx, listOpts)
	if err != nil {
		return fmt.Errorf("list secrets in %q: %w", namespace, err)
	}
	for i := range secrets.Items {
		name := secrets.Items[i].Name
		if err := c.cs.CoreV1().Secrets(namespace).Delete(ctx, name, delOpts); err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("delete secret %q: %w", name, err)
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
