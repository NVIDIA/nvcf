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
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
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

// clusterFirstCallTimeout bounds the first API call to a cluster whose
// credentials come from the kubeconfig itself. A var so tests can shorten it.
var clusterFirstCallTimeout = 15 * time.Second

// connectCluster builds a client for kubeContext and makes its first API call.
// When that call fails, whatever the error, the cluster is a
// ClusterUnreachableError, unless the run's own budget ended or it was
// interrupted meanwhile: then the run, not the cluster, stopped it, and the
// probe that follows reports that.
func connectCluster(ctx context.Context, kubeContext string) (kubernetes.Interface, error) {
	restCfg, err := loadKubeConfig(kubeContext)
	if err != nil {
		return nil, &ClusterUnreachableError{Context: kubeContext, Err: fmt.Errorf("building kubeconfig: %w", err)}
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, &ClusterUnreachableError{Context: kubeContext, Err: fmt.Errorf("building kubernetes client: %w", err)}
	}
	err = firstClusterCall(ctx, client, restCfg)
	if apierrors.IsUnauthorized(err) && ctx.Err() == nil {
		// A credential plugin replaces a cached token the server rejected on
		// the next call.
		err = firstClusterCall(ctx, client, restCfg)
	}
	if err != nil && ctx.Err() == nil {
		return nil, &ClusterUnreachableError{Context: kubeContext, Err: err}
	}
	return client, nil
}

// firstClusterCall asks the server for its version. Credentials from an exec
// or auth provider are fetched within the call, and a login it waits on takes
// as long as it takes, so then only the run's budget bounds it.
func firstClusterCall(ctx context.Context, client kubernetes.Interface, restCfg *rest.Config) error {
	if restCfg.ExecProvider == nil && restCfg.AuthProvider == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, clusterFirstCallTimeout)
		defer cancel()
	}
	return client.Discovery().RESTClient().Get().AbsPath("/version").Do(ctx).Error()
}
