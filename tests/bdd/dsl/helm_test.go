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

package dsl

import (
	"strings"
	"testing"
)

const deployedHelmReleases = `[
  {"name":"nats","namespace":"nats-system","revision":"2","status":"deployed"},
  {"name":"api","namespace":"nvcf","revision":3,"status":"failed"}
]`

func TestHelmListCommandUsesExplicitContext(t *testing.T) {
	t.Setenv("BDD_TMP_CONTEXT", "k3d-ncp-local")
	got, err := HelmListCommand("${BDD_TMP_CONTEXT}")
	if err != nil {
		t.Fatalf("build command: %v", err)
	}
	want := "helm list --all-namespaces --kube-context k3d-ncp-local -o json"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestHelmListCommandRejectsEmptyContext(t *testing.T) {
	if _, err := HelmListCommand(""); err == nil {
		t.Fatal("expected empty context error")
	}
}

func TestHelmReleasesDeployedMatchesOptionalRevision(t *testing.T) {
	expected := []HelmReleaseExpectation{{Name: "nats", Namespace: "nats-system", Revision: "2"}}
	if err := HelmReleasesDeployed(deployedHelmReleases, expected); err != nil {
		t.Fatalf("assert releases: %v", err)
	}

	expected[0].Revision = ""
	if err := HelmReleasesDeployed(deployedHelmReleases, expected); err != nil {
		t.Fatalf("assert release without revision: %v", err)
	}
}

func TestHelmReleasesDeployedReportsMissingRelease(t *testing.T) {
	err := HelmReleasesDeployed(deployedHelmReleases, []HelmReleaseExpectation{{Name: "missing", Namespace: "nvcf"}})
	if err == nil || !strings.Contains(err.Error(), `helm release "missing" in namespace "nvcf" is missing`) {
		t.Fatalf("error = %v", err)
	}
}

func TestHelmReleasesDeployedReportsNamespaceMismatch(t *testing.T) {
	err := HelmReleasesDeployed(deployedHelmReleases, []HelmReleaseExpectation{{Name: "nats", Namespace: "wrong"}})
	if err == nil || !strings.Contains(err.Error(), `namespace = "nats-system", want "wrong"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestHelmReleasesDeployedReportsStatusMismatch(t *testing.T) {
	err := HelmReleasesDeployed(deployedHelmReleases, []HelmReleaseExpectation{{Name: "api", Namespace: "nvcf"}})
	if err == nil || !strings.Contains(err.Error(), `status = "failed", want "deployed"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestHelmReleasesDeployedReportsRevisionMismatch(t *testing.T) {
	err := HelmReleasesDeployed(deployedHelmReleases, []HelmReleaseExpectation{{Name: "nats", Namespace: "nats-system", Revision: "1"}})
	if err == nil || !strings.Contains(err.Error(), `revision = "2", want "1"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestHelmReleasesDeployedRejectsMalformedJSON(t *testing.T) {
	if err := HelmReleasesDeployed("not json", []HelmReleaseExpectation{{Name: "nats", Namespace: "nats-system"}}); err == nil {
		t.Fatal("expected parse error")
	}
}
