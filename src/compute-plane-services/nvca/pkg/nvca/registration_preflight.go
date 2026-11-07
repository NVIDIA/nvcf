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

package nvca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	listersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	nvcaotel "github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/internal/otel"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures/sharedcluster"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce"
	nvcaerrors "github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// registrationValidator asks ICMS whether it would accept a registration.
type registrationValidator interface {
	ValidateRegistration(ctx context.Context, rreq *types.ICMSRegistrationRequest) error
}

// registrationPreflight builds the ICMS registration that an agent started with opts
// sends, with the agent's own code, and asks ICMS to validate it without applying it.
// It only reads the cluster: it lists the GPU nodes once, or reads the static GPU
// ConfigMap, and starts no informers, controllers, or workers.
type registrationPreflight struct {
	opts      *AgentOptions
	clients   *kubeclients.KubeClients
	validator registrationValidator
}

// runRegistrationPreflight runs `nvca preflight-registration` with opts.
func runRegistrationPreflight(ctx context.Context, opts *AgentOptions) types.RegistrationPreflightResult {
	tokenFetcher, _, err := newTokenFetcher(ctx, "icms", opts.TokenFetcherOptions)
	if err != nil {
		return preflightError(fmt.Errorf("initialize the ICMS token fetcher: %w", err))
	}
	clients, err := defaultNewKubeClients(ctx, opts.KubeConfigPath)
	if err != nil {
		return preflightError(fmt.Errorf("create the Kubernetes clients: %w", err))
	}
	p := &registrationPreflight{
		opts:    opts,
		clients: clients,
		validator: NewICMSClientWithHostHeaderOverride(ctx, opts.ClusterID, opts.EffectiveICMSURL(),
			opts.ICMSHostHeaderOverride, tokenFetcher, nvcaotel.NewTracer()),
	}
	return p.run(ctx)
}

// defaultRegistrationPreflightTimeout bounds the preflight. It leaves room for the
// retries of the ICMS client.
const defaultRegistrationPreflightTimeout = 2 * time.Minute

// runRegistrationPreflightWithin runs run with a deadline of timeout. It returns an
// Error result once the deadline passes, even when run is blocked in a call that
// does not honor its context.
func runRegistrationPreflightWithin(
	ctx context.Context,
	timeout time.Duration,
	run func(context.Context) types.RegistrationPreflightResult,
) types.RegistrationPreflightResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan types.RegistrationPreflightResult, 1)
	go func() { done <- run(ctx) }()
	select {
	case result := <-done:
		return result
	case <-ctx.Done():
		return preflightError(fmt.Errorf("the preflight did not finish within %s: %w", timeout, ctx.Err()))
	}
}

func (p *registrationPreflight) run(ctx context.Context) types.RegistrationPreflightResult {
	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"clusterID": p.opts.ClusterID,
		"ncaID":     p.opts.NCAId,
	})

	rreq, conflictHint, err := p.registrationRequest(ctx)
	if err != nil {
		result := preflightError(fmt.Errorf("build the ICMS registration request: %w", err))
		log.WithError(err).Error("Registration preflight could not build the registration")
		return result
	}

	result := preflightResult(p.validator.ValidateRegistration(ctx, &rreq), conflictHint)
	log = log.WithFields(logrus.Fields{
		"outcome":     result.Outcome,
		"http_status": result.HTTPStatus,
		"reason":      result.Reason,
	})
	switch result.Outcome {
	case types.RegistrationPreflightAccepted, types.RegistrationPreflightUnsupported:
		log.Info(result.Message)
	case types.RegistrationPreflightRejected:
		log.Warn(result.Message)
	default:
		log.Error(result.Message)
	}
	return result
}

// registrationRequest returns the registration request that the agent sends, and a
// function that explains an ICMS 409 for it.
func (p *registrationPreflight) registrationRequest(
	ctx context.Context,
) (types.ICMSRegistrationRequest, func(*types.ICMSRegistrationError) string, error) {
	fetcher := p.opts.FeatureFlagFetcher

	nodeLister, sharedCluster, err := gpuNodes(ctx, p.clients.K8s, fetcher)
	if err != nil {
		return types.ICMSRegistrationRequest{}, nil, err
	}

	// As in the agent, shared-cluster mode only applies to dynamic GPU discovery.
	var (
		nfClient        nodefeatures.Client
		sharedClusterOn *atomic.Bool
	)
	if p.opts.DynamicGPUDiscoveryEnabled {
		provider, err := types.NormalizeClusterProvider(p.opts.CloudProvider)
		if err != nil {
			return types.ICMSRegistrationRequest{}, nil, fmt.Errorf("normalize cluster provider: %w", err)
		}
		sharedClusterOn = &atomic.Bool{}
		sharedClusterOn.Store(sharedCluster)
		nfClient = nodefeatures.NewDynamicClient(nil, nodeLister, provider, nodefeatures.DynamicClientOptions{
			MultipleGPUTypesAllowed: p.opts.MultipleGPUTypesAllowed,
			UniformInstanceLabels:   p.opts.UniformInstanceLabelsEnabled,
			SharedClusterOn:         sharedClusterOn,
			AttributeFetcher:        fetcher,
		})
	} else {
		nfClient = nodefeatures.NewStaticClient(p.clients, nil, p.opts.SystemNamespace, p.opts.GPUCapacity)
	}

	backendGPUs, err := nfClient.GetAllBackendGPUs(ctx)
	if err != nil {
		return types.ICMSRegistrationRequest{}, nil, fmt.Errorf("discover GPUs: %w", err)
	}

	overhead, err := enforce.NewInfraOverheadGetter(fetcher, p.opts.Config,
		enforce.GetRuntimeClassK8sClient(p.clients.K8s)).GetInfraOverhead(ctx)
	if err != nil {
		return types.ICMSRegistrationRequest{}, nil, fmt.Errorf("get infra overhead: %w", err)
	}

	regGPUs, _, err := registrationGPUs(ctx, backendGPUs, p.opts.MultiNodeWorkloadsEnabled,
		enforce.InfraOverheadGetterFunc(func(context.Context) (corev1.ResourceList, error) {
			return overhead, nil
		}),
		nodeLister, sharedClusterOn)
	if err != nil {
		return types.ICMSRegistrationRequest{}, nil, err
	}

	k8sVersion := p.opts.K8sVersion
	if k8sVersion == "" {
		if k8sVersion, err = getCurrentK8sVersion(ctx, p.clients.DiscoveryClient); err != nil {
			return types.ICMSRegistrationRequest{}, nil, err
		}
	}

	removed := instanceTypesRemovedByOverhead(backendGPUs, p.opts.MultiNodeWorkloadsEnabled, overhead)
	conflictHint := func(icmsErr *types.ICMSRegistrationError) string {
		return registrationConflictHint(icmsErr, overhead, removed)
	}
	return p.opts.buildICMSRegistrationRequest(k8sVersion, regGPUs), conflictHint, nil
}

