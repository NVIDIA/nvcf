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

package selfhosted

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
)

// ClusterUnreachableError reports a cluster the run could not contact at all:
// its kubeconfig or context did not load, or no API call to it succeeded,
// whatever stopped it: a refused or dropped connection, a name that did not
// resolve, failed TLS verification, rejected credentials or a credential
// plugin that failed, no answer in time, or a server error. Unlike an error
// part way through a probe, it means nothing about the cluster was checked.
type ClusterUnreachableError struct {
	Context string
	Err     error
}

func (e *ClusterUnreachableError) Error() string {
	name := e.Context
	if name == "" {
		name = "the current kubeconfig context"
	}
	return fmt.Sprintf("cannot reach %s: %v", name, e.Err)
}

func (e *ClusterUnreachableError) Unwrap() error { return e.Err }

// clusterFirstCallTimeout bounds each round trip of the first API call to a
// cluster. A var so tests can shorten it.
var clusterFirstCallTimeout = 15 * time.Second

// firstCallRetryDelay is the pause before a first call that met a server error
// is tried again. A var so tests can shorten it.
var firstCallRetryDelay = time.Second

// firstCallBodyLimit caps the body of a first-call response, which is read
// within the call's bound. /version answers in well under a kilobyte.
const firstCallBodyLimit = 1 << 20

// connectCluster builds a client for kubeContext, its transport wrapped by
// wraps, and makes its first API call. A rejected token or a server error gets
// one more try. When the call still fails, whatever the error, the cluster is
// a ClusterUnreachableError, unless the run's own budget ended or it was
// interrupted meanwhile: then the run, not the cluster, stopped it, and the
// probe that follows reports that.
func connectCluster(
	ctx context.Context, kubeContext string, wraps ...func(http.RoundTripper) http.RoundTripper,
) (kubernetes.Interface, error) {
	restCfg, err := loadKubeConfig(kubeContext)
	if err != nil {
		return nil, &ClusterUnreachableError{Context: kubeContext, Err: fmt.Errorf("building kubeconfig: %w", err)}
	}
	restCfg.Wrap(boundFirstCall)
	for _, wrap := range wraps {
		restCfg.Wrap(wrap)
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, &ClusterUnreachableError{Context: kubeContext, Err: fmt.Errorf("building kubernetes client: %w", err)}
	}
	err = firstClusterCall(ctx, client)
	switch {
	case ctx.Err() != nil:
		// The run's budget ended or it was interrupted: not the cluster's doing.
	case apierrors.IsUnauthorized(err):
		// A credential plugin replaces a cached token the server rejected on
		// the next call.
		err = firstClusterCall(ctx, client)
	case isServerError(err):
		// client-go retries a server error only when it carries a
		// Retry-After, and a load balancer in front of a rolling API server
		// answers 502 or 503 without one. A connection dropped before the
		// response, or part way through its body, client-go retries itself.
		select {
		case <-ctx.Done():
		case <-time.After(firstCallRetryDelay):
			err = firstClusterCall(ctx, client)
		}
	}
	if err != nil && ctx.Err() == nil {
		return nil, &ClusterUnreachableError{Context: kubeContext, Err: err}
	}
	return client, nil
}

// isServerError reports an API call the server answered with a 5xx status.
func isServerError(err error) bool {
	var status apierrors.APIStatus
	return errors.As(err, &status) && status.Status().Code >= http.StatusInternalServerError
}

// firstClusterCall asks the server for its version, each round trip bounded
// by clusterFirstCallTimeout.
func firstClusterCall(ctx context.Context, client kubernetes.Interface) error {
	ctx = context.WithValue(ctx, firstCallBound{}, clusterFirstCallTimeout)
	return client.Discovery().RESTClient().Get().AbsPath("/version").Do(ctx).Error()
}

// firstCallBound is the context key for the bound firstClusterCall puts on
// each of its round trips.
type firstCallBound struct{}

// boundFirstCall wraps a client's transport to bound a round trip whose
// context carries a firstCallBound, its body included. An exec or auth
// provider wraps the transport outside it and fetches its credentials before
// the round trip, and logs in again after a 401 once it returns, so a login
// the operator is waiting on does not count, while an API server that takes
// the connection and never answers still does.
func boundFirstCall(rt http.RoundTripper) http.RoundTripper {
	return boundedRoundTripper{next: rt}
}

type boundedRoundTripper struct{ next http.RoundTripper }

func (b boundedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	bound, ok := req.Context().Value(firstCallBound{}).(time.Duration)
	if !ok {
		return b.next.RoundTrip(req)
	}
	ctx, cancel := context.WithTimeout(req.Context(), bound)
	defer cancel()
	resp, err := b.next.RoundTrip(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	// Read within the round trip, the body is not cut off by the bound running
	// out during a login after it, and a connection dropped part way through
	// the body fails the round trip, which client-go retries.
	body, err := io.ReadAll(io.LimitReader(resp.Body, firstCallBodyLimit+1))
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(body) > firstCallBodyLimit {
		return nil, fmt.Errorf("response to %s is larger than %d bytes", req.URL.Path, firstCallBodyLimit)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}
