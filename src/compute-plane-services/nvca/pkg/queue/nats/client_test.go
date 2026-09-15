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
	"time"

	"github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
)

type staticSecretsFetcher struct {
	secrets auth.NATSSecrets
}

func (f staticSecretsFetcher) FetchNATSSecrets(_ context.Context) (auth.NATSSecrets, error) {
	return f.secrets, nil
}

func TestNewClientRequiresSeed(t *testing.T) {
	fetcher := staticSecretsFetcher{secrets: auth.NATSSecrets{}}
	_, err := NewClient(context.Background(), "cluster", fetcher)
	require.Error(t, err)
}

func TestNATSURLOrDefault(t *testing.T) {
	require.Equal(t, DefaultNATSURL, natsURLOrDefault(""))
	require.Equal(t, "nats://nats.example.com:14222", natsURLOrDefault("nats://nats.example.com:14222"))
}

func TestNATSClientReceiveAndAck(t *testing.T) {
	seed, pubKey := newUserSeed(t)
	srv := runJetStreamServer(t, pubKey)
	t.Cleanup(srv.Shutdown)

	origURL := DefaultNATSURL
	DefaultNATSURL = srv.ClientURL()
	t.Cleanup(func() { DefaultNATSURL = origURL })

	fetcher := staticSecretsFetcher{secrets: auth.NATSSecrets{
		APIAuth: auth.NATSAPIAuthSecrets{UserSeed: string(seed)},
	}}

	qc, err := NewClient(context.Background(), "cluster", fetcher)
	require.NoError(t, err)
	cl := qc.(*client)
	t.Cleanup(func() { _ = cl.nc.Drain() })

	nc := connectWithSeed(t, srv.ClientURL(), seed)
	t.Cleanup(func() { _ = nc.Drain() })

	js, err := nc.JetStream()
	require.NoError(t, err)

	subject := "Create.NVCA.Function.cluster.H100.test"
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     CreateStreamName,
		Subjects: []string{subject},
	})
	require.NoError(t, err)

	_, err = js.Publish(subject, []byte("payload"))
	require.NoError(t, err)

	msgs, err := qc.ReceiveMessage(context.Background(), queue.ReceiveMessageInput{
		QueueInfo: queue.MessageQueueInfo{
			QueueType: queue.CreationQueue,
			QueueURL:  CreateStreamName,
			GPU:       "H100",
		},
		MaxNumberOfMessages:      1,
		WaitTimeSeconds:          1,
		VisibilityTimeoutSeconds: 5,
	})
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	// Extend visibility to exercise InProgress.
	vis := int64(10)
	require.NoError(t, qc.ChangeMessageVisibility(context.Background(), queue.ChangeMessageVisibilityInput{
		QueueInfo:                queue.MessageQueueInfo{QueueType: queue.CreationQueue, QueueURL: CreateStreamName},
		ReceiptHandle:            msgs[0].ReceiptHandle,
		VisibilityTimeoutSeconds: &vis,
	}))

	// Ack the message.
	require.NoError(t, qc.DeleteMessage(context.Background(), queue.DeleteMessageInput{
		QueueInfo:     queue.MessageQueueInfo{QueueType: queue.CreationQueue, QueueURL: CreateStreamName},
		ReceiptHandle: msgs[0].ReceiptHandle,
	}))

	err = qc.DeleteMessage(context.Background(), queue.DeleteMessageInput{
		QueueInfo:     queue.MessageQueueInfo{QueueType: queue.CreationQueue, QueueURL: CreateStreamName},
		ReceiptHandle: msgs[0].ReceiptHandle,
	})
	require.Error(t, err)
	require.True(t, qc.IsMessageNotFoundError(err))
}

