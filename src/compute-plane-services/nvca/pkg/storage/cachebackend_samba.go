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

package storage

import (
	"context"
	"fmt"
	"hash/fnv"
	"strconv"

	"github.com/google/uuid"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// Samba-on-NVMesh model cache backend (HelmCacheBackendSamba).
//
// Selected when CachingSupport is on, neither nvcf-sc-30 (NVMesh 3.x) nor an
// operator-provided nvcf-miniservice-sc exists, and HelmSharedStorage is enabled.
//
// Each cacheHandle gets its OWN Samba server (Deployment + Service) backed by
// its OWN nvcf-sc (NVMesh) data PVC, sized to the function's cacheSize, and
// exporting a single SMB share. There is no shared/global data PVC: a single
// fixed-size volume cannot be sized for an unknown set of models, and hot-adding
// per-handle volumes to one shared server would force pod restarts that drop
// every other handle's live mounts. Per-handle servers isolate lifecycle and
// sizing and can be garbage-collected independently when a handle goes idle.
//
// The per-handle backing PVC (samba-<cacheHandle>) is the durable reuse marker:
// if it already exists the cache is reused instead of re-provisioned, and a
// cachePopulatedLabelKey label on it (stamped by the writer on success) signals
// that readers may safely attach. NVCA does NOT create nvcf-miniservice-sc: that
// StorageClass is exclusively the operator's signal that 3rd-party shared
// storage is present (backend 2). Writer/reader volumes are static SMB PVs bound
// to the per-handle Samba share, with no StorageClass of their own.
const (
	//nolint:gosec
	SambaModelCacheReadWriteSecretName = "nvcf-helm-cache-smb-rw-creds"
	//nolint:gosec
	SambaModelCacheReadOnlySecretName = "nvcf-helm-cache-smb-ro-creds"
	// SambaModelCacheShareName is the SMB share each per-handle server exports.
	SambaModelCacheShareName = "cache"
	// SambaModelCacheDataStorageClassName is the NVMesh block-storage class
	// backing each per-handle Samba data PVC (Samba over NVMesh; never ephemeral).
	SambaModelCacheDataStorageClassName = "nvcf-sc"

	sambaModelCacheDataMountPath = "/shared-data"
	sambaModelCacheServerPort    = 445
	sambaModelCacheMetricsPort   = 9922

	// SMB user ids for the Samba model cache servers. The writer mounts RW; the
	// readers mount RO (a distinct unprivileged user) so a compromised workload
	// cannot write to the shared cache. Credentials are shared across per-handle
	// servers (each share is isolated by its own server + PVC).
	sambaModelCacheRWUsername = "nvcf-helmcache-rw"
	sambaModelCacheROUsername = "nvcf-helmcache-ro"
	sambaModelCacheRWUID      = uint16(1000)
	sambaModelCacheROUID      = uint16(1001)
	sambaModelCacheGID        = uint16(1000)
	sambaModelCacheRWDirMode  = "0775"
	sambaModelCacheRWFileMode = "0664"
	sambaModelCacheRODirMode  = "0555"
	sambaModelCacheROFileMode = "0444"

	// sambaModelCacheComponentLabelKey marks the per-handle Samba backing PVC so
	// idle GC (cleanupIdleModelCaches) can list candidate caches. The cacheHandle
	// is carried as an ANNOTATION (sambaModelCacheHandleAnnotationKey), not a
	// label, so the backing PVC is not matched by cleanupInitModelCache, which
	// deletes the (handle-labeled) ephemeral writer PVC.
	sambaModelCacheComponentLabelKey   = fqdnPrefix + "/modelcache-samba-backing"
	sambaModelCacheComponentLabelValue = "true"
	sambaModelCacheHandleAnnotationKey = fqdnPrefix + "/modelcache-samba-handle"
)

// defaultSambaModelCacheDataSize backs the per-handle data PVC only when the
// request carries no cacheSize. nvcf-sc supports expansion, so this is a
// starting point, not a ceiling.
var defaultSambaModelCacheDataSize = resource.MustParse("100Gi")

// sambaModelCacheResourceName derives the per-cacheHandle name shared by the
// Samba server Deployment, its Service, its backing data PVC, and its
// NetworkPolicy. The cacheHandle is a content hash and is normally DNS-safe;
// the 63-char limit is guarded defensively with a stable hashed suffix.
func sambaModelCacheResourceName(cacheHandle string) string {
	name := "samba-" + cacheHandle
	if len(name) <= 63 {
		return name
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(cacheHandle))
	return fmt.Sprintf("samba-%s-%08x", cacheHandle[:48], h.Sum32())
}

// SambaModelCacheBackingPVCName is the per-handle nvcf-sc data PVC that holds
// the cache bytes behind the Samba share. Its existence is the reuse signal.
func SambaModelCacheBackingPVCName(cacheHandle string) string {
	return sambaModelCacheResourceName(cacheHandle)
}

// sambaModelCacheWriterPVName is the static SMB plumbing PV the writer's RW PVC
// binds to for a handle.
func sambaModelCacheWriterPVName(cacheHandle string) string {
	return "samba-rw-pv-" + cacheHandle
}

// EnsureSambaModelCacheInfra performs the idempotent bootstrap of the per-handle
// Samba model cache server in the model cache control namespace: the shared
// RW/RO credentials Secrets, the per-handle nvcf-sc data PVC (sized to size),
// the per-handle Samba Deployment, its fronting Service, and an ingress
// NetworkPolicy. It returns ready=true once that Deployment is available. It
// intentionally creates NO StorageClass; cache volumes are static SMB PVs bound
// to the per-handle share (see newSambaModelCachePV).
func EnsureSambaModelCacheInfra(
	ctx context.Context,
	c client.Client,
	cacheHandle string,
	image string,
	resources corev1.ResourceRequirements,
	size resource.Quantity,
) (ready bool, err error) {
	log := logf.FromContext(ctx).WithValues("backend", HelmCacheBackendSamba,
		"namespace", ModelCacheInitNamespace, "cacheHandle", cacheHandle)

	if image == "" {
		return false, fmt.Errorf("samba model cache server image is not configured")
	}
	if cacheHandle == "" {
		return false, fmt.Errorf("samba model cache cacheHandle is not set")
	}
	if size.IsZero() {
		size = defaultSambaModelCacheDataSize
	}

	rwSecret, roSecret := newSambaModelCacheSecrets()
	objs := []client.Object{
		NewModelCacheInitNamespace(),
		rwSecret,
		roSecret,
		newSambaModelCacheDataPVC(cacheHandle, size),
		newSambaModelCacheDeployment(cacheHandle, image, resources),
		newSambaModelCacheService(cacheHandle),
		newSambaModelCacheNetworkPolicy(cacheHandle),
	}
	for _, obj := range objs {
		if err := ensureCreated(ctx, c, obj); err != nil {
			return false, fmt.Errorf("ensure samba model cache %T: %w", obj, err)
		}
	}

	dep := &appsv1.Deployment{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: sambaModelCacheResourceName(cacheHandle)}, dep); err != nil {
		return false, fmt.Errorf("get samba deployment: %w", err)
	}
	if dep.Status.AvailableReplicas < 1 {
		log.V(1).Info("Samba model cache server not yet available, waiting")
		return false, nil
	}
	log.V(1).Info("Samba model cache server ready")
	return true, nil
}

