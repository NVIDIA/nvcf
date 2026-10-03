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

package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"nvcf-cli/internal/selfhosted"
	"nvcf-cli/internal/state"
)

// Default-stub every seam that would otherwise reach the developer's cluster or
// the network. Individual tests reassign a variable when they explicitly want
// to exercise that path.
//
// These are not conveniences. Without them `go test ./cmd/` creates a hostPath
// busybox pod per node in `default` (the inotify probe), lists Secrets across
// the stack namespaces of whatever kubeconfig happens to be current, and makes
// an outbound request to nvcr.io per configured registry. That mutates a real
// cluster from a unit test, and it is why the package took minutes and failed
// on a proxied kubeconfig rather than seconds and deterministically.
func TestMain(m *testing.M) {
	// Every command reads ~/.nvcf-cli.yaml and some tests save
	// ~/.nvcf-cli.state, so a developer's real config would steer results and
	// a test run would overwrite their saved credentials.
	home, err := os.MkdirTemp("", "nvcf-cli-cmd-test-home-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("HOME", home)
	// The state manager resolved its path from HOME at package init, before
	// the swap above, so it still points at the real ~/.nvcf-cli.state.
	state.ResetDefaultStateManager()
	// Check reads the stack above the working directory when no
	// --control-plane-stack is given, so run from a directory with none: from
	// the package directory every test would read the repository's stack and
	// any untracked environment file next to it.
	if err := os.Chdir(home); err != nil {
		panic(err)
	}
	// A developer's own toggles, validator settings and NGC keys would change
	// what every check test runs. The NVCF_ prefixed names are viper's
	// automatic aliases of the cluster_validator_* config keys.
	for _, k := range []string{
		"NVCF_CLI_SELFHOSTED_LOCAL_ONLY", "NVCF_CLI_SELFHOSTED_SKIP_CLUSTER_VALIDATION",
		"NVCF_CLI_SELFHOSTED_SKIP_INOTIFY",
		"NVCF_CLI_CLUSTER_VALIDATOR_IMAGE", "NVCF_CLI_CLUSTER_VALIDATOR_REGISTRIES",
		"NVCF_CLI_CLUSTER_VALIDATOR_PROBE_IMAGE", "NVCF_CLI_CLUSTER_VALIDATOR_TOLERATIONS",
		"NVCF_CLI_CLUSTER_VALIDATOR_EXTERNAL_COMPONENTS",
		"NVCF_CLUSTER_VALIDATOR_IMAGE", "NVCF_CLUSTER_VALIDATOR_REGISTRIES",
		"NVCF_CLUSTER_VALIDATOR_PROBE_IMAGE", "NVCF_CLUSTER_VALIDATOR_TOLERATIONS",
		"NVCF_CLUSTER_VALIDATOR_EXTERNAL_COMPONENTS", "NVCF_EXTERNAL_COMPONENTS",
		"NVCF_OPENBAO_NAMESPACE", "NVCF_ENVOY_GATEWAY_NAMESPACE", "NVCF_GATEWAY_NAMES",
		"NVCF_N2N_PROBE_IMAGE", "HELMFILE_ENV",
		"NGC_IMAGE_PULL_API_KEY", "NVCF_NGCR_API_KEY", "NVCF_NGC_API_KEY", "NGC_API_KEY",
	} {
		_ = os.Unsetenv(k)
	}
	// The real tool checks run `helmfile version`, which asks github.com for
	// the latest release, and pass or fail on what the machine has installed.
	checkPreflightTools = passingPreflightTools
	resolveLatestValidatorTagForSelfHosted = func(_ context.Context, _ string) (string, bool) {
		return "", false
	}
	// Nil prober: the inotify check is skipped rather than creating pods.
	newInotifyProberForSelfHosted = func(string) selfhosted.NodeInotifyProber { return nil }
	// No cluster contact, and a clean result so the category still renders.
	newStaleNamespaceProberForSelfHosted = func() selfhosted.StaleNamespaceProber {
		return func(context.Context, string, []string) ([]selfhosted.StaleNamespace, error) {
			return nil, nil
		}
	}
	// No outbound registry request.
	newRegistryCredentialCheckerForSelfHosted = func() selfhosted.RegistryCredentialChecker {
		return func(context.Context, string, string, bool) error { return nil }
	}
	// No validator Job. A test that passes --cluster-validator-image would
	// otherwise create RBAC, Secrets and Jobs in the current kube context and
	// wait up to five minutes per role.
	newClusterValidatorForSelfHosted = func() selfhosted.ClusterValidator {
		return func(_ context.Context, p selfhosted.ClusterValidatorParams) selfhosted.ClusterValidatorResult {
			return selfhosted.ClusterValidatorResult{Passed: true, Logs: "Validator role: " + p.Role + "\nCluster is NVCF-Ready\n"}
		}
	}
	// The SIS reachability check has no seam, but it resolves its URL from
	// NVCF_ICMS_URL, so resetCheckFlags points check tests at this local
	// server instead of the real SIS. Scoped per test: setting it for the
	// whole package would change what the ICMS URL resolution tests resolve.
	sis := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	testSISURL = sis.URL
	code := m.Run()
	sis.Close()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// testSISURL is a local stand-in for SIS, set by TestMain.
var testSISURL string

// resetFlag returns f to its default value and clears its Changed marker. A
// slice flag is replaced, since Set appends once the flag has been set.
func resetFlag(f *pflag.Flag) {
	if sv, ok := f.Value.(pflag.SliceValue); ok {
		_ = sv.Replace(nil)
	} else {
		_ = f.Value.Set(f.DefValue)
	}
	f.Changed = false
}

// resetCheckFlags returns every `self-hosted check` flag to its default,
// including cobra's Changed marker, now and when the test ends. The flag
// variables are package globals that survive rootCmd.Execute, so without this
// a test that passes --pre leaks it into whichever test runs next, and a
// regression guard can pass or fail on test order alone.
func resetCheckFlags(t *testing.T) {
	t.Helper()
	if testSISURL != "" {
		t.Setenv("NVCF_ICMS_URL", testSISURL)
	}
	reset := func() {
		// cobra keeps the first context a command ran with, so a test that
		// cancelled rootCmd's context would otherwise end every later run
		// as interrupted.
		selfHostedCheckCmd.SetContext(context.Background())
		checkPre, checkControlPlane, checkComputePlane, checkAll = false, false, false, false
		checkClusterName = ""
		checkLocalOnly, checkSkipInotifyCheck, checkSkipClusterValidation = false, false, false
		checkClusterValidatorImage, checkClusterValidatorPullSecret = "", ""
		checkClusterValidatorNoCleanup = false
		checkClusterValidatorRegistries = nil
		checkClusterValidatorProbeImage = ""
		checkClusterValidatorTolerations = nil
		checkClusterValidatorExternal = nil
		checkShowLogs = false
		selfHostedJSON, selfHostedPlain = false, false
		selfHostedOutput = "text"
		selfHostedWait = ""
		selfHostedControlPlaneContext, selfHostedComputePlaneContext = "", ""
		// Values too, not only the Changed marker: a test that passes
		// --icms-url would otherwise point every later check at its URL.
		for _, fs := range []*pflag.FlagSet{selfHostedCheckCmd.Flags(), selfHostedCmd.PersistentFlags()} {
			fs.VisitAll(resetFlag)
		}
		// Other tests call viper.Reset(), which drops the bindings made at
		// init, so a flag passed to check would silently not be read.
		for key, flag := range map[string]string{
			"cluster_validator_image":               "cluster-validator-image",
			"cluster_validator_registries":          "cluster-validator-registries",
			"cluster_validator_probe_image":         "cluster-validator-probe-image",
			"cluster_validator_tolerations":         "cluster-validator-tolerations",
			"cluster_validator_external_components": "cluster-validator-external-components",
		} {
			_ = viper.BindPFlag(key, selfHostedCheckCmd.Flags().Lookup(flag))
		}
	}
	reset()
	t.Cleanup(reset)
}

// passingPreflightTools is the real tool set with every tool found at its
// minimum version.
func passingPreflightTools() []selfhosted.BinarySpec {
	specs := selfHostedPreflightTools()
	for i := range specs {
		v := specs[i].MinVer
		if v == nil {
			v = semver.MustParse("1.0.0")
		}
		name := specs[i].Name
		specs[i].LookPath = func(string) (string, error) { return "/usr/local/bin/" + name, nil }
		specs[i].Version = func(context.Context, string) (*semver.Version, error) { return v, nil }
	}
	return specs
}
