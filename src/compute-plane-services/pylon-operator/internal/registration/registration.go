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

// Package registration is the registration observer. It scrapes Pylon's
// /metrics on every transport pod of an InferenceEndpoint and derives the
// TransportReady and Registered conditions, status.registration and
// status.servers from three series: the registration stream and reverse
// tunnel gauges per router, and the counter of registration stream closures
// by router and reason. It never contacts the router or the gateway.
package registration

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/transport"
)

const (
	// MetricsPath is Pylon's metrics path on transport.MetricsPort.
	MetricsPath = "/metrics"
	// DefaultTimeout bounds each scrape.
	DefaultTimeout = 2 * time.Second
	// JitterFraction is the largest fraction of the scrape interval added to
	// or removed from each requeue, so the scrapes of many endpoints spread
	// out.
	JitterFraction = 0.1
	// RejectionMemory is how long a rejecting closure keeps Registered
	// False with reason RegistrationRejected after it was observed.
	RejectionMemory = 60 * time.Second
	// RouterUnreachableGrace is how long the first transport pod must have
	// been ready without any registration stream before Registered reports
	// RouterUnreachable instead of Pending.
	RouterUnreachableGrace = 30 * time.Second
	// LastRegisteredRefresh is how stale status.registration.lastRegisteredTime
	// may get while Registered stays True. Refreshing it on every scrape
	// would write status every scrape interval.
	LastRegisteredRefresh = time.Minute

	// EventReasonRegistrationStreamRejected is the reason of the Warning
	// Event emitted when a scrape shows new rejecting closures. It adds the
	// pod, router and closure reason to the Registered transition Event and
	// also reports rejections by one router while another keeps Registered
	// True.
	EventReasonRegistrationStreamRejected = "RegistrationStreamRejected"

	// maxListed caps the routers or rejections quoted in a message.
	maxListed = 3
)

// Verdict is the computed value of one condition.
type Verdict[R ~string] struct {
	Status  metav1.ConditionStatus
	Reason  R
	Message string
}

// Result is what one observation computed. The registration step writes it
// to the ReconcileContext.
type Result struct {
	TransportReady Verdict[pylonv1alpha1.TransportReadyReason]
	Registered     Verdict[pylonv1alpha1.RegisteredReason]
	Registration   pylonv1alpha1.RegistrationStatus
	// Servers has one entry per transport pod, sorted by pod name.
	Servers []pylonv1alpha1.ServerStatus
	// Rejected describes the rejecting closures first seen in this
	// observation, for a Warning Event; empty when there were none.
	Rejected string
}

// Observer scrapes transport pods and keeps, per endpoint, the closure
// counters of the previous scrape. Reader, Client, MetricsURL, Timeout, Now
// and Rand are injectable for tests.
type Observer struct {
	// Reader lists transport pods.
	Reader client.Reader
	// Client sends the scrapes.
	Client *http.Client
	// MetricsURL returns the metrics URL of a pod with an IP.
	MetricsURL func(*corev1.Pod) string
	// Timeout bounds each scrape.
	Timeout time.Duration
	// Metrics records scrape failures and the time to first registration.
	// Nil disables recording.
	Metrics *metrics.Metrics
	// Now is the clock.
	Now func() time.Time
	// Rand returns a number in [0, 1) for the requeue jitter.
	Rand func() float64
	// Started is when the observer began. The closure counters of a pod
	// that started before it are taken as the baseline at first sight,
	// since the observer cannot tell when they rose; the counters of a pod
	// that started later count from zero.
	Started time.Time

	mu        sync.Mutex
	endpoints map[types.NamespacedName]*endpointState
}

// endpointState is what the observer remembers about one endpoint between
// scrapes.
type endpointState struct {
	uid types.UID
	// baselines holds, per pod that was scraped successfully, the rejecting
	// closure counters of its last successful scrape.
	baselines map[string]map[Closure]float64
	// lastRejection is when a rise of a rejecting closure counter was last
	// observed, and lastRejectionDetail describes it.
	lastRejection       time.Time
	lastRejectionDetail string
	// registeredSeen is set once Registered was True, so the time to
	// registration is observed at most once.
	registeredSeen bool
}

// New returns an Observer that scrapes pods by IP on transport.MetricsPort.
func New(reader client.Reader, m *metrics.Metrics) *Observer {
	return &Observer{
		Reader:     reader,
		Client:     &http.Client{},
		MetricsURL: PodMetricsURL,
		Timeout:    DefaultTimeout,
		Metrics:    m,
		Now:        time.Now,
		Rand:       rand.Float64,
		Started:    time.Now(),
	}
}

