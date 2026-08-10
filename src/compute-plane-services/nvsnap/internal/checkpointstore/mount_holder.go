/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

// Mount-holder pod for the v0.0.51 L2 writer redesign
// (docs/design/ROOTFS-EVERYWHERE.md). The mount-holder's sole job is
// to satisfy kubelet's "PVC needs a referrer to attach+mount" rule:
//
//   - The pod schedules on the capturing agent's node (nodeName pin).
//   - Kubelet asks PD-CSI to attach the Hyperdisk-ML disk, then mounts
//     it under /var/lib/kubelet/pods/<uid>/volumes/kubernetes.io~csi/
//     <pv-name>/mount on that node.
//   - The agent (which mounts the node root at /host) writes captured
//     rootfs directly into that mount path — a single bandwidth pass,
//     no privileged pod required in the source namespace.
//   - When the agent finishes, it deletes the holder, kubelet unmounts
//     the PV, and the backend snapshots.
//
// Why a pause-style sleeper instead of a privileged copier:
// nvcf-backend enforces Kyverno policies (runAsNonRoot, no add caps,
// readOnlyRootFilesystem, RuntimeDefault seccomp, required probes,
// registry allowlist, pinned tag). A pod that does no I/O satisfies
// all of them trivially; a pod that lstats /host/proc/1/root cannot.
//
// Image choice: reuse the agent image (already on every node, already
// allowlisted) with a sleep entrypoint. This avoids a separate pull
// and a separate allowlist entry. The image must provide /bin/sleep
// and /usr/bin/test (or /bin/test) for the readiness probe.

package checkpointstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

const (
	// MountHolderUID is the non-root user the holder pod runs as.
	// 65532 is the convention for "non-root nobody" in distroless and
	// most hardened images.
	MountHolderUID int64 = 65532

	// MountHolderRunningTimeout bounds the wait for Hyperdisk-ML
	// attach + kubelet mount. Empirically ~30s on GCP-H100-a; budget
	// 3 minutes to cover tail latencies.
	MountHolderRunningTimeout = 3 * time.Minute

	// MountHolderDeleteTimeout bounds the wait for kubelet to remove
	// the pod and unmount the PV. ~5s typical; budget 30s.
	MountHolderDeleteTimeout = 30 * time.Second

	// MountHolderDestPath is where the pod mounts the rwx PVC inside
	// its own filesystem view. The readiness probe stats this path.
	MountHolderDestPath = "/dest"
)

// MountHolder owns the lifecycle of a single mount-holder pod for one
// (hash → rwx PVC) capture. Construct with NewMountHolder, then call
// Create → WaitRunning → PVMountPath → (caller copies) → Delete.
type MountHolder struct {
	kube       kubernetes.Interface
	log        *logrus.Entry
	namespace  string
	name       string
	nodeName   string
	rwxPVCName string
	rwxPVCUID  types.UID
	image      string
	hostRoot   string // agent's view of the node root, typically "/host"
	// pullSecrets are imagePullSecret names set on the pod. The holder
	// runs in the source/workload namespace (e.g. nvcf-backend), where
	// NVCA's admission webhook injects an init container whose image may
	// not be cached on a fresh cluster. Without a pull secret the holder
	// gets stuck in ImagePullBackOff and never reaches Running, so the
	// copy times out. Empty = no secrets (preserves prior behavior).
	pullSecrets []string

	podUID string // populated by Create
	pvName string // populated by WaitRunning
}

// NewMountHolder builds (does not create) a MountHolder.
//   - namespace: where the rwx PVC lives (source pod's namespace)
//   - name: pod name, e.g., "mh-<short-hash>"
//   - nodeName: pin scheduling here (the capturing agent's node)
//   - rwxPVCName, rwxPVCUID: for the volume + OwnerReference
//   - image: container image (reuse the agent image)
//   - hostRoot: agent's host-mount prefix; PV mount path is rooted here
//   - pullSecrets: imagePullSecret names for the holder pod (may be nil)
func NewMountHolder(kube kubernetes.Interface, log *logrus.Entry,
	namespace, name, nodeName, rwxPVCName string, rwxPVCUID types.UID,
	image, hostRoot string, pullSecrets []string) *MountHolder {
	if hostRoot == "" {
		hostRoot = "/host"
	}
	return &MountHolder{
		kube:        kube,
		log:         log,
		namespace:   namespace,
		name:        name,
		nodeName:    nodeName,
		rwxPVCName:  rwxPVCName,
		rwxPVCUID:   rwxPVCUID,
		image:       image,
		hostRoot:    hostRoot,
		pullSecrets: pullSecrets,
	}
}

