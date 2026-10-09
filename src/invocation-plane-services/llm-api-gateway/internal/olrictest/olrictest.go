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

// Package olrictest starts embedded Olric clusters for tests.
package olrictest

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/util"
)

// FreeTCPPort returns a port that was free on 127.0.0.1 when checked.
func FreeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener addr type %T", l.Addr())
	}
	return addr.Port, nil
}

// NodeConfig returns a loopback Olric config on fresh ports that joins peers.
func NodeConfig(t testing.TB, peers []string) config.OlricConfig {
	t.Helper()
	clientPort, err := FreeTCPPort()
	if err != nil {
		t.Fatalf("free client port: %v", err)
	}
	memberlistPort, err := FreeTCPPort()
	if err != nil {
		t.Fatalf("free memberlist port: %v", err)
	}
	return config.OlricConfig{
		Enabled:            true,
		Environment:        "local",
		BindAddr:           "127.0.0.1",
		BindPort:           clientPort,
		MemberlistBindAddr: "127.0.0.1",
		MemberlistBindPort: memberlistPort,
		Peers:              peers,
		ReplicaCount:       1,
		PartitionCount:     7,
		DMapName:           "olrictest",
		StartupTimeout:     15 * time.Second,
		ShutdownTimeout:    5 * time.Second,
		LogLevel:           "ERROR",
		LogOutput:          io.Discard,
	}
}

// StartCluster boots size embedded nodes joined over memberlist and waits
// until every node sees every member. Nodes shut down on test cleanup.
func StartCluster(t testing.TB, size int) []*util.OlricNode {
	t.Helper()
	ctx := context.Background()
	var (
		nodes []*util.OlricNode
		peers []string
	)
	for i := 0; i < size; i++ {
		cfg := NodeConfig(t, append([]string(nil), peers...))
		node, err := util.NewOlricNode(ctx, cfg)
		if err != nil {
			t.Fatalf("start olric node %d: %v", i, err)
		}
		t.Cleanup(func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = node.Shutdown(shutdownCtx)
		})
		nodes = append(nodes, node)
		peers = append(peers, net.JoinHostPort(cfg.MemberlistBindAddr, strconv.Itoa(cfg.MemberlistBindPort)))
	}
	waitForClusterSize(t, nodes, size, 10*time.Second)
	return nodes
}

func waitForClusterSize(t testing.TB, nodes []*util.OlricNode, want int, timeout time.Duration) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	for {
		ready := true
		for _, n := range nodes {
			members, err := n.Client.Members(ctx)
			if err != nil || len(members) < want {
				ready = false
				break
			}
		}
		if ready {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("olric cluster did not reach %d members within %s", want, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
