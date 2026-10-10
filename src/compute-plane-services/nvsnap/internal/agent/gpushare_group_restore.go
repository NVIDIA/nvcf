// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// Group restore: move a multi-node instance's checkpoints into new pods,
// keeping the TCP connections between its pods (NCCL bootstrap, Gloo, ZMQ,
// TCPStore). Every new pod has a new IP, so CRIU restores each pod with one
// address map for the whole instance (--inet-addr-map) and leaves its
// connections locked (--keep-network-lock): a pod restored early must not
// send to a peer whose kernel would answer with a reset. Once every pod is
// restored, the driver unlocks them all (criu net-unlock in each pod's
// network namespace), and only then do the GPU resumes run, with the
// gpushare handle exchange.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// groupRestorePodWait bounds the wait for every target pod's IP.
const groupRestorePodWait = 10 * time.Minute

// GroupRestoreMember pairs one checkpoint of the instance with its new pod.
type GroupRestoreMember struct {
	CheckpointID         string `json:"checkpointId"`
	PlaceholderNamespace string `json:"placeholderNamespace"`
	PlaceholderPodName   string `json:"placeholderPodName"`
	// PlaceholderContainerName and ReservePIDs as in RestoreRequest.
	PlaceholderContainerName string `json:"placeholderContainerName,omitempty"`
	ReservePIDs              bool   `json:"reservePids,omitempty"`
}

// GroupRestoreRequest restores every checkpoint of one instance.
type GroupRestoreRequest struct {
	Members []GroupRestoreMember `json:"members"`
	// KeepFailedPods leaves the target pods in place when the restore
	// fails. By default they are deleted: a failed member's peers hold
	// their network lock until their pod (and its network namespace) is
	// gone.
	KeepFailedPods bool `json:"keepFailedPods,omitempty"`
}

// GroupRestoreMemberResult is one member's outcome.
type GroupRestoreMemberResult struct {
	CheckpointID string         `json:"checkpointId"`
	Pod          string         `json:"pod"`
	OldIP        string         `json:"oldIp,omitempty"`
	NewIP        string         `json:"newIp,omitempty"`
	Result       *RestoreResult `json:"result,omitempty"`
	Error        string         `json:"error,omitempty"`
}

// GroupRestoreResult reports a group restore.
type GroupRestoreResult struct {
	Session     string                     `json:"session"`
	InetAddrMap string                     `json:"inetAddrMap"`
	Members     []GroupRestoreMemberResult `json:"members"`
	Fabric      fabricOutcome              `json:"fabric"`
	DeletedPods []string                   `json:"deletedPods,omitempty"`
}

// groupRestoreEntry is what planGroupRestore checks for one member.
type groupRestoreEntry struct {
	Name         string
	OldIP, NewIP string
	Group        *GPUShareGroupInfo
}

// planGroupRestore checks that the checkpoints form one complete instance
// and that each maps to exactly one new pod, and returns the address map
// every pod restores with ("OLD=NEW,...", in the instance's index order).
func planGroupRestore(entries []groupRestoreEntry) (string, error) {
	if len(entries) < 2 {
		return "", errors.New("a group restore needs at least two members")
	}
	session := ""
	seenIndex := make(map[int]string, len(entries))
	seenOld := make(map[string]string, len(entries))
	seenNew := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.Group == nil {
			return "", fmt.Errorf("%s: the checkpoint was not taken by a group checkpoint (no gpushareGroup in its metadata)", e.Name)
		}
		if session == "" {
			session = e.Group.Session
		}
		if e.Group.Session != session {
			return "", fmt.Errorf("%s: from group checkpoint %s, the others from %s", e.Name, e.Group.Session, session)
		}
		if e.Group.Size != len(entries) {
			return "", fmt.Errorf("%s: the instance had %d pods, the request restores %d", e.Name, e.Group.Size, len(entries))
		}
		if e.Group.Index < 0 || e.Group.Index >= e.Group.Size {
			return "", fmt.Errorf("%s: index %d out of range", e.Name, e.Group.Index)
		}
		if other, dup := seenIndex[e.Group.Index]; dup {
			return "", fmt.Errorf("%s and %s are the same pod (index %d) of the instance", other, e.Name, e.Group.Index)
		}
		seenIndex[e.Group.Index] = e.Name
		oldIP, newIP := net.ParseIP(e.OldIP), net.ParseIP(e.NewIP)
		if oldIP == nil {
			return "", fmt.Errorf("%s: no valid source pod IP in the checkpoint (%q)", e.Name, e.OldIP)
		}
		if newIP == nil {
			return "", fmt.Errorf("%s: target pod has no valid IP (%q)", e.Name, e.NewIP)
		}
		if (oldIP.To4() == nil) != (newIP.To4() == nil) {
			return "", fmt.Errorf("%s: %s and %s are not the same address family", e.Name, e.OldIP, e.NewIP)
		}
		if other, dup := seenOld[oldIP.String()]; dup {
			return "", fmt.Errorf("%s and %s had the same source IP %s", other, e.Name, oldIP)
		}
		if other, dup := seenNew[newIP.String()]; dup {
			return "", fmt.Errorf("%s and %s have the same target IP %s", other, e.Name, newIP)
		}
		seenOld[oldIP.String()], seenNew[newIP.String()] = e.Name, e.Name
	}
	sorted := append([]groupRestoreEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Group.Index < sorted[j].Group.Index })
	pairs := make([]string, len(sorted))
	for i, e := range sorted {
		pairs[i] = net.ParseIP(e.OldIP).String() + "=" + net.ParseIP(e.NewIP).String()
	}
	return strings.Join(pairs, ","), nil
}

