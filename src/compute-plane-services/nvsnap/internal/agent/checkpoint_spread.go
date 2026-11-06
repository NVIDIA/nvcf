// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// A group capture's small checkpoints (an instance's etcd, NATS,
// frontends: megabytes) are kept on more than one node. With no shared
// storage a checkpoint lives only on the node that captured it and on the
// nodes that restored it since, and a group restore needs every member's:
// losing the one node that holds a companion would lose the whole capture.
// So once a group capture is recorded, every member under the rails minimum
// is copied to the capture nodes of the group's other members, by asking
// their agents to make it local; the HTTP peer tier moves it in about a
// second. A copy that fails is logged, and the next restore's own fetch
// tries again. See docs/design/RAILS-FETCH-CONCURRENCY.md, 3.1.

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"
)

// spreadTimeout bounds one copy of a small checkpoint to one node.
const spreadTimeout = 5 * time.Minute

// spreadSmallCheckpoints copies each of results under the rails minimum to
// the other nodes of nodes; results[i] was captured on nodes[i]. A result
// of unknown size is left where it is: it may be an engine rank's.
func (a *Agent) spreadSmallCheckpoints(ctx context.Context, results []*CheckpointResult, nodes []string, log *logrus.Entry) {
	limit := a.config.Rails.minBytes()
	for i, r := range results {
		if r == nil || i >= len(nodes) || r.CheckpointSize <= 0 || r.CheckpointSize >= limit {
			continue
		}
		done := map[string]bool{nodes[i]: true}
		for _, node := range nodes {
			if node == "" || done[node] {
				continue
			}
			done[node] = true
			l := log.WithFields(logrus.Fields{"checkpoint_id": r.CheckpointID, "bytes": r.CheckpointSize, "to_node": node})
			base, err := a.peerAgentURL(ctx, node)
			if err != nil {
				l.WithError(err).Warn("group capture: cannot reach a member node's agent; the small checkpoint stays where it is")
				continue
			}
			t0 := time.Now()
			cctx, cancel := context.WithTimeout(ctx, spreadTimeout)
			err = ensureLocalOn(cctx, peerHTTPClient, base, r.CheckpointID)
			cancel()
			if err != nil {
				l.WithError(err).Warn("group capture: cannot copy a small checkpoint to another member's node; a restore fetches it")
				continue
			}
			l.WithField("elapsed", time.Since(t0).Round(time.Millisecond).String()).Info("group capture: copied a small checkpoint to another member's node")
		}
	}
}
