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

package nvcf

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/clients"
	"github.com/cenkalti/backoff/v4"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"
	"github.com/samber/lo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"

	nvauth "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/auth"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/consumer"
	nvcfMetrics "github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/token"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/tracing"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/types"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
)

const cachedNvcfTokenFilename = "nvcf-worker-token.json"

// By default, grpc-go doesn't have a timeout.
const DefaultNvcfClientTimeout = 20 * time.Second

type ConnectionRegions struct {
	Primary     string
	Secondaries []string
}

type Client struct {
	Client                  pb.WorkerClient
	regionalNvcfClientsLock sync.RWMutex
	regionalNvcfClients     map[string]pb.WorkerClient
	NvcfTokenProvider       *auth.SettableTokenSource
	ncaId                   string
	instanceId              string
	functionId              string
	functionVersionId       string
	NatsConn                *nats.Conn
	JetStream               jetstream.JetStream
	assertionTokenPath      string
	sharedConfigDir         string
	clientTimeout           time.Duration

	// for keeping nvcf region state
	ConnectedRegions       atomic.Pointer[ConnectionRegions]
	TriggerNewNatsConsumer atomic.Pointer[func()]
	CurrentNatsConsumer    atomic.Pointer[consumer.GlobalNatsConsumer]
}

func CreateClient(nvcfFqdn string, nvcfFqdnNats *string, nvcfWorkerToken string, nkeySeed *string, ncaId string, instanceId string, functionId string, functionVersionId string, sharedConfigDir string, nvcfClientTimeout time.Duration) (*Client, error) {
	workerClient, err := createWorkerClient(nvcfFqdn, nvcfClientTimeout)
	if err != nil {
		return nil, err
	}

	nvcfToken := &oauth2.Token{
		AccessToken: nvcfWorkerToken,
	}

	// By design, NVCA and newt will always mount sharedConfigDir in the worker spec
	_, err = os.Stat(sharedConfigDir)
	if errors.Is(err, os.ErrNotExist) {
		// Support local dev and Slurm cluster environments where the dirs might need to be created
		zap.L().Warn("couldn't find directory, assuming it is not mounted via cluster agent. Attempting to create", zap.String("sharedConfigDir", sharedConfigDir))
		err = utils.CreateDirectory(sharedConfigDir, os.FileMode(0755))
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	zap.L().Info("checking for cached NVCF token")
	tokenFile := filepath.Join(sharedConfigDir, cachedNvcfTokenFilename)
	cachedNvcfToken, err := token.LoadCachedTokenIfExists(tokenFile)
	if err != nil {
		zap.L().Error("error while loading cached NVCF token", zap.Error(err))
		zap.L().Warn("cached token found, but unloadable - using environment token")
	} else if cachedNvcfToken != nil {
		zap.L().Info("cached token found")
		nvcfToken = cachedNvcfToken
	} else {
		zap.L().Info("no cached token found - using environment token")
	}

	tokenProvider := auth.NewSettableTokenSource(oauth2.StaticTokenSource(nvcfToken))

	client := &Client{
		Client: workerClient,
		regionalNvcfClients: map[string]pb.WorkerClient{
			nvcfFqdn: workerClient,
		},
		NvcfTokenProvider: tokenProvider,
		ncaId:             ncaId,
		instanceId:        instanceId,
		functionId:        functionId,
		functionVersionId: functionVersionId,
		sharedConfigDir:   sharedConfigDir,
		clientTimeout:     nvcfClientTimeout,
	}

	if nvcfFqdnNats != nil {
		nc, err := newNatsConnection(*nvcfFqdnNats, nkeySeed, tokenProvider, instanceId, functionVersionId)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to nats: %w", err)
		}

		js, err := jetstream.New(nc, jetstream.WithPublishAsyncErrHandler(func(stream jetstream.JetStream, msg *nats.Msg, err error) {
			nvcfMetrics.NatsErrorCounter.Inc()
			zap.L().Warn("jetstream error", zap.Error(err))
		}))
		if err != nil {
			return nil, fmt.Errorf("failed to create jetstream: %w", err)
		}

		client.NatsConn = nc
		client.JetStream = js
	}
	return client, nil
}