// groupRestore drives a group restore. See the file comment.
func (a *Agent) groupRestore(ctx context.Context, req GroupRestoreRequest, log *logrus.Entry) (*GroupRestoreResult, error) {
	if len(req.Members) < 2 {
		return nil, errors.New("a group restore needs at least two members")
	}
	if a.kubeClient == nil {
		return nil, errors.New("a group restore needs the kube client to find each target pod")
	}
	for i, m := range req.Members {
		if err := validPathSegment("checkpointId", m.CheckpointID); err != nil {
			return nil, fmt.Errorf("member %d: %w", i, err)
		}
		if m.PlaceholderNamespace == "" || m.PlaceholderPodName == "" {
			return nil, fmt.Errorf("member %d: placeholderNamespace and placeholderPodName required", i)
		}
	}

	// a. Every target pod must have its IP before any restore starts.
	newIPs, nodes, err := a.awaitTargetPods(ctx, req.Members, groupRestorePodWait)
	if err != nil {
		return nil, err
	}

	// b. The checkpoints' source IPs and places, read on the target nodes
	// (which fetch the checkpoints there first).
	res := &GroupRestoreResult{Members: make([]GroupRestoreMemberResult, len(req.Members))}
	entries := make([]groupRestoreEntry, len(req.Members))
	bases := make([]string, len(req.Members))
	for i, m := range req.Members {
		name := m.PlaceholderNamespace + "/" + m.PlaceholderPodName
		base, err := a.peerAgentURL(ctx, nodes[i])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		bases[i] = base
		md, err := fetchCheckpointMetadata(ctx, fabricHTTPClient, base, m.CheckpointID)
		if err != nil {
			return nil, fmt.Errorf("%s: checkpoint %s: %w", name, m.CheckpointID, err)
		}
		entries[i] = groupRestoreEntry{Name: name, OldIP: md.SourcePodIP, NewIP: newIPs[i], Group: md.GPUShareGroup}
		res.Members[i] = GroupRestoreMemberResult{CheckpointID: m.CheckpointID, Pod: name, OldIP: md.SourcePodIP, NewIP: newIPs[i]}
	}
	addrMap, err := planGroupRestore(entries)
	if err != nil {
		return nil, err
	}
	res.InetAddrMap = addrMap
	res.Session = newFabricSession()
	log = log.WithFields(logrus.Fields{"fabricSession": res.Session, "inetAddrMap": addrMap})
	log.Info("group restore: starting every member's restore")

	// c-e. Restore every pod, unlock them together, then exchange handles.
	members := make([]fabricMember, len(req.Members))
	for i, m := range req.Members {
		members[i] = &httpFabricMember{client: fabricHTTPClient, base: bases[i], session: res.Session, ns: m.PlaceholderNamespace, pod: m.PlaceholderPodName}
	}
	var mu sync.Mutex
	finished := make([]bool, len(req.Members))
	var wg sync.WaitGroup
	for i, m := range req.Members {
		wg.Add(1)
		go func(i int, m GroupRestoreMember) {
			defer wg.Done()
			r, err := postRestore(ctx, fabricHTTPClient, bases[i], RestoreRequest{
				CheckpointID:             m.CheckpointID,
				PlaceholderNamespace:     m.PlaceholderNamespace,
				PlaceholderPodName:       m.PlaceholderPodName,
				PlaceholderContainerName: m.PlaceholderContainerName,
				ReservePIDs:              m.ReservePIDs,
				GPUShareFabricSession:    res.Session,
				InetAddrMap:              addrMap,
			})
			mu.Lock()
			defer mu.Unlock()
			res.Members[i].Result = r
			if err != nil {
				res.Members[i].Error = err.Error()
			}
			finished[i] = true
		}(i, m)
	}
	done := func(i int) bool {
		mu.Lock()
		defer mu.Unlock()
		return finished[i]
	}
	res.Fabric = driveFabric(ctx, members, done, fabricDriveOpts{Unlock: true}, log)
	wg.Wait()

	var failed []string
	for _, m := range res.Members {
		if m.Error != "" {
			failed = append(failed, m.Pod+": "+m.Error)
		}
	}
	if len(failed) == 0 {
		return res, nil
	}
	// f. One member failed: the instance cannot run. Its peers hold their
	// lock until their network namespace goes away with the pod.
	if !req.KeepFailedPods {
		for _, m := range req.Members {
			name := m.PlaceholderNamespace + "/" + m.PlaceholderPodName
			if derr := a.kubeClient.CoreV1().Pods(m.PlaceholderNamespace).Delete(context.WithoutCancel(ctx), m.PlaceholderPodName, metav1.DeleteOptions{}); derr != nil {
				log.WithError(derr).WithField("pod", name).Warn("group restore: delete target pod after the failure")
				continue
			}
			res.DeletedPods = append(res.DeletedPods, name)
		}
	}
	return res, &groupRestoreFailure{fmt.Errorf("group restore %s: %d of %d members failed: %s", res.Session, len(failed), len(res.Members), strings.Join(failed, "; "))}
}

