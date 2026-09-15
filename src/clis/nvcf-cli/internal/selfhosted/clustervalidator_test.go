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

func TestRunClusterValidator_EmptyImage(t *testing.T) {
	// In normal flow the caller gates on a configured image, so this
	// branch is defensive. Verify it returns a clear error and makes
	// no API calls.
	client := fake.NewSimpleClientset()
	res := runClusterValidator(context.Background(), client, "", "", false, "", nil)
	require.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "image is empty")
	assert.False(t, res.Passed)
	assert.Empty(t, client.Actions(), "no API calls should be made when input validation fails")
}

func TestClusterValidatorCheck_OrchestratorErrorStaysWarning(t *testing.T) {
	cv := func(_ context.Context, _ ClusterValidatorParams) ClusterValidatorResult {
		return ClusterValidatorResult{Err: fmt.Errorf("transient: API server unreachable")}
	}
	r := clusterValidatorCheck(cv, "", "", "", false, "", nil).Run(context.Background())
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

	res := runClusterValidator(context.Background(), client, "test-image:1.0", "", false, "", nil)
	require.NoError(t, res.Err, "happy path must not surface an error")
	assert.True(t, res.Passed, "Succeeded>0 maps to Passed=true")
	assert.Equal(t, int32(0), res.ExitCode)
	assert.NotEmpty(t, res.JobName, "JobName must be populated so operators can read logs after the run")

	// Sweep must run before create so the singleton invariant holds. The sweep
	// lists rather than issuing a DeleteCollection, because the generated name
	// has to be checked too and a collection selector cannot express that.
	actions := client.Actions()
	var sweepIdx, createIdx = -1, -1
	for i, a := range actions {
		if a.GetVerb() == "list" && a.GetResource().Resource == "jobs" && sweepIdx == -1 {
			sweepIdx = i
		}
		if a.GetVerb() == "create" && a.GetResource().Resource == "jobs" && createIdx == -1 {
			createIdx = i
		}
	}
	require.NotEqual(t, -1, sweepIdx, "runClusterValidator must sweep prior Jobs before creating the new one")
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

	res := runClusterValidator(context.Background(), client, "test-image:1.0", "", false, "", nil)
	require.NoError(t, res.Err, "a clean Passed=false verdict must not set Err")
	assert.False(t, res.Passed, "Failed>0 maps to Passed=false")
	assert.NotEmpty(t, res.JobName, "JobName must be populated on failure for kubectl-logs follow-up")
}

func TestRunClusterValidator_RBACIdempotent(t *testing.T) {
	// Seed the SA, ClusterRole and ClusterRoleBinding exactly as a prior run
	// would have left them: present, and carrying our managed labels. Each
	// Create then returns AlreadyExists and the runner must treat that as
	// success and proceed to Job creation.
	//
	// Seeding real objects rather than faking AlreadyExists with a reactor
	// matters: the runner refetches on conflict to confirm it owns what it is
	// about to bind, so a reactor with nothing behind it is not a valid model.
	client := fake.NewSimpleClientset(
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
			Name: clusterValidatorName, Namespace: clusterValidatorNamespace,
			Labels: clusterValidatorLabels(),
		}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{
			Name: clusterValidatorName, Labels: clusterValidatorLabels(),
		}},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterValidatorName, Labels: clusterValidatorLabels(),
			},
			Subjects: []rbacv1.Subject{{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      clusterValidatorName,
				Namespace: clusterValidatorNamespace,
			}},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterValidatorName,
			},
		},
	)

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

	res := runClusterValidator(context.Background(), client, "test-image:1.0", "", false, "", nil)
	require.NoError(t, res.Err, "AlreadyExists on RBAC bootstrap must be treated as success")
	assert.True(t, res.Passed)
}