func (c *Client) Close() {
	if c.NatsConn != nil {
		utils.Close(c.NatsConn.Flush)
		c.NatsConn.Close()
		// TODO add defer w.js.Close() when the nats sdk gets fixed to plug the js reply inbox subscription leak
	}
}

func (c *Client) GetRegionalNvcfClient(nvcfFqdn string) (pb.WorkerClient, error) {
	if nvcfFqdn == "" {
		return c.Client, nil
	}
	c.regionalNvcfClientsLock.RLock()
	if client, ok := c.regionalNvcfClients[nvcfFqdn]; ok {
		c.regionalNvcfClientsLock.RUnlock()
		return client, nil
	}
	c.regionalNvcfClientsLock.RUnlock()
	c.regionalNvcfClientsLock.Lock()
	defer c.regionalNvcfClientsLock.Unlock()
	if client, ok := c.regionalNvcfClients[nvcfFqdn]; ok {
		return client, nil
	}
	client, err := createWorkerClient(nvcfFqdn, c.clientTimeout)
	if err != nil {
		return nil, err
	}
	c.regionalNvcfClients[nvcfFqdn] = client
	return client, nil
}

func (c *Client) updateConnectedRegions(primaryRegion string, secondaryRegions []string) {
	old := c.ConnectedRegions.Swap(&ConnectionRegions{
		Primary:     primaryRegion,
		Secondaries: secondaryRegions,
	})
	if old == nil {
		// Send initial value
		nvcfMetrics.WorkerSubscriptionsConnectedPrimaryRegionGauge.WithLabelValues(primaryRegion).Set(1)
		setConnectedSecondaryRegionsMetric(secondaryRegions, 1)
		return
	}
	oldPrimaryRegion := old.Primary

	currentConsumer := c.CurrentNatsConsumer.Load()
	if currentConsumer == nil {
		// Setting values of old consumer connected to old regions to 0
		nvcfMetrics.WorkerSubscriptionsConnectedPrimaryRegionGauge.WithLabelValues(oldPrimaryRegion).Set(0)
		setConnectedSecondaryRegionsMetric(old.Secondaries, 0)
		return
	}

	regions := map[string]struct{}{primaryRegion: {}}
	for _, region := range secondaryRegions {
		regions[region] = struct{}{}
	}

	currentlyConnectedRegions := currentConsumer.GetCurrentlyConnectedRegions()
	if maps.Equal(currentlyConnectedRegions, regions) && oldPrimaryRegion == primaryRegion {
		zap.L().Info("region list did not change, not reloading", zap.Strings("regions", lo.Keys(regions)))
		return
	}

	zap.L().Info("region list changed, reloading", zap.Strings("regions", lo.Keys(regions)))
	sendSecondaryRegionMetrics := false
	if oldPrimaryRegion != primaryRegion {
		setConnectedPrimaryRegionsMetric(oldPrimaryRegion, primaryRegion)
		delete(currentlyConnectedRegions, oldPrimaryRegion)
		delete(regions, primaryRegion)
		if !maps.Equal(currentlyConnectedRegions, regions) {
			sendSecondaryRegionMetrics = true
		}
	} else {
		sendSecondaryRegionMetrics = true
	}

	if sendSecondaryRegionMetrics {
		setConnectedSecondaryRegionsMetric(old.Secondaries, 0)
		setConnectedSecondaryRegionsMetric(secondaryRegions, 1)
	}

	trigger := c.TriggerNewNatsConsumer.Load()
	if trigger == nil {
		return
	}

	(*trigger)()
}

func setConnectedPrimaryRegionsMetric(oldPrimary string, newPrimary string) {
	nvcfMetrics.WorkerSubscriptionsConnectedPrimaryRegionGauge.WithLabelValues(oldPrimary).Set(0)
	nvcfMetrics.WorkerSubscriptionsConnectedPrimaryRegionGauge.WithLabelValues(newPrimary).Set(1)
}

