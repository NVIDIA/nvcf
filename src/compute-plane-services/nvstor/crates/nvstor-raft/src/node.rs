// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//! Raft node — the core consensus algorithm.
//!
//! Implements leader election, log replication, and commit tracking.
//!
//! The node runs as an async task with a 10ms tick interval.
//! On each tick:
//! - Leader: send heartbeats/append entries, update commit index
//! - Follower/Candidate: check election timeout, start election if expired

use std::sync::Arc;
use std::time::{Duration, Instant};

use parking_lot::Mutex;
use rand::Rng;
use tokio::sync::Notify;
use tracing::{debug, info, warn};

use crate::log::RaftLog;
use crate::network::RaftNetwork;
use crate::state_machine::StateMachine;
use crate::types::*;

/// The Raft node — holds all state and runs the consensus algorithm.
pub struct RaftNode {
    /// Configuration.
    config: RaftConfig,
    /// Persistent log (RocksDB-backed).
    log: Mutex<RaftLog>,
    /// Network layer for peer communication.
    network: Arc<RaftNetwork>,
    /// State machine for applying committed entries.
    state_machine: Box<dyn StateMachine>,

    // --- Volatile state ---
    /// Current role.
    role: Mutex<Role>,
    /// Current leader ID (0 = unknown).
    leader_id: Mutex<NodeId>,
    /// Commit index.
    commit_index: Mutex<LogIndex>,
    /// Last applied index.
    last_applied: Mutex<LogIndex>,
    /// Leader-specific state (None if not leader).
    leader_state: Mutex<Option<LeaderState>>,
    /// Last time we received a heartbeat (or voted).
    last_heartbeat: Mutex<Instant>,
    /// Current election timeout (randomized).
    election_timeout: Mutex<Duration>,
    /// Notifier for commit advancement (wakes up propose waiters).
    commit_notify: Notify,
    /// Running flag.
    running: Mutex<bool>,
}

impl RaftNode {
    /// Create a new Raft node with the given state machine.
    pub fn new(
        config: RaftConfig,
        log: RaftLog,
        network: Arc<RaftNetwork>,
        state_machine: Box<dyn StateMachine>,
    ) -> Self {
        let timeout = random_election_timeout(
            config.election_timeout_min_ms,
            config.election_timeout_max_ms,
        );

        Self {
            config,
            log: Mutex::new(log),
            network,
            state_machine,
            role: Mutex::new(Role::Follower),
            leader_id: Mutex::new(0),
            commit_index: Mutex::new(0),
            last_applied: Mutex::new(0),
            leader_state: Mutex::new(None),
            last_heartbeat: Mutex::new(Instant::now()),
            election_timeout: Mutex::new(timeout),
            commit_notify: Notify::new(),
            running: Mutex::new(false),
        }
    }

    /// Get current role.
    pub fn role(&self) -> Role {
        *self.role.lock()
    }

    /// Check if this node is the leader.
    pub fn is_leader(&self) -> bool {
        *self.role.lock() == Role::Leader
    }

    /// Get current leader ID (0 = unknown).
    pub fn leader_id(&self) -> NodeId {
        *self.leader_id.lock()
    }

    /// Get current term.
    pub fn current_term(&self) -> Term {
        self.log.lock().get_term()
    }

    /// Get commit index.
    pub fn commit_index(&self) -> LogIndex {
        *self.commit_index.lock()
    }

    /// Get last log index.
    pub fn last_log_index(&self) -> LogIndex {
        self.log.lock().last_index()
    }

    // ====================================================================
    // Core algorithm
    // ====================================================================

    /// Run the Raft node (ticker loop). Call from a tokio::spawn.
    pub async fn run(&self) {
        *self.running.lock() = true;
        info!(node_id = self.config.node_id, "raft node started");

        while *self.running.lock() {
            tokio::time::sleep(Duration::from_millis(10)).await;
            self.tick().await;
        }

        info!(node_id = self.config.node_id, "raft node stopped");
    }

    /// Stop the node.
    pub fn stop(&self) {
        *self.running.lock() = false;
    }

    /// Single tick of the Raft algorithm.
    async fn tick(&self) {
        let role = *self.role.lock();
        let elapsed = self.last_heartbeat.lock().elapsed();

        match role {
            Role::Leader => {
                if elapsed >= Duration::from_millis(self.config.heartbeat_ms) {
                    self.leader_heartbeat().await;
                    *self.last_heartbeat.lock() = Instant::now();
                }
                self.check_quorum();
            }
            Role::Follower | Role::Candidate => {
                let timeout = *self.election_timeout.lock();
                if elapsed >= timeout {
                    self.start_election().await;
                }
            }
        }
    }

    // ====================================================================
    // Role transitions
    //
    // Every change of role or term happens while the log lock is held.
    // `propose` checks the role and reads the term under that same lock, so
    // it can never append an entry stamped with a term this node does not
    // lead, and `become_leader` can never take leadership of a term that a
    // concurrent RPC has already moved past.
    // ====================================================================

    /// Become a follower, adopting `term` if it is newer than ours.
    ///
    /// The caller holds the log lock. A `term` older than the current one is
    /// stale information and is ignored. `voted_for` is cleared only when
    /// the term actually advances: clearing it within a term would let this
    /// node vote twice in that term.
    fn step_down_locked(&self, log: &RaftLog, term: Term) {
        let current = log.get_term();
        if term < current {
            return;
        }
        if term > current {
            // Term first, then the vote: a crash between the two leaves the
            // old vote attached to the new term, which only restricts us.
            if let Err(e) = log.set_term(term) {
                warn!(error = %e, term, "failed to persist term on step-down");
            } else if let Err(e) = log.set_voted_for(-1) {
                warn!(error = %e, term, "failed to clear vote on step-down");
            }
            *self.leader_id.lock() = 0;
        }

        let was_leader = {
            let mut role = self.role.lock();
            let was = *role == Role::Leader;
            *role = Role::Follower;
            was
        };
        *self.leader_state.lock() = None;
        if was_leader {
            // We no longer know who leads; don't redirect clients to ourselves.
            let mut leader = self.leader_id.lock();
            if *leader == self.config.node_id {
                *leader = 0;
            }
            info!(node_id = self.config.node_id, term, "leader stepped down");
        } else {
            debug!(node_id = self.config.node_id, term, "became follower");
        }
        // Wake proposers so they report NotLeader now instead of waiting
        // out the propose timeout.
        self.commit_notify.notify_waiters();
    }