func TestNATSClientNakRedelivers(t *testing.T) {
	seed, pubKey := newUserSeed(t)
	srv := runJetStreamServer(t, pubKey)
	t.Cleanup(srv.Shutdown)

	origURL := DefaultNATSURL
	DefaultNATSURL = srv.ClientURL()
	t.Cleanup(func() { DefaultNATSURL = origURL })

	fetcher := staticSecretsFetcher{secrets: auth.NATSSecrets{
		APIAuth: auth.NATSAPIAuthSecrets{UserSeed: string(seed)},
	}}

	qc, err := NewClient(context.Background(), "cluster", fetcher)
	require.NoError(t, err)
	cl := qc.(*client)
	t.Cleanup(func() { _ = cl.nc.Drain() })

	nc := connectWithSeed(t, srv.ClientURL(), seed)
	t.Cleanup(func() { _ = nc.Drain() })

	js, err := nc.JetStream()
	require.NoError(t, err)

	subject := "Create.NVCA.Function.cluster.H100.retry"
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     CreateStreamName,
		Subjects: []string{subject},
	})
	require.NoError(t, err)

	_, err = js.Publish(subject, []byte("payload"))
	require.NoError(t, err)

	msgs, err := qc.ReceiveMessage(context.Background(), queue.ReceiveMessageInput{
		QueueInfo: queue.MessageQueueInfo{
			QueueType: queue.CreationQueue,
			QueueURL:  CreateStreamName,
			GPU:       "H100",
		},
		MaxNumberOfMessages:      1,
		WaitTimeSeconds:          1,
		VisibilityTimeoutSeconds: 5,
	})
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	zero := int64(0)
	require.NoError(t, qc.ChangeMessageVisibility(context.Background(), queue.ChangeMessageVisibilityInput{
		QueueInfo:                queue.MessageQueueInfo{QueueType: queue.CreationQueue, QueueURL: CreateStreamName},
		ReceiptHandle:            msgs[0].ReceiptHandle,
		VisibilityTimeoutSeconds: &zero,
	}))

	// Give the server a moment to process the NAK.
	time.Sleep(50 * time.Millisecond)

	msgsAfter, err := qc.ReceiveMessage(context.Background(), queue.ReceiveMessageInput{
		QueueInfo: queue.MessageQueueInfo{
			QueueType: queue.CreationQueue,
			QueueURL:  CreateStreamName,
			GPU:       "H100",
		},
		MaxNumberOfMessages:      1,
		WaitTimeSeconds:          1,
		VisibilityTimeoutSeconds: 5,
	})
	require.NoError(t, err)
	require.Len(t, msgsAfter, 1)
	// Receipt handle will be the same since it's based on stream sequence number
	require.Equal(t, msgs[0].ReceiptHandle, msgsAfter[0].ReceiptHandle)
	require.Equal(t, msgs[0].Body, msgsAfter[0].Body)

	// Ack to clean up.
	require.NoError(t, qc.DeleteMessage(context.Background(), queue.DeleteMessageInput{
		QueueInfo:     queue.MessageQueueInfo{QueueType: queue.CreationQueue, QueueURL: CreateStreamName},
		ReceiptHandle: msgsAfter[0].ReceiptHandle,
	}))
}

func newUserSeed(t *testing.T) ([]byte, string) {
	t.Helper()
	kp, err := nkeys.CreateUser()
	require.NoError(t, err)

	seed, err := kp.Seed()
	require.NoError(t, err)
	pubKey, err := kp.PublicKey()
	require.NoError(t, err)

	return seed, pubKey
}

func runJetStreamServer(t *testing.T, pubKey string) *server.Server {
	t.Helper()
	opts := &server.Options{
		Port:      -1,
		JetStream: true,
		Host:      "127.0.0.1",
		StoreDir:  t.TempDir(),
		Nkeys: []*server.NkeyUser{
			{Nkey: pubKey},
		},
	}
	srv := natstest.RunServer(opts)
	if !srv.ReadyForConnections(10 * time.Second) {
		srv.Shutdown()
		t.Fatalf("NATS server not ready")
	}
	return srv
}

func connectWithSeed(t *testing.T, url string, seed []byte) *nats.Conn {
	t.Helper()
	kp, err := nkeys.FromSeed(seed)
	require.NoError(t, err)

	pubKey, err := kp.PublicKey()
	require.NoError(t, err)

	nc, err := nats.Connect(url, nats.Nkey(pubKey, func(nonce []byte) ([]byte, error) {
		return kp.Sign(nonce)
	}))
	require.NoError(t, err)
	return nc
}

