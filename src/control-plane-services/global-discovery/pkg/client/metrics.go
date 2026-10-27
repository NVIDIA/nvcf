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

package client

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
)

// Metric names. Every client in a process shares them; the label sets are
// bounded by the API (rpc, code), the configured and advertised URLs (url)
// and the watch and cache names (watch, cache).
const (
	MetricRequestsTotal          = "nvcf_gds_client_requests_total"
	MetricRequestDurationSeconds = "nvcf_gds_client_request_duration_seconds"
	MetricEndpointFailoversTotal = "nvcf_gds_client_endpoint_failovers_total"
	MetricConnected              = "nvcf_gds_client_connected"
	MetricWatchReconnectsTotal   = "nvcf_gds_client_watch_reconnects_total"
	MetricCacheAgeSeconds        = "nvcf_gds_client_cache_age_seconds"
)

// Watch and cache names used as metric labels by the built-in caches; a
// WatchLoop or cache may use any other bounded name.
const (
	WatchClusters       = "clusters"
	WatchServiceImports = "service_imports"
	WatchPools          = "pools"
	WatchRouters        = "routers"
)

// serviceDescs are the GDS services; their methods label the RPC metrics
// and the unary ones get the retry policy.
var serviceDescs = []*grpc.ServiceDesc{
	&gdsv1.ClusterService_ServiceDesc,
	&gdsv1.ServiceExportService_ServiceDesc,
	&gdsv1.ServiceImportService_ServiceDesc,
	&gdsv1.PoolService_ServiceDesc,
	&gdsv1.ServerInfoService_ServiceDesc,
}

// rpcNames returns the names of every GDS RPC.
func rpcNames() []string {
	var names []string
	for _, sd := range serviceDescs {
		for _, m := range sd.Methods {
			names = append(names, m.MethodName)
		}
		for _, s := range sd.Streams {
			names = append(names, s.StreamName)
		}
	}
	return names
}

// rpcName returns the RPC name of a full method name.
func rpcName(fullMethod string) string {
	return fullMethod[strings.LastIndex(fullMethod, "/")+1:]
}

type metrics struct {
	requests        *prometheus.CounterVec
	duration        *prometheus.HistogramVec
	failovers       prometheus.Counter
	connected       *prometheus.GaugeVec
	watchReconnects *prometheus.CounterVec
	cacheAge        *cacheAgeCollector
}

// register registers c with reg, returning the collector already
// registered under the same descriptors when another client registered it
// first.
func register[C prometheus.Collector](reg prometheus.Registerer, c C) (C, error) {
	if reg == nil {
		return c, nil
	}
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(C); ok {
				return existing, nil
			}
		}
		return c, err
	}
	return c, nil
}

func newMetrics(reg prometheus.Registerer, urls []string) (*metrics, error) {
	m := &metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricRequestsTotal,
			Help: "GDS client calls by RPC and gRPC status code. A watch counts once, when it ends.",
		}, []string{"rpc", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    MetricRequestDurationSeconds,
			Help:    "Duration of GDS client unary calls, retries included.",
			Buckets: prometheus.DefBuckets,
		}, []string{"rpc"}),
		failovers: prometheus.NewCounter(prometheus.CounterOpts{
			Name: MetricEndpointFailoversTotal,
			Help: "Connections established to a different GDS URL than the previous connection.",
		}),
		connected: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: MetricConnected,
			Help: "1 for the GDS URL the client is connected to, 0 for the others.",
		}, []string{"url"}),
		watchReconnects: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricWatchReconnectsTotal,
			Help: "Watch streams reopened after an error or the end of a stream.",
		}, []string{"watch"}),
		cacheAge: newCacheAgeCollector(),
	}
	var err error
	if m.requests, err = register(reg, m.requests); err != nil {
		return nil, err
	}
	if m.duration, err = register(reg, m.duration); err != nil {
		return nil, err
	}
	if m.failovers, err = register(reg, m.failovers); err != nil {
		return nil, err
	}
	if m.connected, err = register(reg, m.connected); err != nil {
		return nil, err
	}
	if m.watchReconnects, err = register(reg, m.watchReconnects); err != nil {
		return nil, err
	}
	if m.cacheAge, err = register(reg, m.cacheAge); err != nil {
		return nil, err
	}
	for _, rpc := range rpcNames() {
		for c := codes.OK; c <= codes.Unauthenticated; c++ {
			m.requests.WithLabelValues(rpc, c.String())
		}
	}
	for _, w := range []string{WatchClusters, WatchServiceImports, WatchPools, WatchRouters} {
		m.watchReconnects.WithLabelValues(w)
	}
	for _, u := range urls {
		m.connected.WithLabelValues(u).Set(0)
	}
	return m, nil
}

// cacheAgeCollector reports, at scrape time, the seconds since each running
// cache last confirmed its data with a list or a watch event.
type cacheAgeCollector struct {
	desc   *prometheus.Desc
	mu     sync.Mutex
	caches map[string]func() time.Time
}

func newCacheAgeCollector() *cacheAgeCollector {
	return &cacheAgeCollector{
		desc: prometheus.NewDesc(MetricCacheAgeSeconds,
			"Seconds since the cache last confirmed its data with a list or a watch event, or since it started.",
			[]string{"cache"}, nil),
		caches: map[string]func() time.Time{},
	}
}

func (c *cacheAgeCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *cacheAgeCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for name, fresh := range c.caches {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, now.Sub(fresh()).Seconds(), name)
	}
}

func (c *cacheAgeCollector) set(name string, fresh func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.caches[name] = fresh
}

func (c *cacheAgeCollector) remove(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.caches, name)
}

// unaryInterceptor applies RPCTimeout to calls without a deadline and
// records the call metrics. gRPC retries happen inside the invoker, so a
// call counts once whatever its attempts.
func (c *Client) unaryInterceptor(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	if c.cfg.RPCTimeout > 0 {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, c.cfg.RPCTimeout)
			defer cancel()
		}
	}
	start := time.Now()
	err := invoker(ctx, method, req, reply, cc, opts...)
	rpc := rpcName(method)
	c.metrics.requests.WithLabelValues(rpc, status.Code(err).String()).Inc()
	c.metrics.duration.WithLabelValues(rpc).Observe(time.Since(start).Seconds())
	return err
}

// streamInterceptor counts a stream once, with the status it ends with.
func (c *Client) streamInterceptor(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	rpc := rpcName(method)
	s, err := streamer(ctx, desc, cc, method, opts...)
	if err != nil {
		c.metrics.requests.WithLabelValues(rpc, status.Code(err).String()).Inc()
		return nil, err
	}
	return &meteredStream{ClientStream: s, report: func(err error) {
		code := codes.OK
		if !errors.Is(err, io.EOF) {
			code = status.Code(err)
		}
		c.metrics.requests.WithLabelValues(rpc, code.String()).Inc()
	}}, nil
}

type meteredStream struct {
	grpc.ClientStream
	once   sync.Once
	report func(error)
}

func (s *meteredStream) RecvMsg(m any) error {
	err := s.ClientStream.RecvMsg(m)
	if err != nil {
		s.once.Do(func() { s.report(err) })
	}
	return err
}
