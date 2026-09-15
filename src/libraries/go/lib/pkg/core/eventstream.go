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
	"sync"
	"time"
)

type TickerStream struct {
	kind               string
	interval           time.Duration
	immediate          bool
	randomOffset       bool
	bufferSize         int
	zeroEventTimestamp bool
}

func NewTickerStream() *TickerStream {
	return &TickerStream{immediate: true}
}

func (s *TickerStream) WithKind(kind string) *TickerStream {
	next := *s
	next.kind = kind
	return &next
}

func (s *TickerStream) WithInterval(interval time.Duration) *TickerStream {
	next := *s
	next.interval = interval
	return &next
}

func (s *TickerStream) WithBufferSize(bufferSize int) *TickerStream {
	next := *s
	next.bufferSize = bufferSize
	return &next
}

func (s *TickerStream) WithImmediate(immediate bool) *TickerStream {
	next := *s
	next.immediate = immediate
	return &next
}

func (s *TickerStream) WithRandomOffset(randomOffset bool) *TickerStream {
	next := *s
	next.randomOffset = randomOffset
	return &next
}

func (s *TickerStream) WithZeroEventTimestamp(zeroEventTimestamp bool) *TickerStream {
	next := *s
	next.zeroEventTimestamp = zeroEventTimestamp
	return &next
}

func (s *TickerStream) Start(ctx context.Context) <-chan *Event {
	log := GetLogger(ctx)

	interval := s.interval
	kind := s.kind
	immediate := s.immediate
	offset := 0 * time.Second
	if s.randomOffset {
		offset = time.Duration(GetRandFloat64(ctx) * float64(interval))
	}

	out := make(chan *Event, s.bufferSize)
	go func() {
		defer func() {
			log.Infof("Stopping ticker events stream: %+v", s)
			close(out)
			log.Infof("Stopped ticker events stream: %+v", s)
		}()
		log.Infof("Starting ticker events stream with offset: %v, immediate: %v, %+v", offset, immediate, s)

		if immediate {
			now := time.Now()
			if s.zeroEventTimestamp {
				now = time.Time{}
			}
			select {
			case <-ctx.Done():
				return
			case out <- &Event{Kind: kind, Object: &now}:
			}
		}

		first := time.NewTimer(offset)
		defer first.Stop()
		select {
		case <-ctx.Done():
			return
		case ts := <-first.C:
			if s.zeroEventTimestamp {
				ts = time.Time{}
			}
			select {
			case <-ctx.Done():
				return
			case out <- &Event{Kind: kind, Object: &ts}:
			}
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case ts := <-ticker.C:
				if s.zeroEventTimestamp {
					ts = time.Time{}
				}
				select {
				case <-ctx.Done():
					return
				case out <- &Event{Kind: kind, Object: &ts}:
				}
			}
		}
	}()
	return out
}

type EventStreamMerger struct {
	bufferSize int
}

func NewEventStreamMerger() *EventStreamMerger { return &EventStreamMerger{} }

func (s *EventStreamMerger) WithBufferSize(bufferSize int) *EventStreamMerger {
	next := *s
	next.bufferSize = bufferSize
	return &next
}

func (s *EventStreamMerger) Merge(ctx context.Context, eventChs ...<-chan *Event) <-chan *Event {
	out := make(chan *Event, s.bufferSize)

	var wg sync.WaitGroup
	wg.Add(len(eventChs))

	each := func(ch <-chan *Event) {
		defer wg.Done()

		for event := range ch {
			select {
			case out <- event:
			case <-ctx.Done():
				return
			}
		}
	}

	for _, ch := range eventChs {
		go each(ch)
	}

	go func() {
		wg.Wait()
		close(out)
	}()

	return out
}