// PodMetricsURL is http://<pod IP>:<transport.MetricsPort>/metrics.
func PodMetricsURL(pod *corev1.Pod) string {
	return "http://" + net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(int(transport.MetricsPort))) + MetricsPath
}

// NextScrape returns interval moved by up to JitterFraction of it in either
// direction, or zero when interval is not positive.
func (o *Observer) NextScrape(interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	r := rand.Float64
	if o.Rand != nil {
		r = o.Rand
	}
	return interval + time.Duration((2*r()-1)*JitterFraction*float64(interval))
}

// Forget drops what the observer remembers about an endpoint that no
// longer exists.
func (o *Observer) Forget(key types.NamespacedName) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.endpoints, key)
}

// Idle returns status.registration for a reconcile that did not scrape
// because the transport step reached a fast negative: no router is
// connected, and lastRegisteredTime is kept.
func Idle(previous *pylonv1alpha1.RegistrationStatus, clusterID string) *pylonv1alpha1.RegistrationStatus {
	r := &pylonv1alpha1.RegistrationStatus{ClusterID: clusterID}
	if previous != nil {
		r.LastRegisteredTime = previous.LastRegisteredTime
	}
	return r
}

func (o *Observer) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *Observer) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return DefaultTimeout
}

// podScrape is the outcome of scraping one pod. ok is false when the pod
// was not scraped or the scrape failed.
type podScrape struct {
	sample Sample
	ok     bool
}

// Observe scrapes the transport pods of ep and computes the conditions and
// status it owns. ep is the reconcile's working copy, with Ready as computed
// in the same reconcile; st is the transport step's State. It must not be
// called while the transport step reports a fast negative.
func (o *Observer) Observe(ctx context.Context, ep *pylonv1alpha1.InferenceEndpoint, st transport.State, clusterID string) (Result, error) {
	selector := st.PodLabels
	if len(selector) == 0 {
		selector = transport.PodLabels(ep)
	}
	var list corev1.PodList
	if err := o.Reader.List(ctx, &list, client.InNamespace(ep.Namespace), client.MatchingLabels(selector)); err != nil {
		return Result{}, fmt.Errorf("listing transport pods: %w", err)
	}
	pods := transportPods(list.Items)
	scrapes := o.scrapeAll(ctx, pods)
	now := o.now()
	agg := aggregate(pods, scrapes)

	key := types.NamespacedName{Namespace: ep.Namespace, Name: ep.Name}
	o.mu.Lock()
	state := o.stateLocked(key, ep.UID)
	fresh := state.observe(pods, scrapes, o.Started, now)
	rejected := !state.lastRejection.IsZero() && now.Sub(state.lastRejection) <= RejectionMemory
	rejectionDetail := state.lastRejectionDetail
	firstRegistration := false
	if agg.podsWithStream > 0 && !state.registeredSeen {
		state.registeredSeen = true
		firstRegistration = true
	}
	o.mu.Unlock()

	previous := ep.Status.Registration
	if firstRegistration && (previous == nil || previous.LastRegisteredTime == nil) && !ep.CreationTimestamp.IsZero() {
		o.Metrics.ObserveTimeToRegistered(now.Sub(ep.CreationTimestamp.Time))
	}

	ready := meta.IsStatusConditionTrue(ep.Status.Conditions, string(pylonv1alpha1.ConditionReady))
	res := Result{
		Registered:     registeredVerdict(agg, ready, rejected, rejectionDetail, st, now),
		TransportReady: transportReadyVerdict(agg, ready, st),
		Registration:   registrationStatus(ep, clusterID, agg, now),
		Servers:        servers(ep, clusterID, pods, scrapes),
	}
	if len(fresh) > 0 {
		res.Rejected = "Registration rejected since the last scrape: " + listN(fresh, "; ")
	}
	return res, nil
}

