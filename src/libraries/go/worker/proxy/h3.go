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

package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"math"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/qlog"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/ca"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics/nvcf"
)

var quicInsecure = os.Getenv("QUIC_INSECURE") == "true"

func createH3RoundTripper() *h3ConnectionCache {
	proxyCAs, err := ca.ProxyCAs()
	if err != nil {
		zap.L().Fatal("failed to load proxy CA trust pool", zap.Error(err))
	}
	h3 := &http3.Transport{
		DisableCompression: true,
		TLSClientConfig: &tls.Config{
			RootCAs:            proxyCAs,
			InsecureSkipVerify: quicInsecure,
		},
		QUICConfig: &quic.Config{
			// TODO we are fully relying on client side timeouts for connection issue detection
			// TODO https://github.com/quic-go/quic-go/issues/153 is not implemented
			KeepAlivePeriod:       3 * time.Second,
			MaxIdleTimeout:        8 * time.Second,
			MaxIncomingStreams:    math.MaxInt,
			MaxIncomingUniStreams: math.MaxInt,
		},
	}

	if utils.LevelFromEnv().Level() == zap.DebugLevel {
		h3.QUICConfig.Tracer = qlog.DefaultConnectionTracer
	}
	return &h3ConnectionCache{
		wrappedTransport: h3,
		clients:          make(map[string]*roundTripperWithCount),
		dialFailures:     make(map[string]int),
	}
}

// mostly copied from http3.Transport because we need to hijack the http3 client stream
// and when doing that we can't use the built in http3.Transport.RoundTrip function which caches
// connections.
// dialFailuresBeforeRotate is how many consecutive network timeout failures
// to one resolved destination are treated as evidence that its UDP flow is
// blackholed. Measured on stage: healthy operation produces zero dial failures
// over long windows, while a blackholed flow produces hundreds within a single
// 60s window, so anything in between separates the two cleanly.
const dialFailuresBeforeRotate = 3

// h3ConnectionCache reuses HTTP/3 clients by hostname and manages the shared
// QUIC transport used to dial them.
type h3ConnectionCache struct {
	wrappedTransport *http3.Transport
	mutex            sync.Mutex
	clients          map[string]*roundTripperWithCount

	// transportMu guards quicTransport and dialFailures. It is deliberately
	// not the mutex above: dial runs on the goroutine spawned by getClient,
	// which does not hold that lock, so quicTransport was previously read and
	// written with no synchronisation at all.
	transportMu   sync.Mutex
	quicTransport *quic.Transport
	dialFailures  map[string]int
}

// transport returns the shared QUIC transport, creating it on first use.
//
// Every connection dialled from one quic.Transport leaves from its single UDP
// socket, so the whole worker shares one source port. For a fixed destination,
// an AWS NLB hashes UDP flows on the 5-tuple, which means that port decides
// which proxy pod every dial reaches. If the flow is pinned to a pod that has
// gone away, retrying cannot help: the retry leaves from the same port and
// lands on the same dead entry. Rotating the socket is the only thing the
// worker can do to change it.
func (t *h3ConnectionCache) transport() (*quic.Transport, error) {
	t.transportMu.Lock()
	defer t.transportMu.Unlock()
	return t.transportLocked()
}

func (t *h3ConnectionCache) transportLocked() (*quic.Transport, error) {
	if t.quicTransport == nil {
		udpConn, err := net.ListenUDP("udp", nil)
		if err != nil {
			return nil, err
		}
		t.quicTransport = &quic.Transport{Conn: udpConn}
	}
	return t.quicTransport, nil
}