    /// Take leadership of `term`, if this node is still a candidate in it.
    ///
    /// Returns false when the term moved on (a higher-term RPC arrived while
    /// votes were being collected) or the election no-op could not be
    /// persisted.
    fn become_leader(&self, term: Term) -> bool {
        let mut log = self.log.lock();
        if log.get_term() != term || *self.role.lock() != Role::Candidate {
            return false;
        }

        // Append a no-op entry to establish leadership: entries from earlier
        // terms only commit once an entry of this term does (Raft §5.4.2).
        let last_index = log.last_index();
        let noop = LogEntry {
            term,
            index: last_index + 1,
            data: LogEntryData::Noop,
        };
        if let Err(e) = log.append(noop) {
            warn!(error = %e, term, "failed to append election noop; not taking leadership");
            self.step_down_locked(&log, term);
            return false;
        }

        // Leader state and leader_id first, role last: a lock-free reader
        // that sees `is_leader()` also sees `leader_id() == self`.
        *self.leader_state.lock() = Some(LeaderState::new(self.network.peer_count(), last_index));
        *self.leader_id.lock() = self.config.node_id;
        *self.role.lock() = Role::Leader;
        true
    }

    /// Check-quorum: a leader that has not heard from a majority (itself
    /// included) within one maximum election timeout steps down.
    ///
    /// Without this a leader cut off from the majority keeps claiming
    /// leadership indefinitely: its clients are never redirected and every
    /// proposal waits out the full propose timeout. The majority side elects
    /// a new leader within the same window, so stepping down here costs
    /// nothing that was not already lost.
    fn check_quorum(&self) {
        let window = Duration::from_millis(self.config.election_timeout_max_ms);
        let now = Instant::now();
        let log = self.log.lock();
        let lost = {
            let ls = self.leader_state.lock();
            let Some(state) = ls.as_ref() else {
                return;
            };
            if now.saturating_duration_since(state.elected_at) < window {
                return;
            }
            let heard = 1 + state
                .last_ack
                .iter()
                .filter(|ack| ack.is_some_and(|t| now.saturating_duration_since(t) < window))
                .count();
            heard < self.network.quorum_size()
        };
        if lost {
            let term = log.get_term();
            warn!(
                node_id = self.config.node_id,
                term,
                window_ms = self.config.election_timeout_max_ms,
                "lost contact with a majority; stepping down"
            );
            self.step_down_locked(&log, term);
        }
    }

    // ====================================================================
    // Leader election
    // ====================================================================

    /// Start a new election (become candidate, request votes).
    async fn start_election(&self) {
        let new_term;
        let last_log_index;
        let last_log_term;

        {
            let log = self.log.lock();
            new_term = log.get_term() + 1;
            if let Err(e) = log.set_term(new_term) {
                warn!(error = %e, "failed to persist term");
                return;
            }
            if let Err(e) = log.set_voted_for(self.config.node_id as i64) {
                // We are in the new term without a recorded vote; stay a
                // follower rather than campaign on a vote we can't persist.
                warn!(error = %e, "failed to persist vote");
                *self.leader_id.lock() = 0;
                return;
            }
            last_log_index = log.last_index();
            last_log_term = log.last_term();
            *self.role.lock() = Role::Candidate;
            *self.leader_id.lock() = 0;
        }

        *self.last_heartbeat.lock() = Instant::now();
        *self.election_timeout.lock() = random_election_timeout(
            self.config.election_timeout_min_ms,
            self.config.election_timeout_max_ms,
        );

        debug!(
            node_id = self.config.node_id,
            term = new_term,
            "starting election"
        );

        let rv = RequestVote {
            term: new_term,
            candidate_id: self.config.node_id,
            last_log_index,
            last_log_term,
        };

        let results = self.network.broadcast_request_vote(&rv).await;

        // Count votes (including self).
        let mut votes = 1u64; // Self-vote
        for (peer_id, result) in &results {
            match result {
                Ok(resp) => {
                    if resp.term > new_term {
                        // Discovered higher term — step down.
                        let log = self.log.lock();
                        self.step_down_locked(&log, resp.term);
                        return;
                    }
                    if resp.vote_granted && resp.term == new_term {
                        votes += 1;
                        debug!(peer_id, term = new_term, "received vote");
                    }
                }
                Err(e) => {
                    debug!(peer_id, error = %e, "vote request failed");
                }
            }
        }

        let quorum = self.network.quorum_size() as u64;
        if votes < quorum {
            debug!(
                node_id = self.config.node_id,
                term = new_term,
                votes,
                quorum,
                "election failed — not enough votes"
            );
            return;
        }

        // `become_leader` re-checks, under the log lock, that we are still a
        // candidate in `new_term`: an RPC may have moved us on while the
        // votes were in flight.
        if self.become_leader(new_term) {
            info!(
                node_id = self.config.node_id,
                term = new_term,
                votes,
                "elected leader"
            );
        }
    }

    // ====================================================================
    // Log replication (leader)
    // ====================================================================

