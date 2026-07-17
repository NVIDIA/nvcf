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
	"maps"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	nvcaenvtest "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/envtest"
	modelcachetypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/modelcachetypes"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestReconcile_ModelCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg, _, cleanup, err := nvcaenvtest.SetupEnvtest()
	require.NoError(t, err)
	t.Cleanup(cleanup)

	mgr, err := ctrl.NewManager(cfg, manager.Options{
		Scheme:                  mgrScheme,
		GracefulShutdownTimeout: new(time.Duration),
		BaseContext:             func() context.Context { return ctx },
		WebhookServer:           nvcaenvtest.NewFakeWebhookServer(),
		Metrics:                 nvcaenvtest.NewFakeMetricsOptions(),
	})
	require.NoError(t, err)

	defaultTimeConfig := (&k8sutil.TimeConfig{}).Complete()
	nvcaCfg := nvcaconfig.Config{}
	err = BuildController(nvcaCfg, nvcav1new.ModelCacheRequest, mgr, "my-cluster", "us-west-1", defaultTimeConfig, ControllerOptions{})
	require.NoError(t, err)

	mgrErrCh, err := nvcaenvtest.StartManager(ctx, mgr)
	require.NoError(t, err)

	cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
	mgr.GetCache().WaitForCacheSync(cctx)
	ccancel()

	c := mgr.GetClient()

	srNamespace := &corev1.Namespace{}
	srNamespace.Name = types.DefaultICMSRequestNamespace
	err = c.Create(ctx, srNamespace)
	require.NoError(t, err)
	err = c.Create(ctx, NewModelCacheInitNamespace())
	require.NoError(t, err)

	cacheHandle := "abc123handle"
	srSpec := nvcav2beta1.ICMSRequestSpec{
		FunctionDetails: function.Details{
			FunctionID:        "funcid-1",
			FunctionVersionID: "funcverid-1",
			FunctionType:      "DEFAULT",
		},
		Action:         common.FunctionCreationAction,
		NCAId:          "ncaid-1",
		RequestID:      "reqid1",
		MessageBatchID: "mbatchid1",
		CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
			CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
				Action:            common.FunctionCreationAction,
				RequestID:         "reqid1",
				MessageBatchID:    "mbatchid1",
				InstanceType:      "ON-PREM.GPU.L40",
				InstanceTypeName:  "ON-PREM.GPU.L40_1x",
				InstanceTypeValue: "ON-PREM.GPU.L40",
				GPUType:           "L40",
				RequestedGPUCount: 1,
				InstanceCount:     1,
				NCAID:             "ncaid-1",
			},
			FunctionLaunchSpecification: &function.LaunchSpecification{
				CloudProvider:   "DGXCLOUD",
				ICMSEnvironment: "prod",
				GPUName:         "L40",
				EnvironmentB64:  "QVRUQUNIRURfR1BVX0NPVU5UPSIxIgpCWU9PX09URUxfQ09MTEVDVE9SX0NPTlRBSU5FUj1udmNyLmlvL3F0ZnB0MWgwYmlldS9udmNmLWNvcmUvYnlvby1vdGVsLWNvbGxlY3RvcjoxLjIuMwpDTE9VRF9QUk9WSURFUj1PTi1QUkVNCkVTU19BR0VOVF9DT05UQUlORVI9bnZjci5pby9udi1jZi9udmNmLWNvcmUvZXNzLWFnZW50OjEuMC4wCkZVTkNUSU9OX0lEPTVhM2Q0YTdlLTllZTMtNDc2Mi04ZDM3LWQzYjQwYTZmODRjNgpGVU5DVElPTl9OQU1FPW15LWZ1bmMKRlVOQ1RJT05fVkVSU0lPTl9JRD0yYzk0OGQ5Yi1kYjVkLTRmOTMtOGMyOS1mNWQ4YTVkODljYjkKR1BVX05BTUU9TDQwCkhFTE1fQ0hBUlRfSU5GRVJFTkNFX1NFUlZJQ0VfTkFNRT1teXNlcnZpY2UKQ09OVEFJTkVSX1JFR0lTVFJJRVNfQ1JFREVOVElBTFM9ZXlKck9ITlRaV055WlhSeklqcGJleUpoZFhSb2N5STZleUp1ZG1OeUxtbHZJanA3SW1GMWRHZ2lPaUpqTTFKdVRGZEdhVmw2UlhsTmVtOHlXbXBaZWs1SFVUUk9VekExVFhwR2JFeFVVWGhhYWxsMFdWUktiRmw1TURKT01razBUbnBWZDFwcVJYbE9NbFU5SW4xOWZWMTkKSU5GRVJFTkNFX0NPTlRBSU5FUl9FTlY9VzNzaWEyVjVJam9pU1U1R1JWSkZUa05GWDBWT1ZsOUxSVmtpTENKMllXeDFaU0k2SW1sdVptVnlaVzVqWlY5MllXeDFaU0o5WFE9PQpJTkZFUkVOQ0VfSEVBTFRIX0VORFBPSU5UPS92Mi9oZWFsdGgvcmVhZHkKSU5GRVJFTkNFX0hFQUxUSF9FWFBFQ1RFRF9SRVNQT05TRV9DT0RFPSIyMDAiCklORkVSRU5DRV9IRUFMVEhfUE9SVD0iNTAwNTEiCklORkVSRU5DRV9QT1JUPSI1MDA1MSIKSU5GRVJFTkNFX1BST1RPQ09MPUdSUEMKSU5GRVJFTkNFX1VSTD0vZ3JwYwpJTklUX0NPTlRBSU5FUj1udmNyLmlvL3F0ZnB0MWgwYmlldS9udmNmLWNvcmUvbnZjZl93b3JrZXJfaW5pdDowLjI0LjEwCk1BWF9SRVFVRVNUX0NPTkNVUlJFTkNZPSIxIgpOQ0FfSUQ9X2xJTFhCLTFOZk5tQm5RU2tfc3BxVldPdENBWFFtNTBVRU13ajNUUmd5bUpKMkF5dXdjZ3hxCk5WQ0ZfRlFETj1odHRwczovL3VzLXdlc3QtMi5hcGkubnZjZi5udmlkaWEuY29tCk5WQ0ZfRlFETl9HUlBDPWh0dHBzOi8vZ3JwYy5hcGkubnZjZi5udmlkaWEuY29tCk5WQ0ZfRlFETl9OQVRTPXRsczovL3VzLXdlc3QtMi5hd3MuY2xvdWQubmF0cy5udmNmLm52aWRpYS5jb206NDIyMgpOVkNGX1dPUktFUl9UT0tFTj10b2sKT1RFTF9DT05UQUlORVI9bnZjci5pby9xdGZwdDFoMGJpZXUvbnZjZi1jb3JlL29wZW50ZWxlbWV0cnktY29sbGVjdG9yOjAuNzQuMApPVEVMX0VYUE9SVEVSX09UTFBfRU5EUE9JTlQ9aHR0cHM6Ly9wcm9kLm90ZWwua2FpemVuLm52aWRpYS5jb206ODI4MgpTRUNSRVRTX0FTU0VSVElPTl9UT0tFTj1leUpoYkdjaU9pSlNVekkxTmlJc0luUjVjQ0k2SWtwWFZDSjkuZXlKemRXSWlPaUl4TWpNME5UWTNPRGt3SWl3aWJtRnRaU0k2SWtwdmFHNGdSRzlsSWl3aVlYTnpaWEowYVc5dUlqcDdJbk5sWTNKbGRGQmhkR2h6SWpwYkltRmpZMjkxYm5SekwxOXNTVXhZUWkweFRtWk9iVUp1VVZOclgzTndjVlpYVDNSRFFWaFJiVFV3VlVWTmQyb3pWRkpuZVcxS1NqSkJlWFYzWTJkNGNTOTBaV3hsYldWMGNua3ZObVpoT0RNMk5tVXROMkpoTWkwME1UUXpMV0UzTkRRdE56VXhOV1poWmpsaVpqY3lJaXdpWVdOamIzVnVkSE12WDJ4SlRGaENMVEZPWms1dFFtNVJVMnRmYzNCeFZsZFBkRU5CV0ZGdE5UQlZSVTEzYWpOVVVtZDViVXBLTWtGNWRYZGpaM2h4TDNSbGJHVnRaWFJ5ZVM4MlptRTRNelkyWlMwM1ltRXlMVFF4TkRNdFlUYzBOQzAzTlRFMVptRm1PV0ptT0RFaVhYMHNJbUZrYldsdUlqcDBjblZsTENKcFlYUWlPakUxTVRZeU16a3dNako5LlNwUVliRmUxbmZyUTVLc2hSbHk5U1VDMjZXX2oycFFoNkRNaW5zYnJzUUh2S2cxc2Uyb0gzVnpvaW5iTWJRel81TFhjZy1YTmt4NGNOSk4yQWp1d1VJems2RElVTElDSGVxdWpxLXhBYWdGUjhfejI1bzExZDAxekJTNU54RjlBQ2d0SWw2OWRoVEhrOHNLMmVRYjRBRkdDRmZmNjFqMGtYYWJJWUVTR0p4ZHY5UmtOZld0WVotRm1JYzl1RjRqWTU5elIxRUJkWGlsY2NjUjBSaUN2S0FsVFlvckU3VGotMDRLZ1RGbnZRYm1QMFRRR1FkNnhicWRBYVBSQnBYeUJHMDRxbUEyOTZUZnJBT1ZfMDJhSWR0akhhNVNqbXZ0UEFiVmVIVlY1QnhfWmQ4eVZteU4wZTdxZWduQU9xYzVOUDNrRjM4VzRuV2hURThWa05UWmpUUEEKU0lERUNBUl9SRUdJU1RSWV9DUkVERU5USUFMPWV5SmhkWFJvY3lJNmV5SnVkbU55TG1sdklqcDdJbUYxZEdnaU9pSktSemxvWkZoU2IyUkhPWEphVnpRMlltNWFhR05IYTNSak0xSnVURmRHYVZsNlJYbE5kejA5SW4xOWZRbz0KVFJBQ0lOR19BQ0NFU1NfVE9LRU49dHJhY2UtdG9rLTEKVVRJTFNfQ09OVEFJTkVSPW52Y3IuaW8vcXRmcHQxaDBiaWV1L252Y2YtY29yZS9udmNmX3dvcmtlcl91dGlsczoyLjIxLjQKSEVMTV9SRUdJU1RSSUVTX0NSRURFTlRJQUxTPWV5SnJPSE5UWldOeVpYUnpJanBiZXlKaGRYUm9jeUk2ZXlKb1pXeHRMbTVuWXk1dWRtbGthV0V1WTI5dElqcDdJbUYxZEdnaU9pSmpNMUp1VEZkR2FWbDZSWGxOZW04eVdtcFplazVIVVRST1V6QTFUWHBHYkV4VVVYaGFhbGwwV1ZSS2JGbDVNREpPTWtrMFRucFZkMXBxUlhsT01sVTlJbjE5ZlYxOQo=",
				HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{
					HelmChartURL: "https://helm.ngc.nvidia.com/myorg/myteam/charts/image-segmentation-1.0.3.tgz",
					Values:       []byte(`{"foo":{"bar":"baz"}}`),
				},
				CacheLaunchSpecification: &common.CacheLaunchSpecification{
					CacheArtifacts: true,
					CacheHandle:    cacheHandle,
					CacheSize:      262144000,
				},
			},
		},
	}

	sts := []*nvcav1new.StorageRequest{}
	for i := range 3 {
		namespace := &corev1.Namespace{}
		namespace.Name = fmt.Sprintf("sr-%d", i)
		err = c.Create(ctx, namespace)
		require.NoError(t, err)
		sr := &nvcav2beta1.ICMSRequest{}
		sr.Name, sr.Namespace = namespace.Name, srNamespace.Name
		sr.Spec = srSpec
		err = c.Create(ctx, sr)
		require.NoError(t, err)
		st := &nvcav1new.StorageRequest{}
		st.Name, st.Namespace = nvcav1new.ModelCacheRequest.Name(), namespace.Name
		st.Spec.Type = nvcav1new.ModelCacheRequest
		st.Spec.ICMSRequestName = sr.Name
		st.Spec.ICMSRequestNamespace = srNamespace.Name
		st.Spec.ModelCache = &nvcav1new.ModelCacheSpec{
			CacheHandle: cacheHandle,
			Encryption:  &nvcav1new.ModelCacheEncryption{Required: true},
		}
		err = c.Create(ctx, st)
		require.NoError(t, err)
		sts = append(sts, st)
	}
	primaryST := sts[0]

	// Test fan-out on all conditions.
	for _, st := range sts {
		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			err = c.Get(ctx, client.ObjectKeyFromObject(st), st)
			if assert.NoError(ct, err) {
				assert.Equal(ct, nvcav1new.StoragePending, st.Status.Phase)
			}
		}, 2*time.Second, 50*time.Millisecond)
	}

	// Ensure init artifacts exist.
	gotSecret := &corev1.Secret{}
	err = c.Get(ctx, client.ObjectKey{Namespace: ModelCacheInitNamespace, Name: "scsec-d5f545ee492260223239e813ad6a5795"}, gotSecret)
	require.NoError(t, err)
	gotSecret.TypeMeta = metav1.TypeMeta{}
	assert.Contains(t, gotSecret.Data, "dmcryptKey")

	gotSCName := "sc-d5f545ee492260223239e813ad6a5795"
	gotSC := &storagev1.StorageClass{}
	err = c.Get(ctx, client.ObjectKey{Name: "sc-d5f545ee492260223239e813ad6a5795"}, gotSC)
	require.NoError(t, err)
	gotSC.ObjectMeta = metav1.ObjectMeta{}
	gotSC.TypeMeta = metav1.TypeMeta{}
	vbm := storagev1.VolumeBindingImmediate
	reclaimPolicy := corev1.PersistentVolumeReclaimRetain
	assert.Equal(t, &storagev1.StorageClass{
		Provisioner:          NVMeshStorageClassProvisioner,
		AllowVolumeExpansion: newBool(true),
		VolumeBindingMode:    &vbm,
		ReclaimPolicy:        &reclaimPolicy,
		Parameters: map[string]string{
			NVMeshStorageClassVPG:       NVMeshStorageClassVPGType,
			NVMeshStorageClassCSIFS:     NVMeshStorageClassFS,
			NVMeshStorageClassCSISecret: "scsec-d5f545ee492260223239e813ad6a5795",
			NVMeshStorageClassCSINS:     ModelCacheInitNamespace,
		},
	}, gotSC)

	rwPVC := &corev1.PersistentVolumeClaim{}
	err = c.Get(ctx, client.ObjectKey{Name: "rw-pvc-" + cacheHandle, Namespace: ModelCacheInitNamespace}, rwPVC)
	require.NoError(t, err)
	if assert.NotNil(t, rwPVC.Spec.StorageClassName) {
		assert.Equal(t, gotSCName, *rwPVC.Spec.StorageClassName)
	}

	initJob := &batchv1.Job{}
	err = c.Get(ctx, client.ObjectKey{Name: "writer-job-" + cacheHandle, Namespace: ModelCacheInitNamespace}, initJob)
	require.NoError(t, err)

	// Create the job pod and set to running, ensure job is marked started.
	initJobPod := &corev1.Pod{}
	initJobPod.Name, initJobPod.Namespace = initJob.Spec.Template.Name+"-foobar", initJob.Namespace
	initJobPod.Labels = make(map[string]string, len(initJob.Spec.Template.Labels))
	maps.Copy(initJobPod.Labels, initJob.Spec.Template.Labels)
	maps.Copy(initJobPod.Labels, initJob.Spec.Selector.MatchLabels)
	initJobPod.Annotations = initJob.Spec.Template.Annotations
	initJobPod.Spec = initJob.Spec.Template.Spec
	err = c.Create(ctx, initJobPod)
	require.NoError(t, err)
	initJobPod.Status = corev1.PodStatus{Phase: corev1.PodRunning}
	err = c.Status().Update(ctx, initJobPod)
	require.NoError(t, err)

	initJob.Status.StartTime = &metav1.Time{Time: time.Now().Add(-1 * 1 * time.Minute)}
	err = c.Status().Update(ctx, initJob)
	require.NoError(t, err)

	// Ensure primart request is marked init running.
	// Dependents eventually will be on object updates.
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err = c.Get(ctx, client.ObjectKeyFromObject(primaryST), primaryST)
		if assert.NoError(ct, err) {
			assert.Equal(ct, nvcav1new.StorageInitRunning, primaryST.Status.Phase,
				fmt.Sprintf("storage request %s/%s", primaryST.Name, primaryST.Namespace))
		}
	}, 5*time.Second, 50*time.Millisecond)

	primaryPV := &corev1.PersistentVolume{}
	primaryPV.Name = "primary-randomsuffix"
	primaryPV.Spec.ClaimRef = &corev1.ObjectReference{
		APIVersion: "v1",
		Kind:       "PersistentVolumeClaim",
		Namespace:  rwPVC.Namespace,
		Name:       rwPVC.Name,
		UID:        rwPVC.UID,
	}
	volumeHandlePrefix := "single-zone-cluster:csi-5326ce57-8cae-456c:ef7bc990-47e7-11f0-91b6-c952fffeea08:"
	primaryPV.Spec.CSI = &corev1.CSIPersistentVolumeSource{
		Driver:       "nvmesh",
		VolumeHandle: volumeHandlePrefix + ModelCacheInitNamespace,
	}
	primaryPV.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	primaryPV.Spec.Capacity = corev1.ResourceList{"storage": resource.MustParse("1Gi")}
	err = c.Create(ctx, primaryPV)
	require.NoError(t, err)

	rwPVC.Spec.VolumeName = primaryPV.Name
	err = c.Update(ctx, rwPVC)
	require.NoError(t, err)
	rwPVC.Status.Phase = corev1.ClaimBound
	err = c.Status().Update(ctx, rwPVC)
	require.NoError(t, err)

	// Ensure all are marked init running after PV/C bind events.
	for _, st := range sts {
		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			err = c.Get(ctx, client.ObjectKeyFromObject(st), st)
			if assert.NoError(ct, err) {
				assert.Equal(ct, nvcav1new.StorageInitRunning, st.Status.Phase,
					fmt.Sprintf("storage request %s/%s", st.Name, st.Namespace))
			}
		}, 5*time.Second, 50*time.Millisecond)
	}

	// Drive the writer Job to completion. The reconciler keys off
	// CompletionTime + Succeeded (see modelcache.go), not the conditions, but
	// the apiserver validates the condition shape and its rules differ by
	// version: k8s >= 1.34 requires SuccessCriteriaMet before Complete, while
	// k8s 1.30-1.33 reject SuccessCriteriaMet on this NonIndexed Job (it has no
	// SuccessPolicy). The Job's spec is reconciler-owned and immutable here, so
	// apply the modern shape and fall back to the pre-1.34 shape if the running
	// apiserver rejects it.
	completeJob(ctx, t, c, initJob)

	for _, st := range sts {
		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			err = c.Get(ctx, client.ObjectKeyFromObject(st), st)
			if assert.NoError(ct, err) {
				assert.Equal(ct, nvcav1new.StorageCreating, st.Status.Phase,
					fmt.Sprintf("storage request %s/%s", st.Name, st.Namespace))
			}
		}, 5*time.Second, 50*time.Millisecond)
	}

	err = c.Get(ctx, client.ObjectKeyFromObject(primaryPV), primaryPV)
	require.NoError(t, err)
	primaryPV.Status.Phase = corev1.VolumeBound
	err = c.Status().Update(ctx, primaryPV)
	require.NoError(t, err)

	roPVCs := make([]*corev1.PersistentVolumeClaim, len(sts))
	for i, st := range sts {
		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			roPVC := &corev1.PersistentVolumeClaim{}
			err = c.Get(ctx, client.ObjectKey{Name: "ro-pvc-" + cacheHandle, Namespace: st.Namespace}, roPVC)
			if assert.NoError(ct, err) {
				roPVCs[i] = roPVC
			}
		}, 5*time.Second, 50*time.Millisecond)
	}

	for _, pvc := range roPVCs {
		if pvc == nil {
			continue
		}
		if assert.NotNil(t, pvc.Spec.StorageClassName) {
			assert.Equal(t, gotSCName, *pvc.Spec.StorageClassName)
		}
		pvc.Status.Phase = corev1.ClaimBound
		err = c.Status().Update(ctx, pvc)
		require.NoError(t, err)
	}

	for _, st := range sts {
		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			err = c.Get(ctx, client.ObjectKeyFromObject(st), st)
			if assert.NoError(ct, err) {
				assert.Equal(ct, nvcav1new.StorageReady, st.Status.Phase,
					fmt.Sprintf("storage request %s/%s", st.Name, st.Namespace))
			}
		}, 5*time.Second, 50*time.Millisecond)
	}

	for _, st := range sts {
		// Ensure secondary PV has updated volume handle.
		secondaryPV := &corev1.PersistentVolume{}
		err := c.Get(ctx, client.ObjectKey{Name: "secondary-pv-" + st.Namespace}, secondaryPV)
		require.NoError(t, err)
		assert.Equal(t, volumeHandlePrefix+st.Namespace, secondaryPV.Spec.CSI.VolumeHandle)

		err = c.Delete(ctx, st)
		require.NoError(t, err)
	}

	for _, st := range sts {
		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			err = c.Get(ctx, client.ObjectKeyFromObject(st), st)
			assert.True(ct, apierrors.IsNotFound(err))
		}, 10*time.Second, 50*time.Millisecond)
	}

	cancel()
	<-mgrErrCh
}

