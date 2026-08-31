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
	return &h3ConnectionCache{wrappedTransport: h3, clients: make(map[string]*roundTripperWithCount)}
}

// mostly copied from http3.Transport because we need to hijack the http3 client stream
// and when doing that we can't use the built in http3.Transport.RoundTrip function which caches
// connections.
// dialFailuresBeforeRotate is how many consecutive failed dials, across any
// hosts, are treated as evidence that the shared UDP socket itself is dead
// rather than that one proxy host is unreachable. Measured on stage: healthy
// operation produces zero dial failures over long windows, while a blackholed
// socket produces hundreds within a single 60s window, so anything in between
// separates the two cleanly.
const dialFailuresBeforeRotate = 3

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
	dialFailures  int
}

// transport returns the shared QUIC transport, creating it on first use.
//
// Every connection dialled from one quic.Transport leaves from its single UDP
// socket, so the whole worker shares one source port. An AWS NLB hashes UDP
// flows on the 5-tuple, which means that port decides which proxy pod every
// dial reaches. If the flow is pinned to a pod that has gone away, retrying
// cannot help: the retry leaves from the same port and lands on the same dead
// entry. Rotating the socket is the only thing the worker can do to change it.
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
// once consecutive failures cross the threshold. A success resets the count,
// so an established worker never rotates on isolated failures.
func (t *h3ConnectionCache) noteDialResult(dialed *quic.Transport, dialErr error) {
	t.transportMu.Lock()
	defer t.transportMu.Unlock()

	if dialErr == nil {
		t.dialFailures = 0
		return
	}
	t.dialFailures++
	if t.dialFailures < dialFailuresBeforeRotate {
		return
	}
	// Another dial may have rotated already while this one was in flight.
	// Rotating again would discard a socket that has not been shown to be
	// bad and would reset the evidence we just gathered.
	if t.quicTransport == nil || t.quicTransport != dialed {
		return
	}
	zap.L().Warn("rotating quic transport after consecutive dial failures",
		zap.Int("consecutive_failures", t.dialFailures),
		zap.String("local_addr", t.quicTransport.Conn.LocalAddr().String()))
	t.closeTransportLocked()
	t.dialFailures = 0
}

// closeTransportLocked releases the shared socket. The next dial recreates it
// via transportLocked, which yields a fresh ephemeral port. Errors are logged
// rather than returned: the socket is being discarded either way, and failing
// to close it must not stop a new one being created.
func (t *h3ConnectionCache) closeTransportLocked() {
	if t.quicTransport == nil {
		return
	}
	if err := t.quicTransport.Close(); err != nil {
		zap.L().Warn("closing quic transport", zap.Error(err))
	}
	if err := t.quicTransport.Conn.Close(); err != nil {
		zap.L().Warn("closing quic transport socket", zap.Error(err))
	}
	t.quicTransport = nil
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
	}
	select {
	case <-cl.dialing:
		if cl.dialErr != nil {
			delete(t.clients, hostname)
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
			t.noteDialResult(quicTransport, err)
			return conn, err
		}
	}
	conn, err := dial(ctx, hostname, tlsConf, t.wrappedTransport.QUICConfig)
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
	delete(t.clients, hostname)
}

// Close closes the QUIC connections that this Transport has used.
func (t *h3ConnectionCache) Close() error {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	for _, cl := range t.clients {
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
