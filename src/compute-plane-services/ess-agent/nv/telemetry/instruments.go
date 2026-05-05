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

package telemetry

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Instruments manages the various metric instruments for monitoring the runner
type Instruments struct {
	// MeasureTemplatesunterTemplatesRendered is a counter of how many templates are configured
	// for rendering.
	MeasureTemplates metric.Int64Counter

	// CounterTemplatesRendered is a counter for the number of templates rendered
	// during a run that did render or not.
	CounterTemplatesRendered metric.Int64Counter

	// CounterTemplateSecretFetchFailures is a counter tracking number of succes/failures
	// on secret fetch
	CounterTemplateSecretFetchFailures metric.Int64Counter

	// StoppedTemplatesCount is a gauge showing current number of stopped templates
	StoppedTemplatesCount metric.Int64ObservableGauge

	// LastTokenFileRefreshTimestamp reports time elapsed since Unix epoch.
	LastTokenFileRefreshTimestamp metric.Int64ObservableGauge

	// ProcessUptimeInSec reports uptime in seconds every reporting interval
	ProcessUptimeInSec metric.Int64Counter

	tags map[string]string
}

var lastTokenFileRefreshTimestampVal int64
var stoppedTemplatesCountVal int64
var m sync.Mutex

// SetLastTokeFileRefreshTimestamp allows to update the last token file refresh timestamp
func SetLastTokeFileRefreshTimestamp(val int64) {
	m.Lock()
	defer m.Unlock()

	lastTokenFileRefreshTimestampVal = val
}

// SetStoppedTemplatesCount allows to update the stopped templates count
func SetStoppedTemplatesCount(val int64) {
	m.Lock()
	defer m.Unlock()

	stoppedTemplatesCountVal = val
}

type instrumentsParams struct {
	meter metric.Meter
	tags  map[string]string
}

// newInstruments returns instrumentations that can be tracked for metrics purpose
func newInstruments(params instrumentsParams) (*Instruments, error) {
	measureTmpls, err := params.meter.Int64Counter("ess.configured_templates",
		metric.WithDescription("The number of templates configured."),
	)
	if err != nil {
		return nil, err
	}

	renderedTmpls, err := params.meter.Int64Counter("ess.templates_rendered",
		metric.WithDescription("A counter of templates rendered with labels "+
			"id=templateID and status=(success|fail)"))
	if err != nil {
		return nil, err
	}

	templateFetchErrors, err := params.meter.Int64Counter("ess.templates_request",
		metric.WithDescription("A counter of template secret fetch status with labels "+
			"id=templateID secret=secretPath and status=(success|fail)"))
	if err != nil {
		return nil, err
	}

	processUptime, err := params.meter.Int64Counter("ess.process_uptime_sec",
		metric.WithDescription("The uptime of the current process in seconds."))
	if err != nil {
		return nil, err
	}

	cback := func(_ context.Context, o metric.Int64Observer) error {
		m.Lock()
		defer m.Unlock()

		o.Observe(lastTokenFileRefreshTimestampVal, labels(params.tags))
		return nil
	}
	lastTokenFileRefreshTimestamp, err := params.meter.Int64ObservableGauge("ess.last_token_file_refresh_unix",
		metric.WithDescription("The last ess token file refresh unix timestamp since epoch"),
		metric.WithInt64Callback(cback),
	)
	if err != nil {
		return nil, err
	}

	stoppedTemplatesCallback := func(_ context.Context, o metric.Int64Observer) error {
		m.Lock()
		defer m.Unlock()

		o.Observe(stoppedTemplatesCountVal, labels(params.tags))
		return nil
	}
	stoppedTemplatesGauge, err := params.meter.Int64ObservableGauge("ess.templates_stopped_total",
		metric.WithDescription("The current number of templates stopped due to client errors"),
		metric.WithInt64Callback(stoppedTemplatesCallback),
	)
	if err != nil {
		return nil, err
	}

	return &Instruments{
		CounterTemplatesRendered:           renderedTmpls,
		CounterTemplateSecretFetchFailures: templateFetchErrors,
		StoppedTemplatesCount:              stoppedTemplatesGauge,
		LastTokenFileRefreshTimestamp:      lastTokenFileRefreshTimestamp,
		MeasureTemplates:                   measureTmpls,
		ProcessUptimeInSec:                 processUptime,
		tags:                               params.tags,
	}, nil
}

func (r *Instruments) Labels(labels map[string]string) metric.MeasurementOption {
	var attributes []attribute.KeyValue
	for k, v := range r.tags {
		attributes = append(attributes, attribute.Key(k).String(v))
	}
	for k, v := range labels {
		attributes = append(attributes, attribute.Key(k).String(v))
	}
	return metric.WithAttributes(attributes...)
}

func labels(labels map[string]string) metric.MeasurementOption {
	var attributes []attribute.KeyValue
	for k, v := range labels {
		attributes = append(attributes, attribute.Key(k).String(v))
	}
	return metric.WithAttributes(attributes...)
}