// Spec returns the corev1.Pod manifest the holder posts. Exposed so
// tests can validate Kyverno-policy compliance without hitting the
// API server.
func (h *MountHolder) Spec() *corev1.Pod {
	nonRoot := true
	roRoot := true
	noEscalate := false
	uid := MountHolderUID
	zero := int64(0)
	controllerTrue := true

	pullSecrets := make([]corev1.LocalObjectReference, 0, len(h.pullSecrets))
	for _, n := range h.pullSecrets {
		if n == "" {
			continue
		}
		pullSecrets = append(pullSecrets, corev1.LocalObjectReference{Name: n})
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      h.name,
			Namespace: h.namespace,
			Labels: map[string]string{
				"nvsnap.io/mount-holder":       h.rwxPVCName,
				"app.kubernetes.io/managed-by": "nvsnap-agent",
				"app.kubernetes.io/component":  "mount-holder",
			},
			// OwnerReference back to the rwx PVC is the crash-safety
			// net: if the agent dies before its inline Delete fires,
			// the eventual rwx PVC GC cascades to this pod.
			// Controller=true so the garbage collector treats this
			// reference as authoritative. BlockOwnerDeletion=false
			// (the default) so PVC delete is not blocked on us.
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "v1",
					Kind:       "PersistentVolumeClaim",
					Name:       h.rwxPVCName,
					UID:        h.rwxPVCUID,
					Controller: &controllerTrue,
				},
			},
		},
		Spec: corev1.PodSpec{
			// Pin to the agent's node so the PV is mounted where the
			// agent can reach it via /host/var/lib/kubelet/. Use
			// nodeSelector (NOT nodeName): with a WaitForFirstConsumer
			// StorageClass (Hyperdisk-ML on GKE) the rwx PVC only binds
			// after the kube-scheduler picks a node for this pod and
			// writes volume.kubernetes.io/selected-node onto the PVC —
			// the PD-CSI external-provisioner watches that annotation to
			// know which zone to provision the disk in. nodeName bypasses
			// the scheduler entirely, so the annotation is never written,
			// the disk is never provisioned, the PVC stays Pending, and
			// the holder hangs in Init forever (v0.0.51 deadlock, caught
			// on GCP-H100-a whisper e2e 2026-06-10). nodeSelector with
			// kubernetes.io/hostname constrains the scheduler to exactly
			// the agent's node while still going through scheduling, so
			// WFC binding completes. This mirrors what the pre-v0.0.51
			// writer Job did correctly.
			NodeSelector: map[string]string{"kubernetes.io/hostname": h.nodeName},
			// Tolerate every taint. The agent's node typically has
			// nvidia.com/gpu:NoSchedule (A3-Mega); the holder doesn't
			// need a GPU but must be allowed to run there anyway.
			Tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
			// imagePullSecrets so the holder (and any webhook-injected
			// init container) can pull from a private registry on a
			// fresh cluster. nil → field stays unset, no secrets.
			ImagePullSecrets: pullSecrets,
			// Holder does no work after sleep — never restart.
			RestartPolicy: corev1.RestartPolicyNever,
			// No graceful shutdown work.
			TerminationGracePeriodSeconds: &zero,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: &nonRoot,
				RunAsUser:    &uid,
				FSGroup:      &uid,
				SeccompProfile: &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeRuntimeDefault,
				},
			},
			Containers: []corev1.Container{{
				Name:    "holder",
				Image:   h.image,
				Command: []string{"sleep", "infinity"},
				SecurityContext: &corev1.SecurityContext{
					RunAsNonRoot:             &nonRoot,
					RunAsUser:                &uid,
					ReadOnlyRootFilesystem:   &roRoot,
					AllowPrivilegeEscalation: &noEscalate,
					Capabilities: &corev1.Capabilities{
						Drop: []corev1.Capability{"ALL"},
					},
					SeccompProfile: &corev1.SeccompProfile{
						Type: corev1.SeccompProfileTypeRuntimeDefault,
					},
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("10m"),
						corev1.ResourceMemory: resource.MustParse("16Mi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("50m"),
						corev1.ResourceMemory: resource.MustParse("32Mi"),
					},
				},
				// Exec probes only read — compatible with the RO
				// rootfs. Kyverno's require-probes policy is satisfied.
				ReadinessProbe: holderProbe(),
				LivenessProbe:  holderProbe(),
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "dest",
					MountPath: MountHolderDestPath,
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "dest",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: h.rwxPVCName,
					},
				},
			}},
		},
	}
}

func holderProbe() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			Exec: &corev1.ExecAction{
				Command: []string{"test", "-d", MountHolderDestPath},
			},
		},
		InitialDelaySeconds: 1,
		PeriodSeconds:       5,
		FailureThreshold:    6,
	}
}