func TestSanitizeMessageIDForK8s(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantLen  int
		wantHash bool
	}{
		{
			name:     "short input",
			input:    "test",
			wantLen:  63,
			wantHash: true,
		},
		{
			name:     "long input",
			input:    "Create.NVCA.Function.cluster.H100.test:12345678901234567890",
			wantLen:  63,
			wantHash: true,
		},
		{
			name:     "empty input",
			input:    "",
			wantLen:  63,
			wantHash: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeMessageIDForK8s(tt.input)
			require.Equal(t, tt.wantLen, len(result))
			// Verify it's a valid hex string
			require.Regexp(t, "^[a-f0-9]+$", result)
			// Verify different inputs produce different hashes
			if tt.input != "" {
				different := sanitizeMessageIDForK8s(tt.input + "x")
				require.NotEqual(t, result, different)
			}
		})
	}
}

func TestTerminateStreamReceive(t *testing.T) {
	seed, pubKey := newUserSeed(t)
	srv := runJetStreamServer(t, pubKey)
	t.Cleanup(srv.Shutdown)

	origURL := DefaultNATSURL
	DefaultNATSURL = srv.ClientURL()
	t.Cleanup(func() { DefaultNATSURL = origURL })

	fetcher := staticSecretsFetcher{secrets: auth.NATSSecrets{
		APIAuth: auth.NATSAPIAuthSecrets{UserSeed: string(seed)},
	}}

	qc, err := NewClient(context.Background(), "cluster", fetcher)
	require.NoError(t, err)
	cl := qc.(*client)
	t.Cleanup(func() { _ = cl.nc.Drain() })

	nc := connectWithSeed(t, srv.ClientURL(), seed)
	t.Cleanup(func() { _ = nc.Drain() })

	js, err := nc.JetStream()
	require.NoError(t, err)

	subject := "Terminate.NVCA.cluster"
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     TermStreamName,
		Subjects: []string{subject},
	})
	require.NoError(t, err)

	_, err = js.Publish(subject, []byte("terminate-payload"))
	require.NoError(t, err)

	msgs, err := qc.ReceiveMessage(context.Background(), queue.ReceiveMessageInput{
		QueueInfo: queue.MessageQueueInfo{
			QueueType: queue.TerminationQueue,
			QueueURL:  TermStreamName,
		},
		MaxNumberOfMessages:      1,
		WaitTimeSeconds:          1,
		VisibilityTimeoutSeconds: 5,
	})
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, []byte("terminate-payload"), msgs[0].Body)

	// Clean up
	require.NoError(t, qc.DeleteMessage(context.Background(), queue.DeleteMessageInput{
		QueueInfo:     queue.MessageQueueInfo{QueueType: queue.TerminationQueue, QueueURL: TermStreamName},
		ReceiptHandle: msgs[0].ReceiptHandle,
	}))
}

