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
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
)

// creationQueueInput is the receive input used by the creation queue.
func creationQueueInput() queue.ReceiveMessageInput {
	return queue.ReceiveMessageInput{
		QueueInfo: queue.MessageQueueInfo{
			QueueType: queue.CreationQueue,
			QueueURL:  CreateStreamName,
			GPU:       "H100",
		},
		MaxNumberOfMessages:      1,
		WaitTimeSeconds:          1,
		VisibilityTimeoutSeconds: 5,
	}
}

// TestReceiveMessageSurfacesBrokenConsumer covers both halves of nvcf#1590 at
// the queue-client boundary.
//
// A consumer that disappears server-side used to be invisible to callers: the
// batch error was logged at warn level and ReceiveMessage returned (nil, nil),
// so the queue manager counted a failed poll as a successful empty one and the
// backend kept reporting healthy. The cached consumer handle was also never
// evicted, so the failure persisted until the process restarted, which is the
// NVCA restart the issue describes.
func TestReceiveMessageSurfacesBrokenConsumer(t *testing.T) {
	seed, pubKey := newUserSeed(t)
	srv := runJetStreamServer(t, pubKey)
	t.Cleanup(srv.Shutdown)

	origURL := DefaultNATSURL
	DefaultNATSURL = srv.ClientURL()
	t.Cleanup(func() { DefaultNATSURL = origURL })

	qc, err := NewClient(context.Background(), "cluster", staticSecretsFetcher{
		secrets: auth.NATSSecrets{APIAuth: auth.NATSAPIAuthSecrets{UserSeed: string(seed)}},
	})
	require.NoError(t, err)
	cl := qc.(*client)
	t.Cleanup(func() { _ = cl.nc.Drain() })

	nc := connectWithSeed(t, srv.ClientURL(), seed)
	t.Cleanup(func() { _ = nc.Drain() })
	js, err := nc.JetStream()
	require.NoError(t, err)

	subject := "Create.NVCA.Function.cluster.H100.test"
	streamCfg := &nats.StreamConfig{Name: CreateStreamName, Subjects: []string{subject}}
	_, err = js.AddStream(streamCfg)
	require.NoError(t, err)

	ctx := context.Background()

	// A healthy poll on an empty queue: no messages and no error. This is the
	// case a broken consumer used to be indistinguishable from.
	msgs, err := qc.ReceiveMessage(ctx, creationQueueInput())
	require.NoError(t, err)
	require.Empty(t, msgs)
	require.Len(t, cl.consumers, 1, "consumer should now be cached")

	// Break it the way the reproduction does: remove the stream, which takes its
	// consumers with it, while the client holds a cached handle.
	require.NoError(t, js.DeleteStream(CreateStreamName))

	_, err = qc.ReceiveMessage(ctx, creationQueueInput())
	require.Error(t, err,
		"a poll against a consumer that no longer exists must not look like an empty queue")
	require.Empty(t, cl.consumers,
		"the dead consumer must be evicted so the next poll rebuilds it")

	// Recovery has to happen on its own, without restarting the process: the
	// issue requires that work is processed without an NVCA restart.
	_, err = js.AddStream(streamCfg)
	require.NoError(t, err)
	_, err = js.Publish(subject, []byte("payload"))
	require.NoError(t, err)

	msgs, err = qc.ReceiveMessage(ctx, creationQueueInput())
	require.NoError(t, err, "client must recover once the stream is back")
	require.Len(t, msgs, 1, "and must deliver the queued message without a restart")
}

// TestReceiveMessageKeepsPartialBatchResults guards the one case where the
// batch error is deliberately not returned: messages that already came off the
// stream are handed back rather than dropped, and the condition surfaces on the
// next poll if it persists.
func TestReceiveMessageKeepsPartialBatchResults(t *testing.T) {
	seed, pubKey := newUserSeed(t)
	srv := runJetStreamServer(t, pubKey)
	t.Cleanup(srv.Shutdown)

	origURL := DefaultNATSURL
	DefaultNATSURL = srv.ClientURL()
	t.Cleanup(func() { DefaultNATSURL = origURL })

	qc, err := NewClient(context.Background(), "cluster", staticSecretsFetcher{
		secrets: auth.NATSSecrets{APIAuth: auth.NATSAPIAuthSecrets{UserSeed: string(seed)}},
	})
	require.NoError(t, err)
	cl := qc.(*client)
	t.Cleanup(func() { _ = cl.nc.Drain() })

	nc := connectWithSeed(t, srv.ClientURL(), seed)
	t.Cleanup(func() { _ = nc.Drain() })
	js, err := nc.JetStream()
	require.NoError(t, err)

	subject := "Create.NVCA.Function.cluster.H100.test"
	_, err = js.AddStream(&nats.StreamConfig{Name: CreateStreamName, Subjects: []string{subject}})
	require.NoError(t, err)
	_, err = js.Publish(subject, []byte("payload"))
	require.NoError(t, err)

	msgs, err := qc.ReceiveMessage(context.Background(), creationQueueInput())
	require.NoError(t, err)
	require.Len(t, msgs, 1, "a successful fetch must still return its message")
}