    /// Send heartbeats and replicate log entries to all peers.
    ///
    /// Peers are contacted CONCURRENTLY. Sequentially awaiting each peer
    /// meant one unreachable follower added its full connect/RPC timeout to
    /// every heartbeat round; with a 50 ms heartbeat interval and a 2 s
    /// timeout, a single down peer stretched the round far past the
    /// followers' election timeout, so healthy followers stopped hearing from
    /// a perfectly healthy leader and triggered spurious elections. A round
    /// now costs the slowest peer, not the sum.
    async fn leader_heartbeat(&self) {
        let peer_ids = self.network.peer_ids();

        // Phase 1: build every request under the locks (cheap, no I/O).
        let mut requests = Vec::with_capacity(peer_ids.len());
        for (i, peer_id) in peer_ids.iter().enumerate() {
            if let Some(ae) = self.prepare_append_entries(i) {
                requests.push((i, *peer_id, ae));
            }
        }

        // Phase 2: fan out with no locks held. `network` is an Arc, so each
        // task owns what it needs and they all run at once.
        let mut inflight = Vec::with_capacity(requests.len());
        for (peer_idx, peer_id, ae) in requests {
            let network = Arc::clone(&self.network);
            let ae_for_task = ae.clone();
            let task =
                tokio::spawn(
                    async move { network.send_append_entries(peer_id, &ae_for_task).await },
                );
            inflight.push((peer_idx, peer_id, ae, task));
        }

        // Phase 3: apply the responses.
        for (peer_idx, peer_id, ae, task) in inflight {
            match task.await {
                Ok(result) => self.apply_append_entries_response(peer_idx, peer_id, &ae, result),
                Err(e) => debug!(peer_id, error = %e, "append entries task failed"),
            }
        }

        // Update commit index based on majority match.
        self.update_commit_index();
    }

    /// Build the `AppendEntries` for one peer, or None if we're no longer
    /// leader / the peer index is stale. Takes the log + leader_state locks
    /// briefly and performs no I/O.
    fn prepare_append_entries(&self, peer_idx: usize) -> Option<AppendEntries> {
        let log = self.log.lock();
        let ls = self.leader_state.lock();
        let leader_state = ls.as_ref()?;

        if peer_idx >= leader_state.next_index.len() {
            return None;
        }

        let next = leader_state.next_index[peer_idx];
        let prev_index = next.saturating_sub(1);
        let prev_term = log.term_at(prev_index);
        let entries = log.entries_from(next).to_vec();

        Some(AppendEntries {
            term: log.get_term(),
            leader_id: self.config.node_id,
            prev_log_index: prev_index,
            prev_log_term: prev_term,
            leader_commit: *self.commit_index.lock(),
            entries,
        })
    }

    /// Apply one peer's AppendEntries response to leader state.
    ///
    /// The follower's log is known to match ours only through the last entry
    /// this request carried, so that — not whatever the follower reports —
    /// bounds `match_index`. A follower can hold a stale tail from an older
    /// term beyond that point; counting it would let the leader commit
    /// entries of its own that the follower never received.
    fn apply_append_entries_response(
        &self,
        peer_idx: usize,
        peer_id: NodeId,
        ae: &AppendEntries,
        result: Result<AppendEntriesResp, crate::rpc::RpcError>,
    ) {
        let resp = match result {
            Ok(resp) => resp,
            Err(e) => {
                debug!(peer_id, error = %e, "append entries failed");
                return;
            }
        };

        let log = self.log.lock();
        let current_term = log.get_term();
        if resp.term > current_term {
            self.step_down_locked(&log, resp.term);
            return;
        }
        // A reply to a request from another term, or one that arrives after
        // we stopped leading, says nothing about this term's log.
        if ae.term != current_term || resp.term != current_term || *self.role.lock() != Role::Leader
        {
            return;
        }

        let mut ls = self.leader_state.lock();
        let Some(state) = ls.as_mut() else {
            return;
        };
        if peer_idx >= state.match_index.len() {
            return;
        }

        // The peer acknowledged us as leader of this term.
        state.last_ack[peer_idx] = Some(Instant::now());

        if resp.success {
            let sent_through = ae.prev_log_index + ae.entries.len() as u64;
            let matched = resp.match_index.min(sent_through);
            if matched > state.match_index[peer_idx] {
                state.match_index[peer_idx] = matched;
            }
            let next = state.match_index[peer_idx] + 1;
            if next > state.next_index[peer_idx] {
                state.next_index[peer_idx] = next;
            }
        } else if state.next_index[peer_idx] == ae.prev_log_index + 1
            && state.next_index[peer_idx] > 1
        {
            // Log inconsistency at the probe we sent — step back and retry.
            state.next_index[peer_idx] -= 1;
        }
    }

    /// Update commit_index based on majority match_index (Raft §5.4).
    fn update_commit_index(&self) {
        let log = self.log.lock();
        let current_term = log.get_term();
        let last_index = log.last_index();
        let mut commit = *self.commit_index.lock();
        let old_commit = commit;

        let ls = self.leader_state.lock();
        let leader_state = match ls.as_ref() {
            Some(s) => s,
            None => return,
        };

        for idx in (commit + 1)..=last_index {
            let entry_term = log.term_at(idx);
            // Only commit entries from the current term (Raft safety).
            if entry_term != current_term {
                continue;
            }

            // Count replicas (including self).
            let mut matches = 1u64; // Self
            for &match_idx in &leader_state.match_index {
                if match_idx >= idx {
                    matches += 1;
                }
            }

            let quorum = self.network.quorum_size() as u64;
            if matches >= quorum {
                commit = idx;
            }
        }

        drop(ls);
        drop(log); // MUST drop before apply_committed (which locks log)

        if commit > old_commit {
            *self.commit_index.lock() = commit;
        }
        // Retry failed local application even when the commit index is unchanged.
        self.apply_committed();
    }

    // ====================================================================
    // Handling incoming RPCs
    // ====================================================================

