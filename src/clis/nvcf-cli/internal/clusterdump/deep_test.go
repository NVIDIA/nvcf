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

package clusterdump

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestDumpOptions(t *testing.T) {
	assert.False(t, DumpOptions{}.Deep())
	o := DumpOptions{Sections: map[string]bool{SectionResources: true}}
	assert.True(t, o.Deep())
	assert.True(t, o.Has(SectionResources))
	assert.False(t, o.Has(SectionLogs))

	all := DumpOptions{Sections: map[string]bool{SectionAll: true}}
	assert.True(t, all.Has(SectionLogs))
	assert.True(t, all.Has(SectionHelm))
	assert.True(t, all.Has("anything"), "SectionAll matches any section name")

	// Redaction defaults: empty mode masks secrets but not "all".
	assert.True(t, DumpOptions{}.redactSecrets())
	assert.False(t, DumpOptions{}.redactAll())
	assert.False(t, DumpOptions{Redact: RedactNone}.redactSecrets())
	assert.True(t, DumpOptions{Redact: RedactAll}.redactAll())
}

func TestMaskValue(t *testing.T) {
	assert.Equal(t, "", maskValue(""))
	assert.Equal(t, "****", maskValue("abcd"))
	// Long values keep a head/tail hint and are clearly marked.
	out := maskValue(strings.Repeat("x", 60))
	assert.True(t, strings.HasPrefix(out, "xxxx"))
	assert.Contains(t, out, "(redacted)")
	assert.NotContains(t, out, strings.Repeat("x", 40))
}

func TestRedactObject_Secret(t *testing.T) {
	secret := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]interface{}{"name": "s", "namespace": "nvcf"},
		"data": map[string]interface{}{
			"token": "c3VwZXItc2VjcmV0LXZhbHVlLXRoYXQtaXMtbG9uZw==",
		},
	}}
	redactObject(secret, DumpOptions{Redact: RedactSecrets})
	masked, _, _ := unstructured.NestedString(secret.Object, "data", "token")
	assert.NotEqual(t, "c3VwZXItc2VjcmV0LXZhbHVlLXRoYXQtaXMtbG9uZw==", masked)
	assert.Contains(t, masked, "(redacted)")

	// redact=none leaves the value intact.
	plain := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "Secret",
		"data": map[string]interface{}{"token": "c3VwZXItc2VjcmV0LXZhbHVl"},
	}}
	redactObject(plain, DumpOptions{Redact: RedactNone})
	v, _, _ := unstructured.NestedString(plain.Object, "data", "token")
	assert.Equal(t, "c3VwZXItc2VjcmV0LXZhbHVl", v)
}

// allCoreListKinds maps every captured GVR to its List kind so the fake dynamic
// client can answer List for each.
func allCoreListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		{Group: "", Version: "v1", Resource: "pods"}:                   "PodList",
		{Group: "", Version: "v1", Resource: "services"}:               "ServiceList",
		{Group: "", Version: "v1", Resource: "endpoints"}:              "EndpointsList",
		{Group: "", Version: "v1", Resource: "configmaps"}:             "ConfigMapList",
		{Group: "", Version: "v1", Resource: "secrets"}:                "SecretList",
		{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}: "PersistentVolumeClaimList",
		{Group: "", Version: "v1", Resource: "serviceaccounts"}:        "ServiceAccountList",
		{Group: "apps", Version: "v1", Resource: "deployments"}:        "DeploymentList",
		{Group: "apps", Version: "v1", Resource: "statefulsets"}:       "StatefulSetList",
		{Group: "apps", Version: "v1", Resource: "daemonsets"}:         "DaemonSetList",
		{Group: "apps", Version: "v1", Resource: "replicasets"}:        "ReplicaSetList",
		{Group: "batch", Version: "v1", Resource: "jobs"}:              "JobList",
		{Group: "batch", Version: "v1", Resource: "cronjobs"}:          "CronJobList",
	}
}

func unstructuredObj(apiVersion, kind, ns, name string, extra map[string]interface{}) *unstructured.Unstructured {
	obj := map[string]interface{}{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
	}
	for k, v := range extra {
		obj[k] = v
	}
	return &unstructured.Unstructured{Object: obj}
}

