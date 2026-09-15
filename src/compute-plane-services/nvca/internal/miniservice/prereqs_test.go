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

package mscontroller

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
)

func TestImageCredentialUpdater(t *testing.T) {
	ctx := newTestContext()

	crclient, _ := newFakeClient(mgrScheme,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nvca-system"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nvcf-backend"}},
	)
	r := &Reconciler{
		ControllerOptions: ControllerOptions{
			K8sTimeConfig:      (&k8sutil.TimeConfig{}).Complete(),
			FeatureFlagFetcher: &featureflagmock.Fetcher{},
			SystemNamespace:    "nvca-system",
			Metrics:            metrics.NewDefaultMetrics("test-nca-id", "test-cluster", "test-group", "test-version", metrics.WithRegisterer(prometheus.NewRegistry())),
		},
		Client:                crclient,
		eventRecorder:         record.NewFakeRecorder(10),
		newPermissionsChecker: newFakePermissionsChecker,
	}
	configuredToleration := corev1.Toleration{
		Key:      "nvidia.com/test-workload",
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoSchedule,
	}
	r.cfg.Workload.Tolerations = []corev1.Toleration{configuredToleration}

	sr := &nvcav2beta1.ICMSRequest{}
	sr.UID = "0e55f7ae-8c40-4397-a510-d4a655cf734e"
	sr.Name, sr.Namespace = "sr-1b9ed9a3-a583-49f4-b729-caa58a94a3d6", "nvcf-backend"
	sr.Spec.RequestID = "reqid1"
	sr.Spec.MessageBatchID = "msgbatchid1"
	sr.Spec.CreationMsgInfo.FunctionLaunchSpecification = &function.LaunchSpecification{
		EnvironmentB64: base64.StdEncoding.EncodeToString(fmt.Appendf(nil,
			"%s=%s\n%s=%s",
			common.ContainerRegistriesCredentialsEnv, "foobar",
			common.SidecarRegistryCredentialEnv, "foobar",
		)),
	}
	ms := &v1alpha1.MiniService{}
	ms.Name = sr.Name + "-miniservice"
	ms.Spec.Namespace = sr.Name

	objectMutators := newEmptyObjectMutators()
	objectMutators.setGeneralObjectMetadataMutators(r.FeatureFlagFetcher, ms, sr, "region", "cname", "func-name", "", true)

	// No image set.
	requeue, err := r.ensureImageCredentialUpdaterObjects(ctx, ms, sr, objectMutators)
	require.NoError(t, err)
	assert.False(t, requeue)

	// Image set, objects should be created.
	r.ImageCredentialHelperImage = "image-credential-helper:latest"

	requeue, err = r.ensureImageCredentialUpdaterObjects(ctx, ms, sr, objectMutators)
	require.NoError(t, err)
	assert.True(t, requeue)

	gotSecret := &corev1.Secret{}
	err = crclient.Get(ctx, client.ObjectKey{Name: sr.Name + "-image-creds", Namespace: ms.Spec.Namespace}, gotSecret)
	require.NoError(t, err)
	assert.Equal(t, "foobar", string(gotSecret.Data[common.ContainerRegistriesCredentialsEnv]))
	assert.Equal(t, "foobar", string(gotSecret.Data[common.SidecarRegistryCredentialEnv]))

	gotJob := &batchv1.Job{}
	err = crclient.Get(ctx, client.ObjectKey{Name: sr.Name + "-cred-init", Namespace: r.SystemNamespace}, gotJob)
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"/usr/bin/image-credential-helper"},
		gotJob.Spec.Template.Spec.Containers[0].Command,
	)
	assert.Equal(t,
		[]string{"-global", "-target-namespace", "sr-1b9ed9a3-a583-49f4-b729-caa58a94a3d6"},
		gotJob.Spec.Template.Spec.Containers[0].Args,
	)
	assert.Empty(t, gotJob.Spec.Template.Spec.ImagePullSecrets)
	assert.Contains(t, gotJob.Spec.Template.Spec.Tolerations, configuredToleration)

	// Update job to failed, should see error.
	gotJob.Status = batchv1.JobStatus{
		Conditions: []batchv1.JobCondition{{
			Type:   batchv1.JobFailed,
			Status: corev1.ConditionTrue,
			Reason: "reason",
		}},
	}
	err = crclient.Status().Update(ctx, gotJob)
	require.NoError(t, err)
	requeue, err = r.ensureImageCredentialUpdaterObjects(ctx, ms, sr, objectMutators)
	require.EqualError(t, err, "terminal error: image pull updater init job failed: reason")
	assert.False(t, requeue)

	// Update job to completed, should see no requeue.
	gotJob.Status = batchv1.JobStatus{
		Conditions: []batchv1.JobCondition{{
			Type:   batchv1.JobComplete,
			Status: corev1.ConditionTrue,
			Reason: "done",
		}},
	}
	err = crclient.Status().Update(ctx, gotJob)
	require.NoError(t, err)
	requeue, err = r.ensureImageCredentialUpdaterObjects(ctx, ms, sr, objectMutators)
	require.NoError(t, err)
	assert.False(t, requeue)
}

func Test_setInfraValues(t *testing.T) {
	var values json.RawMessage

	gotValues, err := setInfraValues(values, &nvcav2beta1.ICMSRequest{})
	require.NoError(t, err)
	assert.Equal(t, values, gotValues)

	values = json.RawMessage(`{"foo":"bar"}`)
	gotValues, err = setInfraValues(values, &nvcav2beta1.ICMSRequest{})
	require.NoError(t, err)
	assert.Equal(t, values, gotValues)

	icmsReq := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			NCAId: "nca1",
			TaskDetails: task.Details{
				TaskID: "task1",
			},
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				TaskLaunchSpecification: &task.LaunchSpecification{
					EnvironmentB64: "VEFTS19OQU1FPWZvbw==",
				},
			},
		},
	}

	values = nil
	gotValues, err = setInfraValues(values, icmsReq)
	require.NoError(t, err)
	assert.JSONEq(t, `{
	"nvctNcaId": "nca1",
	"nvctTaskId": "task1",
	"nvctTaskName": "foo",
	"nvctResultsDir": "/var/task/results",
	"nvctProgressFilePath": "/var/task/results/progress"
}`, string(gotValues))

	values = json.RawMessage(`{"foo":"bar", "nvctNcaId":"", "nvctTaskId":"toobad"}`)
	gotValues, err = setInfraValues(values, icmsReq)
	require.NoError(t, err)
	assert.JSONEq(t, `{
	"foo": "bar",
	"nvctNcaId": "nca1",
	"nvctTaskId": "toobad",
	"nvctTaskName": "foo",
	"nvctResultsDir": "/var/task/results",
	"nvctProgressFilePath": "/var/task/results/progress"
}`, string(gotValues))
}