    /// Handle a RequestVote RPC from a candidate.
    pub fn handle_request_vote(&self, rv: &RequestVote) -> RequestVoteResp {
        let log = self.log.lock();

        // If the candidate's term is higher, step down (and clear our vote).
        if rv.term > log.get_term() {
            self.step_down_locked(&log, rv.term);
        }

        let current_term = log.get_term();
        let voted_for = log.get_voted_for();
        let my_last_index = log.last_index();
        let my_last_term = log.last_term();

        let mut vote_granted = false;

        // `rv.term == current_term` also refuses when persisting the newer
        // term failed above.
        if rv.term == current_term && (voted_for == -1 || voted_for == rv.candidate_id as i64) {
            // Check that candidate's log is at least as up-to-date as ours.
            let log_ok = rv.last_log_term > my_last_term
                || (rv.last_log_term == my_last_term && rv.last_log_index >= my_last_index);

            if log_ok {
                // A vote that is not durable must not be granted: after a
                // restart we could grant a second one in the same term.
                match log.set_voted_for(rv.candidate_id as i64) {
                    Ok(()) => {
                        vote_granted = true;
                        *self.last_heartbeat.lock() = Instant::now();
                        debug!(
                            node_id = self.config.node_id,
                            candidate = rv.candidate_id,
                            term = rv.term,
                            "granted vote"
                        );
                    }
                    Err(e) => warn!(error = %e, "failed to persist vote; refusing it"),
                }
            }
        }

        RequestVoteResp {
            term: current_term,
            vote_granted,
        }
    }

    /// Handle an AppendEntries RPC from the leader.
    pub fn handle_append_entries(&self, ae: &AppendEntries) -> AppendEntriesResp {
        let mut log = self.log.lock();
        let current_term = log.get_term();

        // Reject stale leader.
        if ae.term < current_term {
            return AppendEntriesResp {
                term: current_term,
                success: false,
                match_index: 0,
            };
        }

        // `ae.term >= current_term`: the sender leads `ae.term`. A newer term
        // is adopted (clearing our vote); a candidate in the same term yields
        // but keeps its vote.
        if ae.term > current_term || *self.role.lock() != Role::Follower {
            self.step_down_locked(&log, ae.term);
        }
        let current_term = log.get_term();
        if current_term != ae.term {
            // Persisting the newer term failed; we cannot follow it.
            return AppendEntriesResp {
                term: current_term,
                success: false,
                match_index: 0,
            };
        }

        *self.leader_id.lock() = ae.leader_id;
        *self.last_heartbeat.lock() = Instant::now();

        let reject = AppendEntriesResp {
            term: current_term,
            success: false,
            match_index: 0,
        };

        // Check prev log entry consistency.
        if ae.prev_log_index > 0 {
            let prev_term = log.term_at(ae.prev_log_index);
            if prev_term == 0 || prev_term != ae.prev_log_term {
                return reject;
            }
        }

        // Append entries (handle conflicts by truncating). Any persistence
        // failure is a rejection: claiming success for entries that are not
        // durable here would let the leader count us toward a commit.
        for (i, entry) in ae.entries.iter().enumerate() {
            let new_idx = ae.prev_log_index + 1 + i as u64;
            let existing_term = log.term_at(new_idx);

            if existing_term != 0 && existing_term != entry.term {
                // Conflict — truncate from here.
                if let Err(e) = log.truncate_from(new_idx) {
                    warn!(error = %e, index = new_idx, "truncate failed");
                    return reject;
                }
            }

            if log.term_at(new_idx) == 0 {
                // Entry doesn't exist — append.
                let new_entry = LogEntry {
                    term: entry.term,
                    index: new_idx,
                    data: entry.data.clone(),
                };
                if let Err(e) = log.append(new_entry) {
                    warn!(error = %e, index = new_idx, "append failed");
                    return reject;
                }
            }
        }

        // Our log matches the leader's through the last entry this request
        // carried — no further. Anything after it may be a stale tail from an
        // older term, so it bounds both the commit index and what we report.
        let last_new = ae.prev_log_index + ae.entries.len() as u64;
        {
            let mut commit = self.commit_index.lock();
            let new_commit = ae.leader_commit.min(last_new);
            if new_commit > *commit {
                *commit = new_commit;
            }
        }

        // MUST drop log lock before apply_committed (which also locks log).
        drop(log);

        self.apply_committed();

        AppendEntriesResp {
            term: current_term,
            success: true,
            match_index: last_new,
        }
    }

    // ====================================================================
    // Proposing entries (client API)
    // ====================================================================

    /// Propose a new log entry. Blocks until committed or timeout.
    /// Only succeeds on the leader.
    ///
    /// `Committed` means the entry this call appended — same index, same
    /// term — is committed and applied locally. If this node loses
    /// leadership first the result is `NotLeader`; the entry may or may not
    /// commit later under another leader, as in any Raft client protocol.
    pub async fn propose(&self, data: LogEntryData) -> ProposeResult {
        let (index, term) = {
            let mut log = self.log.lock();
            // Role and term are read under the log lock, which every role
            // transition holds: the entry is stamped with the term we lead.
            if *self.role.lock() != Role::Leader {
                return ProposeResult::NotLeader {
                    leader_id: self.leader_id(),
                };
            }
            let term = log.get_term();
            let index = log.last_index() + 1;
            let entry = LogEntry { term, index, data };
            if let Err(e) = log.append(entry) {
                warn!(error = %e, "propose: append failed");
                return ProposeResult::Timeout;
            }
            (index, term)
        };

        debug!(index, term, "proposed entry, waiting for commit");

        // A quorum commit is insufficient: local durable application must succeed.
        let deadline = Instant::now() + Duration::from_millis(self.config.propose_timeout_ms);

        loop {
            // Register before checking to avoid losing an application notification.
            let notified = self.commit_notify.notified();
            tokio::pin!(notified);
            notified.as_mut().enable();
            if *self.last_applied.lock() >= index {
                // Something is applied at `index`, but it is only ours if it
                // carries our term: a leader that was deposed can see a new
                // leader's entry committed at the same index.
                let applied_term = self.log.lock().term_at(index);
                if applied_term == term {
                    return ProposeResult::Committed { index, term };
                }
                return ProposeResult::NotLeader {
                    leader_id: self.leader_id(),
                };
            }

            if !self.is_leader() {
                return ProposeResult::NotLeader {
                    leader_id: self.leader_id(),
                };
            }

            let remaining = deadline.saturating_duration_since(Instant::now());
            if remaining.is_zero() {
                return ProposeResult::Timeout;
            }

            // Wait for commit notification or timeout.
            tokio::select! {
                _ = notified => {}
                _ = tokio::time::sleep(remaining) => {
                    return ProposeResult::Timeout;
                }
            }
        }
    }

