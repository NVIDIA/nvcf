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

package testutils

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
)

type DoWorkFunc func(ctx context.Context, nc *nats.Conn) error

type MockNVCFAPIServer struct {
	pb.UnimplementedWorkerServer
	srv *grpc.Server

	workerInfo string
	js         jetstream.JetStream
	ctx        context.Context
	nc         *nats.Conn
	t          *testing.T
	doWorkFunc DoWorkFunc
	done       chan struct{}
	doneOnce   sync.Once

	cleanupLock sync.Mutex
	cleanup     []func() error

	region             string
	otherRegions       []string
	regionsToProvision []string
}

func NewMockNVCFAPI(ctx context.Context, t *testing.T, natsUrl string, doWorkFunc DoWorkFunc, regionsToProvision []string) (*MockNVCFAPIServer, error) {
	if regionsToProvision == nil {
		regionsToProvision = []string{"region-1", "region-2", "region-3"}
	}
	s := &MockNVCFAPIServer{ctx: ctx, t: t, doWorkFunc: doWorkFunc, done: make(chan struct{}),
		region:             "region-1",
		otherRegions:       []string{"region-2", "region-3"},
		regionsToProvision: regionsToProvision,
	}

	nc, err := nats.Connect(natsUrl, nats.PingInterval(1*time.Second))
	if err != nil {
		return nil, err
	}
	s.nc = nc
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, err
	}
	s.js = js
	// create the gRPC server
	s.srv = grpc.NewServer()
	pb.RegisterWorkerServer(s.srv, s)

	// Listen for TCP requests on the specified address and port
	var sock net.Listener
	addr := "127.0.0.1:9090"
	if sock, err = net.Listen("tcp", addr); err != nil {
		return nil, fmt.Errorf("could not listen on %s", addr)
	}

	// Run the server
	go s.run(sock)
	zap.L().Info("Starting NVCF API Server", zap.String("listen", addr))
	return s, nil
}

func (s *MockNVCFAPIServer) run(sock net.Listener) {
	defer sock.Close()
	if err := s.srv.Serve(sock); err != nil {
		zap.L().Info("Unable to serve", zap.Error(err))
		s.Shutdown()
	}
}

func (s *MockNVCFAPIServer) ConnectOnce(ctx context.Context, workerConnect *pb.WorkerConnect) (*pb.WorkerConnectOnceResponse, error) {
	zap.L().Debug("Got connect request")
	resp := &pb.WorkerConnectOnceResponse{
		ConnectedRegion: s.region,
		OtherRegions:    s.otherRegions,
		NvcfWorkerToken: "worker-token",
		Expiration:      timestamppb.New(time.Now().Add(time.Hour)),
	}

	s.setWorkerInfo(workerConnect.GetFunctionId(), workerConnect.GetFunctionVersionId(),
		workerConnect.GetInstanceId())
	group, _ := errgroup.WithContext(ctx)
	for _, region := range s.regionsToProvision {
		region := region
		group.Go(func() error {
			_, err := s.createStream(ctx, region, workerConnect.GetFunctionVersionId())
			if err != nil {
				return err
			}
			return nil
		})
	}
	err := group.Wait()
	if err != nil {
		return nil, err
	}

	// fun command to watch messages come through
	// nats sub '$JS.API.STREAM.>'
	go func() {
		defer s.doneOnce.Do(func() {
			close(s.done)
		})
		err := s.doWorkFunc(s.ctx, s.nc)
		if err != nil {
			s.t.Errorf("failed to run doWorkFunc: %v", err)
		}
	}()

	zap.L().Info("sending connection response")
	return resp, nil
}

func (s *MockNVCFAPIServer) ProvisionRegionalWorker(ctx context.Context, request *pb.ProvisionWorkerRequest) (*pb.ProvisionWorkerResponse, error) {
	_, err := s.createStream(ctx, request.RegionToProvision, request.FunctionVersionId)
	if err != nil {
		return nil, err
	}
	return &pb.ProvisionWorkerResponse{}, nil
}