// TestEnsureClusterValidatorRBAC_WritableResources verifies that the
// bootstrapped ClusterRole grants the write verbs required by enforcement
// checks (namespace/pod create+delete), probe log reading (pods/log get),
// and the Gateway API checks.
func TestEnsureClusterValidatorRBAC_WritableResources(t *testing.T) {
	client := fake.NewSimpleClientset()
	ctx := context.Background()

	const role = clusterValidatorControlPlaneRole
	const runID = "testrunid"
	require.NoError(t, ensureClusterValidatorRBAC(ctx, client, role, runID))

	cr, err := client.RbacV1().ClusterRoles().Get(ctx, clusterValidatorRBACName(role, runID), metav1.GetOptions{})
	require.NoError(t, err)

	type check struct {
		group    string
		resource string
		verb     string
	}
	required := []check{
		// Enforcement checks create and delete probe namespaces.
		{"", "namespaces", "create"},
		{"", "namespaces", "delete"},
		// Probe pods spun up for node-to-node and inter-namespace checks.
		{"", "pods", "create"},
		{"", "pods", "delete"},
		// Log reading: fetch probe output without exec.
		{"", "pods/log", "get"},
		// Active LB probe service.
		{"", "services", "create"},
		{"", "services", "delete"},
		// Enforcement check creates/updates/deletes NetworkPolicies in temp namespace.
		{"networking.k8s.io", "networkpolicies", "create"},
		{"networking.k8s.io", "networkpolicies", "update"},
		{"networking.k8s.io", "networkpolicies", "delete"},
		// Gateway API health checks.
		{"gateway.networking.k8s.io", "gatewayclasses", "get"},
		{"gateway.networking.k8s.io", "gateways", "list"},
		{"gateway.networking.k8s.io", "httproutes", "get"},
		{"gateway.networking.k8s.io", "grpcroutes", "list"},
	}

	for _, want := range required {
		t.Run(fmt.Sprintf("%s/%s/%s", want.group, want.resource, want.verb), func(t *testing.T) {
			assert.True(t, rbacRuleCovers(cr.Rules, want.group, want.resource, want.verb),
				"ClusterRole must grant %s on %s (group %q)", want.verb, want.resource, want.group)
		})
	}
}

// rbacRuleCovers returns true when any PolicyRule in rules grants verb on
// resource within group. Wildcard verbs ("*") are treated as matching any verb.
func rbacRuleCovers(rules []rbacv1.PolicyRule, group, resource, verb string) bool {
	for _, r := range rules {
		if len(r.NonResourceURLs) > 0 {
			continue // non-resource rules don't apply to API resources
		}
		if !strSliceContains(r.APIGroups, group) {
			continue
		}
		if !strSliceContains(r.Resources, resource) {
			continue
		}
		if strSliceContains(r.Verbs, verb) || strSliceContains(r.Verbs, "*") {
			return true
		}
	}
	return false
}

func strSliceContains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
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
	res := runClusterValidator(context.Background(), client, "test-image:1.0", "", false, "", nil)
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
	res := runClusterValidator(context.Background(), client, "test-image:1.0", "", false, "", nil)

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
	res := runClusterValidator(ctx, client, "test-image:1.0", "", false, "", nil)
	require.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "context")
}