func setConnectedSecondaryRegionsMetric(regions []string, val float64) {
	if len(regions) == 0 {
		// If regions is an empty list, we label as none
		nvcfMetrics.WorkerSubscriptionsConnectedSecondaryRegionsGauge.WithLabelValues("none").Set(val)
	} else {
		regionsCopy := make([]string, len(regions))
		copy(regionsCopy, regions)
		sort.Strings(regionsCopy)
		nvcfMetrics.WorkerSubscriptionsConnectedSecondaryRegionsGauge.WithLabelValues(strings.Join(regionsCopy, ",")).Set(val)
	}
}

func (c *Client) KeepAliveInvokeFunctionRequest(ctx context.Context, work *consumer.WorkRequest, cancelRequestFunc context.CancelCauseFunc) {
	const maxKeepAliveFailures = 10
	// the nvcf api checks for messages older than 30 seconds that haven't got a keepalive
	const keepAlivePeriod = 10 * time.Second

	failureCount := 0
	for {
		// wait for either the work request to have finished or send keepalive again after 30s
		// short functions won't need to send a keepalive at all
		// the nvcf api checks for messages older than 30 seconds that haven't got a keepalive
		if doneCtx(ctx, keepAlivePeriod) {
			return
		}
		zap.L().Info("sending keepalive", zap.String("req id", work.RequestData.RequestId))
		err := work.InProgress()
		if err != nil {
			zap.L().Error("failed to send keepalive", zap.String("req id", work.RequestData.RequestId), zap.Error(err), zap.Int("try", failureCount))
			trace.SpanFromContext(ctx).RecordError(err, trace.WithAttributes(attribute.String("message", "failed to send keepalive"), attribute.Int("try", failureCount)))
			failureCount += 1
			// if we can't send a keepalive for this long the worker probably has bad auth, or lost
			// communication with NVCF, or the request was expired, so we should stop the request
			if failureCount >= maxKeepAliveFailures {
				cancelRequestFunc(fmt.Errorf("failed to send keepalive %d times: %w", maxKeepAliveFailures, err))
				return
			}
		} else {
			failureCount = 0
		}
	}
}

// doneCtx waits for either timeout or completion of ctx, which ever happens first.
// if ctx finished, returns true. if the timeout was reached, returns false.
func doneCtx(ctx context.Context, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	select {
	case <-ctx.Done():
		if !timer.Stop() {
			<-timer.C
		}
		return true
	case <-timer.C:
		return false
	}
}

func createWorkerClient(nvcfFqdn string, timeout time.Duration) (pb.WorkerClient, error) {
	nvcfUrl, err := utils.PortSafeUrl(nvcfFqdn)
	if err != nil {
		return nil, err
	}

	grpcClientConfig := clients.GRPCClientConfig{BaseClientConfig: &clients.BaseClientConfig{
		Addr: nvcfUrl.Host,
		TLS: nvauth.TLSConfigOptions{
			Enabled: nvcfUrl.Scheme == "https",
		},
	}}
	dialOptions, err := grpcClientConfig.DialOptions()
	if err != nil {
		return nil, err
	}
	// larger than our max expected message size
	const tenMB = utils.OneMB * 10
	dialOptions = append(dialOptions,
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(tenMB)),
		grpc.WithInitialWindowSize(tenMB),
		grpc.WithInitialConnWindowSize(tenMB),
		grpc.WithSharedWriteBuffer(true),
		grpc.WithWriteBufferSize(tenMB),
		grpc.WithTimeout(timeout), //nolint:staticcheck // TODO: migrate to NewClient with context timeout
		grpc.WithContextDialer(func(ctx context.Context, s string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", s)
			if err != nil {
				return nil, err
			}
			initialBufSize := utils.GetWriteBufferSize(conn)
			type WriteBufferSetter interface {
				SetWriteBuffer(bytes int) error
			}
			if setter, ok := conn.(WriteBufferSetter); ok && tenMB > initialBufSize {
				var err error
				for size := ((initialBufSize / utils.OneMB) + 1) * utils.OneMB; err == nil && size <= tenMB; size += utils.OneMB {
					err = setter.SetWriteBuffer(size)
				}
				syscallError := &os.SyscallError{}
				if errors.As(err, &syscallError) {
					if syscallError.Unwrap().Error() == "no buffer space available" {
						err = nil
					}
				}
				zap.L().Info("setting write buffer up to 10MB", zap.Error(err))
				utils.GetWriteBufferSize(conn)
			}
			return conn, nil
		}),
	)
	grpcClientConfig.DialOptOverrides = dialOptions
	conn, err := grpcClientConfig.Dial()
	if err != nil {
		return nil, err
	}
	workerClient := pb.NewWorkerClient(conn)
	return workerClient, nil
}

