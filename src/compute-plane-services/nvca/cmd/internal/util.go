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

package internalutil

import (
	"context"
	"errors"
	"flag"
	"os"
	"slices"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	ContextFlagName  = "context"
	ContextFlagUsage = "Explicitly set kubeconfig current-context, " +
		"to avoid accidentally accessing the wrong cluster"
)

func NewContextFlag() *string {
	f := flag.String(ContextFlagName, "", ContextFlagUsage)
	return f
}

func NewK8sClient(ctx context.Context, currContext string) (*kubernetes.Clientset, *rest.Config, error) {
	kcPath := os.Getenv(clientcmd.RecommendedConfigPathEnvVar)
	if kcPath == "" {
		if _, err := os.Stat(clientcmd.RecommendedHomeFile); err == nil || errors.Is(err, os.ErrExist) {
			kcPath = clientcmd.RecommendedHomeFile
		}
	}

	restCfg, err := getRESTConfig(ctx, kcPath, currContext)
	if err != nil {
		return nil, nil, err
	}

	k8sClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, nil, err
	}
	return k8sClient, restCfg, nil
}

func getRESTConfig(ctx context.Context, kcPath, currContext string) (*rest.Config, error) {
	log := core.GetLogger(ctx)

	if kcPath == "" {
		log.Warn("Kubeconfig path was not specified. Using the inClusterConfig. This might not work.")
		restCfg, err := rest.InClusterConfig()
		if err == nil {
			return restCfg, nil
		}
		log.WithError(err).Warn("error creating inClusterConfig, falling back to default config")
	}

	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kcPath},
		&clientcmd.ConfigOverrides{CurrentContext: currContext})

	raw, err := cc.RawConfig()
	if err != nil {
		return nil, err
	}

	if len(raw.Contexts) > 1 && currContext == "" {
		log.Warnf("More than one context is specified in kubeconfig, but the current context is not explicitly set by -context. "+
			"This may result in the wrong cluster being targeted ! "+
			"Either set -context or ensure the current-context (%q) is correct before using the output from this utility.",
			raw.CurrentContext,
		)
	}

	restCfg, err := cc.ClientConfig()
	if err != nil {
		return nil, err
	}

	return restCfg, nil
}

func ArgsContainConfigFlag(args []string) bool {
	return slices.ContainsFunc(args, func(s string) bool {
		return s == "--config" || strings.HasPrefix(s, "--config=")
	})
}
