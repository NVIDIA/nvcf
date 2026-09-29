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

package registration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	dto "github.com/prometheus/client_model/go"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/metrics"
)

// The Pylon series the observer reads. Pylon's CI pins them with a contract
// test and only the latest Pylon ships with the stack, so the adapter does
// not tolerate renamed series.
const (
	// StreamConnectedSeries is 1 per router while the registration stream
	// to that router is open and acknowledged.
	StreamConnectedSeries = "pylon_registration_stream_connected"
	// TunnelConnectedSeries is 1 per router while the QUIC reverse tunnel to
	// that router is up.
	TunnelConnectedSeries = "pylon_reverse_tunnel_connected"
	// StreamClosuresSeries counts registration stream closures by router and
	// reason.
	StreamClosuresSeries = "pylon_registration_stream_closures_total"
	// RouterLabel names the router pod of a series.
	RouterLabel = "router"
	// ReasonLabel is the closure reason of StreamClosuresSeries.
	ReasonLabel = "reason"
	// ClosureUnauthenticated: the router rejected the cluster credential.
	ClosureUnauthenticated = "unauthenticated"
	// ClosureInvalidArgument: the router rejected the registration, for
	// example a cluster_id that differs from the authenticated one.
	ClosureInvalidArgument = "invalid_argument"
)

const (
	// maxBodyBytes caps how much of a metrics response is read. One Pylon
	// exposes about 50 to 80 KiB at most.
	maxBodyBytes = 4 << 20
	// acceptHeader asks for the Prometheus text format that Parse reads.
	acceptHeader = "text/plain; version=0.0.4"
)

// Closure identifies one series of StreamClosuresSeries.
type Closure struct {
	Router string
	Reason string
}

// Sample is what one scrape of a transport pod reports.
type Sample struct {
	// Streams are the routers, sorted, whose StreamConnectedSeries is
	// non-zero.
	Streams []string
	// Tunnels are the routers, sorted, whose TunnelConnectedSeries is
	// non-zero.
	Tunnels []string
	// Rejections are the values of StreamClosuresSeries whose reason is
	// ClosureUnauthenticated or ClosureInvalidArgument.
	Rejections map[Closure]float64
}

// rejecting reports whether a closure reason is the router's rejection of
// the registration rather than a disconnect.
func rejecting(reason string) bool {
	return reason == ClosureUnauthenticated || reason == ClosureInvalidArgument
}

// Parse reads a Prometheus text exposition and extracts the three series.
// Every other family is ignored. Counters exposed under an OpenMetrics
// family name without the _total suffix parse as untyped and are read the
// same way.
func Parse(r io.Reader) (Sample, error) {
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(r)
	if err != nil {
		return Sample{}, err
	}
	s := Sample{
		Streams:    connectedRouters(families[StreamConnectedSeries]),
		Tunnels:    connectedRouters(families[TunnelConnectedSeries]),
		Rejections: map[Closure]float64{},
	}
	if mf := families[StreamClosuresSeries]; mf != nil {
		for _, m := range mf.GetMetric() {
			c := Closure{Router: label(m, RouterLabel), Reason: label(m, ReasonLabel)}
			if v, ok := value(m); ok && rejecting(c.Reason) {
				s.Rejections[c] += v
			}
		}
	}
	return s, nil
}

// connectedRouters returns the distinct router labels of the series in mf
// with a non-zero value, sorted.
func connectedRouters(mf *dto.MetricFamily) []string {
	if mf == nil {
		return nil
	}
	seen := map[string]struct{}{}
	for _, m := range mf.GetMetric() {
		if v, ok := value(m); ok && v > 0 {
			seen[label(m, RouterLabel)] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func label(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}

// value reads a gauge, counter or untyped sample.
func value(m *dto.Metric) (float64, bool) {
	switch {
	case m.GetGauge() != nil:
		return m.GetGauge().GetValue(), true
	case m.GetCounter() != nil:
		return m.GetCounter().GetValue(), true
	case m.GetUntyped() != nil:
		return m.GetUntyped().GetValue(), true
	}
	return 0, false
}

func sortedKeys[V any](m map[string]V) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ScrapeError is a failed scrape and its failure reason.
type ScrapeError struct {
	Reason metrics.ScrapeFailureReason
	Err    error
}

func (e *ScrapeError) Error() string { return fmt.Sprintf("%s: %v", e.Reason, e.Err) }

func (e *ScrapeError) Unwrap() error { return e.Err }

func scrapeFailure(reason metrics.ScrapeFailureReason, err error) *ScrapeError {
	return &ScrapeError{Reason: reason, Err: err}
}

// transportFailure classifies an error of the request or of reading the
// body as a timeout or a connection failure.
func transportFailure(err error) *ScrapeError {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return scrapeFailure(metrics.ScrapeFailureTimeout, err)
	}
	return scrapeFailure(metrics.ScrapeFailureConnect, err)
}

// scrape GETs url within the observer's timeout and parses the body. Every
// error it returns is a *ScrapeError.
func (o *Observer) scrape(ctx context.Context, url string) (Sample, error) {
	ctx, cancel := context.WithTimeout(ctx, o.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Sample{}, scrapeFailure(metrics.ScrapeFailureConnect, err)
	}
	req.Header.Set("Accept", acceptHeader)
	resp, err := o.Client.Do(req)
	if err != nil {
		return Sample{}, transportFailure(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
		return Sample{}, scrapeFailure(metrics.ScrapeFailureStatus, fmt.Errorf("GET %s returned HTTP %d", url, resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return Sample{}, transportFailure(err)
	}
	if len(body) > maxBodyBytes {
		return Sample{}, scrapeFailure(metrics.ScrapeFailureParse, fmt.Errorf("GET %s returned more than %d bytes", url, maxBodyBytes))
	}
	s, err := Parse(bytes.NewReader(body))
	if err != nil {
		return Sample{}, scrapeFailure(metrics.ScrapeFailureParse, err)
	}
	return s, nil
}
