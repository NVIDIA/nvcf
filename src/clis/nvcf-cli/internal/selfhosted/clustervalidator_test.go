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

package selfhosted

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func jobSucceededReactor(name string) ktesting.ReactionFunc {
	return func(action ktesting.Action) (bool, runtime.Object, error) {
		getAction, ok := action.(ktesting.GetAction)
		if !ok || getAction.GetName() != name {
			return false, nil, nil
		}
		return true, &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: clusterValidatorNamespace},
			Status:     batchv1.JobStatus{Succeeded: 1},
		}, nil
	}
}

func jobFailedReactor(name string) ktesting.ReactionFunc {
	return func(action ktesting.Action) (bool, runtime.Object, error) {
		getAction, ok := action.(ktesting.GetAction)
		if !ok || getAction.GetName() != name {
			return false, nil, nil
		}
		return true, &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: clusterValidatorNamespace},
			Status:     batchv1.JobStatus{Failed: 1},
		}, nil
	}
}

// Stamps the requested LabelSelector onto the returned pod so the fake
// clientset's post-reactor selector filter doesn't drop it.
func podListReactor(waitingReason string) ktesting.ReactionFunc {
	return func(action ktesting.Action) (bool, runtime.Object, error) {
		listAction, ok := action.(ktesting.ListAction)
		if !ok {
			return false, nil, nil
		}
		selector := listAction.GetListRestrictions().Labels.String()
		// Selector shape is exactly "job-name=<jobName>"; trim the prefix.
		jobName := strings.TrimPrefix(selector, "job-name=")
		if jobName == "" || jobName == selector {
			return false, nil, nil
		}
		pod := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      jobName + "-pod",
				Namespace: clusterValidatorNamespace,
				Labels:    map[string]string{"job-name": jobName},
			},
		}
		if waitingReason != "" {
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
				Name: clusterValidatorContainer,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason:  waitingReason,
					Message: "test injected: " + waitingReason,
				}},
			}}
		}
		return true, &corev1.PodList{Items: []corev1.Pod{pod}}, nil
	}
}

func hasActionPrefix(actions []ktesting.Action, verb, resource string) bool {
	for _, a := range actions {
		if a.GetVerb() == verb && a.GetResource().Resource == resource {
			return true
		}
	}
	return false
}

func TestRunClusterValidator_EmptyImage(t *testing.T) {
	// In normal flow the caller gates on a configured image, so this
	// branch is defensive. Verify it returns a clear error and makes
	// no API calls.
	client := fake.NewSimpleClientset()
	res := runClusterValidator(context.Background(), client, "", "", false)
	require.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "image is empty")
	assert.False(t, res.Passed)
	assert.Empty(t, client.Actions(), "no API calls should be made when input validation fails")
}

func TestClusterValidatorCheck_OrchestratorErrorStaysWarning(t *testing.T) {
	cv := func(_ context.Context, _ ClusterValidatorParams) ClusterValidatorResult {
		return ClusterValidatorResult{Err: fmt.Errorf("transient: API server unreachable")}
	}
	r := clusterValidatorCheck(cv, "", "", "", false).Run(context.Background())
	assert.False(t, r.Passed)
	assert.Equal(t, "warning", r.Severity,
		"transient orchestrator failures should not fail the overall preflight")
	assert.Contains(t, r.Message, "cluster-validator did not complete")
}