func TestBuildClusterValidatorJobShape(t *testing.T) {
	job := buildClusterValidatorJob("test-job", "img:1", "", "", "runid", false)

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
	assert.Equal(t, clusterValidatorRBACName("", "runid"), job.Spec.Template.Spec.ServiceAccountName,
		"pod must run under this run's bootstrapped SA, not the namespace default")

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

func TestBuildClusterValidatorJobShape_ValidatorRoleInEnv(t *testing.T) {
	job := buildClusterValidatorJob("test-job", "img:1", "", clusterValidatorControlPlaneRole, "runid", false)
	env := map[string]string{}
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	assert.Equal(t, clusterValidatorControlPlaneRole, env["VALIDATOR_ROLE"],
		"VALIDATOR_ROLE must carry the role to the validator binary")
}

func TestBuildClusterValidatorJobShape_WithPullSecret(t *testing.T) {
	job := buildClusterValidatorJob("test-job", "img:1", "nvcr-pull-secret", "", "runid", false)
	require.Len(t, job.Spec.Template.Spec.ImagePullSecrets, 1)
	assert.Equal(t, "nvcr-pull-secret", job.Spec.Template.Spec.ImagePullSecrets[0].Name)
}

func TestBuildClusterValidatorJobShape_NoPullSecret(t *testing.T) {
	job := buildClusterValidatorJob("test-job", "img:1", "", "", "runid", false)
	assert.Empty(t, job.Spec.Template.Spec.ImagePullSecrets,
		"empty pull-secret arg must not produce an empty-name ImagePullSecrets entry")
}

func TestBuildClusterValidatorJobShape_NoCleanup(t *testing.T) {
	job := buildClusterValidatorJob("test-job", "img:1", "", "", "runid", true)
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
	got := kubectlLogsHint("", "nvcf-preflight-validator-12345")
	assert.Equal(t,
		"kubectl logs -n default job/nvcf-preflight-validator-12345 --tail=-1",
		got,
	)
}

// In ModeSplit the two roles run against different clusters, so the hint has
// to name the context the Job was actually created in.
func TestKubectlLogsHint_PinsContext(t *testing.T) {
	got := kubectlLogsHint("gpu-ctx", "nvcf-preflight-validator-12345")
	assert.Equal(t,
		"kubectl --context gpu-ctx logs -n default job/nvcf-preflight-validator-12345 --tail=-1",
		got,
	)
}

func TestKubectlLogsHint_QuotesContext(t *testing.T) {
	got := kubectlLogsHint("my ctx", "nvcf-preflight-validator-12345")
	assert.Contains(t, got, "--context 'my ctx'",
		"a context name with a space must be quoted so the pasted command does not split it")
}

func TestKubectlLogsHint_EmptyJob(t *testing.T) {
	assert.Equal(t, "", kubectlLogsHint("gpu-ctx", ""),
		"empty jobName must produce empty hint so callers can compose detail without conditionals")
}

func TestBuildControlPlaneValidatorConfig_NoExtras(t *testing.T) {
	got := buildControlPlaneValidatorConfig(nil)
	assert.Equal(t, controlPlaneValidatorConfigTemplate, got,
		"no extra registries must return the template unchanged")
	assert.Contains(t, got, "nvcr.io", "nvcr.io must always be present")
	assert.Contains(t, got, "enforcement:", "enforcement block must be present")
}

func TestBuildControlPlaneValidatorConfig_WithExtras(t *testing.T) {
	got := buildControlPlaneValidatorConfig([]string{"harbor.company.internal:443", "ghcr.io:443"})
	assert.Contains(t, got, "harbor.company.internal")
	assert.Contains(t, got, "ghcr.io")
	assert.Contains(t, got, "nvcr.io", "nvcr.io must still be present alongside extras")
	assert.Contains(t, got, "enforcement:", "enforcement block must still be present after extras")
	// Extra registries must appear BEFORE enforcement.
	harborIdx := strings.Index(got, "harbor.company.internal")
	enforcementIdx := strings.Index(got, "enforcement:")
	assert.Less(t, harborIdx, enforcementIdx, "extra registry endpoints must appear before the enforcement block")
}

func TestBuildControlPlaneValidatorConfig_InvalidRegistrySkipped(t *testing.T) {
	// A blank entry is parsed as host="" → skipped; only the valid entry appears.
	got := buildControlPlaneValidatorConfig([]string{"  ", "valid.registry.internal:5000"})
	assert.Contains(t, got, "valid.registry.internal", "valid registry must appear")
	// The blank entry must not add an empty host: line.
	assert.NotContains(t, got, "host: \n", "blank entry must not produce an empty host line")
}

func TestParseRegistryHostPort(t *testing.T) {
	tests := []struct {
		in       string
		wantHost string
		wantPort int
	}{
		{"nvcr.io:443", "nvcr.io", 443},
		{"harbor.company.internal:5000", "harbor.company.internal", 5000},
		{"registry.example.com", "registry.example.com", 443}, // no port → 443
		{"", "", 0},   // empty → skip
		{"  ", "", 0}, // blank → skip
		// IPv6: net.SplitHostPort handles bracketed literals correctly.
		{"[::1]:5000", "::1", 5000},
		{"[2001:db8::1]:443", "2001:db8::1", 443},
		// An explicit but unusable port is a typo, not a request for 443:
		// probing a different endpoint than configured reports a result for
		// something the operator never asked about.
		{"nvcr.io:", "", 0},
		{"nvcr.io:abc", "", 0},
		{"nvcr.io:0", "", 0},
		{"nvcr.io:70000", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			h, p := parseRegistryHostPort(tt.in)
			assert.Equal(t, tt.wantHost, h)
			assert.Equal(t, tt.wantPort, p)
		})
	}
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

// The same invariant for the network-check ConfigMap.
func TestEnsureClusterValidatorConfig_RefusesUnmanagedConfigMap(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterValidatorConfigName,
			Namespace: clusterValidatorNamespace,
			Labels:    map[string]string{"owner": "operator"},
		},
		Data: map[string]string{"config.yaml": "operator: content"},
	})

	err := ensureClusterValidatorConfig(ctx, client, nil)
	require.Error(t, err, "an unmanaged ConfigMap must not be overwritten")
	assert.Contains(t, err.Error(), "not managed by nvcf-cli")

	got, getErr := client.CoreV1().ConfigMaps(clusterValidatorNamespace).Get(ctx,
		clusterValidatorConfigName, metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.Equal(t, "operator: content", got.Data["config.yaml"],
		"the operator's ConfigMap content must be untouched")
}

