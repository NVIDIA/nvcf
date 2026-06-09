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

package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/codes"

	"github.com/goccy/go-json"
	"github.com/samber/lo"
	"github.com/valyala/bytebufferpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-utils/worker/httpstream"
	utilsMetrics "github.com/NVIDIA/nvcf/src/compute-plane-services/worker-utils/worker/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-utils/worker/polling"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-utils/worker/progress"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-utils/worker/response"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/consumer"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metering"
	nvcfMetrics "github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/tracing"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
)

const (
	utilsErrorResponse         string = "nvcf-worker-service"
	inferenceErrorResponse     string = "inference-service"
	inferenceConnErrorResponse string = "inference-connection"
	successResponseCode        int    = 0
	internalErrorResponseCode  int    = 500
)

func (w *NVCFWorker) handleWorkRequest(ctx context.Context, work *consumer.WorkRequest) error {
	// setup metrics
	requestTracker := nvcfMetrics.NewRequestTracker(
		ctx,
		utilsMetrics.PreInferenceTimeCounter,
		utilsMetrics.PostInferenceTimeCounter,
		utilsMetrics.InferenceRequestTimeCounter,
		utilsMetrics.InferenceRequestLatencyHistogram,
	)
	defer requestTracker.EndRequest()

	// setup tracing and context
	requestData := &work.RequestData
	// nats headers are case-sensitive
	ctx = otel.GetTextMapPropagator().Extract(ctx, tracing.NatsHeaderCarrier(work.Headers()))
	ctx, span := otel.GetTracerProvider().Tracer("nvcf-worker-utils").
		Start(ctx, "Handle Work Request",
			trace.WithAttributes(attribute.String("requestId", requestData.RequestId)),
			trace.WithSpanKind(trace.SpanKindConsumer))
	defer span.End()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	// expose cancel to the NATS cancel-broadcast handler for this request's lifetime
	w.cancelSubMu.Lock()
	w.inFlightCancels[requestData.RequestId] = cancel
	w.cancelSubMu.Unlock()
	defer func() {
		w.cancelSubMu.Lock()
		delete(w.inFlightCancels, requestData.RequestId)
		w.cancelSubMu.Unlock()
	}()

	var err error
	defer utils.ClosePreservingError(&err, utils.CloserFunc(func() error {
		// abandoned requests won't be able to successfully send a response,
		// but we should still ack them to prevent keeping and retrying abandoned requests
		zap.L().Debug("acking request message", zap.String("req id", requestData.RequestId))
		span.AddEvent("acking request message")
		err := work.Ack()
		if err != nil {
			zap.L().Error("failed to ack message", zap.Error(err), zap.String("req id", requestData.RequestId))
			span.AddEvent("failed to ack message")
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		return err
	}))
	releaseWorkLimiterFunc := func() {
		// once we've finished writing the response we can release the work request limiter
		// without waiting for the IS server to respond back with a 200 to allow earlier
		// fetching of the next message. at this point the inference container is already
		// finished with the request, so we are not breaking the max concurrency by continuing
		// with the next request before this request fully finishes.
		_ = work.Close() // close the work request to release the limiter. closing does not ack! we manages acks separately.
	}

	meteringEvent := metering.New(w.meteringConfig, requestData.RequestId, requestData.Subject, requestData.NcaId, requestData.RequestHeaders)
	workResponseStatus := successResponseCode
	defer func() {
		go func() {
			err := meteringEvent.Log()
			if err != nil {
				zap.L().Warn("failed to log metering event", zap.String("req id", requestData.RequestId), zap.Error(err))
			}
			nvcfMetrics.ResponseCounter.WithLabelValues(strconv.Itoa(workResponseStatus)).Inc()
		}()
	}()

	// ALL requests should have a keepalive, even if they are stateful. do not move this below the stateful handler.
	go w.nvcfClient.KeepAliveInvokeFunctionRequest(ctx, work, cancel)

	if requestData.StatefulConfig != nil {
		err = w.handleStatefulWorkRequest(ctx, requestData, work.Region)
		if err != nil {
			meteringEvent.ResponseType = pb.InvokeStatus_ERRORED.String()
			workResponseStatus = internalErrorResponseCode
		} else {
			meteringEvent.ResponseType = pb.InvokeStatus_FULFILLED.String()
		}
		return err
	}

	assetDir := filepath.Join(w.baseAssetDir, requestData.RequestId)
	defer func() {
		go func() {
			_ = os.RemoveAll(assetDir)
		}()
	}()

	// setup response dir, async. cleanup async on exit.
	responseDir := filepath.Join(w.baseResponseDir, requestData.RequestId)
	responseDirCreateDone := make(chan struct{})
	asyncCreateResponseDir := lo.Async(func() error {
		defer func() { close(responseDirCreateDone) }()
		// response files are only used for backwards compatibility
		if w.config.V3BackwardsCompatibilityDisabled {
			return nil
		}
		_, err := os.Stat(responseDir)
		if errors.Is(err, os.ErrNotExist) {
			err = utils.CreateDirectory(responseDir, os.FileMode(0777))
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		return nil
	})
	defer func() {
		if w.config.V3BackwardsCompatibilityDisabled {
			return
		}
		go func() {
			// ensure directory won't be created after delete in case we return before waiting on creation
			<-responseDirCreateDone
			_ = os.RemoveAll(responseDir)
		}()
	}()

	var requestStreamHandler *httpstream.RequestStreamHandler
	requestStreamHandler, err = httpstream.NewRequestStreamHandler(ctx, w.sharedProxyClient, work.RequestData.StatelessConfig, &work.RequestData)
	if err != nil {
		workResponseStatus = internalErrorResponseCode
		return err
	}
	defer utils.Close(requestStreamHandler.Close)

	err = w.downloadAssets(ctx, requestData, assetDir)
	if err != nil {
		zap.L().Error("failed to download assets", zap.String("req id", requestData.RequestId), zap.Error(err))
		inferenceResponse := response.CreateErrorResponse(utilsErrorResponse, 500, "Internal error while downloading assets")
		err = requestStreamHandler.SendResponse(ctx, inferenceResponse, nil)
		meteringEvent.ResponseType = pb.InvokeStatus_ERRORED.String()
		workResponseStatus = internalErrorResponseCode
		return err
	}

	err = <-asyncCreateResponseDir
	if err != nil {
		zap.L().Error("failed to create response dir", zap.String("req id", requestData.RequestId), zap.Error(err))
		inferenceResponse := response.CreateErrorResponse(utilsErrorResponse, 500, "Internal error while preparing for inference")
		err = requestStreamHandler.SendResponse(ctx, inferenceResponse, nil)
		meteringEvent.ResponseType = pb.InvokeStatus_ERRORED.String()
		workResponseStatus = internalErrorResponseCode
		return err
	}

	err = w.makeRestRequest(ctx, &work.RequestData, requestStreamHandler, assetDir, responseDir, meteringEvent, requestTracker, releaseWorkLimiterFunc)
	if err != nil {
		zap.L().Error("error while making inference request", zap.String("req id", requestData.RequestId), zap.Error(err))
		meteringEvent.ResponseType = pb.InvokeStatus_ERRORED.String()
		inferenceResponse := handleInferenceError(err)
		err = requestStreamHandler.SendResponse(ctx, inferenceResponse, nil)
		workResponseStatus = internalErrorResponseCode
	} else {
		meteringEvent.ResponseType = pb.InvokeStatus_FULFILLED.String()
	}
	return err
}

func handleInferenceError(err error) *http.Response {
	var opErr *net.OpError
	var netErr net.Error
	switch {
	case errors.As(err, &netErr) && netErr.Timeout(), errors.As(err, &opErr):
		return response.CreateErrorResponse(inferenceConnErrorResponse, 500, "Inference connection error while making inference request")
	default:
		return response.CreateErrorResponse(utilsErrorResponse, 500, "Internal error while making inference request")
	}
}

type ErrorBody struct {
	Error string `json:"error"`
}

func (w *NVCFWorker) makeRestRequest(ctx context.Context, work *pb.WorkerInvokeFunctionRequest, requestStreamHandler *httpstream.RequestStreamHandler, assetDir string, responseDir string, meteringEvent *metering.EventDetails, requestTracker *nvcfMetrics.RequestTracker, releaseWorkLimiterFunc func()) error {
	ctx, cancel := context.WithCancel(ctx) // in case we need to cancel early
	defer cancel()

	req, shiftedPollDuration, targetPollDuration, err := w.createInferenceRequest(ctx, work, requestStreamHandler, assetDir, responseDir)
	if err != nil {
		return err
	}

	shouldHandleBackwardsCompatibility := w.shouldHandleBackwardsCompatibility(req)
	defer func() {
		requestTracker.EndWork()
		meteringEvent.InferenceDurationSeconds = int(math.Ceil(requestTracker.TotalWorkTime))
	}()

	requestTracker.StartWork()

	client := func(req *http.Request, pollDuration time.Duration) (*http.Response, error) {
		return w.workerRestClient.Client(ctx).Do(req)
	}

	if shouldHandleBackwardsCompatibility {
		zap.L().Debug("enabling backwards compatibility", zap.String("req id", work.RequestId))
		httpMapper := make(chan *http.Response)
		w.progressMonitor.Monitor(work.RequestId, &progress.MonitoredWork{
			Context:       ctx,
			MeteringEvent: meteringEvent,
			RespondToWork: httpMapper,
		})
		defer w.progressMonitor.Drop(work.RequestId)
		longRequest := lo.Async2(func() (*http.Response, error) {
			resp, err := w.workerRestClient.Client(ctx).Do(req)
			zap.L().Debug("backwards compatibility polling request returned", zap.String("req id", work.RequestId), zap.Error(err))
			if err != nil {
				return nil, err
			}
			return w.mapBackwardsCompatibilityHttpResponse(ctx, work, resp, responseDir, meteringEvent)
		})

		client = func(req *http.Request, pollDuration time.Duration) (*http.Response, error) {
			if pollDuration <= 0 {
				// only allow a 2-second buffer since we know the invocation service will wait up to
				// 2 seconds past the expiry time in case of network latency.
				if pollDuration < -2*time.Second {
					return nil, errors.New("response target too far in the past")
				}
				zap.L().Warn("responding with generated polling for a negative poll duration",
					zap.String("req id", work.RequestId), zap.Duration("pollDuration", pollDuration))
			}
			timer := time.NewTimer(pollDuration)
			select {
			case res := <-httpMapper:
				zap.L().Debug("responding with progress", zap.String("req id", work.RequestId))
				return res, nil
			case packed := <-longRequest:
				res, err := packed.Unpack()
				zap.L().Debug("responding with polling request returned", zap.String("req id", work.RequestId), zap.Error(err))
				return res, err
			case <-timer.C:
				zap.L().Debug("responding with generated polling", zap.String("req id", work.RequestId))
				return &http.Response{StatusCode: http.StatusAccepted, Body: http.NoBody}, nil
			}
		}
	}

	res, err := client(req, shiftedPollDuration)
	if err != nil {
		return err
	}
	zap.L().Debug("initial inference response", zap.String("req id", work.RequestId), zap.Int("status", res.StatusCode))
	if res.StatusCode == http.StatusAccepted {
		releaseWorkLimiterFunc = nil // we are expecting more polling requests, so don't release the limiter early
		// start looking for new polling requests before we respond to the first one to ensure we don't miss any
		workChan := make(chan *pb.WorkerInvokeFunctionRequest)
		// we expect client to long-poll so if there is no incoming poll request within 2x of the
		// timeout they are polling at, then we can assume they left and cancel the request
		// min 10s to make sure low poll times (ie 1s) don't overrun.
		// we have an extra 2s wait in the invocation service to account for latency so we can't
		// have a timeout lower than that.
		timeoutPerPollRequest := max(targetPollDuration*2, 10*time.Second)
		pollingErrChan, err := polling.HandlePollingRequests(ctx, w.nvcfClient.NatsConn, work.RequestId, timeoutPerPollRequest, func(work *pb.WorkerInvokeFunctionRequest) {
			select {
			case <-ctx.Done():
				break
			case workChan <- work:
				break
			}
		})
		if err != nil {
			utils.Close(res.Body.Close)
			return err
		}
		for res.StatusCode == http.StatusAccepted {
			var cancelIfClientGoneTimer *time.Timer

			err = handleRestResponse(ctx, requestStreamHandler, res, releaseWorkLimiterFunc)
			if err != nil {
				zap.L().Warn("failed to return polling response", zap.String("req id", work.RequestId), zap.Error(err))
				// it's expected to get an error if the polling duration is in the past.
				// just keep going and listening for more requests. if it times out again while
				// waiting for polling requests then it will actually fail from pollingErrChan,
				// not during a response.
				if shiftedPollDuration > 0 {
					return err
				} else {
					// very high chance the client is gone if we couldn't send it a response,
					// but it may be in-between polls if the invocation service generated a 202 instead of this worker.
					// only wait 5 more seconds for a polling request.
					cancelIfClientGoneTimer = time.AfterFunc(5*time.Second, cancel)
					zap.L().Warn("waiting 5 more seconds for a client to poll", zap.String("req id", work.RequestId))
				}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case err := <-pollingErrChan:
				zap.L().Warn("failed to consume polling requests", zap.String("req id", work.RequestId), zap.Error(err))
				return err
			case work := <-workChan:
				zap.L().Debug("creating new polling inference request", zap.String("req id", work.RequestId))
				if cancelIfClientGoneTimer != nil {
					cancelIfClientGoneTimer.Stop()
					cancelIfClientGoneTimer = nil
				}
				// requestStreamHandler is only good for one request, so we need to create a new one for the new request
				_ = requestStreamHandler.Close()
				requestStreamHandler, err = httpstream.NewRequestStreamHandler(ctx, w.sharedProxyClient, work.StatelessConfig, work)
				if err != nil {
					return err
				}
				req, shiftedPollDuration, _, err = w.createInferenceRequest(ctx, work, requestStreamHandler, assetDir, responseDir)
				if err != nil {
					return err
				}
				res, err = client(req, shiftedPollDuration)
				if err != nil {
					return err
				}
				zap.L().Debug("polling inference response", zap.String("req id", work.RequestId), zap.Int("status", res.StatusCode))
			}
		}
	}

	return handleRestResponse(ctx, requestStreamHandler, res, releaseWorkLimiterFunc)
}

func handleRestResponse(ctx context.Context, requestStreamHandler *httpstream.RequestStreamHandler, res *http.Response, releaseWorkLimiterFunc func()) error {
	defer utils.Close(res.Body.Close)
	recordContentType(ctx, res.Header)
	zap.L().Info("inference response", zap.Int("status code", res.StatusCode))

	// respond with byte stream to the invocation service. do not bother parsing the response.
	return requestStreamHandler.SendResponse(ctx, res, releaseWorkLimiterFunc)
}

func (w *NVCFWorker) mapBackwardsCompatibilityHttpResponse(ctx context.Context, work *pb.WorkerInvokeFunctionRequest, res *http.Response, responseDir string, meteringEvent *metering.EventDetails) (*http.Response, error) {
	defer utils.Close(res.Body.Close)
	zap.L().Info("mapping backwards compatibility inference response", zap.Int("status code", res.StatusCode))

	artifactScanner := lo.Async2(func() ([]string, error) {
		return scanForArtifacts(responseDir)
	})

	buf := bytebufferpool.Get()
	maxSizePlusOne := int64(work.MaxDirectResponseSizeBytes + 1)
	_, copyErr := io.CopyN(buf, res.Body, maxSizePlusOne)

	if res.StatusCode >= http.StatusBadRequest {
		// Attempt to look for a Triton-style error message.
		body := ErrorBody{}
		_ = json.Unmarshal(buf.Bytes(), &body)
		inferenceError := "Inference error"
		if body.Error != "" {
			inferenceError = body.Error
		}
		bytebufferpool.Put(buf)
		return response.CreateErrorResponse(inferenceErrorResponse, res.StatusCode, inferenceError), nil
	}

	artifacts, err := (<-artifactScanner).Unpack()
	if err != nil {
		zap.L().Error("failed to scan for artifacts", zap.String("req id", work.RequestId), zap.Error(err))
		bytebufferpool.Put(buf)
		return nil, err
	}

	// no error means we copied maxSizePlusOne bytes, which is more than MaxDirectResponseSizeBytes,
	// so we'll have to use the large response mechanism
	if copyErr == nil || len(artifacts) > 0 {
		defer bytebufferpool.Put(buf)
		// re-combine the already read buffer back with the rest of the body
		body := io.MultiReader(bytes.NewReader(buf.Bytes()), res.Body)
		resp, err := w.largeResponse(ctx, work, body, responseDir, artifacts, meteringEvent)
		if err != nil {
			utilsMetrics.LargeResponseFailureCounter.Inc()
			return nil, err
		}
		return resp, nil
	}

	// check for actual error aside from EOF.
	// EOF means we consumed the body without reading more than MaxDirectResponseSizeBytes.
	if copyErr != io.EOF {
		bytebufferpool.Put(buf)
		return nil, copyErr
	}

	nvcfMetrics.ResponseBytesCounter.Add(float64(len(buf.Bytes())))
	meteringEvent.InferenceSize += int64(len(buf.Bytes()))
	closeOnce := sync.Once{}
	return &http.Response{
		StatusCode: res.StatusCode,
		Header:     res.Header,
		Body: struct {
			io.Reader
			io.Closer
		}{
			Reader: bytes.NewReader(buf.Bytes()),
			Closer: closerFunc(func() error {
				closeOnce.Do(func() {
					bytebufferpool.Put(buf)
				})
				return nil
			}),
		},
	}, nil
}

type closerFunc func() error

func (f closerFunc) Close() error {
	return f()
}

func (w *NVCFWorker) createInferenceRequest(ctx context.Context, work *pb.WorkerInvokeFunctionRequest, requestStreamHandler *httpstream.RequestStreamHandler, assetDir string, responseDir string) (*http.Request, time.Duration, time.Duration, error) {
	// work.RequestPath contains query parameters. pass the path as-is without escaping.
	invokeUrl := w.inferenceUrlWithoutPath + work.RequestPath
	req, err := http.NewRequestWithContext(ctx, work.RequestMethod, invokeUrl, w.streamRequestBody(work, requestStreamHandler))
	if err != nil {
		return nil, 0, 0, err
	}

	req.Header = http.Header{
		"NVCF-REQID":                   {work.RequestId},
		"NVCF-SUB":                     {work.Subject},
		"NVCF-NCAID":                   {work.NcaId},
		"NVCF-FUNCTION-NAME":           {w.config.FunctionName},
		"NVCF-FUNCTION-ID":             {w.config.FunctionId},
		"NVCF-FUNCTION-VERSION-ID":     {w.config.FunctionVersionId},
		"NVCF-ASSET-DIR":               {assetDir},
		"NVCF-LARGE-OUTPUT-DIR":        {responseDir},
		"NVCF-MAX-RESPONSE-SIZE-BYTES": {strconv.Itoa(int(work.MaxDirectResponseSizeBytes))},
		"NVCF-NSPECTID":                {metering.NspectIdFromEnv()},
		"NVCF-BACKEND":                 {w.meteringConfig.Backend},
		"NVCF-INSTANCETYPE":            {w.meteringConfig.InstanceType},
		"NVCF-REGION":                  {w.meteringConfig.ZoneName},
		"NVCF-ENV":                     {w.meteringConfig.ICMSEnvironment},
	}
	for _, header := range work.RequestHeaders {
		req.Header.Add(header.Key, header.Value)
	}
	if !w.config.V3BackwardsCompatibilityDisabled && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	inputAssetIds := lo.Map(work.InputAssetReference, func(item *pb.InputAssetReference, _ int) string {
		return item.AssetId
	})
	if len(inputAssetIds) > 0 {
		// XXX: http.Header.Set() DOES NOT preserve case! MY-HEADER-NAME -> My-Header-Name
		// XXX: So we're manually add the header this way.
		req.Header["NVCF-FUNCTION-ASSET-IDS"] = []string{strings.Join(inputAssetIds, ",")}
	}

	targetPollDuration := getTargetPollDuration(req)
	// adjust downward for when the request was first sent
	shiftedPollDuration := targetPollDuration - time.Since(work.RequestTime.AsTime())
	// set the poll header so the inference container knows the max duration it has to respond
	req.Header.Set("nvcf-poll-seconds", strconv.Itoa(int(shiftedPollDuration.Seconds())))
	zap.L().Debug("setting poll duration", zap.String("req id", work.RequestId), zap.Duration("poll", shiftedPollDuration))
	contentLength := req.Header.Get("Content-Length")
	if contentLength != "" {
		if length, err := strconv.Atoi(contentLength); err == nil {
			req.ContentLength = int64(length)
		}
	}
	return req, shiftedPollDuration, targetPollDuration, nil
}

func (w *NVCFWorker) streamRequestBody(work *pb.WorkerInvokeFunctionRequest, requestStreamHandler *httpstream.RequestStreamHandler) io.ReadCloser {
	return &lazyReadCloser{
		nextReadCloser: requestStreamHandler.GetClientRequestBody,
		ReadCloser:     io.NopCloser(bytes.NewReader(work.RequestBody)),
	}
}

type lazyReadCloser struct {
	nextReadCloser func() (io.ReadCloser, error)
	io.ReadCloser
}

func (r *lazyReadCloser) Read(p []byte) (n int, err error) {
	n, err = r.ReadCloser.Read(p)
	if err == io.EOF {
		if r.nextReadCloser != nil {
			err = nil
			// use temp vars to always keep a valid r.ReadCloser so it doesn't error if Close() is called
			rc, err := r.nextReadCloser()
			r.nextReadCloser = nil
			if err != nil {
				return n, err
			}
			r.ReadCloser = rc
			// don't return an empty read if there may be more data to read
			if n <= 0 {
				return r.Read(p)
			}
		}
	}
	return n, err
}

// getTargetPollDuration is what end client expects to poll at, but there will be a difference
// between what the end client expects and when the worker actually starts working, especially in
// the case of a queue.
func getTargetPollDuration(req *http.Request) time.Duration {
	// calculate the time when we should return the first polling response and then listen for future requests
	pollDuration := time.Minute // default to 1 minute if not sent
	pollSeconds := req.Header.Get("nvcf-poll-seconds")
	if pollSeconds != "" {
		seconds, err := strconv.Atoi(pollSeconds)
		if err == nil {
			pollDuration = time.Duration(seconds) * time.Second
		}
	}
	return pollDuration
}

func recordContentType(ctx context.Context, header http.Header) {
	span := trace.SpanFromContext(ctx)
	span.AddEvent("received response headers")
	contentTypes := header.Values("Content-Type")
	// the StringSlice attribute doesn't seem to work in lightstep
	span.SetAttributes(attribute.String("http.response_content_type", strings.Join(contentTypes, ", ")))
}

func (w *NVCFWorker) downloadAssets(ctx context.Context, work *pb.WorkerInvokeFunctionRequest, assetDir string) error {
	if len(work.InputAssetReference) == 0 {
		return nil
	}
	zap.L().Info("creating assets dir", zap.String("req id", work.RequestId), zap.String("path", assetDir))
	err := os.Mkdir(assetDir, 0777)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	err = os.Chmod(assetDir, 0777)
	if err != nil {
		return err
	}

	assets, err := w.getValidAssets(ctx, work)
	if err != nil {
		return fmt.Errorf("failed to get valid assets list: %w", err)
	}
	// TODO METRICS - keep track of download times & sizes
	group, ctx := errgroup.WithContext(ctx)
	for _, inputAsset := range assets {
		inputAsset := inputAsset // loop variable capture
		group.Go(func() error {
			err := w.downloadAsset(ctx, work.RequestId, assetDir, inputAsset)
			if err != nil {
				utilsMetrics.AssetDownloadFailureCounter.Inc()
			}
			return err
		})
	}

	return group.Wait()
}

func (w *NVCFWorker) downloadAsset(ctx context.Context, requestID string, assetDir string, inputAsset *pb.InputAssetReference) error {
	zap.L().Info("fetching asset", zap.String("req id", requestID), zap.String("asset id", inputAsset.AssetId))
	downloadStart := time.Now()
	res, err := w.workerRestClient.Get(ctx, inputAsset.Reference)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != 200 {
		resBody, err := io.ReadAll(res.Body)
		if err != nil {
			return err
		}
		zap.L().Error("non-200 response while downloading asset",
			zap.String("req id", requestID),
			zap.String("asset id", inputAsset.AssetId),
			zap.Int("status code", res.StatusCode),
			zap.String("response", string(resBody)))
		return fmt.Errorf("failed to download asset %s", inputAsset.AssetId)
	}

	assetFileName := filepath.Join(assetDir, inputAsset.AssetId)
	assetFile, err := os.OpenFile(assetFileName, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return err
	}

	assetSizeBytes, err := io.Copy(assetFile, res.Body)
	if err != nil {
		_ = assetFile.Close()
		return err
	}

	err = assetFile.Close()
	if err != nil {
		return err
	}

	utilsMetrics.AssetDownloadCounter.Inc()
	utilsMetrics.AssetBytesCounter.Add(float64(assetSizeBytes))
	utilsMetrics.AssetDownloadTimeCounter.Add(time.Since(downloadStart).Seconds())

	return nil
}

// backwards compatibility mode  is off, no matter what the function config is for event streams
// since we don't want any of the backwards compatibility response mapping for those
func (w *NVCFWorker) shouldHandleBackwardsCompatibility(req *http.Request) bool {
	if mediaType, _, _ := mime.ParseMediaType(req.Header.Get("Accept")); mediaType == "text/event-stream" {
		return false
	}
	if req.Header.Get("nvcf-feature-disable-worker-compatibility") == "true" {
		return false
	}
	return !w.config.V3BackwardsCompatibilityDisabled
}

func scanForArtifacts(responseDir string) ([]string, error) {
	var foundArtifacts []string
	err := filepath.WalkDir(responseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.IsDir() && filepath.Base(path) != "progress" {
			foundArtifacts = append(foundArtifacts, path)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return foundArtifacts, nil
}
