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

package core

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	k8sclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestKubeClientsStream(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())
	log := GetLogger(ctx)

	s := NewKubeClientsStream()

	{
		in := make(chan *rest.Config)
		ctx, cancel := context.WithCancel(ctx)
		out := s.WithConfigCh(in).Start(ctx)

		in <- &rest.Config{Host: "http://test.k8s.local"}
		close(in)

		clients, ok := <-out
		assert.True(t, ok)
		log.Infof("clients: %+v", clients)

		cancel()
	}

	{
		in := make(chan *rest.Config)
		ctx, cancel := context.WithCancel(ctx)

		f := func(c *rest.Config) (k8sclient.Interface, error) {
			return nil, fmt.Errorf("failed to build k8s client")
		}
		out := s.WithConfigCh(in).
			WithK8sNewForConfig(f).Start(ctx)

		in <- &rest.Config{Host: "http://test.k8s.local"}
		close(in)

		_, ok := <-out
		assert.False(t, ok)

		cancel()
	}
}

func TestKubeClientsNewKubeClientsFromPath(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())

	{
		clients, err := NewKubeClientsFromPath(ctx, "test/kube.good.yaml")
		assert.Nil(t, err)
		assert.Equal(t, "https://test.k8s.local", clients.Config.Host)
	}

	{
		_, err := NewKubeClientsFromPath(ctx, "test/kube.bad.yaml")
		assert.NotNil(t, err)
		assert.Contains(t, err.Error(), "failed to configure KubeClients")
	}
}

func TestKubeClientsNewKubeClientsFromConfig(t *testing.T) {
	ctx := WithDefaultLogger(context.Background())

	config := &rest.Config{Host: "https://test.k8s.local"}
	clients, err := NewKubeClientsFromConfig(ctx, config)
	assert.Nil(t, err)
	assert.Equal(t, "https://test.k8s.local", clients.Config.Host)
}