// The managed labels are three public constants, so a label check alone cannot
// establish ownership: anyone able to create a ServiceAccount in the probe
// namespace could stamp them. Safety comes from the name being unguessable, so
// there is no predictable object to pre-create and get bound to the validator's
// cluster-wide permissions.
func TestClusterValidatorRBACName_IsUnpredictableAndPerRun(t *testing.T) {
	a, err := newValidatorRunID()
	require.NoError(t, err)
	b, err := newValidatorRunID()
	require.NoError(t, err)

	assert.NotEqual(t, a, b, "each run must get a distinct identity")
	assert.NotEmpty(t, a)

	const role = clusterValidatorControlPlaneRole
	assert.NotEqual(t, clusterValidatorRBACName(role, a), clusterValidatorRBACName(role, b),
		"RBAC names must differ per run")
	assert.NotEqual(t, clusterValidatorRBACName(role, a), clusterValidatorRBACName("compute-plane", a),
		"RBAC names must differ per role within a run")
}

// Nothing pre-existing is adopted: creation is unconditional, so a squatted
// object surfaces as an error rather than being bound to our ClusterRole.
func TestEnsureClusterValidatorRBAC_DoesNotAdoptExistingObjects(t *testing.T) {
	ctx := context.Background()
	const role = clusterValidatorControlPlaneRole
	const runID = "collide"
	name := clusterValidatorRBACName(role, runID)

	// Forged managed labels: a label check alone would have adopted this.
	client := fake.NewSimpleClientset(&corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: clusterValidatorNamespace, Labels: clusterValidatorLabels(),
		},
	})

	err := ensureClusterValidatorRBAC(ctx, client, role, runID)
	require.Error(t, err, "a pre-existing object must not be adopted, forged labels or not")

	_, bindErr := client.RbacV1().ClusterRoleBindings().Get(ctx, name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(bindErr),
		"no binding may be created when the ServiceAccount was not created by this run")
}

