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

package oauth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func timeFromString(dateString string) metav1.Time {
	t, _ := time.Parse(time.RFC3339, dateString)
	return metav1.NewTime(t)
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

func setJWTCacheTest() (*JWTCache, *mockFetcher) {
	mock := &mockFetcher{}
	cache := NewJWTCache().WithFetcher(mock)
	return cache, mock
}

func TestJWTCache(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	log := core.GetLogger(ctx)

	// Current time: 2020-04-17 10:00:00 PDT
	now := timeFromString("2020-04-17T10:00:00-07:00")

	// Expires at 2020-04-16 03:03:48 -0700 PDT
	token1 := `eyJhbGciOiJSUzI1NiIsImtpZCI6IjlhYjI4NzFhLWUzNDctODEyNS1kNDYzLTY0YTE0MDI4OThkOSJ9.eyJhdWQiOiJrdWJlcm5ldGVzIiwiZXhwIjoxNTg3MDMxNDI4LCJncm91cHMiOlsic3lzdGVtOm1hc3RlcnMiXSwiaWF0IjoxNTg2OTg4MjI4LCJpc3MiOiJodHRwczovL2V0cy1udmlkaWEtZGV2aWNlYXV0aC5kZXYuZWd4Lm52aWRpYS5jb20vdjEvaWRlbnRpdHkvb2lkYyIsIm5hbWVzcGFjZSI6InJvb3QiLCJzdWIiOiI4OTQ5ZDBlNy04NTQ0LWVjMDgtNDc2My05NjNiODZlNDBmZjEiLCJ1c2VybmFtZSI6ImR1bW15In0.aTAsLLjUwdvpjE0-Ft_xCWgwqJEtWi8kVbfS60jYVxf9_FL8aDxyPSxbvNDX3kxoe5kfOW2AhAz89v1IuBICi1sdFBZ4BRP-hOUb-SGoWh3erYDvqpItLVXaD90029Sgp5hrQk3L5fecNT3HSgkLlay4IcPKL4Ri42YbESIMLQbeGdYNDH6q65pLEqQj5EiPfhmujDmSnk6f_H0kAMCqA1RSlE9MG3Y3IdsR-O7kCIqFrt55OuFFBs6Pf__Pr-Z7j1EAliq5lAW_6p9q26fw9R-X8tN1iy-eKOjTU9My8DeejhwQ5mXONJZkJduCmQukOX0bAOTSP1s3F2sqrZM0kw`

	// Expires at 2020-04-18 00:05:11 -0700 PDT
	token2 := `eyJhbGciOiJSUzI1NiIsImtpZCI6IjlhYjI4NzFhLWUzNDctODEyNS1kNDYzLTY0YTE0MDI4OThkOSJ9.eyJhdWQiOiJrdWJlcm5ldGVzIiwiZXhwIjoxNTg3MTkzNTExLCJncm91cHMiOlsic3lzdGVtOm1hc3RlcnMiXSwiaWF0IjoxNTg3MTUwMzExLCJpc3MiOiJodHRwczovL2V0cy1udmlkaWEtZGV2aWNlYXV0aC5kZXYuZWd4Lm52aWRpYS5jb20vdjEvaWRlbnRpdHkvb2lkYyIsIm5hbWVzcGFjZSI6InJvb3QiLCJzdWIiOiI4OTQ5ZDBlNy04NTQ0LWVjMDgtNDc2My05NjNiODZlNDBmZjEiLCJ1c2VybmFtZSI6ImR1bW15In0.icoV7ZDRCs7PAnDVQmuH5ZqeRBZMbExdN1ztCtsv7dwij0c6LpygdDMta7VkEuYfqijuFscHbgkMMicxkdTbgIKYxWNi4vMuBXKSbDO50Z4IkqHnzxrVJ4vI_hcGKdTCt_yOgTQkQ97HvKrzTG-eOhYgGQhXyk5mDhT7bv4VGprGGYql-D8ijeG7-gq_IvKT6XWl8Mvl3JZeyt8W4BGCbUHut-34pQwLN1_qs03EHFnUfIZY9S0XD7Wm2cCVgYJAOpPeHunYROhXFSe44Oq0wCWDoRKzXaGLXJP8vkpyJWcPqfGnYJ8Sr0SFKxmIdJPWQR4IggRlZbXRZ-3jtIIEYA`

	// Expires at 2020-04-18 00:51:25 -0700 PDT
	token3 := `eyJhbGciOiJSUzI1NiIsImtpZCI6IjlhYjI4NzFhLWUzNDctODEyNS1kNDYzLTY0YTE0MDI4OThkOSJ9.eyJhdWQiOiJrdWJlcm5ldGVzIiwiZXhwIjoxNTg3MTk2Mjg1LCJncm91cHMiOlsic3lzdGVtOm1hc3RlcnMiXSwiaWF0IjoxNTg3MTUzMDg1LCJpc3MiOiJodHRwczovL2V0cy1udmlkaWEtZGV2aWNlYXV0aC5kZXYuZWd4Lm52aWRpYS5jb20vdjEvaWRlbnRpdHkvb2lkYyIsIm5hbWVzcGFjZSI6InJvb3QiLCJzdWIiOiI4OTQ5ZDBlNy04NTQ0LWVjMDgtNDc2My05NjNiODZlNDBmZjEiLCJ1c2VybmFtZSI6ImR1bW15In0.WPHkO1u49xWq2BvOKQA4TU5egWt0Xqv3ALFX57H35rEbdLptProL8CHBlTIXoZYLSB1jv2M2E-yqlls0d8F4HBpf80aqcHkzYc_x7QU4x_YNgTLCvHjc3LLs9m_EvdPwkMig3-otMok9-Fueh2nqWqDDC30HOoojo10ja8Om4Go4csQIMs7WaqHaBDutfT2t-B7YtkgtQwL-k6YyVmNkrFlmX4dKojDmB42guhd4vDflGb-k9Z_5bt9kPVZhqNsuH4K1VviDxEKcvZ6XDPs157ZpgoG0X471Dhq3D7EYGMwmm5RUbW1Xiu-fOKPQBfc54Bo4n6vNJdoaS6B_Ibmu2Q`

	var token string
	var err error
	cache, mock := setJWTCacheTest()
	cache.RefreshClient()
	cache = cache.
		WithNowFunc(func() time.Time { return now.Time }).
		WithExpiryMargin(10 * time.Second).
		WithBackOffBase(0 * time.Second).
		WithVerifier(tokenVerifierFunc(func(ctx context.Context, token string) (bool, error) {
			return true, nil
		}))

	log.Infof("Test network is down")
	mock.token, mock.tokenErr = "", fmt.Errorf("network is down")
	_, err = cache.FetchToken(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "network is down")

	log.Infof("Test token is empty")
	mock.token, mock.tokenErr = "", nil
	token, err = cache.FetchToken(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "token is empty")

	log.Infof("Test token is invalid")
	mock.token, mock.tokenErr = "invalidToken", nil
	token, err = cache.FetchToken(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse token")

	log.Infof("Test token is already expired")
	mock.token, mock.tokenErr = token1, nil
	token, err = cache.FetchToken(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "token is already expired")
	log.Infof("err.Error(): %s", err.Error())

	// token2 is a valid token.
	log.Infof("Test fetch valid token with force refresh")
	mock.token, mock.tokenErr = token2, nil
	token, err = cache.FetchToken(ctx)
	assert.NoError(t, err)
	assert.Equal(t, token2, token)

	log.Infof("Test return valid token in memory")
	mock.token, mock.tokenErr = "invalidToken", nil
	// Since we now have a valid token in cache, Get() should return the
	// valid token in cache without refresh.
	token, err = cache.FetchToken(ctx)
	assert.NoError(t, err)
	assert.Equal(t, token2, token)

	// token2 is a valid token, but old JWKS
	log.Infof("Test fetch valid token with failed verify")
	mock.token, mock.tokenErr = token2, nil
	cache = cache.WithVerifier(tokenVerifierFunc(func(ctx context.Context, token string) (bool, error) {
		return false, nil
	}))
	token, err = cache.FetchToken(ctx)
	assert.NoError(t, err)
	assert.Equal(t, token2, token)

	// token2 is a valid token, but error retrieving JWKS
	log.Infof("Test fetch valid token with failure to verify")
	mock.token, mock.tokenErr = token2, nil
	cache = cache.WithVerifier(tokenVerifierFunc(func(ctx context.Context, token string) (bool, error) {
		return false, errors.New("jwks fetch failure")
	}))
	token, err = cache.FetchToken(ctx)
	assert.Error(t, err)
	assert.Empty(t, token)

	// Advance time, make token2 about to expire, e.g., five seconds
	// before token2 expires (2020-04-18 00:05:11 -0700 PDT). Since we
	// have ExpiryMargin set to 10 second, it should fetch a new token
	cache = cache.WithNowFunc(func() time.Time {
		return timeFromString("2020-04-18T00:05:06-07:00").Time
	}).WithVerifier(tokenVerifierFunc(func(ctx context.Context, token string) (bool, error) {
		return true, nil
	}))

	log.Infof("Test token about to expire, force refresh token")
	mock.token, mock.tokenErr = token3, nil
	token, err = cache.FetchToken(ctx)
	assert.NoError(t, err)
	assert.Equal(t, token3, token)

	// Tests for client side rate limit with exponential back off.
	//
	// - backOffBase: 2 seconds, backOffMax: 10 seconds, so cool down
	// sequence will be 2, 4, 8, 10, 10, ...
	//
	// - 1st attempted at 22:00:00 PDT, failed due to server error
	// (nextAvailable: 22:00:02)
	//
	// - 2nd attempted at 22:00:01 PDT, won't hit server.
	//
	// - 3rd attempted at 22:00:03 PDT, able to hit server, failed due
	// server error (nextAvailable: 22:00:07)
	//
	// - 4th attempted at 22:00:06 PDT, won't hit server.
	//
	// - 5th attempted at 22:00:08 PDT, able to hit server, failed due
	// server error (nextAvailable: 22:00:16)
	//
	// - 6th attempted at 22:00:15 PDT, won't hit server.
	//
	// - 7th attempted at 22:00:17 PDT, able to hit server, failed due
	// server error (nextAvailable: 22:00:27)
	//
	// - 8th attempted at 22:00:25 PDT, won't hit server.
	//
	// - 9th attempted at 22:00:26 PDT, won't hit server.
	//
	// - 10th attempted at 22:00:28 PDT, able to hit server, failed due
	// server error (nextAvailable: 22:00:38)
	mockTime := timeFromString("2020-12-09T22:00:00-07:00").Time
	cache, mock = setJWTCacheTest()
	cache = cache.
		WithNowFunc(func() time.Time {
			return mockTime
		}).
		WithBackOffBase(2 * time.Second).
		WithBackOffMax(10 * time.Second)
	mock.token, mock.tokenErr = "", fmt.Errorf("server error")

	log.Infof("Test 1st attempt at %v, server error", mockTime)
	_, err = cache.FetchToken(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "server error")

	mockTime = timeFromString("2020-12-09T22:00:01-07:00").Time
	log.Infof("Test 2nd attempt at %v, back off", mockTime)
	_, err = cache.FetchToken(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited")

	mockTime = timeFromString("2020-12-09T22:00:03-07:00").Time
	log.Infof("Test 3nd attempt at %v, server error", mockTime)
	_, err = cache.FetchToken(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "server error")

	mockTime = timeFromString("2020-12-09T22:00:06-07:00").Time
	log.Infof("Test 4th attempt at %v, back off", mockTime)
	_, err = cache.FetchToken(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited")

	mockTime = timeFromString("2020-12-09T22:00:08-07:00").Time
	log.Infof("Test 5th attempt at %v, server error", mockTime)
	_, err = cache.FetchToken(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "server error")

	mockTime = timeFromString("2020-12-09T22:00:15-07:00").Time
	log.Infof("Test 6th attempt at %v, back off", mockTime)
	_, err = cache.FetchToken(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited")

	mockTime = timeFromString("2020-12-09T22:00:17-07:00").Time
	log.Infof("Test 7th attempt at %v, server error", mockTime)
	_, err = cache.FetchToken(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "server error")

	mockTime = timeFromString("2020-12-09T22:00:25-07:00").Time
	log.Infof("Test 8th attempt at %v, back off", mockTime)
	_, err = cache.FetchToken(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited")

	mockTime = timeFromString("2020-12-09T22:00:26-07:00").Time
	log.Infof("Test 9th attempt at %v, back off", mockTime)
	_, err = cache.FetchToken(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited")

	mockTime = timeFromString("2020-12-09T22:00:28-07:00").Time
	log.Infof("Test 10th attempt at %v, server error", mockTime)
	_, err = cache.FetchToken(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "server error")

	_, err = cache.ForceNewToken(ctx)
	assert.Error(t, err)
}

type tokenVerifierFunc func(ctx context.Context, token string) (bool, error)

func (f tokenVerifierFunc) VerifyToken(ctx context.Context, token string) (bool, error) {
	return f(ctx, token)
}
