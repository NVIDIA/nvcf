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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/rest"
)

func TestKubeConfiguratorPath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	{
		c := NewPathKubeConfigurator().WithPath("test/kube.good.yaml")
		out := c.Start(ctx)

		config, ok := <-out
		assert.True(t, ok)
		assert.Equal(t, "https://test.k8s.local", config.Host)
		assert.Nil(t, config.RateLimiter)
		assert.Equal(t, float32(100), config.QPS)
		assert.Equal(t, 200, config.Burst)

		_, ok = <-out
		assert.False(t, ok)
	}

	{
		c := NewPathKubeConfigurator().WithPath("test/kube.bad.yaml")
		out := c.Start(ctx)
		_, ok := <-out
		assert.False(t, ok)
	}
}

func TestKubeConfiguratorClientRateLimitingDisabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	{
		c := NewPathKubeConfigurator().WithPath("test/kube.good.yaml").WithClientRateLimitingDisabled()
		out := c.Start(ctx)

		config, ok := <-out
		assert.True(t, ok)
		assert.Equal(t, "https://test.k8s.local", config.Host)
		assert.NotNil(t, config.RateLimiter)

		_, ok = <-out
		assert.False(t, ok)
	}
}

func TestKubeConfiguratorWithConfigModifier(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	{
		c := NewPathKubeConfigurator().WithPath("test/kube.good.yaml").WithConfigModifier(func(c *rest.Config) {
			c.QPS = 10
		})
		out := c.Start(ctx)

		config, ok := <-out
		assert.True(t, ok)
		assert.Equal(t, "https://test.k8s.local", config.Host)
		assert.Nil(t, config.RateLimiter)
		assert.Equal(t, float32(10), config.QPS)
		assert.Equal(t, 200, config.Burst)

		_, ok = <-out
		assert.False(t, ok)
	}
}

func TestKubeConfiguratorToken(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	timeWait := 200 * time.Millisecond

	mock := &mockFetcher{}
	c := NewTokenKubeConfigurator().
		WithMasterURL("http://test.k8s.local").
		WithFetcher(mock).
		WithTimeout(2 * time.Second).
		WithInterval(100 * time.Millisecond)
	out := c.Start(ctx)

	mock.mtx.Lock()
	mock.token, mock.tokenErr = "abc", nil
	mock.mtx.Unlock()

	config := <-out
	assert.Equal(t, "http://test.k8s.local", config.Host)
	assert.Equal(t, "abc", config.BearerToken)

	mock.mtx.Lock()
	mock.token, mock.tokenErr = "cde", nil
	mock.mtx.Unlock()

	config = <-out
	assert.Equal(t, "http://test.k8s.local", config.Host)
	assert.Equal(t, "cde", config.BearerToken)

	// Same token, no update sent
	mock.mtx.Lock()
	mock.token, mock.tokenErr = "cde", nil
	mock.mtx.Unlock()

	time.Sleep(timeWait)

	// Fetch token error, no update sent
	mock.mtx.Lock()
	mock.token, mock.tokenErr = "", fmt.Errorf("failed fetch token")
	mock.mtx.Unlock()

	time.Sleep(timeWait)

	cancel()

	_, ok := <-out
	assert.False(t, ok)
}

type mockFetcher struct {
	token    string
	tokenErr error
	mtx      sync.Mutex
}

func (m *mockFetcher) RefreshClient() {
	return
}

func (m *mockFetcher) FetchToken(ctx context.Context) (string, error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	return m.token, m.tokenErr
}