// Random names cannot self-heal by being reused next run, so leftovers from a
// killed run are reclaimed by age. Two things must survive: anything recent
// (it may belong to a run happening right now) and anything whose name is not
// one we generate, even when it carries our labels. Those labels are three
// public constants, so they can be copied onto anything.
func TestSweepOrphanClusterValidatorRBAC_RequiresNameLabelsAndAge(t *testing.T) {
	ctx := context.Background()
	old := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	now := metav1.NewTime(time.Now())
	staleName := clusterValidatorRBACName(clusterValidatorControlPlaneRole, "deadbeef01")
	freshName := clusterValidatorRBACName("compute-plane", "cafebabe02")

	client := fake.NewSimpleClientset(
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{
			Name: staleName, Labels: clusterValidatorLabels(), CreationTimestamp: old,
		}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{
			Name: freshName, Labels: clusterValidatorLabels(), CreationTimestamp: now,
		}},
		// Our labels, but not a name we generate: an operator copying the
		// labels onto their own ClusterRole must not have it deleted.
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{
			Name: "operator-owned-role", Labels: clusterValidatorLabels(), CreationTimestamp: old,
		}},
	)

	sweepOrphanClusterValidatorRBAC(ctx, client, orphanValidatorRBACTTL)

	_, err := client.RbacV1().ClusterRoles().Get(ctx, staleName, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "a generated name past the TTL must be reclaimed")

	_, err = client.RbacV1().ClusterRoles().Get(ctx, freshName, metav1.GetOptions{})
	assert.NoError(t, err, "a recent one may belong to a concurrent run")

	_, err = client.RbacV1().ClusterRoles().Get(ctx, "operator-owned-role", metav1.GetOptions{})
	assert.NoError(t, err, "matching labels alone must not authorize deleting someone else's object")
}

// An explicit but unparseable port is a typo. Silently probing 443 would report
// a result for an endpoint the operator never configured.
func TestParseRegistryHostPort_RejectsMalformedExplicitPort(t *testing.T) {
	for _, in := range []string{"registry.example:abc", "registry.example:0", "registry.example:99999", "registry.example:"} {
		host, port := parseRegistryHostPort(in)
		assert.Empty(t, host, "%q must be rejected, not defaulted", in)
		assert.Zero(t, port, "%q must not fall back to a port", in)
	}
	host, port := parseRegistryHostPort("registry.example")
	assert.Equal(t, "registry.example", host, "no explicit port keeps the default")
	assert.Equal(t, 443, port)
}

// The prior-run Job sweep deletes by label plus generated name. Labels alone
// are three public constants, so a Job carrying them under an unrelated name is
// not ours to delete.
func TestSweepPriorClusterValidatorJobs_RequiresGeneratedName(t *testing.T) {
	ctx := context.Background()
	const role = clusterValidatorControlPlaneRole
	ours := clusterValidatorName + "-1700000000"

	client := fake.NewSimpleClientset(
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Name: ours, Namespace: clusterValidatorNamespace,
			Labels: clusterValidatorRoleLabels(role),
		}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Name: "operator-owned-job", Namespace: clusterValidatorNamespace,
			Labels: clusterValidatorRoleLabels(role),
		}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Name: clusterValidatorName + "-9999999999", Namespace: clusterValidatorNamespace,
			Labels: clusterValidatorRoleLabels("compute-plane"),
		}},
	)

	sweepPriorClusterValidatorJobs(ctx, client, role)

	jobs := client.BatchV1().Jobs(clusterValidatorNamespace)
	_, err := jobs.Get(ctx, ours, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "this role's prior Job must be swept")

	_, err = jobs.Get(ctx, "operator-owned-job", metav1.GetOptions{})
	assert.NoError(t, err, "matching labels alone must not authorize deleting someone else's Job")

	_, err = jobs.Get(ctx, clusterValidatorName+"-9999999999", metav1.GetOptions{})
	assert.NoError(t, err, "the other role's Job must survive")
}