    // ====================================================================
    // State machine application
    // ====================================================================

    /// Apply all committed but unapplied entries to the state machine.
    fn apply_committed(&self) {
        let commit = *self.commit_index.lock();
        let mut last_applied = self.last_applied.lock();

        while *last_applied < commit {
            let idx = *last_applied + 1;
            let Some(entry) = self.log.lock().get(idx).cloned() else {
                warn!(index = idx, "committed entry missing from local log");
                break;
            };
            if let Err(error) = self.state_machine.apply(&entry) {
                warn!(index = idx, %error, "state machine application failed; will retry");
                break;
            }
            *last_applied = idx;
            self.commit_notify.notify_waiters();
        }
    }
}

// ============================================================================
// Helpers
// ============================================================================

fn random_election_timeout(min_ms: u64, max_ms: u64) -> Duration {
    let ms = rand::thread_rng().gen_range(min_ms..=max_ms);
    Duration::from_millis(ms)
}

// ============================================================================
// Tests
// ============================================================================

#[cfg(test)]
mod tests {
    use super::*;
    use crate::log::RaftLog;
    use crate::network::RaftNetwork;

    fn tmp_node(node_id: NodeId, peers: Vec<PeerConfig>) -> (tempfile::TempDir, Arc<RaftNode>) {
        let dir = tempfile::TempDir::new().expect("tempdir");
        let log = RaftLog::open(dir.path()).expect("open log");
        let network = Arc::new(RaftNetwork::new(node_id, peers));
        let config = RaftConfig {
            node_id,
            election_timeout_min_ms: 500,
            election_timeout_max_ms: 1000,
            heartbeat_ms: 100,
            propose_timeout_ms: 2000,
            ..Default::default()
        };
        let node = Arc::new(RaftNode::new(
            config,
            log,
            network,
            Box::new(crate::state_machine::NullStateMachine),
        ));
        (dir, node)
    }

    #[test]
    fn failed_application_is_retried_without_skipping_entries() {
        struct FailingOnce(std::sync::atomic::AtomicBool);
        impl StateMachine for FailingOnce {
            fn apply(&self, _entry: &LogEntry) -> Result<(), String> {
                if self.0.swap(false, std::sync::atomic::Ordering::SeqCst) {
                    Err("temporary storage failure".into())
                } else {
                    Ok(())
                }
            }
        }
        let dir = tempfile::TempDir::new().unwrap();
        let mut log = RaftLog::open(dir.path()).unwrap();
        log.append(LogEntry {
            term: 1,
            index: 1,
            data: LogEntryData::Noop,
        })
        .unwrap();
        log.append(LogEntry {
            term: 1,
            index: 2,
            data: LogEntryData::Noop,
        })
        .unwrap();
        let node = RaftNode::new(
            RaftConfig::default(),
            log,
            Arc::new(RaftNetwork::new(1, vec![])),
            Box::new(FailingOnce(std::sync::atomic::AtomicBool::new(true))),
        );
        *node.commit_index.lock() = 2;
        node.apply_committed();
        assert_eq!(*node.last_applied.lock(), 0);
        node.apply_committed();
        assert_eq!(*node.last_applied.lock(), 2);
    }

    #[test]
    fn initial_state() {
        let (_dir, node) = tmp_node(1, vec![]);
        assert_eq!(node.role(), Role::Follower);
        assert_eq!(node.leader_id(), 0);
        assert_eq!(node.current_term(), 0);
        assert_eq!(node.commit_index(), 0);
        assert!(!node.is_leader());
    }

    #[test]
    fn handle_request_vote_grants_vote() {
        let (_dir, node) = tmp_node(1, vec![]);

        let rv = RequestVote {
            term: 1,
            candidate_id: 2,
            last_log_index: 0,
            last_log_term: 0,
        };

        let resp = node.handle_request_vote(&rv);
        assert!(resp.vote_granted);
        assert_eq!(resp.term, 1);
    }

    #[test]
    fn handle_request_vote_rejects_stale_term() {
        let (_dir, node) = tmp_node(1, vec![]);

        // Set node to term 5.
        node.log.lock().set_term(5).expect("ok");

        let rv = RequestVote {
            term: 3, // Lower than current term.
            candidate_id: 2,
            last_log_index: 0,
            last_log_term: 0,
        };

        let resp = node.handle_request_vote(&rv);
        assert!(!resp.vote_granted);
        assert_eq!(resp.term, 5);
    }

    #[test]
    fn handle_request_vote_rejects_stale_log() {
        let (_dir, node) = tmp_node(1, vec![]);

        // Append some entries to our log.
        {
            let mut log = node.log.lock();
            log.set_term(3).expect("ok");
            log.append(LogEntry {
                term: 2,
                index: 1,
                data: LogEntryData::Noop,
            })
            .expect("ok");
            log.append(LogEntry {
                term: 3,
                index: 2,
                data: LogEntryData::Noop,
            })
            .expect("ok");
        }

        // Candidate has an older log.
        let rv = RequestVote {
            term: 3,
            candidate_id: 2,
            last_log_index: 1,
            last_log_term: 2,
        };

        let resp = node.handle_request_vote(&rv);
        assert!(!resp.vote_granted); // Our log is more up-to-date.
    }