// ensureCreated creates obj, treating AlreadyExists as success so the bootstrap
// is idempotent across reconciles.
func ensureCreated(ctx context.Context, c client.Client, obj client.Object) error {
	if err := c.Create(ctx, obj); err != nil && !k8serrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// sambaModelCacheSelector is the unique pod selector for a handle's Samba server.
func sambaModelCacheSelector(cacheHandle string) map[string]string {
	return map[string]string{"app.kubernetes.io/name": sambaModelCacheResourceName(cacheHandle)}
}

// sambaModelCacheServiceDNS is the stable in-cluster DNS name of a handle's
// Samba Service, used as the host in the static PV SMB source. The name stops
// at .svc (no cluster domain suffix) because the cluster domain is
// configurable and .svc resolves in any cluster.
func sambaModelCacheServiceDNS(cacheHandle string) string {
	return fmt.Sprintf("%s.%s.svc", sambaModelCacheResourceName(cacheHandle), ModelCacheInitNamespace)
}

// sambaModelCacheShareRoot is the SMB source for a handle's share. Each handle
// has its own server and backing PVC, so the share root IS the cache root; no
// per-handle subdirectory is needed.
func sambaModelCacheShareRoot(cacheHandle string) string {
	return fmt.Sprintf("//%s/%s", sambaModelCacheServiceDNS(cacheHandle), SambaModelCacheShareName)
}

func newSambaModelCacheSecrets() (rw, ro *corev1.Secret) {
	mk := func(name, user string, uid uint16) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ModelCacheInitNamespace},
			Data: map[string][]byte{
				"username": []byte(user),
				"password": []byte(uuid.New().String()),
				"uid":      []byte(strconv.Itoa(int(uid))),
				"gid":      []byte(strconv.Itoa(int(sambaModelCacheGID))),
			},
		}
	}
	return mk(SambaModelCacheReadWriteSecretName, sambaModelCacheRWUsername, sambaModelCacheRWUID),
		mk(SambaModelCacheReadOnlySecretName, sambaModelCacheROUsername, sambaModelCacheROUID)
}