// Create submits the pod and records its UID. Idempotent on
// AlreadyExists: re-fetches the existing pod and reuses its UID,
// which lets the caller retry Create across transient failures.
func (h *MountHolder) Create(ctx context.Context) error {
	pod, err := h.kube.CoreV1().Pods(h.namespace).Create(ctx, h.Spec(), metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		existing, getErr := h.kube.CoreV1().Pods(h.namespace).Get(ctx, h.name, metav1.GetOptions{})
		if getErr != nil {
			return fmt.Errorf("mount-holder %s/%s exists but cannot Get: %w", h.namespace, h.name, getErr)
		}
		h.podUID = string(existing.UID)
		if h.log != nil {
			h.log.WithFields(logrus.Fields{
				"namespace": h.namespace, "name": h.name, "uid": h.podUID,
			}).Info("mount-holder pod already exists; reusing")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("create mount-holder %s/%s: %w", h.namespace, h.name, err)
	}
	h.podUID = string(pod.UID)
	if h.log != nil {
		h.log.WithFields(logrus.Fields{
			"namespace": h.namespace, "name": h.name, "uid": h.podUID,
			"node": h.nodeName, "pvc": h.rwxPVCName,
		}).Info("mount-holder pod created")
	}
	return nil
}

// WaitRunning blocks until the holder pod is Running with all
// containers Ready, AND the bound PV name is known. Times out after
// MountHolderRunningTimeout.
func (h *MountHolder) WaitRunning(ctx context.Context) error {
	ctx, span := tracing.Tracer().Start(ctx, "l2.mount_holder_wait")
	defer span.End()
	if h.podUID == "" {
		return fmt.Errorf("mount-holder.WaitRunning: Create not yet called")
	}
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, MountHolderRunningTimeout, true, func(ctx context.Context) (bool, error) {
		pod, err := h.kube.CoreV1().Pods(h.namespace).Get(ctx, h.name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, fmt.Errorf("mount-holder %s/%s disappeared", h.namespace, h.name)
			}
			return false, nil // transient API error — retry
		}
		if pod.Status.Phase != corev1.PodRunning {
			return false, nil
		}
		for i := range pod.Status.ContainerStatuses {
			if !pod.Status.ContainerStatuses[i].Ready {
				return false, nil
			}
		}
		// Pod is Running + Ready: kubelet has attached + mounted the PV.
		// Resolve the bound PV name from the PVC.
		pvc, err := h.kube.CoreV1().PersistentVolumeClaims(h.namespace).Get(ctx, h.rwxPVCName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		if pvc.Spec.VolumeName == "" {
			return false, nil
		}
		h.pvName = pvc.Spec.VolumeName
		if h.log != nil {
			h.log.WithFields(logrus.Fields{
				"namespace": h.namespace, "name": h.name, "uid": h.podUID,
				"pv": h.pvName,
			}).Info("mount-holder Running; PV mounted")
		}
		return true, nil
	})
}

// PVMountPath returns the agent-visible host path where the rwx PVC
// is mounted on the agent's node. Requires WaitRunning to have
// completed successfully. The path is sanity-checked with Stat;
// EACCES or ENOENT here means kubelet hasn't finished the mount yet
// despite the pod being Ready (rare, but the agent should fail-fast
// rather than silently writing nowhere).
func (h *MountHolder) PVMountPath() (string, error) {
	if h.podUID == "" || h.pvName == "" {
		return "", fmt.Errorf("mount-holder not ready: podUID=%q pvName=%q", h.podUID, h.pvName)
	}
	p := filepath.Join(h.hostRoot,
		"var", "lib", "kubelet", "pods", h.podUID,
		"volumes", "kubernetes.io~csi", h.pvName, "mount")
	st, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("PV mount path %s not accessible: %w", p, err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("PV mount path %s is not a directory", p)
	}
	return p, nil
}

// Delete tears down the holder pod synchronously, then waits for
// actual removal so kubelet's PV unmount has completed before the
// caller proceeds to snapshot. Idempotent: NotFound is not an error.
func (h *MountHolder) Delete(ctx context.Context) error {
	zero := int64(0)
	err := h.kube.CoreV1().Pods(h.namespace).Delete(ctx, h.name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete mount-holder %s/%s: %w", h.namespace, h.name, err)
	}
	if h.log != nil {
		h.log.WithFields(logrus.Fields{
			"namespace": h.namespace, "name": h.name,
		}).Info("mount-holder pod delete requested; waiting for unmount")
	}
	return wait.PollUntilContextTimeout(ctx, 1*time.Second, MountHolderDeleteTimeout, true, func(ctx context.Context) (bool, error) {
		_, err := h.kube.CoreV1().Pods(h.namespace).Get(ctx, h.name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, nil
	})
}

// PodUID returns the holder pod's UID, populated by Create. Useful
// for tests and for callers that need to construct the PV mount path
// themselves.
func (h *MountHolder) PodUID() string { return h.podUID }

// PVName returns the bound PV name, populated by WaitRunning.
func (h *MountHolder) PVName() string { return h.pvName }