func TestUnsupportedQueueType(t *testing.T) {
	seed, pubKey := newUserSeed(t)
	srv := runJetStreamServer(t, pubKey)
	t.Cleanup(srv.Shutdown)

	origURL := DefaultNATSURL
	DefaultNATSURL = srv.ClientURL()
	t.Cleanup(func() { DefaultNATSURL = origURL })

	fetcher := staticSecretsFetcher{secrets: auth.NATSSecrets{
		APIAuth: auth.NATSAPIAuthSecrets{UserSeed: string(seed)},
	}}

	qc, err := NewClient(context.Background(), "cluster", fetcher)
	require.NoError(t, err)
	cl := qc.(*client)
	t.Cleanup(func() { _ = cl.nc.Drain() })

	_, err = qc.ReceiveMessage(context.Background(), queue.ReceiveMessageInput{
		QueueInfo: queue.MessageQueueInfo{
			QueueType: queue.CreationQueue,
			QueueURL:  "InvalidQueueName",
		},
		MaxNumberOfMessages:      1,
		WaitTimeSeconds:          1,
		VisibilityTimeoutSeconds: 5,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, errUnsupportedQueueType)
}

func TestChangeMessageVisibilityNilTimeout(t *testing.T) {
	seed, pubKey := newUserSeed(t)
	srv := runJetStreamServer(t, pubKey)
	t.Cleanup(srv.Shutdown)

	origURL := DefaultNATSURL
	DefaultNATSURL = srv.ClientURL()
	t.Cleanup(func() { DefaultNATSURL = origURL })

	fetcher := staticSecretsFetcher{secrets: auth.NATSSecrets{
		APIAuth: auth.NATSAPIAuthSecrets{UserSeed: string(seed)},
	}}

	qc, err := NewClient(context.Background(), "cluster", fetcher)
	require.NoError(t, err)
	cl := qc.(*client)
	t.Cleanup(func() { _ = cl.nc.Drain() })

	nc := connectWithSeed(t, srv.ClientURL(), seed)
	t.Cleanup(func() { _ = nc.Drain() })

	js, err := nc.JetStream()
	require.NoError(t, err)

	subject := "Create.NVCA.Function.cluster.H100.nil"
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     CreateStreamName,
		Subjects: []string{subject},
	})
	require.NoError(t, err)

	_, err = js.Publish(subject, []byte("payload"))
	require.NoError(t, err)

	msgs, err := qc.ReceiveMessage(context.Background(), queue.ReceiveMessageInput{
		QueueInfo: queue.MessageQueueInfo{
			QueueType: queue.CreationQueue,
			QueueURL:  CreateStreamName,
			GPU:       "H100",
		},
		MaxNumberOfMessages:      1,
		WaitTimeSeconds:          1,
		VisibilityTimeoutSeconds: 5,
	})
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	// Test nil visibility timeout - should be a no-op
	err = qc.ChangeMessageVisibility(context.Background(), queue.ChangeMessageVisibilityInput{
		QueueInfo:                queue.MessageQueueInfo{QueueType: queue.CreationQueue, QueueURL: CreateStreamName},
		ReceiptHandle:            msgs[0].ReceiptHandle,
		VisibilityTimeoutSeconds: nil,
	})
	require.NoError(t, err)

	// Message should still be in pending state and can be acked
	require.NoError(t, qc.DeleteMessage(context.Background(), queue.DeleteMessageInput{
		QueueInfo:     queue.MessageQueueInfo{QueueType: queue.CreationQueue, QueueURL: CreateStreamName},
		ReceiptHandle: msgs[0].ReceiptHandle,
	}))
}

func TestConsumerCaching(t *testing.T) {
	seed, pubKey := newUserSeed(t)
	srv := runJetStreamServer(t, pubKey)
	t.Cleanup(srv.Shutdown)

	origURL := DefaultNATSURL
	DefaultNATSURL = srv.ClientURL()
	t.Cleanup(func() { DefaultNATSURL = origURL })

	fetcher := staticSecretsFetcher{secrets: auth.NATSSecrets{
		APIAuth: auth.NATSAPIAuthSecrets{UserSeed: string(seed)},
	}}

	qc, err := NewClient(context.Background(), "cluster", fetcher)
	require.NoError(t, err)
	cl := qc.(*client)
	t.Cleanup(func() { _ = cl.nc.Drain() })

	nc := connectWithSeed(t, srv.ClientURL(), seed)
	t.Cleanup(func() { _ = nc.Drain() })

	js, err := nc.JetStream()
	require.NoError(t, err)

	subject := "Create.NVCA.Function.cluster.H100.cache"
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     CreateStreamName,
		Subjects: []string{subject},
	})
	require.NoError(t, err)

	// First call should create consumer
	require.Len(t, cl.consumers, 0)

	input := queue.ReceiveMessageInput{
		QueueInfo: queue.MessageQueueInfo{
			QueueType: queue.CreationQueue,
			QueueURL:  CreateStreamName,
			GPU:       "H100",
		},
		MaxNumberOfMessages:      1,
		WaitTimeSeconds:          1,
		VisibilityTimeoutSeconds: 5,
	}

	_, err = qc.ReceiveMessage(context.Background(), input)
	require.NoError(t, err)
	require.Len(t, cl.consumers, 1)

	// Second call should reuse cached consumer
	_, err = qc.ReceiveMessage(context.Background(), input)
	require.NoError(t, err)
	require.Len(t, cl.consumers, 1)
}