// newSambaModelCacheDataPVC builds the per-handle nvcf-sc data PVC. The PVC is
// not labeled with modelCacheHandleLabelKey on purpose: cleanupInitModelCache
// deletes the (handle-labeled) ephemeral writer RW PVC, and labeling the durable
// backing PVC with the same handle would collide with that single-PVC cleanup.
func newSambaModelCacheDataPVC(cacheHandle string, size resource.Quantity) *corev1.PersistentVolumeClaim {
	storageClassName := SambaModelCacheDataStorageClassName
	labels := sambaModelCacheSelector(cacheHandle)
	labels[sambaModelCacheComponentLabelKey] = sambaModelCacheComponentLabelValue
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        SambaModelCacheBackingPVCName(cacheHandle),
			Namespace:   ModelCacheInitNamespace,
			Labels:      labels,
			Annotations: map[string]string{sambaModelCacheHandleAnnotationKey: cacheHandle},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &storageClassName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: size},
			},
		},
	}
}

// DeleteSambaModelCacheInfra removes the per-handle Samba server (Deployment,
// Service, NetworkPolicy) and its backing data PVC. The Deployment is deleted
// first so the server pod releases the PVC before it is removed. Shared
// credentials Secrets are left in place for other handles. Not-found is success.
//
// It also sweeps the handle's static SMB PVs (the writer plumbing PV and any
// reader PVs whose namespace was deleted without a StorageRequest cleanup).
// Static PVs have no provisioner, so a Released one is never removed by the PV
// controller and would leak forever.
func DeleteSambaModelCacheInfra(ctx context.Context, c client.Client, cacheHandle string) error {
	name := sambaModelCacheResourceName(cacheHandle)
	objs := []client.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ModelCacheInitNamespace}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ModelCacheInitNamespace}},
		&netv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ModelCacheInitNamespace}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ModelCacheInitNamespace}},
	}
	for _, obj := range objs {
		if err := c.Delete(ctx, obj); err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("delete samba model cache %T %s: %w", obj, name, err)
		}
	}

	pvs := &corev1.PersistentVolumeList{}
	if err := c.List(ctx, pvs, client.MatchingLabels{modelCacheHandleLabelKey: cacheHandle}); err != nil {
		return fmt.Errorf("list samba model cache PVs for %s: %w", cacheHandle, err)
	}
	for i := range pvs.Items {
		pv := &pvs.Items[i]
		// Only this backend's static SMB PVs; never NVMesh primary/secondary PVs
		// that share the handle label.
		if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != SMBCSIDriverName {
			continue
		}
		if err := c.Delete(ctx, pv); err != nil && !k8serrors.IsNotFound(err) {
			return fmt.Errorf("delete samba model cache PV %s: %w", pv.Name, err)
		}
	}
	return nil
}