// transportPods drops pods in a terminal phase, which a Deployment leaves
// behind after an eviction, and sorts the rest by name.
func transportPods(items []corev1.Pod) []corev1.Pod {
	out := make([]corev1.Pod, 0, len(items))
	for _, p := range items {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func scrapeable(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != ""
}

// scrapeAll scrapes every running pod with an IP concurrently, so one
// reconcile waits at most one timeout. A pod that cannot be scraped counts
// as having no streams and no tunnels.
func (o *Observer) scrapeAll(ctx context.Context, pods []corev1.Pod) []podScrape {
	out := make([]podScrape, len(pods))
	var wg sync.WaitGroup
	for i := range pods {
		if !scrapeable(&pods[i]) {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			url := o.MetricsURL(&pods[i])
			s, err := o.scrape(ctx, url)
			if err != nil {
				reason := metrics.ScrapeFailureConnect
				var se *ScrapeError
				if errors.As(err, &se) {
					reason = se.Reason
				}
				o.Metrics.IncScrapeFailure(reason)
				log.FromContext(ctx).V(1).Info("Scraping a transport pod failed; counting it as no streams and no tunnels",
					"pod", pods[i].Name, "url", url, "reason", string(reason), "error", err.Error())
				return
			}
			out[i] = podScrape{sample: s, ok: true}
		}(i)
	}
	wg.Wait()
	return out
}

func (o *Observer) stateLocked(key types.NamespacedName, uid types.UID) *endpointState {
	if o.endpoints == nil {
		o.endpoints = map[types.NamespacedName]*endpointState{}
	}
	s := o.endpoints[key]
	if s == nil || s.uid != uid {
		// A new endpoint, or one deleted and recreated under the same name.
		s = &endpointState{uid: uid, baselines: map[string]map[Closure]float64{}}
		o.endpoints[key] = s
	}
	return s
}

// observe compares the rejecting closure counters of each scraped pod with
// its baseline, records the rises and makes the current values the new
// baselines. It returns a description of every rise.
//
// A pod scraped before compares with its last successful scrape, and a
// series that appeared since counts from zero. A counter below its baseline
// means Pylon restarted and its counters with it, so the baseline is zero
// again and the whole current value is new. A pod never scraped before
// counts from zero only when it started after the observer; otherwise its
// current values become the baseline. A pod that could not be scraped keeps
// its baseline, and a pod that is gone is dropped.
func (s *endpointState) observe(pods []corev1.Pod, scrapes []podScrape, started, now time.Time) []string {
	var fresh []string
	live := make(map[string]struct{}, len(pods))
	for i := range pods {
		pod := &pods[i]
		live[pod.Name] = struct{}{}
		if !scrapes[i].ok {
			continue
		}
		current := scrapes[i].sample.Rejections
		baseline, known := s.baselines[pod.Name]
		fromZero := known || (pod.Status.StartTime != nil && pod.Status.StartTime.After(started))
		for _, c := range closureKeys(current, baseline) {
			cur := current[c]
			prev, had := baseline[c]
			switch {
			case !had && !fromZero:
				prev = cur
			case !had, cur < prev:
				prev = 0
			}
			if cur > prev {
				fresh = append(fresh, fmt.Sprintf("router %q closed the stream of pod %s with %s (%s new)",
					c.Router, pod.Name, c.Reason, strconv.FormatFloat(cur-prev, 'f', -1, 64)))
			}
		}
		next := make(map[Closure]float64, len(current))
		for c, v := range current {
			next[c] = v
		}
		s.baselines[pod.Name] = next
	}
	for name := range s.baselines {
		if _, ok := live[name]; !ok {
			delete(s.baselines, name)
		}
	}
	if len(fresh) > 0 {
		s.lastRejection = now
		s.lastRejectionDetail = listN(fresh, "; ")
	}
	return fresh
}

// closureKeys returns the keys of a and b, sorted.
func closureKeys(a, b map[Closure]float64) []Closure {
	seen := make(map[Closure]struct{}, len(a)+len(b))
	for c := range a {
		seen[c] = struct{}{}
	}
	for c := range b {
		seen[c] = struct{}{}
	}
	out := make([]Closure, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Router != out[j].Router {
			return out[i].Router < out[j].Router
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

// aggregation is the scrape collated across pods.
type aggregation struct {
	pods               int
	running            int
	unscraped          int
	podsWithStream     int
	podsWithTunnel     int
	podsStreamNoTunnel int
	streamRouters      []string
	tunnelRouters      []string
	firstReady         time.Time
	firstReadyKnown    bool
}

func aggregate(pods []corev1.Pod, scrapes []podScrape) aggregation {
	a := aggregation{pods: len(pods)}
	streams := map[string]struct{}{}
	tunnels := map[string]struct{}{}
	for i := range pods {
		pod := &pods[i]
		if pod.Status.Phase == corev1.PodRunning {
			a.running++
			if !scrapes[i].ok {
				a.unscraped++
			}
			if t, ok := readySince(pod); ok && (!a.firstReadyKnown || t.Before(a.firstReady)) {
				a.firstReady, a.firstReadyKnown = t, true
			}
		}
		s := scrapes[i].sample
		for _, r := range s.Streams {
			streams[r] = struct{}{}
		}
		for _, r := range s.Tunnels {
			tunnels[r] = struct{}{}
		}
		if len(s.Streams) > 0 {
			a.podsWithStream++
		}
		if len(s.Tunnels) > 0 {
			a.podsWithTunnel++
		}
		if len(s.Streams) > 0 && len(s.Tunnels) == 0 {
			a.podsStreamNoTunnel++
		}
	}
	a.streamRouters = sortedKeys(streams)
	a.tunnelRouters = sortedKeys(tunnels)
	return a
}

// readySince is when a running pod became ready: the last transition of
// its Ready condition to True, or its start time when it reports no Ready
// condition. A running pod whose Ready condition is not True is not ready.
func readySince(pod *corev1.Pod) (time.Time, bool) {
	for _, c := range pod.Status.Conditions {
		if c.Type != corev1.PodReady {
			continue
		}
		if c.Status != corev1.ConditionTrue {
			return time.Time{}, false
		}
		if !c.LastTransitionTime.IsZero() {
			return c.LastTransitionTime.Time, true
		}
		break
	}
	if pod.Status.StartTime != nil {
		return pod.Status.StartTime.Time, true
	}
	return time.Time{}, false
}

const (
	waitingStreamMessage = "Ready is False, so Pylon idles until the backend answers its health probe and opens no registration stream"
	waitingTunnelMessage = "Ready is False, so Pylon idles until the backend answers its health probe and opens no reverse tunnel"
)

// registeredVerdict applies the reasons of Registered in order. A stream
// from any pod to any router is registration, whatever the backend does:
// Ready False with Registered True is the shape of a backend outage. While
// Ready is False and no stream is open, Pylon is expected to idle.
func registeredVerdict(a aggregation, ready, rejected bool, rejection string, st transport.State, now time.Time) Verdict[pylonv1alpha1.RegisteredReason] {
	switch {
	case a.podsWithStream > 0:
		return Verdict[pylonv1alpha1.RegisteredReason]{
			Status: metav1.ConditionTrue,
			Reason: pylonv1alpha1.RegisteredReasonRegisteredWithRouter,
			Message: fmt.Sprintf("%d of %d transport pods have a registration stream open to %s",
				a.podsWithStream, a.pods, describeRouters(a.streamRouters)),
		}
	case !ready:
		return Verdict[pylonv1alpha1.RegisteredReason]{
			Status: metav1.ConditionFalse, Reason: pylonv1alpha1.RegisteredReasonWaitingForUpstream, Message: waitingStreamMessage,
		}
	case rejected:
		return Verdict[pylonv1alpha1.RegisteredReason]{
			Status: metav1.ConditionFalse,
			Reason: pylonv1alpha1.RegisteredReasonRegistrationRejected,
			Message: fmt.Sprintf("The router rejected the registration: %s; check that the cluster credential matches the router's and that --cluster-id is the cluster the router expects",
				rejection),
		}
	case st.ReadyReplicas > 0 && a.running > 0 && a.firstReadyKnown && now.Sub(a.firstReady) > RouterUnreachableGrace:
		return Verdict[pylonv1alpha1.RegisteredReason]{
			Status: metav1.ConditionFalse,
			Reason: pylonv1alpha1.RegisteredReasonRouterUnreachable,
			Message: fmt.Sprintf("No transport pod has a registration stream open more than %s after the first became ready%s; check --router-grpc-address and that the router's gRPC port is reachable from the transport pods",
				RouterUnreachableGrace, unscrapedNote(a)),
		}
	}
	return Verdict[pylonv1alpha1.RegisteredReason]{
		Status:  metav1.ConditionFalse,
		Reason:  pylonv1alpha1.RegisteredReasonPending,
		Message: "Transport pods are starting and opening registration streams" + unscrapedNote(a),
	}
}

// transportReadyVerdict: True when the Deployment has ready pods and any
// pod has a reverse tunnel to any router.
func transportReadyVerdict(a aggregation, ready bool, st transport.State) Verdict[pylonv1alpha1.TransportReadyReason] {
	switch {
	case st.ReadyReplicas > 0 && a.podsWithTunnel > 0:
		return Verdict[pylonv1alpha1.TransportReadyReason]{
			Status: metav1.ConditionTrue,
			Reason: pylonv1alpha1.TransportReadyReasonPylonConnected,
			Message: fmt.Sprintf("%d of %d transport pods have a reverse tunnel connected to %s",
				a.podsWithTunnel, a.pods, describeRouters(a.tunnelRouters)),
		}
	case !ready:
		return Verdict[pylonv1alpha1.TransportReadyReason]{
			Status: metav1.ConditionFalse, Reason: pylonv1alpha1.TransportReadyReasonWaitingForUpstream, Message: waitingTunnelMessage,
		}
	}
	msg := "No transport pod has a reverse tunnel connected"
	if a.podsStreamNoTunnel > 0 {
		msg += fmt.Sprintf("; %d of %d pods have a registration stream but no tunnel, which points to a blocked UDP port or a QUIC certificate or SNI mismatch",
			a.podsStreamNoTunnel, a.pods)
	}
	return Verdict[pylonv1alpha1.TransportReadyReason]{
		Status:  metav1.ConditionFalse,
		Reason:  pylonv1alpha1.TransportReadyReasonTunnelNotConnected,
		Message: msg + unscrapedNote(a),
	}
}

func unscrapedNote(a aggregation) string {
	if a.unscraped == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d of %d running pods could not be scraped on port %d)", a.unscraped, a.running, transport.MetricsPort)
}

// registrationStatus sets lastRegisteredTime when Registered becomes True
// and refreshes it while True once it is LastRegisteredRefresh old.
func registrationStatus(ep *pylonv1alpha1.InferenceEndpoint, clusterID string, a aggregation, now time.Time) pylonv1alpha1.RegistrationStatus {
	r := pylonv1alpha1.RegistrationStatus{ClusterID: clusterID, RoutersConnected: int32(len(a.streamRouters))}
	var last *metav1.Time
	if ep.Status.Registration != nil {
		last = ep.Status.Registration.LastRegisteredTime
	}
	r.LastRegisteredTime = last
	if a.podsWithStream == 0 {
		return r
	}
	wasRegistered := meta.IsStatusConditionTrue(ep.Status.Conditions, string(pylonv1alpha1.ConditionRegistered))
	if !wasRegistered || last == nil || now.Sub(last.Time) >= LastRegisteredRefresh {
		t := metav1.NewTime(now.Truncate(time.Second))
		r.LastRegisteredTime = &t
	}
	return r
}

// servers lists every transport pod with the routers it has a stream and a
// tunnel to. A pod that was not scraped has neither.
func servers(ep *pylonv1alpha1.InferenceEndpoint, clusterID string, pods []corev1.Pod, scrapes []podScrape) []pylonv1alpha1.ServerStatus {
	if len(pods) == 0 {
		return nil
	}
	out := make([]pylonv1alpha1.ServerStatus, 0, len(pods))
	for i := range pods {
		out = append(out, pylonv1alpha1.ServerStatus{
			InferenceServerID:   ServerID(ep, clusterID, pods[i].Name),
			Pod:                 pods[i].Name,
			RegistrationStreams: int32(len(scrapes[i].sample.Streams)),
			ReverseTunnels:      int32(len(scrapes[i].sample.Tunnels)),
		})
	}
	return out
}

// ServerID is the inference server id the transport pod registers under:
// transport.InferenceServerID with $(POD_NAME) replaced by the pod name, as
// Kubernetes does when it starts the container.
func ServerID(ep *pylonv1alpha1.InferenceEndpoint, clusterID, pod string) string {
	return strings.Replace(transport.InferenceServerID(ep, clusterID), "$("+transport.EnvPodName+")", pod, 1)
}

func describeRouters(routers []string) string {
	noun := "routers"
	if len(routers) == 1 {
		noun = "router"
	}
	quoted := make([]string, 0, len(routers))
	for _, r := range routers {
		quoted = append(quoted, strconv.Quote(r))
	}
	return fmt.Sprintf("%d %s (%s)", len(routers), noun, listN(quoted, ", "))
}

// listN joins up to maxListed items with sep and says how many more there
// are.
func listN(items []string, sep string) string {
	if len(items) <= maxListed {
		return strings.Join(items, sep)
	}
	return fmt.Sprintf("%s%sand %d more", strings.Join(items[:maxListed], sep), sep, len(items)-maxListed)
}