func TestReceiveMessageDefaults(t *testing.T) {
	seed, pubKey := newUserSeed(t)
	srv := runJetStreamServer(t, pubKey)
	t.Cleanup(srv.Shutdown)

	origURL := DefaultNATSURL
	DefaultNATSURL = srv.ClientURL()
	t.Cleanup(func() { DefaultNATSURL = origURL })

	fetcher := staticSecretsFetcher{secrets: auth.NATSSecrets{
		APIAuth: auth.NATSAPIAuthSecrets{UserSeed: string(seed)},
	}}

	qc, err := NewClient(context.Background(), "cluster", fetcher)
	require.NoError(t, err)
	cl := qc.(*client)
	t.Cleanup(func() { _ = cl.nc.Drain() })

	nc := connectWithSeed(t, srv.ClientURL(), seed)
	t.Cleanup(func() { _ = nc.Drain() })

	js, err := nc.JetStream()
	require.NoError(t, err)

	subject := "Create.NVCA.Function.cluster.H100.defaults"
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     CreateStreamName,
		Subjects: []string{subject},
	})
	require.NoError(t, err)

	_, err = js.Publish(subject, []byte("payload1"))
	require.NoError(t, err)

	// Test with zero/negative values - should use defaults
	msgs, err := qc.ReceiveMessage(context.Background(), queue.ReceiveMessageInput{
		QueueInfo: queue.MessageQueueInfo{
			QueueType: queue.CreationQueue,
			QueueURL:  CreateStreamName,
			GPU:       "H100",
		},
		MaxNumberOfMessages:      0, // Should default to 1
		WaitTimeSeconds:          0, // Should default to 3
		VisibilityTimeoutSeconds: 5,
	})
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	require.NoError(t, qc.DeleteMessage(context.Background(), queue.DeleteMessageInput{
		QueueInfo:     queue.MessageQueueInfo{QueueType: queue.CreationQueue, QueueURL: CreateStreamName},
		ReceiptHandle: msgs[0].ReceiptHandle,
	}))
}

func TestReceiveMessageTimeout(t *testing.T) {
	seed, pubKey := newUserSeed(t)
	srv := runJetStreamServer(t, pubKey)
	t.Cleanup(srv.Shutdown)

	origURL := DefaultNATSURL
	DefaultNATSURL = srv.ClientURL()
	t.Cleanup(func() { DefaultNATSURL = origURL })

	fetcher := staticSecretsFetcher{secrets: auth.NATSSecrets{
		APIAuth: auth.NATSAPIAuthSecrets{UserSeed: string(seed)},
	}}

	qc, err := NewClient(context.Background(), "cluster", fetcher)
	require.NoError(t, err)
	cl := qc.(*client)
	t.Cleanup(func() { _ = cl.nc.Drain() })

	nc := connectWithSeed(t, srv.ClientURL(), seed)
	t.Cleanup(func() { _ = nc.Drain() })

	js, err := nc.JetStream()
	require.NoError(t, err)

	subject := "Create.NVCA.Function.cluster.H100.timeout"
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     CreateStreamName,
		Subjects: []string{subject},
	})
	require.NoError(t, err)

	// Don't publish anything - should timeout
	msgs, err := qc.ReceiveMessage(context.Background(), queue.ReceiveMessageInput{
		QueueInfo: queue.MessageQueueInfo{
			QueueType: queue.CreationQueue,
			QueueURL:  CreateStreamName,
			GPU:       "H100",
		},
		MaxNumberOfMessages:      1,
		WaitTimeSeconds:          1,
		VisibilityTimeoutSeconds: 5,
	})
	require.NoError(t, err)
	require.Len(t, msgs, 0) // Should return empty slice, not error
}

