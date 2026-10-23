// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"testing"
)

// TestNATSInventoryPreservesUpstreamPins checks the catalog-to-inventory contract
// without copying release versions into fixtures. Helm rendering is covered by
// deploy/stacks/self-managed/tests/nats-upstream-images.sh.
func TestNATSInventoryPreservesUpstreamPins(t *testing.T) {
	catalog, err := LoadCatalog("../../docs/version-catalog/main.yaml")
	if err != nil {
		t.Fatal(err)
	}
	wantRepositories := map[string]string{
		"nats-box":                    "docker.io/natsio/nats-box",
		"nats-server":                 "docker.io/synadia/nats-server",
		"nats-server-config-reloader": "docker.io/natsio/nats-server-config-reloader",
	}
	for _, artifact := range catalog.Artifacts {
		wantRepository, ok := wantRepositories[artifact.Name]
		if !ok || artifact.Type != ArtifactTypeImage {
			continue
		}
		delete(wantRepositories, artifact.Name)
		if artifact.UpstreamRepository != wantRepository {
			t.Fatalf("%s repository = %q, want %q", artifact.Name, artifact.UpstreamRepository, wantRepository)
		}
		if artifact.Name == "nats-box" && !lowercaseSHA256DigestRe.MatchString(artifact.Digest) {
			t.Fatalf("box index digest is missing or invalid: %q", artifact.Digest)
		}
		reference, err := catalog.artifactPath(artifact)
		if err != nil {
			t.Fatal(err)
		}
		inputs := []resolvedInventoryPlaneInput{{
			Name: "control-plane",
			ReleaseList: mustMarshalHelmfileReleases(t, []helmfileRelease{{
				Name: "nats", Chart: "nvcf/helm-nvcf-nats", Version: "0.0.1", Enabled: true, Installed: true,
			}}),
			ManifestByRelease: map[string][]byte{
				"nats": []byte(fmt.Sprintf("kind: Pod\nspec:\n  containers:\n    - name: nats\n      image: %s\n", reference)),
			},
		}}
		inventory, err := generateResolvedStackInventory(resolvedInventoryTestSource(), inputs)
		if err != nil {
			t.Fatal(err)
		}
		got := findResolvedInventoryArtifact(t, inventory, "container-image", reference)
		if got.Repository != wantRepository || got.Version != artifact.Version || got.Digest != artifact.Digest {
			t.Fatalf("inventory lost %s identity: %+v", artifact.Name, got)
		}
		if len(got.Sources) != 1 || got.Sources[0].Plane != "control-plane" || got.Sources[0].Requirement != ManifestRequired {
			t.Fatalf("inventory lost required control-plane ownership: %+v", got.Sources)
		}
	}
	if len(wantRepositories) != 0 {
		t.Fatalf("missing NATS artifacts: %v", wantRepositories)
	}
}
