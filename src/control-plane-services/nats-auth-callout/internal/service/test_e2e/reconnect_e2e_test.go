// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package test_e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/nkey"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

// reconnectSetup starts NATS and the auth service with one nkey user mapped to an
// account, and returns a function that performs a fresh login as that user.
func reconnectSetup(t *testing.T) (*TestSetup, func() error) {
	t.Helper()
	setup := setupCommonTestInfrastructure(t)

	user, err := nkeys.CreateUser()
	if err != nil {
		t.Fatalf("Failed to create user nkey pair: %v", err)
	}
	userPublicKey, err := user.PublicKey()
	if err != nil {
		t.Fatalf("Failed to get user public key: %v", err)
	}

	testConfig := setup.createBaseConfig()
	testConfig.AccountConfigs["test-account"] = config.AccountConfig{
		EnabledPlugins: []config.EnabledPlugin{{ID: "nkey"}},
	}
	testConfig.PluginConfigs["nkey"] = config.PluginConfig{
		PluginType: "nkey",
		Config: nkey.Config{
			NkeyMappings: []nkey.NkeyMapping{{Nkey: userPublicKey, Account: "test-account"}},
		},
	}
	setup.startAuthServiceWithConfig(t, testConfig)

	login := func() error {
		nc, err := nats.Connect(setup.NATSServer.ClientURL(),
			nats.Nkey(userPublicKey, user.Sign),
			nats.Timeout(3*time.Second),
			nats.NoReconnect())
		if err != nil {
			return err
		}
		nc.Close()
		return nil
	}
	return setup, login
}

// restartNATS stops the NATS server, keeps it down for outage, and starts it again with
// the same configuration on the same port.
func restartNATS(t *testing.T, setup *TestSetup, outage time.Duration) {
	t.Helper()
	authServiceLoginPublicKey := publicKeyFromSeed(t, setup.AuthServiceLoginSeed)
	issuerPublicKey := publicKeyFromSeed(t, setup.SigningKeySeed)

	setup.NATSServer.Shutdown()
	setup.NATSServer.WaitForShutdown()
	time.Sleep(outage)

	setup.NATSServer, _ = runNATSServerWithAuthCallout(t, authServiceLoginPublicKey, issuerPublicKey)
	t.Cleanup(setup.NATSServer.Shutdown)
}

// publicKeyFromSeed returns the public key for an nkey seed held by the test setup.
func publicKeyFromSeed(t *testing.T, seed string) string {
	t.Helper()
	kp, err := nkeys.FromSeed([]byte(seed))
	if err != nil {
		t.Fatalf("Failed to parse seed: %v", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		t.Fatalf("Failed to get public key: %v", err)
	}
	return pub
}

// eventually polls cond until it returns nil or the timeout expires.
func eventually(t *testing.T, timeout time.Duration, what string, cond func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var err error
	for time.Now().Before(deadline) {
		if err = cond(); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("Timed out after %s waiting for %s: %v", timeout, what, err)
}

// TestLoginsRecoverAfterLongNATSOutage is the regression test for a NATS outage longer
// than the NATS client's default reconnect budget (60 attempts, 2 s apart). The service
// used to stop reconnecting after ~2 minutes and then reject every login until it was
// restarted by hand, while still reporting healthy. It only checks logins, so it runs
// against the code before the fix as well, where it fails.
func TestLoginsRecoverAfterLongNATSOutage(t *testing.T) {
	if testing.Short() {
		t.Skip("keeps NATS down for over 2 minutes; skipped with -short")
	}
	setup, login := reconnectSetup(t)
	if err := login(); err != nil {
		t.Fatalf("Expected login to succeed before the outage, got: %v", err)
	}

	restartNATS(t, setup, 130*time.Second)

	eventually(t, 15*time.Second, "logins to be approved again after NATS returned", login)
}

// TestReadinessFollowsNATSConnection checks the signals Kubernetes relies on: the service
// reports not ready while NATS is unreachable, ready again once it reconnects, and its
// Closed channel only fires once the connection is closed for good.
func TestReadinessFollowsNATSConnection(t *testing.T) {
	var setup *TestSetup
	// Registered first, so it runs last: after the setup's own cleanup has stopped the
	// service. Stop must not be called twice (callout.AuthorizationService.Stop closes
	// a channel unconditionally).
	t.Cleanup(func() {
		select {
		case <-setup.AuthService.Closed():
		case <-time.After(5 * time.Second):
			t.Error("Expected Closed to fire once the service was stopped")
		}
	})
	setup, login := reconnectSetup(t)
	if err := setup.AuthService.CheckReady(); err != nil {
		t.Fatalf("Expected service to be ready, got: %v", err)
	}
	if err := login(); err != nil {
		t.Fatalf("Expected login to succeed, got: %v", err)
	}

	setup.NATSServer.Shutdown()
	setup.NATSServer.WaitForShutdown()
	eventually(t, 5*time.Second, "service to report not ready", func() error {
		if setup.AuthService.CheckReady() == nil {
			return errNotYet
		}
		return nil
	})

	restartNATS(t, setup, 3*time.Second)
	eventually(t, 15*time.Second, "service to report ready again", setup.AuthService.CheckReady)
	eventually(t, 15*time.Second, "logins to be approved again", login)

	select {
	case <-setup.AuthService.Closed():
		t.Fatal("Closed fired although the connection recovered")
	default:
	}
}

// TestServiceClosesOnRepeatedAuthRejection covers NATS rejecting the service's own
// credentials outright, for example after they were rotated. The NATS client gives up
// after the same authorization error twice, even with unlimited reconnects. Closed must
// then fire so the server exits and the orchestrator restarts it, which reloads the
// credentials; before the fix the process kept running and never reconnected.
func TestServiceClosesOnRepeatedAuthRejection(t *testing.T) {
	setup, _ := reconnectSetup(t)

	setup.NATSServer.Shutdown()
	setup.NATSServer.WaitForShutdown()
	setup.NATSServer = runNATSServerRejectingAuthService(t)
	t.Cleanup(setup.NATSServer.Shutdown)

	select {
	case <-setup.AuthService.Closed():
	case <-time.After(30 * time.Second):
		t.Fatal("Expected Closed to fire after NATS repeatedly rejected the service's credentials")
	}
	if err := setup.AuthService.CheckReady(); err == nil {
		t.Error("Expected the service to report not ready once its connection is closed")
	}
}

// runNATSServerRejectingAuthService starts NATS on the default port with a configuration
// that does not know the auth service's user, so each of its logins is rejected at once.
func runNATSServerRejectingAuthService(t *testing.T) *server.Server {
	t.Helper()
	otherUser, err := nkeys.CreateUser()
	if err != nil {
		t.Fatalf("Failed to create nkey pair: %v", err)
	}
	otherUserPublicKey, err := otherUser.PublicKey()
	if err != nil {
		t.Fatalf("Failed to get public key: %v", err)
	}

	configPath := filepath.Join(t.TempDir(), "nats_rejecting.conf")
	configContent := fmt.Sprintf(`
accounts {
  AUTH: {
    users: [ { nkey: %s } ]
  }
}
`, otherUserPublicKey)
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("Failed to create NATS config: %v", err)
	}

	srv, _ := test.RunServerWithConfig(configPath)
	return srv
}

// notYetError is returned by eventually conditions that are not met yet.
type notYetError struct{}

// Error implements error.
func (notYetError) Error() string { return "not yet" }

var errNotYet error = notYetError{}