func TestGetPVCState(t *testing.T) {
	tests := []struct {
		name     string
		pvc      *corev1.PersistentVolumeClaim
		expected pvcState
	}{
		{
			name: "bound pvc",
			pvc: &corev1.PersistentVolumeClaim{
				Status: corev1.PersistentVolumeClaimStatus{
					Phase: corev1.ClaimBound,
				},
			},
			expected: 1, // pvcBound
		},
		{
			name: "unbound pvc",
			pvc: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Now(),
				},
				Status: corev1.PersistentVolumeClaimStatus{
					Phase: corev1.ClaimPending,
				},
			},
			expected: 2, // pvcUnbound
		},
		{
			name: "bind failed pvc",
			pvc: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Time{Time: metav1.Now().Add(-time.Hour)},
				},
				Status: corev1.PersistentVolumeClaimStatus{
					Phase: corev1.ClaimPending,
				},
			},
			expected: 3, // pvcBindFailed
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Reconciler{
				k8sTimeConfig: (&k8sutil.TimeConfig{
					ModelCacheROPVCBindTimeGracePeriod: 2 * time.Minute,
				}).Complete(),
				metrics: newTestMetrics(),
			}
			state := r.getPVCState(tt.pvc)
			assert.Equal(t, tt.expected, state)
		})
	}
}

