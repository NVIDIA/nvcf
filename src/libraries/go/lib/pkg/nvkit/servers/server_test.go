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

package servers

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/interop"
	testgrpc "google.golang.org/grpc/interop/grpc_testing"
	"google.golang.org/grpc/status"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/errors"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/tracing"
)

const (
	testCertFile        = "../test/certs/localhost-server.crt"
	testKeyFile         = "../test/certs/localhost-server.fakekey"
	testInvalidCertFile = "invalid-file.crt"
	testInvalidKeyFile  = "invalid-file.key"
)

var (
	testBaseServerConfig = BaseServerConfig{}
)

func TestBaseServerConfig_AddServerFlags(t *testing.T) {
	// Check that passing nil command doesn't add any flags
	var nilCmd *cobra.Command
	ok := testBaseServerConfig.AddServerFlags(nilCmd)
	assert.False(t, ok)

	// Check that there are flags added after the call
	nonNilCmd := &cobra.Command{}
	ok = testBaseServerConfig.AddServerFlags(nonNilCmd)
	assert.True(t, ok)
	assert.True(t, nonNilCmd.Flags().HasFlags())
	assert.Equal(t, "Tracing OTEL endpoint.", nonNilCmd.Flags().Lookup("tracing.endpoint").Usage)
}

func TestBaseServerConfig_SetupTLSConfig(t *testing.T) {
	// When TLS is not enabled, no tls config should occur
	configWithNoTLS := BaseServerConfig{}
	cert, certPool, err := configWithNoTLS.SetupTLSConfig()
	assert.Nil(t, err)
	assert.Nil(t, certPool)
	assert.Equal(t, tls.Certificate{}, cert)

	// Error out when either cert file or key file is missing
	configWithMissingCertFile := BaseServerConfig{
		TLS: auth.TLSConfigOptions{
			Enabled:  true,
			CertFile: testCertFile,
		},
	}
	cert, certPool, err = configWithMissingCertFile.SetupTLSConfig()
	assert.Equal(t, err, errors.ErrCertAndKeyRequired)
	assert.Nil(t, certPool)
	assert.Equal(t, tls.Certificate{}, cert)
	configWithMissingKeyFile := BaseServerConfig{
		TLS: auth.TLSConfigOptions{
			Enabled: true,
			KeyFile: testKeyFile,
		},
	}
	cert, certPool, err = configWithMissingKeyFile.SetupTLSConfig()
	assert.Equal(t, err, errors.ErrCertAndKeyRequired)
	assert.Nil(t, certPool)
	assert.Equal(t, tls.Certificate{}, cert)

	// Error out when either cert file or key file is invalid
	configWithInvalidCertFile := BaseServerConfig{
		TLS: auth.TLSConfigOptions{
			Enabled:  true,
			CertFile: testInvalidCertFile,
			KeyFile:  testKeyFile,
		},
	}
	cert, certPool, err = configWithInvalidCertFile.SetupTLSConfig()
	assert.NotNil(t, err)
	assert.Nil(t, certPool)
	assert.Equal(t, tls.Certificate{}, cert)
	configWithInvalidKeyFile := BaseServerConfig{
		TLS: auth.TLSConfigOptions{
			Enabled:  true,
			CertFile: testCertFile,
			KeyFile:  testInvalidKeyFile,
		},
	}
	cert, certPool, err = configWithInvalidKeyFile.SetupTLSConfig()
	assert.NotNil(t, err)
	assert.Nil(t, certPool)
	assert.Equal(t, tls.Certificate{}, cert)

	// Test that passing invalid cert file format results in an error
	configWithInvalidCertFileFormat := BaseServerConfig{
		TLS: auth.TLSConfigOptions{
			Enabled:  true,
			CertFile: testKeyFile, // Passing same file to trigger error
			KeyFile:  testKeyFile,
		},
	}
	cert, certPool, err = configWithInvalidCertFileFormat.SetupTLSConfig()
	assert.NotNil(t, err)
	assert.Nil(t, certPool)
	assert.Equal(t, tls.Certificate{}, cert)

	// Test valid TLS Config
	configWithValidTLSConfig := BaseServerConfig{
		TLS: auth.TLSConfigOptions{
			Enabled:  true,
			CertFile: testCertFile,
			KeyFile:  testKeyFile,
		},
	}
	cert, certPool, err = configWithValidTLSConfig.SetupTLSConfig()
	assert.Nil(t, err)
	assert.NotNil(t, certPool)
	assert.NotEqual(t, 0, len(cert.Certificate))
}

func TestBaseServerConfig_SetupTracer(t *testing.T) {
	// Test default tracer if not explicit config provided
	configWithNoTracer := BaseServerConfig{}
	tracer, _ := tracing.SetupOTELTracer(&configWithNoTracer.Tracing)
	// When tracing is disabled, SetupOTELTracer returns a noop tracer provider
	// We check that it's not nil rather than comparing exact equality
	assert.NotNil(t, tracer)

	// Test setting up of lightstep tracer when tracing config is enabled
	configWithTracingEnabled := BaseServerConfig{
		Tracing: tracing.OTELConfig{
			Enabled:     true,
			AccessToken: "developer", // Use `developer` as access token. Without this, access token needs to adhere to length requirements.
			Attributes: tracing.Attributes{
				ServiceName:    "test-service",
				ServiceVersion: "test-version",
				Extra:          nil,
			},
		},
	}
	tracer, _ = tracing.SetupOTELTracer(&configWithTracingEnabled.Tracing)
	assert.NotNil(t, otel.GetTracerProvider())
}