func TestCollectStackResources(t *testing.T) {
	objs := []runtime.Object{
		unstructuredObj("v1", "ConfigMap", "nvcf", "cfg", map[string]interface{}{
			"data": map[string]interface{}{"key": "value"},
		}),
		unstructuredObj("v1", "Secret", "nvcf", "sec", map[string]interface{}{
			"data": map[string]interface{}{"password": "bG9uZy1zZWNyZXQtdmFsdWUtZm9yLXRlc3RpbmctMTIz"},
		}),
	}
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), allCoreListKinds(), objs...)
	pc := &PlaneCollector{
		Role:       RoleControlPlane,
		Dynamic:    dc,
		Namespaces: []string{"nvcf"},
		Options:    DumpOptions{Sections: map[string]bool{SectionResources: true}, Redact: RedactSecrets},
	}

	got, warns, err := pc.collectStackResources(context.Background())
	require.NoError(t, err)
	assert.Empty(t, warns)
	require.Len(t, got, 2)

	// Deterministic order: configmaps before secrets.
	assert.Equal(t, "configmaps", got[0].Resource)
	assert.Equal(t, "secrets", got[1].Resource)
	// Secret body is redacted.
	assert.NotContains(t, string(got[1].YAML), "bG9uZy1zZWNyZXQtdmFsdWUtZm9yLXRlc3RpbmctMTIz")
	assert.Contains(t, string(got[1].YAML), "redacted")
	// ConfigMap body is captured verbatim under default redaction.
	assert.Contains(t, string(got[0].YAML), "value")
}

func TestCollectPodLogs(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "nvcf"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}},
	}
	kube := k8sfake.NewSimpleClientset(pod)
	pc := &PlaneCollector{
		Role:       RoleControlPlane,
		Kube:       kube,
		Namespaces: []string{"nvcf"},
		Options:    DumpOptions{Sections: map[string]bool{SectionLogs: true}},
	}
	got, _, err := pc.collectPodLogs(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "nvcf", got[0].Namespace)
	assert.Equal(t, "p1", got[0].Pod)
	assert.Equal(t, "main", got[0].Container)
	assert.NotEmpty(t, got[0].Body)
}

func sampleDeepDump() Dump {
	return Dump{
		CollectedAt: time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC),
		ControlPlane: &PlaneSnapshot{
			Role:    RoleControlPlane,
			Context: "k3d-cp",
			Resources: []CapturedResource{
				{Kind: "Secret", Resource: "secrets", Namespace: "nvcf", Name: "sec", YAML: []byte("kind: Secret\ndata:\n  password: '**** (redacted)'\n")},
			},
			PodLogs: []PodLog{
				{Namespace: "nvcf", Pod: "p1", Container: "main", Body: []byte("hello\n")},
				{Namespace: "nvcf", Pod: "p1", Container: "main", Previous: true, Truncated: true, Body: []byte("old\n")},
			},
			Warnings: []string{"helm: not found"},
		},
	}
}

func TestRenderDirectoryAndArchiveParity(t *testing.T) {
	d := sampleDeepDump()

	dir := t.TempDir()
	require.NoError(t, RenderDirectory(dir, d))

	// Expected files exist with the right content.
	secPath := filepath.Join(dir, "control-plane", "namespaces", "nvcf", "secrets", "sec.yaml")
	body, err := os.ReadFile(secPath)
	require.NoError(t, err)
	assert.Contains(t, string(body), "redacted")

	prevLog := filepath.Join(dir, "control-plane", "namespaces", "nvcf", "pods", "p1", "main.previous.log")
	pl, err := os.ReadFile(prevLog)
	require.NoError(t, err)
	assert.Contains(t, string(pl), "truncated")

	for _, top := range []string{"dump.json", "summary.txt", "upload.txt"} {
		_, err := os.Stat(filepath.Join(dir, top))
		assert.NoError(t, err, top)
	}
	up, err := os.ReadFile(filepath.Join(dir, "upload.txt"))
	require.NoError(t, err)
	assert.Contains(t, string(up), "summary.txt")
	assert.Contains(t, string(up), "helm: not found")

	// Archive contains exactly the same file set.
	var buf bytes.Buffer
	require.NoError(t, RenderArchive(&buf, d))
	archiveSet := map[string]bool{}
	gzr, err := gzip.NewReader(&buf)
	require.NoError(t, err)
	tr := tar.NewReader(gzr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		archiveSet[h.Name] = true
	}

	dirSet := map[string]bool{}
	require.NoError(t, filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		dirSet[filepath.ToSlash(rel)] = true
		return nil
	}))
	assert.Equal(t, dirSet, archiveSet)
}