    #[test]
    fn handle_append_entries_heartbeat() {
        let (_dir, node) = tmp_node(1, vec![]);

        let ae = AppendEntries {
            term: 1,
            leader_id: 2,
            prev_log_index: 0,
            prev_log_term: 0,
            leader_commit: 0,
            entries: vec![],
        };

        let resp = node.handle_append_entries(&ae);
        assert!(resp.success);
        assert_eq!(node.leader_id(), 2);
        assert_eq!(node.role(), Role::Follower);
    }

    #[test]
    fn handle_append_entries_with_entries() {
        let (_dir, node) = tmp_node(1, vec![]);

        let ae = AppendEntries {
            term: 1,
            leader_id: 2,
            prev_log_index: 0,
            prev_log_term: 0,
            leader_commit: 0,
            entries: vec![
                LogEntry {
                    term: 1,
                    index: 1,
                    data: LogEntryData::Noop,
                },
                LogEntry {
                    term: 1,
                    index: 2,
                    data: LogEntryData::BucketPut {
                        name: "test".into(),
                        owner: "u".into(),
                        region: "r".into(),
                        versioning: 0,
                    },
                },
            ],
        };

        let resp = node.handle_append_entries(&ae);
        assert!(resp.success);
        assert_eq!(resp.match_index, 2);
        assert_eq!(node.last_log_index(), 2);
    }

    #[test]
    fn handle_append_entries_rejects_stale() {
        let (_dir, node) = tmp_node(1, vec![]);
        node.log.lock().set_term(5).expect("ok");

        let ae = AppendEntries {
            term: 3, // Stale.
            leader_id: 2,
            prev_log_index: 0,
            prev_log_term: 0,
            leader_commit: 0,
            entries: vec![],
        };

        let resp = node.handle_append_entries(&ae);
        assert!(!resp.success);
    }

    #[test]
    fn handle_append_entries_detects_conflict() {
        let (_dir, node) = tmp_node(1, vec![]);

        // Append entries from term 1.
        {
            let mut log = node.log.lock();
            log.set_term(1).expect("ok");
            log.append(LogEntry {
                term: 1,
                index: 1,
                data: LogEntryData::Noop,
            })
            .expect("ok");
            log.append(LogEntry {
                term: 1,
                index: 2,
                data: LogEntryData::Noop,
            })
            .expect("ok");
        }

        // Leader sends entry at index 2 with term 2 (conflict).
        let ae = AppendEntries {
            term: 2,
            leader_id: 3,
            prev_log_index: 1,
            prev_log_term: 1,
            leader_commit: 0,
            entries: vec![LogEntry {
                term: 2,
                index: 2,
                data: LogEntryData::BucketDelete { name: "x".into() },
            }],
        };

        let resp = node.handle_append_entries(&ae);
        assert!(resp.success);
        // The conflicting entry at index 2 should be replaced.
        let log = node.log.lock();
        let entry = log.get(2).expect("exists");
        assert_eq!(entry.term, 2);
        match &entry.data {
            LogEntryData::BucketDelete { name } => assert_eq!(name, "x"),
            _ => panic!("wrong data"),
        }
    }

    #[test]
    fn handle_append_entries_advances_commit() {
        let (_dir, node) = tmp_node(1, vec![]);

        // Leader sends entries with leader_commit > 0.
        let ae = AppendEntries {
            term: 1,
            leader_id: 2,
            prev_log_index: 0,
            prev_log_term: 0,
            leader_commit: 2,
            entries: vec![
                LogEntry {
                    term: 1,
                    index: 1,
                    data: LogEntryData::Noop,
                },
                LogEntry {
                    term: 1,
                    index: 2,
                    data: LogEntryData::Noop,
                },
            ],
        };

        node.handle_append_entries(&ae);
        assert_eq!(node.commit_index(), 2);
    }

    #[tokio::test]
    async fn propose_on_non_leader_fails() {
        let (_dir, node) = tmp_node(1, vec![]);

        let result = node.propose(LogEntryData::Noop).await;
        match result {
            ProposeResult::NotLeader { leader_id } => {
                assert_eq!(leader_id, 0);
            }
            _ => panic!("expected NotLeader"),
        }
    }

    #[test]
    fn become_leader_appends_noop() {
        let (_dir, node) = tmp_node(1, vec![]);
        make_candidate(&node, 1);

        assert!(node.become_leader(1));

        assert_eq!(node.role(), Role::Leader);
        assert_eq!(node.leader_id(), 1);
        assert_eq!(node.last_log_index(), 1); // Noop appended
        assert_eq!(node.log.lock().term_at(1), 1);
    }

    // ------------------------------------------------------------------
    // Safety and step-down behaviour.
    // ------------------------------------------------------------------

    /// Two peers that are never contacted (the tests drive the node by hand).
    fn two_peers() -> Vec<PeerConfig> {
        vec![
            PeerConfig {
                node_id: 2,
                addr: "127.0.0.1:1".into(),
            },
            PeerConfig {
                node_id: 3,
                addr: "127.0.0.1:1".into(),
            },
        ]
    }

    /// Put `node` in the state `start_election` leaves it in: candidate in
    /// `term`, having voted for itself.
    fn make_candidate(node: &RaftNode, term: Term) {
        let log = node.log.lock();
        log.set_term(term).expect("set term");
        log.set_voted_for(node.config.node_id as i64).expect("vote");
        *node.role.lock() = Role::Candidate;
    }

    fn make_leader(node: &RaftNode, term: Term) {
        make_candidate(node, term);
        assert!(node.become_leader(term), "become_leader({term})");
    }

    fn bucket(name: &str) -> LogEntryData {
        LogEntryData::BucketPut {
            name: name.into(),
            owner: "u".into(),
            region: "r".into(),
            versioning: 0,
        }
    }

    async fn wait_for_last_index(node: &RaftNode, index: LogIndex) {
        tokio::time::timeout(Duration::from_secs(5), async {
            while node.last_log_index() < index {
                tokio::time::sleep(Duration::from_millis(1)).await;
            }
        })
        .await
        .expect("entry appended");
    }

