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

package core

import (
	"context"
	"fmt"

	k8sclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// KubeClients aggregates kubeconfig and generated K8s clients, such
// as native K8s client and EGX CRD client
// client. It is immutable after creation.
type KubeClients struct {
	Config *rest.Config
	K8s    k8sclient.Interface
}

type KubeClientsStream struct {
	configCh        <-chan *rest.Config
	k8sNewForConfig func(*rest.Config) (k8sclient.Interface, error)
}

func NewKubeClientsStream() *KubeClientsStream {
	return &KubeClientsStream{
		k8sNewForConfig: func(c *rest.Config) (k8sclient.Interface, error) { return k8sclient.NewForConfig(c) },
	}
}

func (s *KubeClientsStream) WithConfigCh(ch <-chan *rest.Config) *KubeClientsStream {
	next := *s
	next.configCh = ch
	return &next
}

func (s *KubeClientsStream) WithK8sNewForConfig(f func(c *rest.Config) (k8sclient.Interface, error)) *KubeClientsStream {
	next := *s
	next.k8sNewForConfig = f
	return &next
}

func (s *KubeClientsStream) Start(ctx context.Context) <-chan *KubeClients {
	out := make(chan *KubeClients)
	log := GetLogger(ctx)

	configCh := s.configCh
	go func() {
		defer close(out)
		for config := range configCh {
			clients, err := s.buildKubeClients(config)
			if err != nil {
				log.Errorf("Failed to buildClients, err: %s", err.Error())
				continue
			}
			select {
			case out <- clients:
				log.Infof("Forwarded new clients")
			case <-ctx.Done():
				log.Infof("Context is done, no more refresh client channel events will happen")
				return
			}
		}
		log.Info("configCh is closed for some reason.. quitting kubeclients stream")
	}()

	return out
}

func (s *KubeClientsStream) buildKubeClients(config *rest.Config) (*KubeClients, error) {
	k8sClient, err := s.k8sNewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset for core Kube APIs: %w", err)
	}

	return &KubeClients{
		Config: config,
		K8s:    k8sClient,
	}, nil
}

func NewKubeClientsFromConfigCh(ctx context.Context, configCh <-chan *rest.Config) (*KubeClients, error) {
	log := GetLogger(ctx)
	clientsCh := NewKubeClientsStream().WithConfigCh(configCh).Start(ctx)

	log.Infof("Wait for KubeClients from clientsCh ...")
	select {
	case clients, ok := <-clientsCh:
		if !ok {
			return nil, fmt.Errorf("failed to configure KubeClients")
		}
		return clients, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("ctx.Done(), err: %w", ctx.Err())
	}
}

func NewKubeClientsFromConfig(ctx context.Context, config *rest.Config) (*KubeClients, error) {
	log := GetLogger(ctx)
	log.Infof("Configuring KubeClients from *rest.Config for %s ...", config.Host)

	configCh := make(chan *rest.Config)
	go func() {
		configCh <- config
		close(configCh)
	}()
	return NewKubeClientsFromConfigCh(ctx, configCh)
}

func NewKubeClientsFromPath(ctx context.Context, path string) (*KubeClients, error) {
	log := GetLogger(ctx)
	log.Infof("Configuring KubeClients from kubeconfig path %q ...", path)

	configurator := NewPathKubeConfigurator().WithPath(path)
	configCh := configurator.Start(ctx)
	return NewKubeClientsFromConfigCh(ctx, configCh)
}
