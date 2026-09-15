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

package mockqueue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
)

var _ queue.Client = (*Client)(nil)

type QueueMessage struct {
	nextVisible *time.Time
	queue.ReceiveMessageOutput
}

type Client struct {
	// Speed up tests using 10's of milliseconds instead of seconds.
	Use10MillisForWaits bool

	// If set, ReceiveMessage returns FailReceiveErr for FailReceiveQueueURL.
	FailReceiveQueueURL string
	FailReceiveErr      error

	queues map[string][]QueueMessage
	mu     sync.RWMutex
}

func (c *Client) AddMessage(queueURL string, msg queue.ReceiveMessageOutput) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.queues == nil {
		c.queues = map[string][]QueueMessage{}
	}
	c.queues[queueURL] = append(c.queues[queueURL], QueueMessage{
		ReceiveMessageOutput: msg,
	})
}

func (c *Client) PeekMessage(ctx context.Context, input queue.ReceiveMessageInput) ([]queue.ReceiveMessageOutput, error) {
	qi := input.QueueInfo

	c.mu.Lock()
	if c.queues == nil {
		c.queues = map[string][]QueueMessage{}
	}
	c.mu.Unlock()

	getMessage := func() []queue.ReceiveMessageOutput {
		c.mu.Lock()
		defer c.mu.Unlock()

		for _, message := range c.queues[qi.QueueURL] {
			// TODO: respect max messages.
			// This is ok for now since max is always 1.
			return []queue.ReceiveMessageOutput{message.ReceiveMessageOutput}
		}
		return nil
	}

	if input.WaitTimeSeconds == 0 {
		return getMessage(), nil
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	var waitTime time.Duration
	if c.Use10MillisForWaits {
		waitTime = time.Duration(input.WaitTimeSeconds) * 10 * time.Millisecond
	} else {
		waitTime = time.Duration(input.WaitTimeSeconds) * time.Second
	}
	timer := time.NewTimer(waitTime)

	for {
		select {
		case <-ctx.Done():
			goto done
		case <-timer.C:
			goto done
		case <-ticker.C:
			if messages := getMessage(); messages != nil {
				return messages, nil
			}
		}
	}
done:

	return nil, nil
}

func (c *Client) ReceiveMessage(ctx context.Context, input queue.ReceiveMessageInput) ([]queue.ReceiveMessageOutput, error) {
	qi := input.QueueInfo

	if c.FailReceiveErr != nil && qi.QueueURL == c.FailReceiveQueueURL {
		return nil, c.FailReceiveErr
	}

	c.mu.Lock()
	if c.queues == nil {
		c.queues = map[string][]QueueMessage{}
	}
	c.mu.Unlock()

	getMessage := func() []queue.ReceiveMessageOutput {
		c.mu.Lock()
		defer c.mu.Unlock()

		for i, message := range c.queues[qi.QueueURL] {
			if message.nextVisible != nil {
				if !message.nextVisible.Before(time.Now()) {
					continue
				}
			} else if input.VisibilityTimeoutSeconds != 0 {
				t := time.Now().Add(time.Duration(input.VisibilityTimeoutSeconds) * time.Second)
				c.queues[qi.QueueURL][i].nextVisible = &t
			}
			// TODO: respect max messages.
			// This is ok for now since max is always 1.
			return []queue.ReceiveMessageOutput{message.ReceiveMessageOutput}
		}
		return nil
	}

	if input.WaitTimeSeconds == 0 {
		return getMessage(), nil
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	var waitTime time.Duration
	if c.Use10MillisForWaits {
		waitTime = time.Duration(input.WaitTimeSeconds) * 10 * time.Millisecond
	} else {
		waitTime = time.Duration(input.WaitTimeSeconds) * time.Second
	}
	timer := time.NewTimer(waitTime)

	for {
		select {
		case <-ctx.Done():
			goto done
		case <-timer.C:
			goto done
		case <-ticker.C:
			if messages := getMessage(); messages != nil {
				return messages, nil
			}
		}
	}
done:

	return nil, nil
}

func (c *Client) DeleteMessage(_ context.Context, input queue.DeleteMessageInput) error {
	qi := input.QueueInfo

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.queues == nil {
		c.queues = map[string][]QueueMessage{}
	}

	messages := c.queues[qi.QueueURL]

	for i := 0; i < len(messages); i++ {
		if messages[i].ReceiptHandle == input.ReceiptHandle {
			messages = append(messages[:i], messages[i+1:]...)
			c.queues[qi.QueueURL] = messages
			return nil
		}
	}

	return messageNotFoundError{queueURL: qi.QueueURL, rhdl: input.ReceiptHandle}
}

func (c *Client) ChangeMessageVisibility(_ context.Context, input queue.ChangeMessageVisibilityInput) error {
	qi := input.QueueInfo

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.queues == nil {
		c.queues = map[string][]QueueMessage{}
	}

	messages := c.queues[qi.QueueURL]

	for i, message := range messages {
		if message.ReceiptHandle == input.ReceiptHandle {
			if input.VisibilityTimeoutSeconds != nil {
				t := time.Now().Add(time.Duration(*input.VisibilityTimeoutSeconds) * time.Second)
				messages[i].nextVisible = &t
			} else {
				messages[i].nextVisible = nil
			}
			return nil
		}
	}

	return messageNotFoundError{queueURL: qi.QueueURL, rhdl: input.ReceiptHandle}
}

type messageNotFoundError struct {
	queueURL string
	rhdl     string
}

func (e messageNotFoundError) Error() string {
	return fmt.Sprintf("no message in queue %s with receipt handle %s", e.queueURL, e.rhdl)
}

func (c *Client) IsMessageNotFoundError(err error) bool {
	e := messageNotFoundError{}
	return errors.As(err, &e)
}