    /// A deposed leader whose index is reused by the new leader must not
    /// report the new leader's entry as its own commit. Before the fix,
    /// `propose` returned `Committed` as soon as *anything* was applied at
    /// its index: the caller was told a write committed that no node holds.
    #[tokio::test]
    async fn propose_does_not_claim_an_overwritten_entry() {
        let (_dir, node) = tmp_node(1, two_peers());
        make_leader(&node, 1); // noop at index 1

        let proposer = Arc::clone(&node);
        let pending = tokio::spawn(async move { proposer.propose(bucket("lost")).await });
        wait_for_last_index(&node, 2).await;

        // A new leader in term 2 never saw "lost"; it puts its own entry at
        // index 2 and has committed it.
        let resp = node.handle_append_entries(&AppendEntries {
            term: 2,
            leader_id: 2,
            prev_log_index: 1,
            prev_log_term: 1,
            leader_commit: 2,
            entries: vec![LogEntry {
                term: 2,
                index: 2,
                data: bucket("winner"),
            }],
        });
        assert!(resp.success);
        assert_eq!(node.commit_index(), 2);

        let result = tokio::time::timeout(Duration::from_secs(1), pending)
            .await
            .expect("proposal must resolve promptly")
            .expect("join");
        match result {
            ProposeResult::NotLeader { leader_id } => assert_eq!(leader_id, 2),
            other => panic!("overwritten entry reported as {other:?}"),
        }
    }

    /// Losing leadership wakes a pending proposal immediately with
    /// NotLeader instead of leaving it to the propose timeout.
    #[tokio::test]
    async fn step_down_wakes_pending_proposal() {
        let (_dir, node) = tmp_node(1, two_peers());
        make_leader(&node, 1);

        let proposer = Arc::clone(&node);
        let pending = tokio::spawn(async move { proposer.propose(bucket("x")).await });
        wait_for_last_index(&node, 2).await;

        // A heartbeat from a newer leader, no entries.
        node.handle_append_entries(&AppendEntries {
            term: 2,
            leader_id: 3,
            prev_log_index: 0,
            prev_log_term: 0,
            leader_commit: 0,
            entries: vec![],
        });

        let result = tokio::time::timeout(Duration::from_millis(500), pending)
            .await
            .expect("propose_timeout is 2 s; step-down must answer sooner")
            .expect("join");
        assert!(
            matches!(result, ProposeResult::NotLeader { leader_id: 3 }),
            "got {result:?}"
        );
    }

    /// A candidate that yields to a leader of its own term keeps its vote.
    /// Before the fix the vote was cleared, so the node could vote a second
    /// time in the same term and elect a second leader.
    #[test]
    fn candidate_yielding_in_same_term_keeps_its_vote() {
        let (_dir, node) = tmp_node(1, two_peers());
        make_candidate(&node, 1);

        let resp = node.handle_append_entries(&AppendEntries {
            term: 1,
            leader_id: 2,
            prev_log_index: 0,
            prev_log_term: 0,
            leader_commit: 0,
            entries: vec![],
        });
        assert!(resp.success);
        assert_eq!(node.role(), Role::Follower);
        assert_eq!(node.log.lock().get_voted_for(), 1);

        let vote = node.handle_request_vote(&RequestVote {
            term: 1,
            candidate_id: 3,
            last_log_index: 10,
            last_log_term: 1,
        });
        assert!(!vote.vote_granted, "second vote granted in term 1");

        // A newer term clears the vote as usual.
        let vote = node.handle_request_vote(&RequestVote {
            term: 2,
            candidate_id: 3,
            last_log_index: 10,
            last_log_term: 1,
        });
        assert!(vote.vote_granted);
        assert_eq!(vote.term, 2);
    }

    /// A follower holding a stale tail from an older term reports a match
    /// only through the entries the leader actually sent, and commits no
    /// further than that.
    #[test]
    fn follower_match_and_commit_stop_at_last_new_entry() {
        let (_dir, node) = tmp_node(1, two_peers());
        {
            let mut log = node.log.lock();
            log.set_term(1).expect("term");
            for index in 1..=3 {
                log.append(LogEntry {
                    term: 1,
                    index,
                    data: LogEntryData::Noop,
                })
                .expect("append");
            }
        }

        // The term-2 leader holds only [1, 2] of that history.
        let resp = node.handle_append_entries(&AppendEntries {
            term: 2,
            leader_id: 2,
            prev_log_index: 1,
            prev_log_term: 1,
            leader_commit: 5,
            entries: vec![LogEntry {
                term: 1,
                index: 2,
                data: LogEntryData::Noop,
            }],
        });
        assert!(resp.success);
        assert_eq!(
            resp.match_index, 2,
            "index 3 was never verified against the leader"
        );
        assert_eq!(
            node.commit_index(),
            2,
            "commit must not reach the unverified tail"
        );
        // The unverified entry is still there (no conflict was shown).
        assert_eq!(node.last_log_index(), 3);
    }