func (s *MockNVCFAPIServer) createStream(ctx context.Context, region, functionVersionId string) (jetstream.Stream, error) {
	requestStreamName := fmt.Sprintf("rq_%s_%s", region, functionVersionId)
	requestStreamSubject := fmt.Sprintf("rq.%s.%s", region, functionVersionId)
	consumerName := fmt.Sprintf("%s_workers", requestStreamName)

	zap.L().Debug("creating request stream", zap.String("streamName", requestStreamName))
	rq, err := s.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      requestStreamName,
		Subjects:  []string{requestStreamSubject + ".>"},
		Retention: jetstream.WorkQueuePolicy,
		Storage:   jetstream.MemoryStorage,
		Placement: &jetstream.Placement{Tags: []string{"aws-region:" + region}},
		Replicas:  1, // Single server, so replicas = 1
	})
	if err != nil {
		zap.L().Error("Unable to create request stream", zap.Error(err))
		return nil, err
	}
	// consumer will be used by the worker, not the api so we are not keeping reference
	zap.L().Debug("creating request consumer", zap.String("consumerName", consumerName))
	_, err = s.js.CreateOrUpdateConsumer(ctx, requestStreamName, jetstream.ConsumerConfig{
		Name:            consumerName,
		Durable:         consumerName,
		MaxRequestBatch: 5000,
	})
	if err != nil {
		zap.L().Error("Unable to create request consumer", zap.Error(err))
		return nil, err
	}
	s.cleanupLock.Lock()
	s.cleanup = append(s.cleanup, func() error {
		zap.L().Debug("deleting request stream", zap.String("streamName", requestStreamName))
		return s.js.DeleteStream(context.Background(), requestStreamName)
	})
	s.cleanupLock.Unlock()
	return rq, nil
}

func (s *MockNVCFAPIServer) RefreshLargeUploadCredentials(ctx context.Context, req *pb.RefreshLargeUploadCredentialsRequest) (*pb.RefreshLargeUploadCredentialsResponse, error) {
	return &pb.RefreshLargeUploadCredentialsResponse{
		LargeResponseUrl: "http://127.0.0.1:8002/largeResponse",
	}, nil
}

func (s *MockNVCFAPIServer) RefreshAssetDownloadCredentials(req pb.Worker_RefreshAssetDownloadCredentialsServer) error {
	for {
		r, err := req.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		err = req.Send(&pb.RefreshAssetDownloadCredentialsResponse{
			InputAssetReference: &pb.InputAssetReference{
				AssetId:     r.AssetId,
				Reference:   "http://127.0.0.1:8001/some/asset",
				ContentType: "image/jpg",
			},
		})
		if err != nil {
			return err
		}
	}
}

func (s *MockNVCFAPIServer) RequestInstanceCredentials(ctx context.Context, req *pb.InstanceCredentialsRequest) (*pb.InstanceCredentialsResponse, error) {
	return &pb.InstanceCredentialsResponse{
		InstanceCredentialsToken: "instance-cred-abc-123",
		Expiration:               timestamppb.New(time.Now().Add(1 * time.Hour)),
	}, nil
}

func (s *MockNVCFAPIServer) RequestFunctionMetadataCredentials(ctx context.Context, req *pb.FunctionMetadataCredentialsRequest) (*pb.FunctionMetadataCredentialsResponse, error) {
	return &pb.FunctionMetadataCredentialsResponse{
		FunctionMetadataCredentialsToken: "function-metadata-cred-abc-123",
		Expiration:                       timestamppb.New(time.Now().Add(1 * time.Hour)),
	}, nil
}

func (s *MockNVCFAPIServer) RequestSecretCredentials(ctx context.Context, req *pb.SecretCredentialsRequest) (*pb.SecretCredentialsResponse, error) {
	return &pb.SecretCredentialsResponse{
		SecretCredentialsToken: req.GetFunctionId() + time.Now().String(),
		Expiration:             timestamppb.New(time.Now().Add(1 * time.Second)),
	}, nil
}

func (s *MockNVCFAPIServer) RequestLargeResponseDownloadCredentials(ctx context.Context, req *pb.LargeResponseDownloadCredentialsRequest) (*pb.LargeResponseDownloadCredentialsResponse, error) {
	return &pb.LargeResponseDownloadCredentialsResponse{
		LargeResponseDownloadUrl: "http://127.0.0.1:8002/largeResponse",
	}, nil
}

func (s *MockNVCFAPIServer) Shutdown() {
	zap.L().Info("NVCF API Server shutting down")
	s.srv.GracefulStop()
	for _, f := range s.cleanup {
		err := f()
		if err != nil {
			s.t.Fatal("cleanup function failed", err)
		}
	}
	s.nc.Close()
	zap.L().Info("NVCF API Server has shut down gracefully")
}

func (s *MockNVCFAPIServer) setWorkerInfo(functionId, versionId, instanceId string) {
	s.workerInfo = s.buildWorkerInfo(functionId, versionId, instanceId)
}

func (s *MockNVCFAPIServer) buildWorkerInfo(functionId, versionId, instanceId string) string {
	return functionId + "-" + versionId + "-" + instanceId
}

func (s *MockNVCFAPIServer) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		s.t.Log("mock nvcf api server context done")
		return ctx.Err()
	case <-s.done:
		s.t.Logf("mock nvcf api server work function done")
		return nil
	}
}