func TestRunClusterValidator_HappyPath(t *testing.T) {
	client := fake.NewSimpleClientset()
	// Capture the Job name the runner picks, then drive Get/List reactors to
	// return success for that name.
	var jobName atomic.Value
	jobName.Store("")
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		createAction := action.(ktesting.CreateAction)
		job := createAction.GetObject().(*batchv1.Job)
		jobName.Store(job.Name)
		return false, nil, nil // let the tracker store the Job
	})
	client.PrependReactor("get", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		name := jobName.Load().(string)
		if name == "" {
			return false, nil, nil
		}
		return jobSucceededReactor(name)(action)
	})
	client.PrependReactor("list", "pods", podListReactor(""))
	client.PrependReactor("get", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		// containerExitCode resolves the pod after the Job terminates.
		name := jobName.Load().(string)
		if name == "" {
			return false, nil, nil
		}
		return true, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-pod", Namespace: clusterValidatorNamespace},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name: clusterValidatorContainer,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					ExitCode: 0,
				}},
			}}},
		}, nil
	})

	res := runClusterValidator(context.Background(), client, "test-image:1.0", "", false)
	require.NoError(t, res.Err, "happy path must not surface an error")
	assert.True(t, res.Passed, "Succeeded>0 maps to Passed=true")
	assert.Equal(t, int32(0), res.ExitCode)
	assert.NotEmpty(t, res.JobName, "JobName must be populated so operators can read logs after the run")

	// Sweep must run before create so the singleton invariant holds.
	actions := client.Actions()
	require.True(t, hasActionPrefix(actions, "delete-collection", "jobs"),
		"runClusterValidator must sweep prior Jobs before creating the new one")
	var sweepIdx, createIdx = -1, -1
	for i, a := range actions {
		if a.GetVerb() == "delete-collection" && a.GetResource().Resource == "jobs" && sweepIdx == -1 {
			sweepIdx = i
		}
		if a.GetVerb() == "create" && a.GetResource().Resource == "jobs" && createIdx == -1 {
			createIdx = i
		}
	}
	assert.Less(t, sweepIdx, createIdx, "sweep must precede create in action sequence")
}

func TestRunClusterValidator_JobFailed(t *testing.T) {
	client := fake.NewSimpleClientset()
	var jobName atomic.Value
	jobName.Store("")
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		jobName.Store(action.(ktesting.CreateAction).GetObject().(*batchv1.Job).Name)
		return false, nil, nil
	})
	client.PrependReactor("get", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		return jobFailedReactor(jobName.Load().(string))(action)
	})
	client.PrependReactor("list", "pods", podListReactor(""))

	res := runClusterValidator(context.Background(), client, "test-image:1.0", "", false)
	require.NoError(t, res.Err, "a clean Passed=false verdict must not set Err")
	assert.False(t, res.Passed, "Failed>0 maps to Passed=false")
	assert.NotEmpty(t, res.JobName, "JobName must be populated on failure for kubectl-logs follow-up")
}

func TestRunClusterValidator_RBACIdempotent(t *testing.T) {
	client := fake.NewSimpleClientset()
	// Seed the cluster with the SA, ClusterRole, and ClusterRoleBinding so
	// each Create returns AlreadyExists. The runner must treat that as
	// success and proceed to Job creation.
	existing := []ktesting.ReactionFunc{
		alreadyExistsReactor("serviceaccounts", clusterValidatorName),
		alreadyExistsReactor("clusterroles", clusterValidatorName),
		alreadyExistsReactor("clusterrolebindings", clusterValidatorName),
	}
	verbs := []string{"create"}
	resources := []string{"serviceaccounts", "clusterroles", "clusterrolebindings"}
	for i, r := range existing {
		client.PrependReactor(verbs[0], resources[i], r)
	}

	var jobName atomic.Value
	jobName.Store("")
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		jobName.Store(action.(ktesting.CreateAction).GetObject().(*batchv1.Job).Name)
		return false, nil, nil
	})
	client.PrependReactor("get", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		return jobSucceededReactor(jobName.Load().(string))(action)
	})
	client.PrependReactor("list", "pods", podListReactor(""))

	res := runClusterValidator(context.Background(), client, "test-image:1.0", "", false)
	require.NoError(t, res.Err, "AlreadyExists on RBAC bootstrap must be treated as success")
	assert.True(t, res.Passed)
}