    /// The leader bounds a peer's match_index by what it sent, whatever the
    /// peer claims. Before the fix a stale tail on one follower was counted
    /// as a replica of the leader's own newer entries, committing them on a
    /// minority.
    #[test]
    fn leader_match_index_is_bounded_by_entries_sent() {
        let (_dir, node) = tmp_node(1, two_peers());
        node.log
            .lock()
            .append(LogEntry {
                term: 1,
                index: 1,
                data: LogEntryData::Noop,
            })
            .expect("append");
        make_leader(&node, 2); // noop at 2
        node.log
            .lock()
            .append(LogEntry {
                term: 2,
                index: 3,
                data: bucket("only-on-leader"),
            })
            .expect("append");

        // A heartbeat that carried nothing past index 1, answered with an
        // inflated match.
        let heartbeat = AppendEntries {
            term: 2,
            leader_id: 1,
            prev_log_index: 1,
            prev_log_term: 1,
            leader_commit: 0,
            entries: vec![],
        };
        node.apply_append_entries_response(
            0,
            2,
            &heartbeat,
            Ok(AppendEntriesResp {
                term: 2,
                success: true,
                match_index: 3,
            }),
        );
        node.update_commit_index();
        assert_eq!(node.commit_index(), 0, "committed on a minority");

        // A reply to a request from an older term is ignored entirely.
        let old_term = AppendEntries {
            term: 1,
            ..heartbeat.clone()
        };
        node.apply_append_entries_response(
            1,
            3,
            &old_term,
            Ok(AppendEntriesResp {
                term: 2,
                success: true,
                match_index: 3,
            }),
        );
        node.update_commit_index();
        assert_eq!(node.commit_index(), 0);

        // The real replication round commits both entries of term 2.
        let full = AppendEntries {
            entries: node.log.lock().entries_from(2).to_vec(),
            ..heartbeat
        };
        node.apply_append_entries_response(
            0,
            2,
            &full,
            Ok(AppendEntriesResp {
                term: 2,
                success: true,
                match_index: 3,
            }),
        );
        node.update_commit_index();
        assert_eq!(node.commit_index(), 3);
    }

    /// A reply carrying a newer term deposes the leader.
    #[test]
    fn higher_term_reply_deposes_leader() {
        let (_dir, node) = tmp_node(1, two_peers());
        make_leader(&node, 1);
        let ae = node.prepare_append_entries(0).expect("leader builds AE");
        node.apply_append_entries_response(
            0,
            2,
            &ae,
            Ok(AppendEntriesResp {
                term: 4,
                success: false,
                match_index: 0,
            }),
        );
        assert_eq!(node.role(), Role::Follower);
        assert_eq!(node.current_term(), 4);
        assert_eq!(node.leader_id(), 0);
        assert_eq!(node.log.lock().get_voted_for(), -1);
    }

    /// `become_leader` refuses a term that moved on while votes were being
    /// counted, and appends nothing.
    #[test]
    fn become_leader_refuses_superseded_term() {
        let (_dir, node) = tmp_node(1, two_peers());
        make_candidate(&node, 1);
        node.handle_append_entries(&AppendEntries {
            term: 2,
            leader_id: 3,
            prev_log_index: 0,
            prev_log_term: 0,
            leader_commit: 0,
            entries: vec![],
        });

        assert!(!node.become_leader(1));
        assert_eq!(node.role(), Role::Follower);
        assert_eq!(node.leader_id(), 3);
        assert_eq!(node.last_log_index(), 0, "no noop for a term we don't lead");
    }

    /// Propose on a deposed leader appends nothing.
    #[tokio::test]
    async fn propose_after_step_down_appends_nothing() {
        let (_dir, node) = tmp_node(1, two_peers());
        make_leader(&node, 1);
        node.handle_request_vote(&RequestVote {
            term: 2,
            candidate_id: 2,
            last_log_index: 1,
            last_log_term: 1,
        });
        let result = node.propose(bucket("late")).await;
        assert!(
            matches!(result, ProposeResult::NotLeader { .. }),
            "got {result:?}"
        );
        assert_eq!(node.last_log_index(), 1);
    }

    fn age_leadership(node: &RaftNode) {
        let mut ls = node.leader_state.lock();
        let state = ls.as_mut().expect("leader state");
        state.elected_at = Instant::now()
            .checked_sub(Duration::from_secs(10))
            .expect("monotonic clock is past 10 s");
    }

    /// Check-quorum: a leader that has heard from no peer for an election
    /// timeout steps down and releases its pending proposals.
    #[tokio::test]
    async fn check_quorum_steps_down_without_a_majority() {
        let (_dir, node) = tmp_node(1, two_peers());
        make_leader(&node, 1);

        let proposer = Arc::clone(&node);
        let pending = tokio::spawn(async move { proposer.propose(bucket("x")).await });
        wait_for_last_index(&node, 2).await;

        // Inside the grace period a new leader is not judged.
        node.check_quorum();
        assert!(node.is_leader());

        age_leadership(&node);
        node.check_quorum();
        assert_eq!(node.role(), Role::Follower);
        assert_eq!(node.leader_id(), 0);
        assert_eq!(
            node.current_term(),
            1,
            "stepping down does not bump the term"
        );
        assert_eq!(node.log.lock().get_voted_for(), 1, "nor clear the vote");

        let result = tokio::time::timeout(Duration::from_millis(500), pending)
            .await
            .expect("proposal released")
            .expect("join");
        assert!(
            matches!(result, ProposeResult::NotLeader { leader_id: 0 }),
            "got {result:?}"
        );
        assert_eq!(node.commit_index(), 0);
    }

    /// Check-quorum keeps a leader that one of two peers acknowledged
    /// recently (2 of 3 is a majority).
    #[test]
    fn check_quorum_keeps_leader_with_a_majority() {
        let (_dir, node) = tmp_node(1, two_peers());
        make_leader(&node, 1);
        age_leadership(&node);

        let ae = node.prepare_append_entries(0).expect("leader builds AE");
        node.apply_append_entries_response(
            0,
            2,
            &ae,
            Ok(AppendEntriesResp {
                term: 1,
                success: true,
                match_index: 1,
            }),
        );
        node.check_quorum();
        assert!(node.is_leader());

        // An acknowledgement older than the window no longer counts.
        node.leader_state.lock().as_mut().expect("leader").last_ack[0] =
            Instant::now().checked_sub(Duration::from_secs(5));
        node.check_quorum();
        assert!(!node.is_leader());
    }

    /// A single-node cluster is its own majority and never steps down.
    #[test]
    fn check_quorum_single_node_stays_leader() {
        let (_dir, node) = tmp_node(1, vec![]);
        make_leader(&node, 1);
        age_leadership(&node);
        node.check_quorum();
        assert!(node.is_leader());
    }
}
