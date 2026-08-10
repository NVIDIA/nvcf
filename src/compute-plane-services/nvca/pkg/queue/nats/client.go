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

package nats

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
)

var (
	// DefaultNATSURL points at the in-cluster NATS headless service
	DefaultNATSURL = "nats://nats.nats-system.svc.cluster.local:4222"
)

const (
	CreateStreamName = "CreateNvcaFunctionTaskStream"
	TermStreamName   = "TerminateNvcaStream"
)

var (
	errReceiptHandleNotFound = errors.New("nats queue: receipt handle not found")
	errUnsupportedQueueType  = errors.New("nats queue: unsupported queue type")
)

type storedMessage struct {
	msg jetstream.Msg
}

// client implements queue.Client backed by NATS JetStream.
type client struct {
	clusterID string

	nc *nats.Conn
	js jetstream.JetStream

	consumersMu sync.Mutex
	consumers   map[string]jetstream.Consumer

	pending sync.Map // receiptHandle -> *storedMessage
}

// NewClient creates a JetStream-backed queue client.
func NewClient(ctx context.Context, clusterID string, secretsFetcher auth.NATSSecretsFetcher) (queue.Client, error) {
	return NewClientWithURL(ctx, "", clusterID, secretsFetcher)
}

// NewClientWithURL creates a JetStream-backed queue client for a configured NATS URL.
func NewClientWithURL(ctx context.Context, natsURL, clusterID string, secretsFetcher auth.NATSSecretsFetcher) (queue.Client, error) {
	return NewClientWithURLAndHostOverride(ctx, natsURL, "", clusterID, secretsFetcher)
}

