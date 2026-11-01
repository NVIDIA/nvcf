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

package mockqueueservice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
)

type client struct{}

func NewClient() queue.Client {
	return &client{}
}

func (c *client) do(_ context.Context, req *http.Request, secKey string) (int, []byte, error) {
	req.Header.Set("Authorization", "Bearer "+secKey)
	req.Header.Set("Accept", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()

	b, err := io.ReadAll(res.Body)
	return res.StatusCode, b, err
}

type receiveMessageResponse struct {
	// Either creation or termination.
	Messages []receiveMessageResponseItem `json:"messages"`
}

type CreateMessageResponse struct {
	MessageID     string `json:"msg_id"`
	ReceiptHandle string `json:"receipt_handle"`
}

type receiveMessageResponseItem struct {
	MessageID     string `json:"message_id"`
	ReceiptHandle string `json:"receipt_handle"`
	Body          any    `json:"body"`
}

func (c *client) ReceiveMessage(ctx context.Context, input queue.ReceiveMessageInput) ([]queue.ReceiveMessageOutput, error) {
	log := core.GetLogger(ctx)

	qi := input.QueueInfo
	url := qi.QueueURL

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	query := req.URL.Query()
	query.Set("max_num_messages", fmt.Sprint(input.MaxNumberOfMessages))
	query.Set("wait_time_seconds", fmt.Sprint(input.WaitTimeSeconds))
	query.Set("vis_timeout_seconds", fmt.Sprint(input.VisibilityTimeoutSeconds))
	req.URL.RawQuery = query.Encode()

	code, resBody, err := c.do(ctx, req, qi.SecretKey)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("mock receive messages for queue %s: %v", qi.QueueURL, code)
	}

	var res receiveMessageResponse
	if err := json.Unmarshal(resBody, &res); err != nil {
		return nil, err
	}

	if len(res.Messages) == 0 {
		log.Debugf("no messages available for %s", qi.QueueURL)
		return nil, nil
	}

	out := make([]queue.ReceiveMessageOutput, len(res.Messages))
	for i, m := range res.Messages {
		bodyBytes, err := json.Marshal(m.Body)
		if err != nil {
			return nil, err
		}

		out[i] = queue.ReceiveMessageOutput{
			MessageID:     m.MessageID,
			ReceiptHandle: m.ReceiptHandle,
			Body:          bodyBytes,
		}
	}

	return out, nil
}

func (c *client) DeleteMessage(ctx context.Context, input queue.DeleteMessageInput) error {
	qi := input.QueueInfo

	url := qi.QueueURL + "/" + input.ReceiptHandle
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}

	code, _, err := c.do(ctx, req, qi.SecretKey)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("mock delete message handle %s for queue %s: %v", input.ReceiptHandle, qi.QueueURL, code)
	}
	return nil
}

func (c *client) ChangeMessageVisibility(ctx context.Context, input queue.ChangeMessageVisibilityInput) error {
	qi := input.QueueInfo

	url := qi.QueueURL + "/" + input.ReceiptHandle
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, nil)
	if err != nil {
		return err
	}
	query := req.URL.Query()
	query.Set("vis_timeout_seconds", fmt.Sprint(input.VisibilityTimeoutSeconds))
	req.URL.RawQuery = query.Encode()

	code, _, err := c.do(ctx, req, qi.SecretKey)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("mock update message handle %s vis timeout for queue %s: %v", input.ReceiptHandle, qi.QueueURL, code)
	}
	return nil
}

func (c *client) IsMessageNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.HasSuffix(err.Error(), "404")
}