// noteDialResult records the outcome of a dial and rotates the shared socket
// once consecutive network timeouts to one destination cross the threshold.
// A success resets only that destination, so unrelated flows cannot suppress
// or trigger rotation.
func (t *h3ConnectionCache) noteDialResult(ctx context.Context, dialed *quic.Transport, destination string, dialErr error) {
	t.transportMu.Lock()
	defer t.transportMu.Unlock()

	// A dial that began before a rotation says nothing about the socket in use
	// now, so it must not touch the counter at all. Counting a stale failure
	// would let it rotate a replacement that has not failed on its own, and a
	// stale success would clear failures the current socket really did have.
	// This check therefore has to come before both the reset and the
	// increment, not after them.
	if t.quicTransport == nil || t.quicTransport != dialed {
		logSkip(&skippedStaleDial, nvcf.DialSkipStaleTransport, "dial predates the current transport", destination, dialErr)
		return
	}
	if dialErr == nil {
		delete(t.dialFailures, destination)
		return
	}
	if !isTransportDialFailure(ctx, dialErr) {
		// Two very different reasons land here and they need telling apart: a
		// cancelled context means the dial never got a verdict, while a
		// non-timeout error means the socket reached something. Reporting them
		// as one silent return makes "rotation never fired" indistinguishable
		// from "rotation fired and did not help".
		if cause := context.Cause(ctx); cause != nil {
			logSkip(&skippedCtxCancelled, nvcf.DialSkipCtxCancelled, "dial context cancelled", destination, cause)
		} else {
			logSkip(&skippedNotTimeout, nvcf.DialSkipNotTimeout, "error is not a network timeout", destination, dialErr)
		}
		return
	}
	if t.dialFailures == nil {
		t.dialFailures = make(map[string]int)
	}
	t.dialFailures[destination]++
	failures := t.dialFailures[destination]
	if failures < dialFailuresBeforeRotate {
		return
	}
	t.rotateLocked(destination, failures)
}

// recordDialAttempt counts every dial and classifies its failure. It is
// deliberately separate from noteDialResult: that function governs rotation
// bookkeeping and is reached only from the default dial path, whereas every
// dial regardless of path must be measured.
func recordDialAttempt(ctx context.Context, dialErr error) {
	nvcf.QuicDialCounter.Inc()
	if dialErr != nil {
		nvcf.QuicDialFailureCounter.WithLabelValues(dialFailureReason(ctx, dialErr)).Inc()
	}
}

// dialFailureReason labels a failure for the metric. It mirrors
// isTransportDialFailure so that the "timeout" series counts exactly the
// failures that drive rotation, which is what makes the two comparable in an
// alert expression.
func dialFailureReason(ctx context.Context, dialErr error) string {
	switch {
	case isTransportDialFailure(ctx, dialErr):
		return nvcf.DialFailureTimeout
	case errors.Is(dialErr, ErrAuth):
		return nvcf.DialFailureAuth
	default:
		return nvcf.DialFailureOther
	}
}

// Occurrence counts for the skip paths. These back the log rate limiter only;
// the exported series is nvcf.QuicDialSkipCounter, which is what alerts read.
var (
	skippedStaleDial    atomic.Int64
	skippedCtxCancelled atomic.Int64
	skippedNotTimeout   atomic.Int64
)

// logSkip records the skip and reports the first occurrence, then every
// 1000th. Under a real blackhole these paths are hit thousands of times a
// second, so logging each one would drown the signal it exists to provide.
// The metric is incremented every time regardless; only the log is sampled.
func logSkip(counter *atomic.Int64, reason, detail, destination string, err error) {
	nvcf.QuicDialSkipCounter.WithLabelValues(reason).Inc()
	n := counter.Add(1)
	if n != 1 && n%1000 != 0 {
		return
	}
	zap.L().Warn("dial failure did not count toward rotation",
		zap.String("reason", detail),
		zap.String("destination", destination),
		zap.Int64("occurrences", n),
		zap.Error(err))
}

func isTransportDialFailure(ctx context.Context, dialErr error) bool {
	if dialErr == nil || context.Cause(ctx) != nil {
		return false
	}
	var networkErr net.Error
	return errors.As(dialErr, &networkErr) && networkErr.Timeout()
}

// rotateLocked replaces the shared socket with a freshly bound one.
//
// The replacement is bound while the old socket is still open, deliberately.
// Closing first releases the port and the kernel is free to hand the same one
// back, which would leave the worker on the identical 5-tuple and defeat the
// rotation without any outward sign that it had failed.
func (t *h3ConnectionCache) rotateLocked(destination string, failures int) {
	old := t.quicTransport
	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		// Keep the existing socket rather than leaving the worker with none.
		// The next failure retries the rotation.
		zap.L().Warn("failed to bind a replacement quic socket",
			zap.Error(err),
			zap.String("destination", destination),
			zap.Int("consecutive_failures", failures))
		return
	}
	zap.L().Info("rotating quic transport after consecutive dial failures",
		zap.String("destination", destination),
		zap.Int("consecutive_failures", failures),
		zap.String("old_local_addr", old.Conn.LocalAddr().String()),
		zap.String("new_local_addr", udpConn.LocalAddr().String()))
	t.quicTransport = &quic.Transport{Conn: udpConn}
	clear(t.dialFailures)
	closeTransport(old)
	nvcf.QuicTransportRotationCounter.Inc()
}

