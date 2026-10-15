//
// Copyright 2021 Outfox, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package jwtsecrets

import (
	"context"
	"github.com/go-test/deep"
	"github.com/google/uuid"
	"github.com/hashicorp/vault/sdk/logical"
	"testing"
	"time"
)

func getTestBackend(t *testing.T) (*backend, *logical.Storage) {

	config := logical.TestBackendConfig()
	config.StorageView = new(logical.InmemStorage)
	config.BackendUUID = uuid.New().String()

	b, err := createBackend(config)
	if err != nil {
		t.Fatalf("unable to create backend: %v", err)
	}
	if err := b.Setup(context.Background(), config); err != nil {
		t.Fatalf("unable to create backend: %v", err)
	}

	b.idGen = &fakeIDGenerator{0}

	_ = b.clearConfig(context.Background(), config.StorageView)

	return b, &config.StorageView
}

func TestRotate(t *testing.T) {
	b, storage := getTestBackend(t)

	_, err := writeConfig(b, storage, map[string]interface{}{
		keyRotationDuration: "2s",
		keyTokenTTL:         "1s",
	})
	if err != nil {
		t.Fatalf("%s\n", err)
	}

	err = writeRole(b, storage, "tester", "tester.example.com", map[string]interface{}{}, map[string]interface{}{})
	if err != nil {
		t.Fatalf("%s\n", err)
	}

	config, err := b.getConfig(context.Background(), *storage)
	if err != nil {
		t.Fatalf("%s\n", err)
	}

	policy, err := b.getPolicy(context.Background(), *storage, config, "test")
	if err != nil {
		t.Fatalf("%s\n", err)
	}

	// Pre-rotate checks
	if diff := deep.Equal(policy.LatestVersion, 1); diff != nil {
		t.Error("policy latest version", diff)
	}
	if diff := deep.Equal(policy.MinAvailableVersion, 0); diff != nil {
		t.Error("policy min-available version", diff)
	}
	if diff := deep.Equal(policy.MinDecryptionVersion, 1); diff != nil {
		t.Error("policy min-decryption version", diff)
	}
	if diff := deep.Equal(policy.ArchiveVersion, 1); diff != nil {
		t.Error("policy archive version", diff)
	}
	if diff := deep.Equal(policy.ArchiveMinVersion, 0); diff != nil {
		t.Error("policy archive-min version", diff)
	}

	time.Sleep(config.KeyRotationPeriod + 1)

	// Post-rotate #1 checks
	policy, err = b.getPolicy(context.Background(), *storage, config, "test")
	if err != nil {
		t.Fatalf("%s\n", err)
	}

	if diff := deep.Equal(policy.LatestVersion, 2); diff != nil {
		t.Error("policy latest version", diff)
	}
	if diff := deep.Equal(policy.MinAvailableVersion, 0); diff != nil {
		t.Error("policy min-available version", diff)
	}
	if diff := deep.Equal(policy.MinDecryptionVersion, 1); diff != nil {
		t.Error("policy min-decryption version", diff)
	}
	if diff := deep.Equal(policy.ArchiveVersion, 2); diff != nil {
		t.Error("policy archive version", diff)
	}
	if diff := deep.Equal(policy.ArchiveMinVersion, 0); diff != nil {
		t.Error("policy archive-min version", diff)
	}

	policy, err = b.getPolicy(context.Background(), *storage, config, "test")
	if err != nil {
		t.Fatalf("%s\n", err)
	}

	// Should not have rotated yet
	if diff := deep.Equal(policy.LatestVersion, 2); diff != nil {
		t.Error("policy latest version", diff)
	}
	if diff := deep.Equal(policy.MinAvailableVersion, 0); diff != nil {
		t.Error("policy min-available version", diff)
	}
	if diff := deep.Equal(policy.MinDecryptionVersion, 1); diff != nil {
		t.Error("policy min-decryption version", diff)
	}
	if diff := deep.Equal(policy.ArchiveVersion, 2); diff != nil {
		t.Error("policy archive version", diff)
	}
	if diff := deep.Equal(policy.ArchiveMinVersion, 0); diff != nil {
		t.Error("policy archive-min version", diff)
	}

	time.Sleep(config.KeyRotationPeriod + 1)

	policy, err = b.getPolicy(context.Background(), *storage, config, "test")
	if err != nil {
		t.Fatalf("%s\n", err)
	}

	// Post-rotate #2 checks
	if diff := deep.Equal(policy.LatestVersion, 3); diff != nil {
		t.Error("policy latest version", diff)
	}
	if diff := deep.Equal(policy.MinAvailableVersion, 0); diff != nil {
		t.Error("policy min-available version", diff)
	}
	if diff := deep.Equal(policy.MinDecryptionVersion, 1); diff != nil {
		t.Error("policy min-decryption version", diff)
	}
	if diff := deep.Equal(policy.ArchiveVersion, 3); diff != nil {
		t.Error("policy archive version", diff)
	}
	if diff := deep.Equal(policy.ArchiveMinVersion, 0); diff != nil {
		t.Error("policy archive-min version", diff)
	}
}