// groupRestoreFailure is a group restore that ran and failed: the
// checkpoints themselves are suspect. Any other error of groupRestore
// (a pod without an IP, a fetch that did not finish) says nothing about
// them.
type groupRestoreFailure struct{ err error }

func (e *groupRestoreFailure) Error() string { return e.err.Error() }
func (e *groupRestoreFailure) Unwrap() error { return e.err }

// awaitTargetPods waits until every member's pod is scheduled and has an
// IP, and returns the IPs and nodes in member order.
func (a *Agent) awaitTargetPods(ctx context.Context, members []GroupRestoreMember, limit time.Duration) ([]string, []string, error) {
	ips, nodes := make([]string, len(members)), make([]string, len(members))
	deadline := time.Now().Add(limit)
	for {
		missing := ""
		for i, m := range members {
			if ips[i] != "" {
				continue
			}
			pod, err := a.kubeClient.CoreV1().Pods(m.PlaceholderNamespace).Get(ctx, m.PlaceholderPodName, metav1.GetOptions{})
			if err != nil {
				return nil, nil, fmt.Errorf("target pod %s/%s: %w", m.PlaceholderNamespace, m.PlaceholderPodName, err)
			}
			if pod.Status.PodIP == "" || pod.Spec.NodeName == "" {
				missing = m.PlaceholderNamespace + "/" + m.PlaceholderPodName
				continue
			}
			ips[i], nodes[i] = pod.Status.PodIP, pod.Spec.NodeName
		}
		if missing == "" {
			return ips, nodes, nil
		}
		if time.Now().After(deadline) {
			return nil, nil, fmt.Errorf("target pod %s has no IP after %s", missing, limit)
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// fetchCheckpointMetadata makes a checkpoint local on the agent at base and
// reads its metadata there.
func fetchCheckpointMetadata(ctx context.Context, client *http.Client, base, id string) (*CheckpointMetadata, error) {
	ereq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/checkpoints/"+url.PathEscape(id)+"/ensure-local", http.NoBody)
	if err != nil {
		return nil, err
	}
	eresp, err := client.Do(ereq)
	if err != nil {
		return nil, fmt.Errorf("ensure-local: %w", err)
	}
	b, _ := io.ReadAll(io.LimitReader(eresp.Body, 512))
	eresp.Body.Close()
	if eresp.StatusCode != http.StatusNoContent && eresp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ensure-local: %s: %s", eresp.Status, strings.TrimSpace(string(b)))
	}
	mreq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/checkpoints/"+url.PathEscape(id)+"/file?path=metadata.json", http.NoBody)
	if err != nil {
		return nil, err
	}
	mresp, err := client.Do(mreq)
	if err != nil {
		return nil, fmt.Errorf("read metadata: %w", err)
	}
	defer mresp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(mresp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if mresp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("read metadata: %s: %s", mresp.Status, strings.TrimSpace(string(body[:min(len(body), 512)])))
	}
	var md CheckpointMetadata
	if err := json.Unmarshal(body, &md); err != nil {
		return nil, fmt.Errorf("decode metadata: %w", err)
	}
	return &md, nil
}

// postRestore runs one member's restore on its node's agent.
func postRestore(ctx context.Context, client *http.Client, base string, req RestoreRequest) (*RestoreResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/restore", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var r RestoreResult
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("decode restore result: %w", err)
	}
	return &r, nil
}

func (a *Agent) groupRestoreHandler(w http.ResponseWriter, r *http.Request) {
	var req GroupRestoreRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}
	res, err := a.groupRestore(r.Context(), req, logrus.WithField("op", "group-restore"))
	w.Header().Set("Content-Type", "application/json")
	if err != nil && res == nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
	}
	_ = json.NewEncoder(w).Encode(res)
}
