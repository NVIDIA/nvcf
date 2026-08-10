/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package workerinit

import (
	"context"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-init/configs"
	nvcfclient "github.com/NVIDIA/nvcf/src/libraries/go/worker/nvcf"
	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/test/testutils"
)

// Ports distinct from the NVCT integration test and the service E2E.
const (
	nvcfArtifactAddr = "127.0.0.1:18004"
	nvcfArtifactURL  = "http://127.0.0.1:18004"
	nvcfGrpcAddr     = "127.0.0.1:19095"
	nvcfGrpcURL      = "http://127.0.0.1:19095"
)

// mockNvcfWorkerServer is a minimal in-test NVCF Worker gRPC server. It serves
// just enough surface for NvcfInitializer.Run: a streamed artifacts list and a
// metadata-credentials response for the background refresher.
type mockNvcfWorkerServer struct {
	pb.UnimplementedWorkerServer
	artifactServerURL string
}

func (s *mockNvcfWorkerServer) StreamArtifacts(
	_ *pb.ArtifactsRequest,
	stream grpc.ServerStreamingServer[pb.StreamedArtifactFile],
) error {
	for _, path := range []string{"config.pbtxt", "1/model.graphdef"} {
		fileURL, err := url.JoinPath(s.artifactServerURL, "files", path)
		if err != nil {
			return err
		}
		if err := stream.Send(&pb.StreamedArtifactFile{
			ArtifactName:    "simple_int8",
			ArtifactVersion: "2",
			ArtifactKind:    pb.StreamedArtifactFile_MODEL,
			Path:            path,
			Url:             fileURL,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *mockNvcfWorkerServer) RequestFunctionMetadataCredentials(
	_ context.Context,
	_ *pb.FunctionMetadataCredentialsRequest,
) (*pb.FunctionMetadataCredentialsResponse, error) {
	return &pb.FunctionMetadataCredentialsResponse{
		FunctionMetadataCredentialsToken: "metadata-token",
		Expiration:                       timestamppb.New(time.Now().Add(time.Hour)),
	}, nil
}

// TestNvcfInitializerRun drives NvcfInitializer.Run against an in-test NVCF
// Worker gRPC server and the mock artifact server, covering infra metering,
// the metadata-credentials refresher, metrics init, and artifact download.
func TestNvcfInitializerRun(t *testing.T) {
	// Keep the background metadata-credentials refresher off the host's real
	// /var/run path.
	origTokenFile := nvcfclient.MetadataCredsTokenFile
	nvcfclient.MetadataCredsTokenFile = filepath.Join(t.TempDir(), "info", "self")
	defer func() { nvcfclient.MetadataCredsTokenFile = origTokenFile }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	artifactServer := testutils.MockArtifactServer{}
	require.NoError(t, artifactServer.Start(nvcfArtifactAddr))
	defer artifactServer.Close(context.Background())

	lis, err := net.Listen("tcp", nvcfGrpcAddr)
	require.NoError(t, err)
	grpcServer := grpc.NewServer()
	pb.RegisterWorkerServer(grpcServer, &mockNvcfWorkerServer{artifactServerURL: nvcfArtifactURL})
	go func() { _ = grpcServer.Serve(lis) }()
	defer grpcServer.Stop()

	testDir := t.TempDir()
	cfg := configs.InitConfig{
		BaseConfig: configs.BaseConfig{
			ConcurrentDownloads:      1,
			InstanceId:               "instance-1",
			NcaId:                    "nca-1",
			OTELExporterOTLPEndpoint: "http://127.0.0.1:8360",
			TracingAccessToken:       "fake-tracing-token",
			ModelRepo:                filepath.Join(testDir, "models"),
			SharedConfigDir:          filepath.Join(testDir, "shared"),
		},
		NvcfConfig: configs.NvcfConfig{
			NvcfFqdnGrpc:      nvcfGrpcURL,
			NvcfWorkerToken:   "tok",
			FunctionId:        "fn-1",
			FunctionVersionId: "ver-1",
		},
	}

	init, err := NewInitializer(cfg)
	require.NoError(t, err)

	nvcf, ok := init.(*NvcfInitializer)
	require.True(t, ok)

	require.NoError(t, nvcf.Setup())
	require.NoError(t, nvcf.Run(ctx))

	for _, artifact := range []string{"config.pbtxt", "1/model.graphdef"} {
		local := filepath.Join(cfg.ModelRepo, "simple_int8", artifact)
		_, statErr := os.Stat(local)
		require.NoErrorf(t, statErr, "expected artifact %s to be installed locally", artifact)
	}
}
