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

package ratelimit

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type PubSubConsumerTestSuite struct {
	suite.Suite

	ctx        context.Context
	store      *fakeStore
	now        *atomic.Int64
	requestID1 string
	requestID2 string
}

func (s *PubSubConsumerTestSuite) SetupSuite() {
	s.requestID1 = "req_00aaaaaaaaaaaaaaaaaaaaaaaa"
	s.requestID2 = "req_00aaaaaaaaaaaaaaaaaaaaaaab"
}

func (s *PubSubConsumerTestSuite) SetupTest() {
	s.ctx = context.Background()
	s.store = newFakeStore()
	s.now = &atomic.Int64{}
	s.now.Store(time.Unix(1_700_000_000, 0).UnixMilli())
}

func (s *PubSubConsumerTestSuite) newLimiter() RateLimiter {
	s.T().Helper()
	rl, err := NewRateLimiter(
		s.store,
		withClock(func() int64 { return s.now.Load() }),
	)
	s.Require().NoError(err)
	return rl
}

func (s *PubSubConsumerTestSuite) TestFastFromOtherCluster() {
	rateLimiter := s.newLimiter()
	f := PubSubConsumer(rateLimiter, "cluster-test2", true)

	rle := RateLimitEventWireFormat{
		Key:         "foo1",
		Units:       200,
		Rate:        300,
		Period:      1 * time.Minute,
		ClusterName: "cluster-test1",
		RequestID:   s.requestID1,
		CreatedAt:   time.Now().Unix(),
	}

	err := f(s.ctx, &rle, nil)
	s.Require().NoError(err)

	rlr, err := rateLimiter.CheckLimit(
		s.ctx,
		rle.Key,
		RateLimit{
			Limit:  rle.Rate,
			Period: rle.Period,
		},
		rle.Units,
		true,
		s.requestID2,
		false,
	)
	s.Require().NoError(err)
	s.False(rlr.Allowed())
}

func (s *PubSubConsumerTestSuite) TestFastFromSameCluster() {
	rateLimiter := s.newLimiter()
	f := PubSubConsumer(rateLimiter, "cluster-test2", true)

	rle := RateLimitEventWireFormat{
		Key:         "foo2",
		Units:       200,
		Rate:        300,
		Period:      1 * time.Minute,
		ClusterName: "cluster-test2",
		RequestID:   s.requestID1,
		CreatedAt:   time.Now().Unix(),
	}

	err := f(s.ctx, &rle, nil)
	s.Require().NoError(err)

	rlr, err := rateLimiter.CheckLimit(
		s.ctx,
		rle.Key,
		RateLimit{
			Limit:  rle.Rate,
			Period: rle.Period,
		},
		rle.Units,
		true,
		s.requestID2,
		false,
	)
	s.Require().NoError(err)
	s.True(rlr.Allowed())
}

func (s *PubSubConsumerTestSuite) TestSlowFromOtherCluster() {
	rateLimiter := s.newLimiter()
	f := PubSubConsumer(rateLimiter, "cluster-test2", true)

	// Events older than 120s are dropped as stale, so the subsequent check
	// sees a fresh bucket and remains allowed.
	rle := RateLimitEventWireFormat{
		Key:         "foo3",
		Units:       200,
		Rate:        300,
		Period:      1 * time.Minute,
		ClusterName: "cluster-test1",
		RequestID:   s.requestID1,
		CreatedAt:   time.Now().Add(-125 * time.Second).Unix(),
	}

	err := f(s.ctx, &rle, nil)
	s.Require().NoError(err)

	rlr, err := rateLimiter.CheckLimit(
		s.ctx,
		rle.Key,
		RateLimit{
			Limit:  rle.Rate,
			Period: rle.Period,
		},
		rle.Units,
		true,
		s.requestID2,
		false,
	)
	s.Require().NoError(err)
	s.True(rlr.Allowed())
}

func TestPubSubConsumerTestSuite(t *testing.T) {
	suite.Run(t, new(PubSubConsumerTestSuite))
}