// closeTransport releases a transport and its socket. Errors are logged rather
// than returned: the socket is being discarded either way, and failing to close
// it must not stop the replacement being used.
func closeTransport(tr *quic.Transport) {
	if tr == nil {
		return
	}
	if err := tr.Close(); err != nil {
		zap.L().Warn("closing quic transport", zap.Error(err))
	}
	if err := tr.Conn.Close(); err != nil {
		zap.L().Warn("closing quic transport socket", zap.Error(err))
	}
}

func (t *h3ConnectionCache) closeTransportLocked() {
	closeTransport(t.quicTransport)
	t.quicTransport = nil
	clear(t.dialFailures)
}

func (t *h3ConnectionCache) getDialedClient(ctx context.Context, hostname string) (rtc *roundTripperWithCount, isReused bool, err error) {
	cl, isReused, err := t.getClient(ctx, hostname)
	if err != nil {
		return nil, false, err
	}
	select {
	case <-cl.dialing:
	case <-ctx.Done():
		cl.useCount.Add(-1)
		return nil, false, context.Cause(ctx)
	}
	if cl.dialErr != nil {
		cl.useCount.Add(-1)
		cl.removeFromCache()
		return nil, false, cl.dialErr
	}
	return cl, isReused, nil
}

func (t *h3ConnectionCache) getClient(ctx context.Context, hostname string) (rtc *roundTripperWithCount, isReused bool, err error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	cl, ok := t.clients[hostname]
	if !ok {
		ctx, cancel := context.WithCancel(ctx)
		removeOnce := sync.Once{}
		cl = &roundTripperWithCount{
			dialing: make(chan struct{}),
			cancel:  cancel,
			// may be called multiple times if many callers detect failure
			removeFromCache: func() {
				removeOnce.Do(func() {
					t.removeClient(hostname)
				})
			},
		}
		go func() {
			defer close(cl.dialing)
			defer cancel()
			conn, rt, err := t.dial(ctx, hostname)
			if err != nil {
				cl.dialErr = err
				return
			}
			cl.conn = conn
			cl.clientConn = rt
			context.AfterFunc(conn.Context(), func() {
				zap.L().Debug("detected closed quic connection", zap.String("hostname", hostname))
				// eagerly remove connection from cache
				cl.removeFromCache()
			})
		}()
		t.clients[hostname] = cl
		nvcf.QuicTunnelGauge.Inc()
	}
	select {
	case <-cl.dialing:
		if cl.dialErr != nil {
			t.deleteClientLocked(hostname)
			return nil, false, cl.dialErr
		}
		select {
		case <-cl.conn.HandshakeComplete():
			isReused = true
		default:
		}
	default:
	}
	cl.useCount.Add(1)
	zap.L().Debug("got client", zap.String("hostname", hostname), zap.Bool("isReused", isReused))
	return cl, isReused, nil
}

