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
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"nvcf-cli/internal/selfhosted"
	"nvcf-cli/internal/selfhosted/progress"
)

func TestSelfHostedUmbrellaSelectionPrecedence(t *testing.T) {
	oldRelease, oldBundle, oldDigest, oldCache := selfHostedUmbrellaRelease, selfHostedUmbrellaBundle, selfHostedUmbrellaSHA256, selfHostedUmbrellaCacheDir
	oldControl, oldCompute := selfHostedControlPlaneStack, selfHostedComputePlaneStack
	t.Cleanup(func() {
		selfHostedUmbrellaRelease, selfHostedUmbrellaBundle, selfHostedUmbrellaSHA256, selfHostedUmbrellaCacheDir = oldRelease, oldBundle, oldDigest, oldCache
		selfHostedControlPlaneStack, selfHostedComputePlaneStack = oldControl, oldCompute
	})
	for _, test := range []struct {
		name, release, bundle, digest, control, compute string
		fail                                            bool
	}{
		{name: "legacy component sources", control: "oci://existing", compute: "local"},
		{name: "umbrella", release: "1.0.3"},
		{name: "control override", release: "1.0.3", control: "local", fail: true},
		{name: "compute override", release: "1.0.3", compute: "local", fail: true},
		{name: "both umbrella selectors", release: "1.0.3", bundle: "bundle.tar.gz", fail: true},
		{name: "unpinned local", bundle: "bundle.tar.gz", fail: true},
		{name: "local", bundle: "bundle.tar.gz", digest: "sha256:" + strings.Repeat("a", 64)},
	} {
		t.Run(test.name, func(t *testing.T) {
			selfHostedUmbrellaRelease, selfHostedUmbrellaBundle, selfHostedUmbrellaSHA256 = test.release, test.bundle, test.digest
			selfHostedControlPlaneStack, selfHostedComputePlaneStack = test.control, test.compute
			err := validateSelfHostedUmbrellaSelection()
			if (err != nil) != test.fail {
				t.Fatalf("unexpected precedence result: %v", err)
			}
		})
	}
}

func TestUmbrellaStackDescriptorPreservesComponentIdentity(t *testing.T) {
	identity := &selfhosted.UmbrellaIdentity{Version: "2.0.3", Status: "development", SHA256: "sha256:" + strings.Repeat("a", 64), Stacks: map[string]selfhosted.UmbrellaStack{"control-plane": {Version: "1.0.1"}, "compute-plane": {Version: "1.0.2"}, "observability": {Version: "1.0.0"}}}
	text := stackDescriptor(&selfhosted.ResolvedStack{Path: "/fixture/control", Source: "umbrella", Umbrella: identity})
	for _, part := range []string{identity.Version, identity.Status, identity.SHA256} {
		if !strings.Contains(text, part) {
			t.Fatalf("missing %s in %s", part, text)
		}
	}
	for plane, stack := range identity.Stacks {
		if !strings.Contains(text, plane+" "+stack.Version) {
			t.Fatalf("component identity omitted: %s", text)
		}
	}
	if strings.Contains(text, "qualified") {
		t.Fatal("development selection described as qualified")
	}
}

func TestUmbrellaSelectionDoesNotReplaceObservedStatus(t *testing.T) {
	collector := &statusWithSelection{StatusCollector: &fakeCollector{}, detail: "Selected NVCF fixture"}
	sink := &statusCaptureSink{}
	if err := runStatusLoop(context.Background(), collector, sink, false, time.Second); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) < 2 {
		t.Fatal("missing selection and observed status events")
	}
	selected, ok := sink.events[0].(progress.LastProgress)
	if !ok || selected.Detail != collector.detail {
		t.Fatal("selection diagnostic missing")
	}
	found := false
	for _, event := range sink.events {
		if snapshot, ok := event.(progress.Snapshot); ok {
			found = true
			if snapshot.Cluster != "test-cluster" || snapshot.Identity.StackVersion != "" {
				t.Fatal("selected bundle replaced observed installed identity")
			}
		}
	}
	if !found {
		t.Fatal("observed snapshot missing")
	}
}

func TestComputePlaneUmbrellaHandoffRetainsCache(t *testing.T) {
	oldRelease, oldBundle, oldCache := selfHostedUmbrellaRelease, selfHostedUmbrellaBundle, selfHostedUmbrellaCacheDir
	oldOutput := computePlaneRegisterOutput
	t.Cleanup(func() {
		selfHostedUmbrellaRelease, selfHostedUmbrellaBundle, selfHostedUmbrellaCacheDir = oldRelease, oldBundle, oldCache
		computePlaneRegisterOutput = oldOutput
	})
	computePlaneRegisterOutput = ""
	stack := &selfhosted.ResolvedStack{Path: writeComputePlaneStack(t), Umbrella: &selfhosted.UmbrellaIdentity{Version: "2.0.3", SHA256: "sha256:" + strings.Repeat("a", 64)}}
	for _, selector := range []string{"release", "bundle"} {
		for _, cache := range []string{"", "relative cache with spaces"} {
			t.Run(selector+"/"+cache, func(t *testing.T) {
				selfHostedUmbrellaRelease, selfHostedUmbrellaBundle = "", ""
				selfHostedUmbrellaCacheDir = cache
				if selector == "release" {
					selfHostedUmbrellaRelease = stack.Umbrella.Version
				} else {
					selfHostedUmbrellaBundle = "bundle with spaces.tar.gz"
				}
				handoff, err := computePlaneHandoffForStack(stack, "gpu-a")
				if err != nil {
					t.Fatal(err)
				}
				var output bytes.Buffer
				command := &cobra.Command{}
				command.SetOut(&output)
				writeComputePlaneRegisterSummary(command, computePlaneRegisterSummary{Registration: &selfhosted.RegisterResponse{}, Handoff: handoff})
				if cache == "" {
					if strings.Contains(output.String(), "--umbrella-cache-dir") {
						t.Fatal("implicit cache unexpectedly forwarded")
					}
				} else {
					absolute, err := filepath.Abs(cache)
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(output.String(), "--umbrella-cache-dir "+shellQuote(absolute)) {
						t.Fatalf("handoff lost explicit cache: %s", &output)
					}
				}
				if !strings.Contains(output.String(), "--umbrella-"+selector+" ") || !strings.Contains(output.String(), stack.Umbrella.SHA256) {
					t.Fatalf("handoff lost pinned selection: %s", &output)
				}
			})
		}
	}
}