// NewClientWithURLAndHostOverride creates a JetStream-backed queue client for a configured NATS URL and optional TLS SNI host.
func NewClientWithURLAndHostOverride(
	ctx context.Context,
	natsURL, natsHostOverride, clusterID string,
	secretsFetcher auth.NATSSecretsFetcher,
) (queue.Client, error) {
	secrets, err := secretsFetcher.FetchNATSSecrets(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch NATS secrets: %w", err)
	}

	if secrets.APIAuth.UserSeed == "" {
		return nil, errors.New("nats queue: missing nkey seed")
	}

	nkeyAuthOption, err := newNkeyAuthOption(secrets.APIAuth.UserSeed)
	if err != nil {
		return nil, fmt.Errorf("create nkey auth option: %w", err)
	}

	opts := []nats.Option{
		nkeyAuthOption,
		nats.Name(fmt.Sprintf("nvca-queue-client/%s", clusterID)),
	}
	opts = append(opts, natsHostOverrideOptions(natsURL, natsHostOverride)...)

	nc, err := nats.Connect(natsURLOrDefault(natsURL), opts...)
	if err != nil {
		return nil, fmt.Errorf("connect to NATS: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		_ = nc.Drain()
		return nil, fmt.Errorf("init jetstream: %w", err)
	}

	return &client{
		clusterID: clusterID,
		nc:        nc,
		js:        js,
		consumers: map[string]jetstream.Consumer{},
	}, nil
}

func natsURLOrDefault(natsURL string) string {
	if natsURL == "" {
		return DefaultNATSURL
	}
	return natsURL
}

func (c *client) ReceiveMessage(ctx context.Context, input queue.ReceiveMessageInput) ([]queue.ReceiveMessageOutput, error) {
	log := core.GetLogger(ctx).WithField("component", "queue/nats")

	// TODO(mcamp): investigatae specific consumer per GPU type
	// TODO(mcamp): we should be able to create a consumer up-front so we don't need to fetch it each time
	// we should make this a reactive check that when the consumer is gone, we recreate it
	consumer, err := c.ensureConsumer(ctx, input)
	if err != nil {
		return nil, err
	}

	maxMessages := int(input.MaxNumberOfMessages)
	if maxMessages <= 0 {
		maxMessages = 1
	}

	wait := time.Duration(input.WaitTimeSeconds) * time.Second
	if wait <= 0 {
		wait = 3 * time.Second
	}

	batch, err := consumer.Fetch(maxMessages, jetstream.FetchMaxWait(wait))
	if err != nil {
		if errors.Is(err, nats.ErrTimeout) {
			return nil, nil
		}
		return nil, fmt.Errorf("fetch jetstream messages: %w", err)
	}

	var outputs []queue.ReceiveMessageOutput
	for msg := range batch.Messages() {
		handle, messageID, err := c.registerMessage(msg)
		if err != nil {
			log.WithError(err).Warn("failed to register jetstream message")
			return nil, fmt.Errorf("register jetstream message: %w", err)
		}
		outputs = append(outputs, queue.ReceiveMessageOutput{
			MessageID:     messageID,
			ReceiptHandle: handle,
			Body:          msg.Data(),
		})
	}

	if err := batch.Error(); err != nil && !errors.Is(err, nats.ErrTimeout) {
		log.WithError(err).Warn("nats fetch batch reported error")
	}

	return outputs, nil
}

func (c *client) DeleteMessage(_ context.Context, input queue.DeleteMessageInput) error {
	entry, err := c.loadMessage(input.ReceiptHandle)
	if err != nil {
		return err
	}

	if err := entry.msg.Ack(); err != nil {
		return fmt.Errorf("ack jetstream message: %w", err)
	}
	c.pending.Delete(input.ReceiptHandle)
	return nil
}

func (c *client) ChangeMessageVisibility(ctx context.Context, input queue.ChangeMessageVisibilityInput) error {
	log := core.GetLogger(ctx).WithField("component", "queue/nats")

	entry, err := c.loadMessage(input.ReceiptHandle)
	if err != nil {
		return err
	}

	if input.VisibilityTimeoutSeconds == nil {
		log.WithField("receipt", input.ReceiptHandle).Trace("no visibility timeout provided; skipping")
		return nil
	}

	if *input.VisibilityTimeoutSeconds <= 0 {
		log.WithField("receipt", input.ReceiptHandle).Trace("nak message to make it visible immediately")
		if err := entry.msg.Nak(); err != nil {
			return fmt.Errorf("nak jetstream message: %w", err)
		}
		c.pending.Delete(input.ReceiptHandle)
		return nil
	}

	if err := entry.msg.InProgress(); err != nil {
		return fmt.Errorf("extend jetstream ack wait: %w", err)
	}
	log.WithFields(logrus.Fields{
		"receipt": input.ReceiptHandle,
		"secs":    *input.VisibilityTimeoutSeconds,
	}).Trace("extended jetstream ack deadline")
	return nil
}

func (c *client) IsMessageNotFoundError(err error) bool {
	return errors.Is(err, errReceiptHandleNotFound)
}

func (c *client) ensureConsumer(ctx context.Context, input queue.ReceiveMessageInput) (jetstream.Consumer, error) {
	streamName, subject, err := c.resolveStreamAndSubject(ctx, input.QueueInfo)
	if err != nil {
		return nil, err
	}

	durableName := fmt.Sprintf("%s-%s", streamName, c.clusterID)

	c.consumersMu.Lock()
	defer c.consumersMu.Unlock()

	if consumer, ok := c.consumers[durableName]; ok {
		return consumer, nil
	}

	cfg := jetstream.ConsumerConfig{
		Durable:        durableName,
		Description:    fmt.Sprintf("Consumer for %s NVCA cluster %s", subject, c.clusterID),
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: []string{subject},
	}

	if input.VisibilityTimeoutSeconds > 0 {
		cfg.AckWait = time.Duration(input.VisibilityTimeoutSeconds) * time.Second
	}

	consumer, err := c.js.CreateOrUpdateConsumer(ctx, streamName, cfg)
	if err != nil {
		return nil, fmt.Errorf("create or update jetstream consumer: %w", err)
	}

	c.consumers[durableName] = consumer
	return consumer, nil
}

// sanitizeMessageIDForK8s converts a MessageID to a K8s-label-compliant value.
// K8s labels must be ≤63 chars and contain only alphanumeric, '-', '_', or '.'.
// We use SHA256 hash and encode as hex (64 chars), then take first 63 chars.
func sanitizeMessageIDForK8s(msgID string) string {
	h := sha256.New()
	h.Write([]byte(msgID))
	hash := hex.EncodeToString(h.Sum(nil))
	// SHA256 hex is 64 chars, truncate to 63 for K8s label limit
	if len(hash) > 63 {
		return hash[:63]
	}
	return hash
}

func (c *client) registerMessage(msg jetstream.Msg) (string, string, error) {
	meta, err := msg.Metadata()
	if err != nil {
		return "", "", fmt.Errorf("get jetstream message metadata: %w", err)
	}
	handle := fmt.Sprintf("%s:%d", msg.Subject(), meta.Sequence.Stream)
	// Hash the messageID to make it K8s-label-compliant (≤63 chars, alphanumeric only)
	messageID := sanitizeMessageIDForK8s(handle)
	c.pending.Store(handle, &storedMessage{msg: msg})
	return handle, messageID, nil
}

func (c *client) loadMessage(handle string) (*storedMessage, error) {
	value, ok := c.pending.Load(handle)
	if !ok {
		return nil, errReceiptHandleNotFound
	}
	entry, ok := value.(*storedMessage)
	if !ok {
		return nil, errReceiptHandleNotFound
	}
	return entry, nil
}

func (c *client) resolveStreamAndSubject(ctx context.Context, info queue.MessageQueueInfo) (string, string, error) {
	log := core.GetLogger(ctx).WithField("component", "queue/nats")
	switch info.QueueURL {
	case CreateStreamName:
		// Stream catches Create.NVCA.> (both Function and Task subjects); wildcard
		// the third token so both kinds of creation messages are consumed.
		return CreateStreamName, fmt.Sprintf("Create.NVCA.*.%s.*.*", c.clusterID), nil
	case TermStreamName:
		return TermStreamName, fmt.Sprintf("Terminate.NVCA.%s", c.clusterID), nil
	default:
		log.WithField("queueURL", info.QueueURL).Error("unsupported queue URL")
		return "", "", errUnsupportedQueueType
	}
}
