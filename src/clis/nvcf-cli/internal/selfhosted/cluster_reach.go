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
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
)

// ClusterUnreachableError reports a cluster the run could not contact at all:
// its kubeconfig or context did not load, or the API server refused the
// connection, could not be resolved, failed TLS verification, or rejected the
// credentials. Unlike an error part way through a probe, it means nothing
// about the cluster was checked.
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

// clusterFirstCallTimeout bounds the first API call to a cluster. A var so
// tests can shorten it.
var clusterFirstCallTimeout = 15 * time.Second

// connectCluster builds a client for kubeContext and makes its first API call.
// A cluster that cannot be contacted at all is a ClusterUnreachableError,
// including one that does not answer that call in time. Any other failure of
// that call is left for the probe that follows to report.
func connectCluster(ctx context.Context, kubeContext string) (kubernetes.Interface, error) {
	restCfg, err := loadKubeConfig(kubeContext)
	if err != nil {
		return nil, &ClusterUnreachableError{Context: kubeContext, Err: fmt.Errorf("building kubeconfig: %w", err)}
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, &ClusterUnreachableError{Context: kubeContext, Err: fmt.Errorf("building kubernetes client: %w", err)}
	}
	callCtx, cancel := context.WithTimeout(ctx, clusterFirstCallTimeout)
	defer cancel()
	err = client.Discovery().RESTClient().Get().AbsPath("/version").Do(callCtx).Error()
	// A run whose own budget ended says so itself; the cluster is not to blame.
	if err != nil && ctx.Err() == nil && (callCtx.Err() != nil || isUnreachable(err)) {
		return nil, &ClusterUnreachableError{Context: kubeContext, Err: err}
	}
	return client, nil
}

// isUnreachable reports an error no retry within the run can clear: the
// server refused the connection or the credentials, could not be resolved, or
// failed TLS verification. A server error is not among them.
func isUnreachable(err error) bool {
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	return apierrors.IsUnauthorized(err) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.As(err, &dnsErr) || errors.As(err, &certErr)
}