// getFreePort - helper function to get a free port to run a service
func getFreePort(t *testing.T) int {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.FailNow()
	}
	err = ln.Close()
	if err != nil {
		t.FailNow()
	}
	return ln.Addr().(*net.TCPAddr).Port
}

func TestServer_E2E_WithGracefulShutdown(t *testing.T) {
	// -----------------------------------------------------------------------------------
	// NOTE: The setup here is required to test graceful shutdown properly.
	// The following code invokes go test again in a separate process through exec.Command,
	// limiting execution to the TestServer_E2E_WithGracefulShutdown test (via the -test.run=TestServer_E2E_WithGracefulShutdown switch).
	// It also passes in a flag via an environment variable (BE_CRASHER=1) which the second invocation checks for and,
	// if set, calls the system-under-test, returning immediately afterwards to prevent running into an infinite loop.
	// Thus, we are being dropped back into our original call site and may now validate that the child process completed cleanly.
	// Pattern ref: https://go.dev/talks/2014/testing.slide#23
	if os.Getenv("BE_CRASHER") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=TestServer_E2E_WithGracefulShutdown")
		cmd.Env = append(os.Environ(), "BE_CRASHER=1")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		require.NoError(t, cmd.Run())
		return
	}
	// -----------------------------------------------------------------------------------

	// Setup test service
	var shutdownTriggered atomic.Bool
	testSvc := interop.NewTestServer()
	svrOpts := []Option{
		WithRegisterServer(func(s *grpc.Server) {
			testgrpc.RegisterTestServiceServer(s, testSvc)
		}),
		WithTerminationCallback(func() {
			fmt.Println("In termination callback, sleeping for 1 sec")
			time.Sleep(time.Second)
			shutdownTriggered.Store(true)
		}),
	}
	testServer := NewGRPCServer(&GRPCConfig{
		GRPCAddr:         fmt.Sprintf("localhost:%d", getFreePort(t)),
		AdminAddr:        fmt.Sprintf("localhost:%d", getFreePort(t)),
		HTTPAddr:         fmt.Sprintf("localhost:%d", getFreePort(t)),
		BaseServerConfig: &BaseServerConfig{Tracing: tracing.OTELConfig{Enabled: false}},
	}, svrOpts...)

	// Async thread to send requests and shutdown signals to test server
	go func() {
		time.Sleep(3 * time.Second)
		grpcSvr, ok := testServer.(*grpcServer)
		require.True(t, ok)
		conn, err := grpc.Dial(grpcSvr.config.GRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
		require.Nil(t, err)
		defer conn.Close()
		// Send a request and make sure that it is served correctly
		testClient := testgrpc.NewTestServiceClient(conn)
		_, err = testClient.EmptyCall(context.Background(), &testgrpc.Empty{})
		require.Nil(t, err)
		fmt.Println("Processed request successfully")

		// Trigger a service graceful shutdown
		shutdownEndpoint := fmt.Sprintf("http://%s/shutdown", grpcSvr.config.AdminAddr)
		fmt.Println("Sending shutdown", shutdownEndpoint)
		response, err := http.Get(shutdownEndpoint)
		if err != nil {
			fmt.Println("err on shutdown", err)
		} else {
			fmt.Println("shutdown response", response)
		}
		assert.True(t, shutdownTriggered.Load())

		// Sending a request after graceful shutdown should result in an `Unavailable` status
		_, err = testClient.EmptyCall(context.Background(), &testgrpc.Empty{})
		grpcErr, _ := status.FromError(err)
		require.Equal(t, grpcErr.Code(), codes.Unavailable)
		fmt.Println("Connection refused after shutdown")

		// Kill the server
		fmt.Println("Sending interrupt signal to kill server")
	}()

	// Setup and start test server
	testServer.Setup()
	fmt.Println("Running test server")
	err := testServer.Run()
	if err != nil {
		fmt.Println(err)
	}
}

func TestBaseServerConfig_SetAdminRoutes_WithSwagger(t *testing.T) {
	// Create a temp swagger file
	tmpFile, err := os.CreateTemp(t.TempDir(), "swagger-*.json")
	require.NoError(t, err)
	_, err = tmpFile.WriteString(`{"swagger":"2.0"}`)
	require.NoError(t, err)
	tmpFile.Close()

	cfg := &BaseServerConfig{
		SwaggerFile:      tmpFile.Name(),
		ShutdownEndpoint: "/custom-shutdown",
	}

	mux := http.NewServeMux()
	shutdownCalled := false
	shutdownHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		shutdownCalled = true
		w.WriteHeader(http.StatusOK)
	})

	cfg.SetAdminRoutes(mux, shutdownHandler)

	// Verify metrics endpoint
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Verify custom shutdown endpoint
	req2 := httptest.NewRequest("POST", "/custom-shutdown", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	assert.True(t, shutdownCalled)

	// Verify swagger endpoint is registered (should not 404)
	req3 := httptest.NewRequest("GET", "/openapiv2/swagger.json", nil)
	w3 := httptest.NewRecorder()
	mux.ServeHTTP(w3, req3)
	assert.NotEqual(t, http.StatusNotFound, w3.Code)
}

func TestBaseServerConfig_SetAdminRoutes_DefaultShutdown(t *testing.T) {
	cfg := &BaseServerConfig{}
	mux := http.NewServeMux()
	shutdownHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	cfg.SetAdminRoutes(mux, shutdownHandler)
	// ShutdownEndpoint should be set to default
	assert.Equal(t, "/shutdown", cfg.ShutdownEndpoint)
}
