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

package queuesqs

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sqs"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
)

const (
	defaultAWSRegion = "us-east-1"
)

type client struct {
	queueEndpoint string
	profile       string
	sessions      sync.Map
}

func NewClient(endpoint, profile string) queue.Client {
	return &client{
		queueEndpoint: endpoint,
		profile:       profile,
	}
}

func getAWSRegion(ctx context.Context, queueURL string) string {
	log := core.GetLogger(ctx)
	p := endpoints.AwsPartition()
	for id := range p.Regions() {
		if strings.Contains(queueURL, id) {
			// return the AWS Region key
			return id
		}
	}
	log.Warnf("mising AWS region in URL (%v), returning %v", queueURL, defaultAWSRegion)
	return defaultAWSRegion
}

func (c *client) getSession(ctx context.Context, qi queue.MessageQueueInfo) (*session.Session, error) {
	if sessVal, exists := c.sessions.Load(qi); exists {
		return sessVal.(*session.Session), nil
	}

	sess, err := c.newSession(ctx, qi)
	if err != nil {
		return nil, fmt.Errorf("create session for %s: %v", qi.QueueType, err)
	}
	c.sessions.Store(qi, sess)

	return sess, nil
}

func (c *client) newSession(ctx context.Context, qi queue.MessageQueueInfo) (*session.Session, error) {
	creds := credentials.NewStaticCredentials(qi.AccessKey, qi.SecretKey, qi.SessionToken)
	return session.NewSessionWithOptions(
		session.Options{
			Config: aws.Config{
				Credentials: creds,
				Endpoint:    aws.String(c.queueEndpoint),
				Region:      aws.String(getAWSRegion(ctx, qi.QueueURL)),
			},
			Profile: c.profile,
		},
	)
}

func (c *client) ReceiveMessage(ctx context.Context, input queue.ReceiveMessageInput) ([]queue.ReceiveMessageOutput, error) {
	log := core.GetLogger(ctx)

	qi := input.QueueInfo

	sess, err := c.getSession(ctx, qi)
	if err != nil {
		return nil, err
	}

	sqsInput := &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(qi.QueueURL),
	}
	if input.MaxNumberOfMessages != 0 {
		sqsInput.MaxNumberOfMessages = aws.Int64(input.MaxNumberOfMessages)
	}
	if input.WaitTimeSeconds != 0 {
		sqsInput.WaitTimeSeconds = aws.Int64(input.WaitTimeSeconds)
	}
	if input.VisibilityTimeoutSeconds != 0 {
		sqsInput.VisibilityTimeout = aws.Int64(input.VisibilityTimeoutSeconds)
	}
	mo, err := sqs.New(sess).ReceiveMessageWithContext(ctx, sqsInput)
	if err != nil {
		log.WithError(err).Error("Receive SQS messages")
		return nil, fmt.Errorf("receive messages from queue %s: %v", qi.QueueType, err)
	}

	if len(mo.Messages) == 0 {
		log.Debugf("no messages available for %s", qi.QueueType)
		return nil, nil
	}

	out := make([]queue.ReceiveMessageOutput, len(mo.Messages))
	for i, m := range mo.Messages {
		out[i] = queue.ReceiveMessageOutput{
			MessageID:     *m.MessageId,
			ReceiptHandle: *m.ReceiptHandle,
			Body:          []byte(*m.Body),
		}
	}

	return out, nil
}

func (c *client) DeleteMessage(ctx context.Context, input queue.DeleteMessageInput) error {
	log := core.GetLogger(ctx)

	qi := input.QueueInfo

	sess, err := c.getSession(ctx, qi)
	if err != nil {
		return err
	}

	if _, err := sqs.New(sess).DeleteMessageWithContext(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(qi.QueueURL),
		ReceiptHandle: aws.String(input.ReceiptHandle),
	}); err != nil {
		log.WithError(err).Error("Delete SQS message")
		return fmt.Errorf("delete SQS message from %s: %v", qi.QueueType, err)
	}

	return nil
}

func (c *client) ChangeMessageVisibility(ctx context.Context, input queue.ChangeMessageVisibilityInput) error {
	log := core.GetLogger(ctx)

	qi := input.QueueInfo

	sess, err := c.getSession(ctx, qi)
	if err != nil {
		return err
	}

	if _, err := sqs.New(sess).ChangeMessageVisibilityWithContext(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(qi.QueueURL),
		ReceiptHandle:     aws.String(input.ReceiptHandle),
		VisibilityTimeout: input.VisibilityTimeoutSeconds,
	}); err != nil {
		log.WithError(err).Error("Change SQS message visibility")
		return fmt.Errorf("change message visibility in %s: %v", qi.QueueType, err)
	}

	return nil
}

// IsMessageNotFoundError checks if err indicates the receipt handle is invalid.
// See https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ChangeMessageVisibility.html
func (*client) IsMessageNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	// The SQS Go lib doesn't have error unwrapping implemented.
	_, rhIsInvalid := err.(*sqs.ReceiptHandleIsInvalid)
	if rhIsInvalid {
		return rhIsInvalid
	}
	es := err.Error()
	return strings.Contains(es, sqs.ErrCodeReceiptHandleIsInvalid)
}