func newNatsConnection(nvcfFqdnNats string, nkeySeed *string, nvcfTokenProvider *auth.SettableTokenSource, instanceId string, functionVersionId string) (*nats.Conn, error) {
	const maxNatsReconnectDelay = 15 * time.Second
	const maxNatsReconnects = 12
	// backoff is not thread-safe, so we need to lock it
	expBackoffMutex := sync.Mutex{}
	expBackoff := backoff.NewExponentialBackOff(
		backoff.WithMaxInterval(maxNatsReconnectDelay),
	)
	authOption, err := natsAuthOption(nkeySeed, nvcfTokenProvider)
	if err != nil {
		return nil, err
	}
	return nats.Connect(nvcfFqdnNats, nats.PingInterval(10*time.Second),
		nats.RetryOnFailedConnect(true),
		nats.Timeout(5*time.Second),
		nats.MaxReconnects(maxNatsReconnects),
		nats.Name("worker_"+functionVersionId+"_"+instanceId),
		nats.CustomReconnectDelay(func(attempt int) time.Duration {
			expBackoffMutex.Lock()
			defer expBackoffMutex.Unlock()
			delay := expBackoff.NextBackOff()
			if delay == backoff.Stop {
				delay = maxNatsReconnectDelay
			}
			return delay
		}),
		authOption,
		nats.LameDuckModeHandler(func(conn *nats.Conn) {
			nvcfMetrics.NatsLameDuckCounter.Inc()
			expBackoffMutex.Lock()
			defer expBackoffMutex.Unlock()
			expBackoff.Reset()
			go func() {
				// TODO remove when the SDK natively handles reconnects on lame duck
				time.Sleep(time.Duration(rand.Int63n(int64(time.Second * 10))))
				zap.L().Info("got lame duck message, force reconnecting")
				_ = conn.ForceReconnect()
			}()
		}),
		nats.ReconnectHandler(func(conn *nats.Conn) {
			// TODO maybe we want to lock the connection until we are reconnected to prevent errors
			zap.L().Info("reconnected to nats", zap.String("server", conn.ConnectedServerName()), zap.String("cluster", conn.ConnectedClusterName()))
			nvcfMetrics.NatsReconnectCounter.Inc()
			expBackoffMutex.Lock()
			defer expBackoffMutex.Unlock()
			expBackoff.Reset()
		}),
		nats.ErrorHandler(func(conn *nats.Conn, sub *nats.Subscription, err error) {
			nvcfMetrics.NatsErrorCounter.Inc()
			fields := []zap.Field{zap.Error(err), zap.String("function version id", functionVersionId)}
			if sub != nil {
				fields = append(fields, zap.String("subject", sub.Subject), zap.String("queue", sub.Queue))
			}
			// Permission violations are rejected asynchronously (Subscribe/Publish return nil),
			// so log loudly to expose a subject missing from the worker's NATS allow-list.
			if errors.Is(err, nats.ErrPermissionViolation) {
				zap.L().Error("nats permission violation; a subject is likely missing from the worker allow-list",
					append(fields, utils.PublicLogMarker)...)
				return
			}
			zap.L().Warn("nats connection error", fields...)
		}),
		nats.ConnectHandler(func(conn *nats.Conn) {
			zap.L().Info("connected to nats", zap.String("server", conn.ConnectedServerName()), zap.String("cluster", conn.ConnectedClusterName()))
		}),
		nats.DisconnectErrHandler(func(conn *nats.Conn, err error) {
			if err != nil {
				zap.L().Warn("disconnected from nats server", zap.Error(err))
			}
			nvcfMetrics.NatsDisconnectCounter.Inc()
		}),
		nats.ClosedHandler(func(conn *nats.Conn) {
			zap.L().Info("nats connection closed.", zap.Error(conn.LastError()))
		}),
	)
}