func TestReceiveMultipleMessages(t *testing.T) {
	seed, pubKey := newUserSeed(t)
	srv := runJetStreamServer(t, pubKey)
	t.Cleanup(srv.Shutdown)

	origURL := DefaultNATSURL
	DefaultNATSURL = srv.ClientURL()
	t.Cleanup(func() { DefaultNATSURL = origURL })

	fetcher := staticSecretsFetcher{secrets: auth.NATSSecrets{
		APIAuth: auth.NATSAPIAuthSecrets{UserSeed: string(seed)},
	}}

	qc, err := NewClient(context.Background(), "cluster", fetcher)
	require.NoError(t, err)
	cl := qc.(*client)
	t.Cleanup(func() { _ = cl.nc.Drain() })

	nc := connectWithSeed(t, srv.ClientURL(), seed)
	t.Cleanup(func() { _ = nc.Drain() })

	js, err := nc.JetStream()
	require.NoError(t, err)

	subject := "Create.NVCA.Function.cluster.H100.multi"
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     CreateStreamName,
		Subjects: []string{subject},
	})
	require.NoError(t, err)

	// Publish multiple messages
	payloads := []string{"msg1", "msg2", "msg3"}
	for _, payload := range payloads {
		_, err = js.Publish(subject, []byte(payload))
		require.NoError(t, err)
	}

	// Receive multiple messages
	msgs, err := qc.ReceiveMessage(context.Background(), queue.ReceiveMessageInput{
		QueueInfo: queue.MessageQueueInfo{
			QueueType: queue.CreationQueue,
			QueueURL:  CreateStreamName,
			GPU:       "H100",
		},
		MaxNumberOfMessages:      10,
		WaitTimeSeconds:          1,
		VisibilityTimeoutSeconds: 5,
	})
	require.NoError(t, err)
	require.Len(t, msgs, 3)

	// Verify all payloads received
	receivedPayloads := make(map[string]bool)
	for _, msg := range msgs {
		receivedPayloads[string(msg.Body)] = true
		// Verify MessageID is K8s compliant (63 chars, hex)
		require.Len(t, msg.MessageID, 63)
		require.Regexp(t, "^[a-f0-9]+$", msg.MessageID)
	}
	for _, payload := range payloads {
		require.True(t, receivedPayloads[payload])
	}

	// Clean up
	for _, msg := range msgs {
		require.NoError(t, qc.DeleteMessage(context.Background(), queue.DeleteMessageInput{
			QueueInfo:     queue.MessageQueueInfo{QueueType: queue.CreationQueue, QueueURL: CreateStreamName},
			ReceiptHandle: msg.ReceiptHandle,
		}))
	}
}

func TestDeleteMessageInvalidHandle(t *testing.T) {
	seed, pubKey := newUserSeed(t)
	srv := runJetStreamServer(t, pubKey)
	t.Cleanup(srv.Shutdown)

	origURL := DefaultNATSURL
	DefaultNATSURL = srv.ClientURL()
	t.Cleanup(func() { DefaultNATSURL = origURL })

	fetcher := staticSecretsFetcher{secrets: auth.NATSSecrets{
		APIAuth: auth.NATSAPIAuthSecrets{UserSeed: string(seed)},
	}}

	qc, err := NewClient(context.Background(), "cluster", fetcher)
	require.NoError(t, err)
	cl := qc.(*client)
	t.Cleanup(func() { _ = cl.nc.Drain() })

	// Try to delete with invalid handle
	err = qc.DeleteMessage(context.Background(), queue.DeleteMessageInput{
		QueueInfo:     queue.MessageQueueInfo{QueueType: queue.CreationQueue, QueueURL: CreateStreamName},
		ReceiptHandle: "invalid-handle",
	})
	require.Error(t, err)
	require.True(t, qc.IsMessageNotFoundError(err))
}

func TestChangeMessageVisibilityInvalidHandle(t *testing.T) {
	seed, pubKey := newUserSeed(t)
	srv := runJetStreamServer(t, pubKey)
	t.Cleanup(srv.Shutdown)

	origURL := DefaultNATSURL
	DefaultNATSURL = srv.ClientURL()
	t.Cleanup(func() { DefaultNATSURL = origURL })

	fetcher := staticSecretsFetcher{secrets: auth.NATSSecrets{
		APIAuth: auth.NATSAPIAuthSecrets{UserSeed: string(seed)},
	}}

	qc, err := NewClient(context.Background(), "cluster", fetcher)
	require.NoError(t, err)
	cl := qc.(*client)
	t.Cleanup(func() { _ = cl.nc.Drain() })

	// Try to change visibility with invalid handle
	vis := int64(10)
	err = qc.ChangeMessageVisibility(context.Background(), queue.ChangeMessageVisibilityInput{
		QueueInfo:                queue.MessageQueueInfo{QueueType: queue.CreationQueue, QueueURL: CreateStreamName},
		ReceiptHandle:            "invalid-handle",
		VisibilityTimeoutSeconds: &vis,
	})
	require.Error(t, err)
	require.True(t, qc.IsMessageNotFoundError(err))
}
