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

// Package testutils implements utilities for tests
//
// The idea is that if this package is only included in tests,
// it won't be included in the production app.
package testutils

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.uber.org/zap/zaptest"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
)

func InitTestLogger(t *testing.T) *otelzap.Logger {
	logger := zaptest.NewLogger(t)
	return otelzap.New(logger)
}

func InitTestVersion(t *testing.T) string {
	t.Helper()
	return "testVersion"
}

func TestRegexOnStrings(t *testing.T, regexs []string, contents string) {
	for _, metric := range regexs {
		t.Run(metric, func(tt *testing.T) {
			re, err := regexp.Compile(metric)
			if err != nil {
				tt.Fatalf("failed to compile regex: %v", err)
			}

			if !re.MatchString(contents) {
				tt.Errorf("missing %s", metric)
			}
		})
	}
}

type TestPublisher struct {
	events   []types.StageTransitionEvent
	eventsV2 []types.DeploymentStageTransitionEvent
}

func NewTestPublisher() *TestPublisher {
	return &TestPublisher{
		events:   make([]types.StageTransitionEvent, 0),
		eventsV2: make([]types.DeploymentStageTransitionEvent, 0),
	}
}

func (p *TestPublisher) Publish(_ context.Context, event types.StageTransitionEvent) {
	p.events = append(p.events, event)
}

func (p *TestPublisher) PublishV2(_ context.Context, event types.DeploymentStageTransitionEvent) {
	p.eventsV2 = append(p.eventsV2, event)
}

func (p *TestPublisher) GetEvents() []types.StageTransitionEvent {
	return p.events
}

func (p *TestPublisher) GetEventsV2() []types.DeploymentStageTransitionEvent {
	return p.eventsV2
}

type TestIndexer struct {
	events   []types.StageTransitionEvent
	eventsV2 []types.DeploymentStageTransitionEvent
}

func NewTestIndexer() *TestIndexer {
	return &TestIndexer{
		events:   make([]types.StageTransitionEvent, 0),
		eventsV2: make([]types.DeploymentStageTransitionEvent, 0),
	}
}

func (i *TestIndexer) Index(_ context.Context, event types.StageTransitionEvent) error {
	i.events = append(i.events, event)
	return nil
}

func (i *TestIndexer) IndexV2(_ context.Context, event types.DeploymentStageTransitionEvent) error {
	i.eventsV2 = append(i.eventsV2, event)
	return nil
}

type FailingTestIndexer struct{}

func NewFailingTestIndexer() *FailingTestIndexer {
	return &FailingTestIndexer{}
}

func (i *FailingTestIndexer) Index(_ context.Context, event types.StageTransitionEvent) error {
	return errors.New("indexer failure")
}

func (i *FailingTestIndexer) IndexV2(_ context.Context, event types.DeploymentStageTransitionEvent) error {
	return errors.New("indexer v2 failure")
}