// Regression guard: a ClusterRole left over from an older CLI version must
// be refreshed with the current rule set instead of silently kept stale.
func TestRunClusterValidator_RBACRefreshesClusterRoleRules(t *testing.T) {
	oldRules := []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"get"}},
	}
	client := fake.NewSimpleClientset(&rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: clusterValidatorName},
		Rules:      oldRules,
	})

	var jobName atomic.Value
	jobName.Store("")
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		jobName.Store(action.(ktesting.CreateAction).GetObject().(*batchv1.Job).Name)
		return false, nil, nil
	})
	client.PrependReactor("get", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		return jobSucceededReactor(jobName.Load().(string))(action)
	})
	client.PrependReactor("list", "pods", podListReactor(""))

	res := runClusterValidator(context.Background(), client, "test-image:1.0", "", false)
	require.NoError(t, res.Err)

	got, err := client.RbacV1().ClusterRoles().Get(context.Background(), clusterValidatorName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Greater(t, len(got.Rules), len(oldRules),
		"ClusterRole rules must be refreshed to the current set on each run, not kept stale")
}

func TestRunClusterValidator_ImagePullBackOffShortCircuits(t *testing.T) {
	client := fake.NewSimpleClientset()
	var jobName atomic.Value
	jobName.Store("")
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		jobName.Store(action.(ktesting.CreateAction).GetObject().(*batchv1.Job).Name)
		return false, nil, nil
	})
	// Job never reaches terminal: Succeeded=0, Failed=0.
	client.PrependReactor("get", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		name := jobName.Load().(string)
		if action.(ktesting.GetAction).GetName() != name {
			return false, nil, nil
		}
		return true, &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: clusterValidatorNamespace},
		}, nil
	})
	// Pod is stuck in ImagePullBackOff.
	client.PrependReactor("list", "pods", podListReactor("ImagePullBackOff"))
	client.PrependReactor("get", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		// The "get pods" verb also fires for GetLogs (subresource "log"),
		// which arrives as a GenericAction and would panic the GetAction
		// type assertion below. Skip those so the fake's built-in log
		// handler can respond with an empty stream.
		if action.GetSubresource() != "" {
			return false, nil, nil
		}
		return true, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      action.(ktesting.GetAction).GetName(),
				Namespace: clusterValidatorNamespace,
			},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name: clusterValidatorContainer,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason:  "ImagePullBackOff",
					Message: "back-off pulling image",
				}},
			}}},
		}, nil
	})

	start := time.Now()
	res := runClusterValidator(context.Background(), client, "test-image:1.0", "", false)
	elapsed := time.Since(start)

	require.Error(t, res.Err, "ImagePullBackOff must short-circuit the wait with an error")
	assert.Contains(t, res.Err.Error(), "ImagePullBackOff")
	assert.Contains(t, res.Err.Error(), "cannot pull image")
	assert.NotEmpty(t, res.JobName, "JobName must still be populated so the operator can describe the pod")
	assert.Less(t, elapsed, clusterValidatorTimeout,
		"short-circuit must return well before the 5-minute timeout; otherwise the early-detect path is broken")
}