func TestGetInitCacheJobState(t *testing.T) {
	backoffLimit := int32(3)
	tests := []struct {
		name     string
		job      *batchv1.Job
		expected initCacheJobState
	}{
		{
			name: "job in progress",
			job: &batchv1.Job{
				Spec: batchv1.JobSpec{BackoffLimit: &backoffLimit},
				Status: batchv1.JobStatus{
					Conditions: []batchv1.JobCondition{
						{
							Type:   batchv1.JobComplete,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			expected: initCacheJobInProgress,
		},
		{
			name: "job completed",
			job: &batchv1.Job{
				Spec: batchv1.JobSpec{BackoffLimit: &backoffLimit},
				Status: batchv1.JobStatus{
					CompletionTime: &metav1.Time{},
					Succeeded:      1,
				},
			},
			expected: initCacheJobCompleted,
		},
		{
			name: "job failed",
			job: &batchv1.Job{
				Spec: batchv1.JobSpec{BackoffLimit: &backoffLimit},
				Status: batchv1.JobStatus{
					Failed: 4,
				},
			},
			expected: initCacheJobFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Reconciler{
				fff:     &featureflagmock.Fetcher{},
				metrics: newTestMetrics(),
			}
			state := r.getInitCacheJobState(context.Background(), tt.job)
			assert.Equal(t, tt.expected, state)
		})
	}
}

func TestNewInitLease(t *testing.T) {
	stCopy := &nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-storage",
			Namespace: "test-ns",
		},
		Spec: nvcav1new.StorageRequestSpec{
			ICMSRequestName: "test-storage",
			ModelCache: &nvcav1new.ModelCacheSpec{
				CacheHandle: "foo",
			},
		},
	}

	lease := newInitLease(stCopy)

	assert.Equal(t, ModelCacheInitNamespace, lease.Namespace)
	assert.Equal(t, lease.Name, "modelcache-init-foo")
	assert.NotNil(t, lease.Spec)
	assert.NotNil(t, lease.Spec.HolderIdentity)
	assert.Equal(t, stCopy.Spec.ICMSRequestName, *lease.Spec.HolderIdentity)
}

func TestGetPrimaryPV(t *testing.T) {
	tests := []struct {
		name    string
		objects []client.Object
		wantErr bool
	}{
		{
			name: "pv found",
			objects: []client.Object{
				&corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "primary-pv",
						Labels: map[string]string{
							primaryPVLabelKey:        "true",
							modelCacheHandleLabelKey: "exp",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "pv not found no label",
			objects: []client.Object{
				&corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "primary-pv",
						Labels: map[string]string{
							primaryPVLabelKey: "true",
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "pv not found other label",
			objects: []client.Object{
				&corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "primary-pv",
						Labels: map[string]string{
							primaryPVLabelKey:        "true",
							modelCacheHandleLabelKey: "other",
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "pv too many",
			objects: []client.Object{
				&corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "primary-pv-1",
						Labels: map[string]string{
							primaryPVLabelKey:        "true",
							modelCacheHandleLabelKey: "exp",
						},
					},
				},
				&corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "primary-pv-2",
						Labels: map[string]string{
							primaryPVLabelKey:        "true",
							modelCacheHandleLabelKey: "exp",
						},
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewClientBuilder().
				WithScheme(mgrScheme).
				WithObjects(tt.objects...).
				Build()

			r := &Reconciler{Client: client, metrics: newTestMetrics()}
			st := &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						types.NCAIDKey:             "test-nca",
						types.FunctionIDKey:        "test-function",
						types.FunctionVersionIDKey: "test-version",
					},
				},
				Spec: nvcav1new.StorageRequestSpec{
					ModelCache: &nvcav1new.ModelCacheSpec{
						CacheHandle: "exp",
					},
				},
			}
			_, err := r.getPrimaryPV(context.Background(), st)
			if (err != nil) != tt.wantErr {
				t.Errorf("getPrimaryPV() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_updateSecondaryPVVolumeHandle(t *testing.T) {
	namespace := "sr-fd7d88ab-6e18-4442-8a94-344da5f7341e"
	tests := []struct {
		name            string
		volumeHandle    string
		expVolumeHandle string
		expError        string
	}{
		{
			name:         "empty",
			volumeHandle: "",
			expError:     `volume handle "" has no colons`,
		},
		{
			name:         "no colons",
			volumeHandle: "foobar",
			expError:     `volume handle "foobar" has no colons`,
		},
		{
			name:            "colon only",
			volumeHandle:    ":",
			expVolumeHandle: ":" + namespace,
		},
		{
			name:            "empty namespace",
			volumeHandle:    "single-zone-cluster:csi-5326ce57-8cae-456c:ef7bc990-47e7-11f0-91b6-c952fffeea08:",
			expVolumeHandle: "single-zone-cluster:csi-5326ce57-8cae-456c:ef7bc990-47e7-11f0-91b6-c952fffeea08:" + namespace,
		},
		{
			name:            "mcinit namespace",
			volumeHandle:    "single-zone-cluster:csi-5326ce57-8cae-456c:ef7bc990-47e7-11f0-91b6-c952fffeea08:" + ModelCacheInitNamespace,
			expVolumeHandle: "single-zone-cluster:csi-5326ce57-8cae-456c:ef7bc990-47e7-11f0-91b6-c952fffeea08:" + namespace,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := updateSecondaryPVVolumeHandle(tt.volumeHandle, namespace)
			if tt.expError != "" {
				assert.EqualError(t, err, tt.expError)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expVolumeHandle, got)
			}
		})
	}
}

func getPrimaryPVObj() *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "primary-pv",
			Labels: map[string]string{
				primaryPVLabelKey:          "true",
				types.FunctionIDKey:        "test-function",
				types.FunctionVersionIDKey: "test-version",
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("1Gi"),
			},
			StorageClassName: "test-sc",
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       "test-driver",
					VolumeHandle: "test-handle",
				},
			},
		},
	}
}