func (t *h3ConnectionCache) dial(ctx context.Context, hostname string) (*quic.Conn, *http3.ClientConn, error) {
	var tlsConf *tls.Config
	if t.wrappedTransport.TLSClientConfig == nil {
		tlsConf = &tls.Config{}
	} else {
		tlsConf = t.wrappedTransport.TLSClientConfig.Clone()
	}
	if tlsConf.ServerName == "" {
		sni, _, err := net.SplitHostPort(hostname)
		if err != nil {
			// It's ok if net.SplitHostPort returns an error - it could be a hostname/IP address without a port.
			sni = hostname
		}
		tlsConf.ServerName = sni
	}
	// Replace existing ALPNs by H3
	tlsConf.NextProtos = []string{http3.NextProtoH3}

	dial := t.wrappedTransport.Dial
	if dial == nil {
		quicTransport, err := t.transport()
		if err != nil {
			return nil, nil, err
		}
		dial = func(ctx context.Context, addr string, tlsCfg *tls.Config, cfg *quic.Config) (*quic.Conn, error) {
			network := "udp"
			udpAddr, err := t.resolveUDPAddr(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			conn, err := quicTransport.DialEarly(ctx, udpAddr, tlsCfg, cfg)
			t.noteDialResult(ctx, quicTransport, udpAddr.String(), err)
			return conn, err
		}
	}
	conn, err := dial(ctx, hostname, tlsConf, t.wrappedTransport.QUICConfig)
	// Counted here rather than in noteDialResult so that a configured
	// wrappedTransport.Dial is measured too. noteDialResult is reached only
	// from the default dial path, so leaving the counters there would make
	// the metrics silently go quiet if that field were ever set.
	recordDialAttempt(ctx, err)
	if err != nil {
		zap.L().Warn("failed to dial quic connection", zap.Error(err), zap.String("hostname", hostname))
		return nil, nil, err
	}
	zap.L().Debug("dialed quic connection", zap.String("hostname", hostname))
	return conn, t.wrappedTransport.NewClientConn(conn), nil
}

func (t *h3ConnectionCache) resolveUDPAddr(ctx context.Context, network, addr string) (*net.UDPAddr, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := net.LookupPort(network, portStr)
	if err != nil {
		return nil, err
	}
	resolver := net.DefaultResolver
	ipAddrs, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	addrs := addrList(ipAddrs)
	ip := addrs.forResolve(network, addr)
	return &net.UDPAddr{IP: ip.IP, Port: port, Zone: ip.Zone}, nil
}

func (t *h3ConnectionCache) removeClient(hostname string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	if t.clients == nil {
		return
	}
	t.deleteClientLocked(hostname)
}

// deleteClientLocked drops a cached client and keeps the tunnel gauge balanced
// with it. The presence check is what keeps them balanced: removal is reached
// from more than one path for the same hostname, and decrementing on a key
// that is already gone would drive the gauge negative.
func (t *h3ConnectionCache) deleteClientLocked(hostname string) {
	if _, ok := t.clients[hostname]; !ok {
		return
	}
	delete(t.clients, hostname)
	nvcf.QuicTunnelGauge.Dec()
}

// Close closes the QUIC connections that this Transport has used.
func (t *h3ConnectionCache) Close() error {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	for hostname, cl := range t.clients {
		// Decrement here rather than relying on the cache-removal callback.
		// cl.Close triggers that callback on another goroutine, which blocks
		// on t.mutex until this function returns and then finds the map
		// already nil, so it would never decrement and the gauge would leak.
		delete(t.clients, hostname)
		nvcf.QuicTunnelGauge.Dec()
		if err := cl.Close(); err != nil {
			return err
		}
	}
	t.clients = nil
	t.transportMu.Lock()
	defer t.transportMu.Unlock()
	t.closeTransportLocked()
	return nil
}

type roundTripperWithCount struct {
	cancel     context.CancelFunc
	dialing    chan struct{} // closed as soon as quic.Dial(Early) returned
	dialErr    error
	conn       *quic.Conn
	clientConn *http3.ClientConn

	useCount        atomic.Int64
	removeFromCache func()
}

func (r *roundTripperWithCount) Close() error {
	r.cancel()
	<-r.dialing
	if r.conn != nil {
		return r.conn.CloseWithError(0, "")
	}
	return nil
}

// An addrList represents a list of network endpoint addresses.
// Copy from [net.addrList] and change type from [net.Addr] to [net.IPAddr]
type addrList []net.IPAddr

// isIPv4 reports whether addr contains an IPv4 address.
func isIPv4(addr net.IPAddr) bool {
	return addr.IP.To4() != nil
}

// isNotIPv4 reports whether addr does not contain an IPv4 address.
func isNotIPv4(addr net.IPAddr) bool { return !isIPv4(addr) }

// forResolve returns the most appropriate address in address for
// a call to ResolveTCPAddr, ResolveUDPAddr, or ResolveIPAddr.
// IPv4 is preferred, unless addr contains an IPv6 literal.
func (addrs addrList) forResolve(network, addr string) net.IPAddr {
	var want6 bool
	switch network {
	case "ip":
		// IPv6 literal (addr does NOT contain a port)
		want6 = strings.ContainsRune(addr, ':')
	case "tcp", "udp":
		// IPv6 literal. (addr contains a port, so look for '[')
		want6 = strings.ContainsRune(addr, '[')
	}
	if want6 {
		return addrs.first(isNotIPv4)
	}
	return addrs.first(isIPv4)
}

// first returns the first address which satisfies strategy, or if
// none do, then the first address of any kind.
func (addrs addrList) first(strategy func(net.IPAddr) bool) net.IPAddr {
	for _, addr := range addrs {
		if strategy(addr) {
			return addr
		}
	}
	return addrs[0]
}
