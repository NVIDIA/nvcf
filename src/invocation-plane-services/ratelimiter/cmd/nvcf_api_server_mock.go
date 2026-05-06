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

package main

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	"github.com/NVIDIA/nvcf-go/pkg/nvkit/logs"

	"ratelimiter/nvcf/pb"
)

type MockNVCFAPIServer struct {
	pb.UnimplementedRateLimitServer
	logger *logs.ZapLogger
	srv    *grpc.Server

	ctx context.Context
	t   *testing.T

	done     chan struct{}
	doneOnce sync.Once

	cleanupLock sync.Mutex
	cleanup     []func() error

	region             string
	otherRegions       []string
	regionsToProvision []string

	lowRate                      bool
	noExemption                  bool
	updateBothRateAndExemption   bool
	limitAll                     bool
	withPerNcaIdRate             bool
	perNcaIdRateOnly             bool
	perNcaIdWithExclusions       bool
	noConfig                     bool
	lazyLoadMultipleNcaIds       bool
	transitionTest               bool // For testTransitionGlobalToPerNcaId
	transitionToPerNcaId         bool // Phase 2 of transition test
	immediateGlobalRateChange    bool // For testImmediateGlobalRateChange (4-S -> 2-S)
	perNcaIdRateChange           bool // For testPerNcaIdRateChange (5-S -> 10-S)
	withMultipleGlobalRates      bool // Global rate with multiple limits (e.g., "5-S,10-M")
	withMultiplePerNcaIdRates    bool // Per-NCA-ID rate with multiple limits
	perNcaIdOverridesGlobalMulti bool // Per-NCA-ID single rate should replace global multi-rate
	perNcaIdMultiOverridesGlobal bool // Per-NCA-ID multi-rate should replace global multi-rate
	withUnorderedMultipleRates   bool // Rates listed largest-window-first (e.g., "10-M,5-S")
	transitionSingleToMulti      bool // Transition test: single rate -> multi rate
	transitionMultiToSingle      bool // Transition test: multi rate -> single rate
	transitionMultiToMulti       bool // Transition test: multi rate -> different multi rate

	// Counter for tracking gRPC calls (for testing optimization)
	callCountMu sync.Mutex
	callCount   int

	// Response delay for testing concurrent single-flight behavior
	responseDelayMu sync.RWMutex
	responseDelay   time.Duration
}