// Regression guard: when vctx expires during the wait, the post-wait log
// fetch must run under a fresh ctx so the partial transcript survives.
func TestRunClusterValidator_LogFetchSurvivesValidatorTimeout(t *testing.T) {
	prevTimeout := clusterValidatorTimeout
	clusterValidatorTimeout = 100 * time.Millisecond
	defer func() { clusterValidatorTimeout = prevTimeout }()

	client := fake.NewSimpleClientset()
	var jobName atomic.Value
	jobName.Store("")
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		jobName.Store(action.(ktesting.CreateAction).GetObject().(*batchv1.Job).Name)
		return false, nil, nil
	})
	// Job never reaches terminal so the wait drains the deadline.
	client.PrependReactor("get", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      action.(ktesting.GetAction).GetName(),
				Namespace: clusterValidatorNamespace,
			},
		}, nil
	})
	// Pod is healthy (no pull failure); pull-failure short-circuit does not fire.
	client.PrependReactor("list", "pods", podListReactor(""))

	// Parent ctx stays alive for the entire run; only vctx expires.
	res := runClusterValidator(context.Background(), client, "test-image:1.0", "", false)

	require.Error(t, res.Err, "wait must surface the deadline-exceeded error")
	assert.Contains(t, res.Err.Error(), "waiting for job",
		"timeout path must report the wait failure verbatim")
	assert.NotEmpty(t, res.JobName,
		"JobName must be populated so operators can run the kubectl-logs hint")

	// Log fetch under logCtx must have issued a list-pods call after the wait
	// returned. If fetchClusterValidatorLogs were still using the expired
	// vctx, in a real cluster (or any ctx-honoring fake) the List would return
	// DeadlineExceeded immediately and Logs would be silently empty.
	actions := client.Actions()
	lastGetJob, firstPostWaitListPods := -1, -1
	for i, a := range actions {
		if a.GetVerb() == "get" && a.GetResource().Resource == "jobs" {
			lastGetJob = i
		}
		if a.GetVerb() == "list" && a.GetResource().Resource == "pods" &&
			i > lastGetJob && firstPostWaitListPods == -1 {
			firstPostWaitListPods = i
		}
	}
	assert.NotEqual(t, -1, firstPostWaitListPods,
		"a list-pods action must be recorded after the wait gives up so the log fetch runs under the fresh logCtx")
}

func TestRunClusterValidator_ContextCanceled(t *testing.T) {
	client := fake.NewSimpleClientset()
	var jobName atomic.Value
	jobName.Store("")
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		jobName.Store(action.(ktesting.CreateAction).GetObject().(*batchv1.Job).Name)
		return false, nil, nil
	})
	// Job never terminates.
	client.PrependReactor("get", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      action.(ktesting.GetAction).GetName(),
				Namespace: clusterValidatorNamespace,
			},
		}, nil
	})
	// Pod is running but never terminates; no pull failure reason.
	client.PrependReactor("list", "pods", podListReactor(""))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Cancel quickly so we don't wait the full 5 minutes.
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	res := runClusterValidator(ctx, client, "test-image:1.0", "", false)
	require.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "context")
}

func TestBuildClusterValidatorJobShape(t *testing.T) {
	job := buildClusterValidatorJob("test-job", "img:1", "", false)

	assert.Equal(t, "test-job", job.Name)
	assert.Equal(t, clusterValidatorNamespace, job.Namespace)
	require.NotNil(t, job.Spec.BackoffLimit)
	assert.Equal(t, int32(0), *job.Spec.BackoffLimit, "no retries on validator failure: one verdict per Job")
	require.NotNil(t, job.Spec.TTLSecondsAfterFinished)
	assert.Equal(t, clusterValidatorTTLSeconds, *job.Spec.TTLSecondsAfterFinished)

	require.Len(t, job.Spec.Template.Spec.Containers, 1)
	c := job.Spec.Template.Spec.Containers[0]
	assert.Equal(t, clusterValidatorContainer, c.Name)
	assert.Equal(t, "img:1", c.Image)
	assert.Equal(t, corev1.PullIfNotPresent, c.ImagePullPolicy,
		"IfNotPresent so locally-imported images (k3d image import, kind load) are picked up without a registry pull")
	assert.Equal(t, corev1.RestartPolicyNever, job.Spec.Template.Spec.RestartPolicy,
		"validator is a one-shot command, not a long-running service")
	assert.Equal(t, clusterValidatorName, job.Spec.Template.Spec.ServiceAccountName,
		"pod must run under the CLI-bootstrapped SA, not the namespace default")

	labels := job.Labels
	assert.Equal(t, clusterValidatorAppLabel, labels["app.kubernetes.io/name"])
	assert.Equal(t, "nvcf-cli", labels["app.kubernetes.io/managed-by"])
	assert.Equal(t, "preflight", labels["app.kubernetes.io/component"])

	// The validator must run in preflight mode so it skips the summary
	// ConfigMap write (the metrics path needs the NVCA agent, which is not
	// installed pre-flight).
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	assert.Equal(t, "true", env["VALIDATOR_PREFLIGHT"],
		"preflight invocation must tag the validator so it does not attempt the metrics ConfigMap write")
}

