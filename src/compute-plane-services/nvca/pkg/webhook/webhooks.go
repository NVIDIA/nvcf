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

package webhook

import (
	"context"
	"net/http"
	"sync/atomic"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
)

const (
	// Alias for convenience
	instanceTypeLK = nodefeatures.UniformInstanceTypeLabelKey
)

func NewHelmMiniServiceValidatingWebhook(ctx context.Context, name string, fff featureflag.Fetcher) (http.Handler, error) {
	wh := newHelmMiniServiceValWebhookHandler(fff)
	return newStandaloneWebhook(ctx, name, wh)
}

type PodAffinityOptions struct {
	SharedClusterOn        *atomic.Bool
	UniformInstanceLabels  bool
	HostIsolation          bool
	AccountIsolation       bool
	BinPackTenantWorkloads bool
}

func NewPodAffinityMutatingWebhook(ctx context.Context, name string, opts PodAffinityOptions) (http.Handler, error) {
	sch := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(sch))
	wh := admission.WithCustomDefaulter(sch, &corev1.Pod{}, &podAffinityWebhook{opts})
	return newStandaloneWebhook(ctx, name, wh)
}

func NewPodEnforcementMutatingWebhook(ctx context.Context, name string, opts EnforcementOptions) (http.Handler, error) {
	if opts.AttributeFetcher == nil {
		opts.AttributeFetcher = featureflag.DefaultFetcher
	}
	sch := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(sch))
	wh := admission.WithCustomDefaulter(sch, &corev1.Pod{}, &podEnforcementWebhook{
		EnforcementOptions: opts,
	})
	return newStandaloneWebhook(ctx, name, wh)
}

func newStandaloneWebhook(ctx context.Context, name string, wh admission.Handler) (http.Handler, error) {
	log := core.GetLogger(ctx).WithField("webhook_name", name)
	ctx = core.WithLogger(ctx, log)

	return admission.StandaloneWebhook(&admission.Webhook{
		Handler: wh,
		WithContextFunc: func(ctx context.Context, r *http.Request) context.Context {
			// Since we don't have middleware, we need to log the request information here
			log.WithField("content_length", r.ContentLength).Debug("webhook request information")
			return core.WithLogger(ctx, log)
		},
	}, admission.StandaloneOptions{
		Logger: ctrllog.FromContext(ctx),
	})
}