func newSambaModelCacheDeployment(cacheHandle, image string, resources corev1.ResourceRequirements) *appsv1.Deployment {
	replicas := int32(1)
	secretEnv := func(name, secret, key string) corev1.EnvVar {
		return corev1.EnvVar{
			Name: name,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					Key:                  key,
					LocalObjectReference: corev1.LocalObjectReference{Name: secret},
				},
			},
		}
	}

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:  SMBServerContainerName,
				Image: image,
				// dperson/samba-style args (matching newSMBPod): an RW user and a
				// distinct RO user, both exporting the "cache" share rooted on the
				// per-handle data PVC.
				Args: []string{
					"-w", "NVIDIA",
					"-u", "$(USERNAME);$(PASSWORD);$(UID);smb;$(GID)",
					"-u", "$(ROUSERNAME);$(ROPASSWORD);$(ROUID);smb;$(ROGID)",
					"-p", "-g", "log level = 3",
					"-G", "share;log level = 3",
					"-s", fmt.Sprintf("%[1]s;%[2]s/%[1]s;yes;no;no;$(USERNAME),$(ROUSERNAME);;$(USERNAME);none",
						SambaModelCacheShareName, sambaModelCacheDataMountPath),
				},
				Env: []corev1.EnvVar{
					{Name: "GROUPID", Value: strconv.Itoa(int(sambaModelCacheGID))},
					secretEnv("USERNAME", SambaModelCacheReadWriteSecretName, "username"),
					secretEnv("PASSWORD", SambaModelCacheReadWriteSecretName, "password"),
					secretEnv("UID", SambaModelCacheReadWriteSecretName, "uid"),
					secretEnv("GID", SambaModelCacheReadWriteSecretName, "gid"),
					secretEnv("ROUSERNAME", SambaModelCacheReadOnlySecretName, "username"),
					secretEnv("ROPASSWORD", SambaModelCacheReadOnlySecretName, "password"),
					secretEnv("ROUID", SambaModelCacheReadOnlySecretName, "uid"),
					secretEnv("ROGID", SambaModelCacheReadOnlySecretName, "gid"),
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "data-volume", MountPath: sambaModelCacheDataMountPath},
				},
				Ports: []corev1.ContainerPort{
					{ContainerPort: sambaModelCacheServerPort},
					{Name: "metrics", ContainerPort: sambaModelCacheMetricsPort},
				},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(sambaModelCacheServerPort)},
					},
					InitialDelaySeconds: 3,
				},
				LivenessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(sambaModelCacheServerPort)},
					},
					InitialDelaySeconds: 30,
				},
				Resources: *resources.DeepCopy(),
			},
		},
		Volumes: []corev1.Volume{
			{
				Name: "data-volume",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: SambaModelCacheBackingPVCName(cacheHandle),
					},
				},
			},
		},
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sambaModelCacheResourceName(cacheHandle),
			Namespace: ModelCacheInitNamespace,
			Labels:    sambaModelCacheSelector(cacheHandle),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			// The server owns a single RWO backing PVC, so a rolling update
			// would start a second pod against the same volume and block on
			// multi-attach (or serve the share twice). Recreate stops the old
			// pod before starting the new one.
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: sambaModelCacheSelector(cacheHandle)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: sambaModelCacheSelector(cacheHandle)},
				Spec:       podSpec,
			},
		},
	}
}

func newSambaModelCacheService(cacheHandle string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sambaModelCacheResourceName(cacheHandle),
			Namespace: ModelCacheInitNamespace,
			Labels:    sambaModelCacheSelector(cacheHandle),
		},
		Spec: corev1.ServiceSpec{
			Selector: sambaModelCacheSelector(cacheHandle),
			Ports: []corev1.ServicePort{
				{
					Name:       "smb",
					Port:       sambaModelCacheServerPort,
					TargetPort: intstr.FromInt(sambaModelCacheServerPort),
				},
			},
		},
	}
}