func TestMapPodIssuesToFailureReason(t *testing.T) {
	tests := []struct {
		name           string
		podIssues      []string
		expectedReason string
	}{
		{
			name:           "empty issues returns init_job_failed",
			podIssues:      []string{},
			expectedReason: modelcachetypes.ReasonInitJobFailed,
		},
		{
			name:           "image pull issues",
			podIssues:      []string{"image pull issues"},
			expectedReason: modelcachetypes.ReasonImagePull,
		},
		{
			name:           "init stuck initializing",
			podIssues:      []string{"init stuck initializing"},
			expectedReason: modelcachetypes.ReasonInitStuck,
		},
		{
			name:           "scheduling timeout",
			podIssues:      []string{"timed out waiting to be scheduled"},
			expectedReason: modelcachetypes.ReasonSchedulingTimeout,
		},
		{
			name:           "admission rejected",
			podIssues:      []string{"admission rejected"},
			expectedReason: modelcachetypes.ReasonAdmissionRejected,
		},
		{
			name:           "unknown issue returns init_job_failed",
			podIssues:      []string{"some unknown issue"},
			expectedReason: modelcachetypes.ReasonInitJobFailed,
		},
		{
			name:           "image pull takes priority over other issues",
			podIssues:      []string{"init stuck initializing", "image pull issues", "admission rejected"},
			expectedReason: modelcachetypes.ReasonImagePull,
		},
		{
			name:           "init stuck takes priority over scheduling timeout",
			podIssues:      []string{"timed out waiting to be scheduled", "init stuck initializing"},
			expectedReason: modelcachetypes.ReasonInitStuck,
		},
		{
			name:           "scheduling timeout takes priority over admission rejected",
			podIssues:      []string{"admission rejected", "timed out waiting to be scheduled"},
			expectedReason: modelcachetypes.ReasonSchedulingTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := sets.New[string](tt.podIssues...)
			result := mapPodIssuesToFailureReason(issues)
			assert.Equal(t, tt.expectedReason, result)
		})
	}
}

