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

import "time"

type RecurrentEventSource struct {
	Kind     string
	Interval time.Duration
	Ticker   *time.Ticker
}

type RecurrentEventTicker struct {
	Sources []RecurrentEventSource
	C       Queue
}

func (t *RecurrentEventTicker) Stop() {
	for _, s := range t.Sources {
		if s.Ticker != nil {
			s.Ticker.Stop()
			s.Ticker = nil
		}
	}
}

// TODO: find better tickers such that some randomness could be added
// into the sleep interval.
func (t *RecurrentEventTicker) Start(stopCh <-chan struct{}) {
	t.Stop()
	for i := range t.Sources {
		go func(i int) {
			src := t.Sources[i]
			now := time.Now()
			t.C <- &Event{Kind: src.Kind, Object: &now}
			src.Ticker = time.NewTicker(src.Interval)
			for {
				ts, ok := <-src.Ticker.C
				if !ok {
					return
				}
				t.C <- &Event{Kind: src.Kind, Object: &ts}
			}
		}(i)
	}

	<-stopCh
	t.Stop()
}
