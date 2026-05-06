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
	"os"
	"runtime/pprof"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/flowcontrol"
)

// KubeConfigurator is an interface sends (updated) kubeconfig over a
// channel for configuring K8s clients.
type KubeConfigurator interface {
	Start(ctx context.Context) <-chan *rest.Config
}

// PathKubeConfigurator implements KubeConfigurator by building
// rest.Config from given kubeconfig file path. Currently it only send
// kubeconfig once during init. It can be extended to watch kubeconfig
// file content changes if it is useful.
type PathKubeConfigurator struct {
	path              string
	clientRateLimiter flowcontrol.RateLimiter
	configModifier    func(*rest.Config)
}

func NewPathKubeConfigurator() *PathKubeConfigurator { return &PathKubeConfigurator{} }

func (c *PathKubeConfigurator) WithPath(p string) *PathKubeConfigurator {
	next := *c
	next.path = p
	return &next
}

func (c *PathKubeConfigurator) WithClientRateLimitingDisabled() *PathKubeConfigurator {
	next := *c
	next.clientRateLimiter = flowcontrol.NewFakeAlwaysRateLimiter()
	return &next
}

func (c *PathKubeConfigurator) WithConfigModifier(configModifier func(*rest.Config)) *PathKubeConfigurator {
	next := *c
	next.configModifier = configModifier
	return &next
}

func (c *PathKubeConfigurator) Start(ctx context.Context) <-chan *rest.Config {
	log := GetLogger(ctx)
	path := c.path
	out := make(chan *rest.Config, 1)
	go func() {
		defer close(out)

		select {
		case <-ctx.Done():
			return
		default:
			config, err := clientcmd.BuildConfigFromFlags("", path)
			if err != nil {
				log.Errorf("Failed to clientcmd.BuildConfigFromFlags at %s, err: %s", path, err.Error())
				return
			}
			// Apply options for config
			if c.clientRateLimiter != nil {
				config.RateLimiter = c.clientRateLimiter
			} else {
				// Set default values higher than standard
				config.QPS = 100
				config.Burst = 200
			}
			// Apply config modifier if set
			if c.configModifier != nil {
				c.configModifier(config)
			}
			select {
			case <-ctx.Done():
				return
			case out <- config:
			}
		}
	}()
	return out
}

type TokenFetcher interface {
	FetchToken(ctx context.Context) (string, error)
	RefreshClient()
}

// TokenKubeConfigurator implements KubeConfigurator by building
// rest.Config use the bear token retrieved from a tokenFetcher. It
// checks token update at given `Interval`, and only sends new
// kubeconfig down to the channel if cached token get's changed.
type TokenKubeConfigurator struct {
	masterURL string
	fetcher   TokenFetcher
	interval  time.Duration
	timeout   time.Duration

	sync.Mutex
	token string
}

func NewTokenKubeConfigurator() *TokenKubeConfigurator { return &TokenKubeConfigurator{} }

func (c *TokenKubeConfigurator) WithMasterURL(url string) *TokenKubeConfigurator {
	next := &TokenKubeConfigurator{
		masterURL: url,
		fetcher:   c.fetcher,
		interval:  c.interval,
		token:     c.token,
		timeout:   c.timeout,
	}
	return next
}

func (c *TokenKubeConfigurator) WithFetcher(f TokenFetcher) *TokenKubeConfigurator {
	next := &TokenKubeConfigurator{
		masterURL: c.masterURL,
		fetcher:   f,
		interval:  c.interval,
		token:     c.token,
		timeout:   c.timeout,
	}

	return next
}

func (c *TokenKubeConfigurator) WithTimeout(t time.Duration) *TokenKubeConfigurator {
	next := &TokenKubeConfigurator{
		masterURL: c.masterURL,
		fetcher:   c.fetcher,
		interval:  c.interval,
		token:     c.token,
		timeout:   t,
	}
	return next
}

func (c *TokenKubeConfigurator) WithInterval(i time.Duration) *TokenKubeConfigurator {
	next := &TokenKubeConfigurator{
		masterURL: c.masterURL,
		fetcher:   c.fetcher,
		interval:  i,
		token:     c.token,
		timeout:   c.timeout,
	}
	return next
}

func (c *TokenKubeConfigurator) Start(ctx context.Context) <-chan *rest.Config {
	log := GetLogger(ctx)
	out := make(chan *rest.Config, 1)
	go func() {
		defer close(out)

		select {
		case <-ctx.Done():
			log.Infof("Context is done, token kube configurator events will not be triggered anymore")
			return
		default:
			c.once(ctx, out)
		}

		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()

	sel_loop:
		for {
			select {
			case <-ctx.Done():
				log.Infof("Context is done, token kube configurator events will not be triggered anymore")
				break sel_loop
			case <-ticker.C:
				c.once(ctx, out)
			}
		}
		if strings.HasSuffix(os.Args[0], ".test") {
			// avoid panic from test code
			// normally we should avoid such checks, but for
			// this special case of panic, should be ok.
			return
		}
		if pprof.Lookup("goroutine").WriteTo(os.Stdout, 1) != nil {
			log.Error("Failed to dump debug info")
		}
		log.Fatal("Closing out this kubeconfigurator, all clients will receive a close channel signal")
	}()
	return out
}

func (c *TokenKubeConfigurator) once(ctx context.Context, out chan<- *rest.Config) {
	c.Lock()
	defer c.Unlock()

	log := GetLogger(ctx)

	newToken, err := c.fetcher.FetchToken(ctx)
	if err != nil {
		log.Errorf("c.Fetcher.FetchToken failed, err: %s", err.Error())
		if strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "connect: permission denied") {
			log.Errorf("Quitting because refresh client does not recover from permission error")
			c.Unlock()
			// nolint gocritic
			os.Exit(3)
		}
		c.fetcher.RefreshClient()
		return
	}

	if newToken == c.token {
		log.Infof("token is not changed, no new kubeconfig sent")
		return
	}
	log.Infof("token changed, sending new kubeconfig")

	config := &rest.Config{
		Host:        c.masterURL,
		BearerToken: newToken,
		Timeout:     c.timeout,
	}

	select {
	case <-ctx.Done():
		return
	case out <- config:
		log.Infof("new kubeconfig sent, updating token in cache")
		c.token = newToken
		log.Infof("token in cache updated")
	}
}