func NewMockNVCFAPI(ctx context.Context, t *testing.T, regionsToProvision []string) (*MockNVCFAPIServer, error) {
	if regionsToProvision == nil {
		regionsToProvision = []string{"region-1", "region-2", "region-3"}
	}
	s := &MockNVCFAPIServer{ctx: ctx, t: t, done: make(chan struct{}),
		region:             "region-1",
		otherRegions:       []string{"region-2", "region-3"},
		regionsToProvision: regionsToProvision,
	}

	// create the gRPC server
	s.srv = grpc.NewServer()
	pb.RegisterRateLimitServer(s.srv, s)

	// Listen for TCP requests on the specified address and port
	var sock net.Listener
	var err error
	addr := "localhost:9091"
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

func (s *MockNVCFAPIServer) RateLimitPolicy(ctx context.Context, req *pb.RateLimitPolicyRequest) (*pb.RateLimitPolicyResponse, error) {
	// Apply response delay if configured (for testing concurrent single-flight)
	s.responseDelayMu.RLock()
	delay := s.responseDelay
	s.responseDelayMu.RUnlock()
	if delay > 0 {
		time.Sleep(delay)
	}

	// Increment call counter for testing
	s.callCountMu.Lock()
	s.callCount++
	s.callCountMu.Unlock()

	zap.L().Info("Got RateLimitPolicy request")

	if req.FunctionId == "test_function_id" && req.FunctionVersionId == "test_function_version_id" {
		if s.lowRate {
			rate := "2-M"
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					Rate:           &rate,
					ExcludedNcaIds: []string{"test_nca_id_exclude1", "test_nca_id_exclude2"},
				},
			}, nil
		}
		if s.noExemption {
			rate := "4-S"
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					Rate:           &rate,
					ExcludedNcaIds: []string{},
				},
			}, nil
		}
		if s.updateBothRateAndExemption {
			rate := "2-M"
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					Rate:           &rate,
					ExcludedNcaIds: []string{"test_nca_id_exclude3"},
				},
			}, nil
		}
		if s.limitAll {
			rate := "0-S"
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					Rate:           &rate,
					ExcludedNcaIds: []string{"test_nca_id_exclude1", "test_nca_id_exclude2"},
				},
			}, nil
		}
		if s.withPerNcaIdRate {
			testNcaIdRate := &pb.RateLimitPolicyResponse_RateLimitConfig_PerNcaIdConfigs{Rate: "4-S", NcaId: "test_nca_id_1"}
			rate := "0-S"
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					Rate:            &rate,
					ExcludedNcaIds:  []string{"test_nca_id_exclude1", "test_nca_id_exclude2"},
					PerNcaIdConfigs: []*pb.RateLimitPolicyResponse_RateLimitConfig_PerNcaIdConfigs{testNcaIdRate},
				},
			}, nil
		}
		if s.perNcaIdRateOnly {
			testNcaIdRate := &pb.RateLimitPolicyResponse_RateLimitConfig_PerNcaIdConfigs{Rate: "4-S", NcaId: "test_nca_id_1"}
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					PerNcaIdConfigs: []*pb.RateLimitPolicyResponse_RateLimitConfig_PerNcaIdConfigs{testNcaIdRate},
				},
			}, nil
		}
		if s.perNcaIdWithExclusions {
			// NCA ID that has per-NCAID rate but is also in excluded list
			testNcaIdRate := &pb.RateLimitPolicyResponse_RateLimitConfig_PerNcaIdConfigs{Rate: "2-S", NcaId: "test_nca_id_exclude1"}
			rate := "4-S"
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					Rate:            &rate,
					ExcludedNcaIds:  []string{"test_nca_id_exclude1", "test_nca_id_exclude2"},
					PerNcaIdConfigs: []*pb.RateLimitPolicyResponse_RateLimitConfig_PerNcaIdConfigs{testNcaIdRate},
				},
			}, nil
		}
		if s.lazyLoadMultipleNcaIds {
			// Return policy with multiple per-NCA-ID configs to test lazy loading
			rate := "10-S"
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					Rate: &rate,
					PerNcaIdConfigs: []*pb.RateLimitPolicyResponse_RateLimitConfig_PerNcaIdConfigs{
						{Rate: "5-S", NcaId: "lazy_nca_id_1"},
						{Rate: "15-S", NcaId: "lazy_nca_id_2"},
						{Rate: "20-S", NcaId: "lazy_nca_id_3"},
					},
				},
			}, nil
		}
		if s.transitionTest && !s.transitionToPerNcaId {
			// Phase 1 of transition test: Only global rate (4-M, minute-based)
			rate := "4-M"
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					Rate:           &rate,
					ExcludedNcaIds: []string{"test_nca_id_exclude1", "test_nca_id_exclude2"},
				},
			}, nil
		}
		if s.transitionToPerNcaId {
			// Phase 2 of transition test: Per-NCA-ID override (2-S, second-based) + global (4-M)
			// Demonstrates transition from minute-based to second-based rates
			// Support multiple transition test NCA IDs
			perNcaIdConfigs := []*pb.RateLimitPolicyResponse_RateLimitConfig_PerNcaIdConfigs{
				{Rate: "2-S", NcaId: "test_nca_id_transition"},
				{Rate: "2-S", NcaId: "test_nca_id_transition_global_to_per_nca"},
				{Rate: "2-S", NcaId: "test_nca_id_transition_per_nca_to_global"},
			}
			rate := "4-M"
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					Rate:            &rate,
					ExcludedNcaIds:  []string{"test_nca_id_exclude1", "test_nca_id_exclude2"},
					PerNcaIdConfigs: perNcaIdConfigs,
				},
			}, nil
		}
		if s.immediateGlobalRateChange {
			// Immediate global rate change: 2-S (no delay between changes)
			rate := "2-S"
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					Rate:           &rate,
					ExcludedNcaIds: []string{"test_nca_id_exclude1", "test_nca_id_exclude2"},
				},
			}, nil
		}
		if s.perNcaIdRateChange {
			// Per-NCA-ID rate change: 10-S for test_nca_id_rate_change
			testNcaIdRate := &pb.RateLimitPolicyResponse_RateLimitConfig_PerNcaIdConfigs{Rate: "10-S", NcaId: "test_nca_id_rate_change"}
			rate := "4-S"
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					Rate:            &rate,
					ExcludedNcaIds:  []string{"test_nca_id_exclude1", "test_nca_id_exclude2"},
					PerNcaIdConfigs: []*pb.RateLimitPolicyResponse_RateLimitConfig_PerNcaIdConfigs{testNcaIdRate},
				},
			}, nil
		}
		if s.transitionSingleToMulti {
			rate := "5-S,10-M"
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					Rate:           &rate,
					ExcludedNcaIds: []string{"test_nca_id_exclude1", "test_nca_id_exclude2"},
				},
			}, nil
		}
		if s.transitionMultiToSingle {
			rate := "4-S"
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					Rate:           &rate,
					ExcludedNcaIds: []string{"test_nca_id_exclude1", "test_nca_id_exclude2"},
				},
			}, nil
		}
		if s.transitionMultiToMulti {
			rate := "3-S,8-M"
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					Rate:           &rate,
					ExcludedNcaIds: []string{"test_nca_id_exclude1", "test_nca_id_exclude2"},
				},
			}, nil
		}
		if s.withMultipleGlobalRates {
			rate := "5-S,10-M"
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					Rate:           &rate,
					ExcludedNcaIds: []string{"test_nca_id_exclude1", "test_nca_id_exclude2"},
				},
			}, nil
		}
		if s.withMultiplePerNcaIdRates {
			testNcaIdRate := &pb.RateLimitPolicyResponse_RateLimitConfig_PerNcaIdConfigs{Rate: "3-S,8-M", NcaId: "test_nca_id_multi"}
			rate := "5-S,10-M"
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					Rate:            &rate,
					ExcludedNcaIds:  []string{"test_nca_id_exclude1", "test_nca_id_exclude2"},
					PerNcaIdConfigs: []*pb.RateLimitPolicyResponse_RateLimitConfig_PerNcaIdConfigs{testNcaIdRate},
				},
			}, nil
		}
		if s.perNcaIdOverridesGlobalMulti {
			testNcaIdRate := &pb.RateLimitPolicyResponse_RateLimitConfig_PerNcaIdConfigs{Rate: "2-S", NcaId: "test_nca_id_override_single"}
			rate := "5-S,4-M"
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					Rate:            &rate,
					ExcludedNcaIds:  []string{"test_nca_id_exclude1", "test_nca_id_exclude2"},
					PerNcaIdConfigs: []*pb.RateLimitPolicyResponse_RateLimitConfig_PerNcaIdConfigs{testNcaIdRate},
				},
			}, nil
		}
		if s.perNcaIdMultiOverridesGlobal {
			testNcaIdRate := &pb.RateLimitPolicyResponse_RateLimitConfig_PerNcaIdConfigs{Rate: "3-S,8-M", NcaId: "test_nca_id_override_multi"}
			rate := "5-S,4-M"
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					Rate:            &rate,
					ExcludedNcaIds:  []string{"test_nca_id_exclude1", "test_nca_id_exclude2"},
					PerNcaIdConfigs: []*pb.RateLimitPolicyResponse_RateLimitConfig_PerNcaIdConfigs{testNcaIdRate},
				},
			}, nil
		}
		if s.withUnorderedMultipleRates {
			rate := "10-M,5-S"
			return &pb.RateLimitPolicyResponse{
				Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
					Rate:           &rate,
					ExcludedNcaIds: []string{"test_nca_id_exclude1", "test_nca_id_exclude2"},
				},
			}, nil
		}
		if s.noConfig {
			return nil, nil
		}
		rate := "4-S"
		return &pb.RateLimitPolicyResponse{
			Config: &pb.RateLimitPolicyResponse_RateLimitConfig{
				Rate:           &rate,
				ExcludedNcaIds: []string{"test_nca_id_exclude1", "test_nca_id_exclude2"},
			},
		}, nil
	}

	return &pb.RateLimitPolicyResponse{}, nil
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
	zap.L().Info("NVCF API Server has shut down gracefully")
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

// GetCallCount returns the number of times RateLimitPolicy has been called
func (s *MockNVCFAPIServer) GetCallCount() int {
	s.callCountMu.Lock()
	defer s.callCountMu.Unlock()
	return s.callCount
}

// ResetCallCount resets the call counter to zero
func (s *MockNVCFAPIServer) ResetCallCount() {
	s.callCountMu.Lock()
	defer s.callCountMu.Unlock()
	s.callCount = 0
}

func (s *MockNVCFAPIServer) SetResponseDelay(delay time.Duration) {
	s.responseDelayMu.Lock()
	defer s.responseDelayMu.Unlock()
	s.responseDelay = delay
}