func TestPrune(t *testing.T) {
	b, storage := getTestBackend(t)

	_, err := writeConfig(b, storage, map[string]interface{}{
		keyRotationDuration: "2s",
		keyTokenTTL:         "1s",
	})
	if err != nil {
		t.Fatalf("%s\n", err)
	}

	err = writeRole(b, storage, "tester", "tester.example.com", map[string]interface{}{}, map[string]interface{}{})
	if err != nil {
		t.Fatalf("%s\n", err)
	}

	config, err := b.getConfig(context.Background(), *storage)
	if err != nil {
		t.Fatalf("%s\n", err)
	}

	policy, err := b.getPolicy(context.Background(), *storage, config, "test")
	if err != nil {
		t.Fatalf("%s\n", err)
	}
	if diff := deep.Equal(policy.LatestVersion, 1); diff != nil {
		t.Error("policy latest version", diff)
	}

	time.Sleep(config.KeyRotationPeriod + 1)

	policy, err = b.getPolicy(context.Background(), *storage, config, "test")
	if err != nil {
		t.Fatalf("%s\n", err)
	}
	if diff := deep.Equal(policy.LatestVersion, 2); diff != nil {
		t.Error("policy latest version", diff)
	}

	time.Sleep(config.KeyRotationPeriod + 1)

	policy, err = b.getPolicy(context.Background(), *storage, config, "test")
	if err != nil {
		t.Fatalf("%s\n", err)
	}
	if diff := deep.Equal(policy.LatestVersion, 3); diff != nil {
		t.Error("policy latest version", diff)
	}

	time.Sleep(config.KeyRotationPeriod + config.TokenTTL + 1)

	err = b.pruneKeyVersions(context.Background(), *storage, policy, config, "test")
	if err != nil {
		t.Fatalf("%s\n", err)
	}

	// Post-prune checks
	if diff := deep.Equal(policy.LatestVersion, 3); diff != nil {
		t.Error("policy latest version", diff)
	}
	if diff := deep.Equal(policy.MinAvailableVersion, 3); diff != nil {
		t.Error("policy min-available version", diff)
	}
	if diff := deep.Equal(policy.MinDecryptionVersion, 3); diff != nil {
		t.Error("policy min-decryption version", diff)
	}
	if diff := deep.Equal(policy.ArchiveVersion, 3); diff != nil {
		t.Error("policy archive version", diff)
	}
	if diff := deep.Equal(policy.ArchiveMinVersion, 3); diff != nil {
		t.Error("policy archive-min version", diff)
	}

	time.Sleep(config.KeyRotationPeriod)

	// Check that JWKS set contains the correct key versions.
	// Should be 2 keys because pruning should have reduced it to 1 version
	// and fetching will rotate again, leaving two keys.
	jwks, err := FetchJWKS(b, storage)
	if err != nil {
		t.Fatalf("%s\n", err)
	}

	if diff := deep.Equal(len(jwks.Keys), 2); diff != nil {
		t.Error("jwks key count", diff)
	}
}