// completeJob marks a writer Job as succeeded in a way the running apiserver
// accepts. The reconciler detects completion via CompletionTime + Succeeded
// (see modelcache.go), not the Job conditions, but the apiserver still
// validates the condition shape and its rules changed across versions:
//   - k8s >= 1.34 requires a SuccessCriteriaMet condition before Complete, and
//     rejects CompletionTime without Complete.
//   - k8s 1.30-1.33 reject SuccessCriteriaMet on a NonIndexed Job that has no
//     SuccessPolicy, which is exactly this writer Job's shape.
//
// The Job's spec is reconciler-owned and immutable on update, so the test
// cannot reshape it to satisfy both. Apply the modern (>= 1.34) condition shape
// and fall back to the pre-1.34 shape only if the apiserver rejects it.
func completeJob(ctx context.Context, t *testing.T, c client.Client, job *batchv1.Job) {
	t.Helper()

	job.Status.Succeeded = 1
	job.Status.CompletionTime = &metav1.Time{Time: time.Now()}
	job.Status.Conditions = append(job.Status.Conditions,
		batchv1.JobCondition{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue},
		batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
	)
	err := c.Status().Update(ctx, job)
	if err == nil {
		return
	}
	// Only a validation rejection means the modern condition shape is wrong for
	// this apiserver version. Any other error (conflict, network, RBAC) is a
	// real failure and must surface, not be masked by the fallback write.
	require.True(t, apierrors.IsInvalid(err), "unexpected error updating Job status: %v", err)

	// Pre-1.34 apiserver rejected SuccessCriteriaMet. Re-fetch to reset the
	// stale in-memory status and apply only Complete, which those versions
	// accept alongside CompletionTime.
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(job), job))
	job.Status.Succeeded = 1
	job.Status.CompletionTime = &metav1.Time{Time: time.Now()}
	job.Status.Conditions = append(job.Status.Conditions,
		batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
	)
	require.NoError(t, c.Status().Update(ctx, job))
}