func TestBuildClusterValidatorJobShape_WithPullSecret(t *testing.T) {
	job := buildClusterValidatorJob("test-job", "img:1", "nvcr-pull-secret", false)
	require.Len(t, job.Spec.Template.Spec.ImagePullSecrets, 1)
	assert.Equal(t, "nvcr-pull-secret", job.Spec.Template.Spec.ImagePullSecrets[0].Name)
}

func TestBuildClusterValidatorJobShape_NoPullSecret(t *testing.T) {
	job := buildClusterValidatorJob("test-job", "img:1", "", false)
	assert.Empty(t, job.Spec.Template.Spec.ImagePullSecrets,
		"empty pull-secret arg must not produce an empty-name ImagePullSecrets entry")
}

func TestBuildClusterValidatorJobShape_NoCleanup(t *testing.T) {
	job := buildClusterValidatorJob("test-job", "img:1", "", true)
	assert.Nil(t, job.Spec.TTLSecondsAfterFinished,
		"--no-cleanup must omit TTLSecondsAfterFinished so the Job persists for debugging")
}

func TestCleanValidatorOutput_StripsANSI(t *testing.T) {
	raw := "\x1b[32mGreen text\x1b[0m and \x1b[1;31mbold red\x1b[0m\n"
	got := cleanValidatorOutput(raw)
	assert.NotContains(t, got, "\x1b", "all ANSI escapes must be stripped")
	assert.Contains(t, got, "Green text")
	assert.Contains(t, got, "bold red")
}

func TestCleanValidatorOutput_StripsBoxDrawing(t *testing.T) {
	raw := "─────────────\nHeader\n─────────────\nContent\n"
	got := cleanValidatorOutput(raw)
	// All Box Drawing characters (U+2500 to U+257F) must be gone.
	for _, r := range got {
		if r >= 0x2500 && r <= 0x257F {
			t.Fatalf("found Box Drawing rune %U in cleaned output: %q", r, got)
		}
	}
	assert.Contains(t, got, "Header")
	assert.Contains(t, got, "Content")
}

func TestCleanValidatorOutput_CollapsesBlankLines(t *testing.T) {
	raw := "Line A\n\n\n\n\nLine B\n"
	got := cleanValidatorOutput(raw)
	assert.NotContains(t, got, "\n\n\n", "runs of 3+ newlines must collapse to one blank line")
	assert.Contains(t, got, "Line A")
	assert.Contains(t, got, "Line B")
}

func TestCleanValidatorOutput_Idempotent(t *testing.T) {
	raw := "\x1b[32m─Header─\x1b[0m\n\n\n\nBody\n"
	once := cleanValidatorOutput(raw)
	twice := cleanValidatorOutput(once)
	assert.Equal(t, once, twice, "cleanup must be idempotent so chained calls do not corrupt output")
}

func TestCleanValidatorOutput_EmptyInput(t *testing.T) {
	assert.Equal(t, "", cleanValidatorOutput(""))
}

func TestKubectlLogsHint(t *testing.T) {
	got := kubectlLogsHint("nvcf-preflight-validator-12345")
	assert.Equal(t,
		"kubectl logs -n default job/nvcf-preflight-validator-12345 --tail=-1",
		got,
	)
}

func TestKubectlLogsHint_EmptyJob(t *testing.T) {
	assert.Equal(t, "", kubectlLogsHint(""),
		"empty jobName must produce empty hint so callers can compose detail without conditionals")
}

func alreadyExistsReactor(resource, name string) ktesting.ReactionFunc {
	gr := schema.GroupResource{Resource: resource}
	return func(action ktesting.Action) (bool, runtime.Object, error) {
		createAction, ok := action.(ktesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		meta, ok := createAction.GetObject().(metav1.Object)
		if !ok || meta.GetName() != name {
			return false, nil, nil
		}
		return true, nil, apierrors.NewAlreadyExists(gr, name)
	}
}