// newSambaModelCacheNetworkPolicy restricts inbound traffic to a handle's Samba
// server to the SMB port. Mirrors the SharedStorage SMB server ingress policy
// (allow-ingress-sharedstorage), which is likewise port-only with no "from"
// clause: SMB mounts are performed by the kubelet/CSI node plugin from the
// node's HOST network namespace, so a pod-selector or namespace-selector peer
// would not match the real traffic source and would break every mount. Access
// control is enforced by the SMB credentials (distinct RW and RO users).
func newSambaModelCacheNetworkPolicy(cacheHandle string) *netv1.NetworkPolicy {
	smbPort := intstr.FromInt(sambaModelCacheServerPort)
	tcp := corev1.ProtocolTCP
	return &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sambaModelCacheResourceName(cacheHandle),
			Namespace: ModelCacheInitNamespace,
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: sambaModelCacheSelector(cacheHandle)},
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress},
			Ingress: []netv1.NetworkPolicyIngressRule{
				{Ports: []netv1.NetworkPolicyPort{{Port: &smbPort, Protocol: &tcp}}},
			},
		},
	}
}

// newSambaModelCachePV builds a static SMB CSI PersistentVolume bound (by
// ClaimRef) to the named PVC, pointing at the per-handle Samba share root. The
// writer (readOnly=false) mounts RW; readers (readOnly=true) mount RO as a
// distinct unprivileged user. The CSI secret refs point at the control-namespace
// creds Secrets, so no secret copies are needed in workload namespaces.
func newSambaModelCachePV(pvName, pvcName, pvcNamespace, cacheHandle string, readOnly bool, capacity resource.Quantity) *corev1.PersistentVolume {
	uid, gid := sambaModelCacheRWUID, sambaModelCacheGID
	dirMode, fileMode := sambaModelCacheRWDirMode, sambaModelCacheRWFileMode
	secret := SambaModelCacheReadWriteSecretName
	accessMode := corev1.ReadWriteMany
	if readOnly {
		uid, dirMode, fileMode = sambaModelCacheROUID, sambaModelCacheRODirMode, sambaModelCacheROFileMode
		secret = SambaModelCacheReadOnlySecretName
		accessMode = corev1.ReadOnlyMany
	}
	source := sambaModelCacheShareRoot(cacheHandle)
	secretRef := &corev1.SecretReference{Name: secret, Namespace: ModelCacheInitNamespace}
	emptySC := ""
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:   pvName,
			Labels: map[string]string{modelCacheHandleLabelKey: cacheHandle},
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: capacity},
			AccessModes:                   []corev1.PersistentVolumeAccessMode{accessMode},
			StorageClassName:              emptySC,
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
			ClaimRef: &corev1.ObjectReference{
				APIVersion: "v1",
				Kind:       "PersistentVolumeClaim",
				Name:       pvcName,
				Namespace:  pvcNamespace,
			},
			MountOptions: []string{
				fmt.Sprintf("dir_mode=%s", dirMode),
				fmt.Sprintf("file_mode=%s", fileMode),
				fmt.Sprintf("uid=%d", uid),
				fmt.Sprintf("gid=%d", gid),
				"noperm", "mfsymlinks", "cache=strict", "noserverino", "vers=3.1.1",
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver: SMBCSIDriverName,
					// Unique per PV: the static SMB CSI driver dedups by VolumeHandle,
					// so every writer/reader PV for the share must carry a distinct one.
					VolumeHandle:         fmt.Sprintf("%s#%s", source, pvName),
					NodeStageSecretRef:   secretRef,
					NodePublishSecretRef: secretRef,
					VolumeAttributes:     map[string]string{"source": source},
				},
			},
		},
	}
}

// newSambaModelCachePVC builds a static PVC bound (by volumeName) to the given
// Samba model cache PV. storageClassName is empty so no dynamic provisioning or
// default-class injection occurs.
func newSambaModelCachePVC(pvcName, pvcNamespace, pvName string, readOnly bool, capacity resource.Quantity) *corev1.PersistentVolumeClaim {
	accessMode := corev1.ReadWriteMany
	if readOnly {
		accessMode = corev1.ReadOnlyMany
	}
	emptySC := ""
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: pvcName, Namespace: pvcNamespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{accessMode},
			StorageClassName: &emptySC,
			VolumeName:       pvName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: capacity},
			},
		},
	}
}