// gpuNodes lists the nodes that the agent's node informer watches, and reports whether
// shared-cluster mode is on: the agent turns it on while any of those nodes carries the
// shared-cluster schedule label.
func gpuNodes(
	ctx context.Context,
	k8s kubernetes.Interface,
	fetcher featureflag.AttributeFetcher,
) (listersv1.NodeLister, bool, error) {
	nodes, err := k8s.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: nodefeatures.NodeLabelSelector(fetcher),
	})
	if err != nil {
		return nil, false, fmt.Errorf("list nodes: %w", err)
	}
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	sharedCluster := false
	for i := range nodes.Items {
		node := &nodes.Items[i]
		if err := indexer.Add(node); err != nil {
			return nil, false, fmt.Errorf("index node %s: %w", node.Name, err)
		}
		if node.Labels[sharedcluster.ScheduleLabelKey] == "true" {
			sharedCluster = true
		}
	}
	return listersv1.NewNodeLister(indexer), sharedCluster, nil
}

// preflightResult turns the outcome of a registration validation into the preflight
// result. conflictHint explains an ICMS 409.
func preflightResult(
	err error,
	conflictHint func(*types.ICMSRegistrationError) string,
) types.RegistrationPreflightResult {
	if err == nil {
		return types.RegistrationPreflightResult{
			Outcome: types.RegistrationPreflightAccepted,
			Message: "ICMS accepts the registration",
		}
	}
	status := nvcaerrors.GetHTTPStatusCode(err)
	if errors.Is(err, ErrRegistrationValidationUnsupported) {
		return types.RegistrationPreflightResult{
			Outcome:    types.RegistrationPreflightUnsupported,
			HTTPStatus: status,
			Message:    err.Error(),
		}
	}
	var icmsErr *ICMSErrorResponse
	if errors.As(err, &icmsErr) && isRegistrationRejection(status) {
		message := "ICMS rejects the registration: " + icmsErr.Body.Message
		if status == http.StatusConflict && conflictHint != nil {
			if hint := conflictHint(&icmsErr.Body); hint != "" {
				message += "; " + hint
			}
		}
		return types.RegistrationPreflightResult{
			Outcome:           types.RegistrationPreflightRejected,
			HTTPStatus:        status,
			Reason:            icmsErr.Body.Reason,
			InstanceTypes:     icmsErr.Body.InstanceTypes,
			ActiveInstanceIDs: icmsErr.Body.ActiveInstanceIDs,
			Message:           message,
		}
	}
	return types.RegistrationPreflightResult{
		Outcome:    types.RegistrationPreflightError,
		HTTPStatus: status,
		Message:    err.Error(),
	}
}

// isRegistrationRejection reports whether status is an ICMS verdict on the
// registration: a request it does not accept, a cluster it does not know, or a
// conflict with its data. Other statuses, such as 401, 429, or 5xx, say nothing about
// the registration.
func isRegistrationRejection(status int) bool {
	return status == http.StatusBadRequest || status == http.StatusNotFound || status == http.StatusConflict
}

func preflightError(err error) types.RegistrationPreflightResult {
	return types.RegistrationPreflightResult{
		Outcome:    types.RegistrationPreflightError,
		HTTPStatus: nvcaerrors.GetHTTPStatusCode(err),
		Message:    err.Error(),
	}
}

// Kubernetes keeps at most 4096 bytes of a termination message. These bounds keep the
// JSON well below that.
const (
	maxTerminationMessageListItems = 20
	maxTerminationMessageTextBytes = 1024
)

// registrationPreflightTerminationMessage returns result as JSON for the termination
// message. Long lists and messages are shortened so the JSON stays complete.
func registrationPreflightTerminationMessage(result types.RegistrationPreflightResult) []byte {
	if len(result.InstanceTypes) > maxTerminationMessageListItems {
		result.InstanceTypes = result.InstanceTypes[:maxTerminationMessageListItems]
	}
	if len(result.ActiveInstanceIDs) > maxTerminationMessageListItems {
		result.ActiveInstanceIDs = result.ActiveInstanceIDs[:maxTerminationMessageListItems]
	}
	result.Message = truncateUTF8(result.Message, maxTerminationMessageTextBytes)
	b, err := json.Marshal(result)
	if err != nil {
		// Marshaling a struct of strings, a slice of strings, and an int does not fail.
		return []byte(fmt.Sprintf(`{"outcome":%q}`, result.Outcome))
	}
	return b
}

// truncateUTF8 shortens s to at most n bytes without splitting a character, and marks
// the cut with "...".
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	n -= len("...")
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}