// natsAuthOption will use the nkey if provided, else will use the worker token
func natsAuthOption(nkeySeed *string, nvcfTokenProvider *auth.SettableTokenSource) (nats.Option, error) {
	if nkeySeed == nil {
		return nats.TokenHandler(func() string {
			token, err := nvcfTokenProvider.Token()
			if err != nil {
				zap.L().Warn("failed to fetch nvcf token for nats", zap.Error(err))
				return ""
			}
			// token=b64({"account":"$account","pluginName":"$pluginName","payload":"$payload"})
			tokenJson, err := json.Marshal(struct {
				Account    string `json:"account"`
				PluginName string `json:"pluginName"`
				Payload    string `json:"payload"`
			}{
				Account:    "Worker",
				PluginName: "webhook",
				Payload:    token.AccessToken,
			})
			if err != nil {
				zap.L().Warn("failed to marshal nvcf token for nats", zap.Error(err))
				return ""
			}
			return base64.RawURLEncoding.EncodeToString(tokenJson)
		}), nil
	}
	kp, err := nkeys.FromSeed([]byte(*nkeySeed))
	if err != nil {
		return nil, err
	}
	usrNKey, err := kp.PublicKey()
	if err != nil {
		return nil, err
	}
	return nats.Nkey(usrNKey, func(nonce []byte) ([]byte, error) {
		return kp.Sign(nonce)
	}), nil
}

func (c *Client) GetArtifacts(ctx context.Context) (*types.ArtifactsList, error) {
	zap.L().Info("Fetching artifacts list from NVCF")
	ctx, span := otel.GetTracerProvider().Tracer("nvcf-worker-lib").Start(ctx, "Get Artifacts")
	defer span.End()

	var models []types.Artifact
	var resources []types.Artifact
	var invalidArtifacts int

	err := backoff.Retry(func() error {
		var internalModels []types.Artifact
		var internalResources []types.Artifact
		var internalInvalidArtifacts int

		stream, err := c.Client.StreamArtifacts(ctx, &pb.ArtifactsRequest{}, auth.GrpcTokenFromSource(c.NvcfTokenProvider))
		if err != nil {
			span.AddEvent(fmt.Sprintf("Failed to start streaming artifacts from NVCF: %s", err.Error()))
			zap.L().Warn("failed to start streaming artifacts from NVCF", zap.Error(err))
			return err
		}

		for {
			artifactFile, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				span.AddEvent(fmt.Sprintf("Failed to receive artifact from NVCF stream: %s", err.Error()))
				zap.L().Warn("failed to receive artifact from NVCF stream", zap.Error(err))
				return err
			}

			artifact := types.Artifact{
				Name:    artifactFile.GetArtifactName(),
				Version: artifactFile.GetArtifactVersion(),
				Path:    artifactFile.GetPath(),
				Url:     artifactFile.GetUrl(),
			}

			switch artifactFile.GetArtifactKind() {
			case pb.StreamedArtifactFile_MODEL:
				internalModels = append(internalModels, artifact)
			case pb.StreamedArtifactFile_RESOURCE:
				internalResources = append(internalResources, artifact)
			default:
				zap.L().Warn(
					"invalid artifact kind",
					zap.String("name", artifactFile.GetArtifactName()),
					zap.Stringer("kind", artifactFile.GetArtifactKind()),
				)
				internalInvalidArtifacts++
			}
		}

		models = internalModels
		resources = internalResources
		invalidArtifacts = internalInvalidArtifacts
		return nil
	}, backoff.WithContext(backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 10), ctx))

	if err != nil {
		return nil, tracing.RecordSpanError(span, err)
	}

	artifactsList := types.ArtifactsList{
		Models:    models,
		Resources: resources,
	}

	span.SetAttributes(
		attribute.Int("artifacts.models", len(artifactsList.Models)),
		attribute.Int("artifacts.resources", len(artifactsList.Resources)),
		attribute.Int("artifacts.invalid", invalidArtifacts),
	)

	return &artifactsList, nil
}
