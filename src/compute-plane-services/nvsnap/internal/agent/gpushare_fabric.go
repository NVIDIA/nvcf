// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// Multi-node gpushare. A workload whose ranks share GPU memory across nodes
// (multi-node NVLink, fabric handles) is suspended and resumed by one
// nvsnap-gpu-suspend per pod, and those tools must agree twice:
//
//   - Quiesce vote. No node may release memory while another node's kernels
//     still read it. Each attempt, every tool writes DIR/q
//     ("<attempt> ok|busy") and waits for DIR/d ("<attempt> go|retry").
//   - Handle exchange. Fabric handles change across a restore. After load,
//     every tool writes its new handles to DIR/out and waits for DIR/in,
//     the out files of every pod concatenated.
//
// DIR is a fabric session directory the agent creates host-side inside the
// pod's gpushare store (the workload sees it under GPUShareStoreInContainer),
// fresh for every session, so a list from an earlier cycle can never be
// merged into a later one. Each agent serves the directories of its pods
// (fabricStateHandler and friends); the agent that receives a group
// checkpoint drives the session: it starts every member's checkpoint and
// answers the votes and the exchange through those endpoints.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// fabricDirPrefix names a session's directory inside a pod's store.
	// The dot keeps it apart from the tool's own chunk lists.
	fabricDirPrefix = ".fabric-"

	// fabricEnv switches libnvsnap_gpushare to fabric handles for memory
	// shared across nodes; the webhook sets it on pods annotated
	// GPUShareFabricAnnotation.
	fabricEnv = "NVSNAP_GPUSHARE_FABRIC"

	// fabricVerdictAbort answers a vote once the session cannot succeed.
	// The tool treats any verdict but "go" as "retry" and gives up at its
	// own timeout, rolling the workload back.
	fabricVerdictAbort = "abort"

	fabricPollInterval = 200 * time.Millisecond
)

var fabricSessionRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// validFabricSession reports whether s can name a session directory.
func validFabricSession(s string) bool { return fabricSessionRe.MatchString(s) }

// newFabricSession returns a fresh session id.
func newFabricSession() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "s" + hex.EncodeToString(b)
}

// fabricKey identifies one pod's directory in a session.
func fabricKey(session, namespace, pod string) string {
	return session + "/" + namespace + "/" + pod
}

// fabricContainerDir is the session directory as the workload (and the tool
// running in its namespaces) sees it.
func fabricContainerDir(session string) string {
	return GPUShareStoreInContainer + "/" + fabricDirPrefix + session
}

// fabricHostDir is the session directory as the agent sees it, in the pod's
// store under the checkpoint dir.
func (a *Agent) fabricHostDir(podUID, session string) string {
	return filepath.Join(a.config.CheckpointDir, GPUSharePodStoresSubdir, podUID, fabricDirPrefix+session)
}

// openFabricSession creates a pod's session directory and registers it, so
// the driver can reach it. The returned func removes both.
func (a *Agent) openFabricSession(session, namespace, pod, podUID string) (containerDir string, closeFn func(), err error) {
	if !validFabricSession(session) {
		return "", nil, fmt.Errorf("gpushare: invalid fabric session %q", session)
	}
	if podUID == "" {
		return "", nil, errors.New("gpushare: a fabric session needs the pod uid to find its store")
	}
	host := a.fabricHostDir(podUID, session)
	if err := os.RemoveAll(host); err != nil {
		return "", nil, fmt.Errorf("gpushare: clear fabric dir: %w", err)
	}
	if err := os.MkdirAll(host, 0o755); err != nil {
		return "", nil, fmt.Errorf("gpushare: create fabric dir: %w", err)
	}
	key := fabricKey(session, namespace, pod)
	a.fabricSessions.Store(key, host)
	return fabricContainerDir(session), func() {
		a.fabricSessions.Delete(key)
		_ = os.RemoveAll(host)
	}, nil
}

// fabricVote is one tool's vote for one attempt.
type fabricVote struct {
	Attempt int  `json:"attempt"`
	OK      bool `json:"ok"`
}

// fabricState is what a driver needs to know about one pod's session.
type fabricState struct {
	Vote *fabricVote `json:"vote,omitempty"`
	// Out is the pod's handle list, once its tool has written it.
	Out *string `json:"out,omitempty"`
	// HaveIn reports whether the merged list has been delivered.
	HaveIn bool `json:"haveIn"`
}

// parseFabricVote reads "<attempt> ok|busy".
func parseFabricVote(s string) (*fabricVote, error) {
	f := strings.Fields(s)
	if len(f) != 2 || (f[1] != "ok" && f[1] != "busy") {
		return nil, fmt.Errorf("malformed vote %q", strings.TrimSpace(s))
	}
	n, err := strconv.Atoi(f[0])
	if err != nil || n < 0 {
		return nil, fmt.Errorf("malformed vote %q", strings.TrimSpace(s))
	}
	return &fabricVote{Attempt: n, OK: f[1] == "ok"}, nil
}

// readFabricState reads a session directory. The tool writes q and out by
// rename, so a file that exists is complete.
func readFabricState(dir string) (fabricState, error) {
	var st fabricState
	if b, err := os.ReadFile(filepath.Join(dir, "q")); err == nil {
		v, perr := parseFabricVote(string(b))
		if perr != nil {
			return st, perr
		}
		st.Vote = v
	} else if !errors.Is(err, os.ErrNotExist) {
		return st, err
	}
	if b, err := os.ReadFile(filepath.Join(dir, "out")); err == nil {
		s := string(b)
		st.Out = &s
	} else if !errors.Is(err, os.ErrNotExist) {
		return st, err
	}
	if _, err := os.Stat(filepath.Join(dir, "in")); err == nil {
		st.HaveIn = true
	}
	return st, nil
}

// writeFileAtomic writes name in dir through a rename, so the tool never
// reads a partial file.
func writeFileAtomic(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil { //nolint:gosec // read by the tool inside the workload
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}

// Member endpoints.

func (a *Agent) fabricDirFor(w http.ResponseWriter, r *http.Request) (string, bool) {
	v := mux.Vars(r)
	if !validFabricSession(v["session"]) {
		http.Error(w, "invalid session", http.StatusBadRequest)
		return "", false
	}
	d, ok := a.fabricSessions.Load(fabricKey(v["session"], v["namespace"], v["pod"]))
	if !ok {
		// Not yet registered (the member's checkpoint has not reached the
		// suspend) or already closed: the driver polls again.
		http.Error(w, "no such fabric session on this node", http.StatusNotFound)
		return "", false
	}
	return d.(string), true
}

func (a *Agent) fabricStateHandler(w http.ResponseWriter, r *http.Request) {
	dir, ok := a.fabricDirFor(w, r)
	if !ok {
		return
	}
	st, err := readFabricState(dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

func (a *Agent) fabricVerdictHandler(w http.ResponseWriter, r *http.Request) {
	dir, ok := a.fabricDirFor(w, r)
	if !ok {
		return
	}
	attempt, err := strconv.Atoi(r.URL.Query().Get("attempt"))
	verdict := r.URL.Query().Get("verdict")
	if err != nil || attempt < 0 || (verdict != "go" && verdict != "retry" && verdict != fabricVerdictAbort) {
		http.Error(w, "attempt (>= 0) and verdict (go, retry, abort) required", http.StatusBadRequest)
		return
	}
	if err := writeFileAtomic(dir, "d", []byte(fmt.Sprintf("%d %s\n", attempt, verdict))); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fabricInLimit bounds a merged handle list: one line per shared allocation
// per process, around a hundred bytes each.
const fabricInLimit = 64 << 20

func (a *Agent) fabricInHandler(w http.ResponseWriter, r *http.Request) {
	dir, ok := a.fabricDirFor(w, r)
	if !ok {
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, fabricInLimit+1))
	if err != nil || len(data) > fabricInLimit {
		http.Error(w, "unreadable or oversized handle list", http.StatusBadRequest)
		return
	}
	if err := writeFileAtomic(dir, "in", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Driver.

// fabricMember is one pod of a session as the driver reaches it.
type fabricMember interface {
	Name() string
	// State returns ok=false while the member's session is not registered.
	State(ctx context.Context) (st fabricState, ok bool, err error)
	Verdict(ctx context.Context, attempt int, verdict string) error
	PutIn(ctx context.Context, data []byte) error
}

// fabricOutcome records how the driver ended a session, for the result.
type fabricOutcome struct {
	Attempts  int  `json:"attempts"`
	Went      bool `json:"went"`
	Exchanged bool `json:"exchanged"`
	Handles   int  `json:"handles"`
}

// driveFabric runs the votes and the exchange of one session until every
// member's checkpoint has returned (done reports that per member) or ctx
// ends.
//
// Votes: once every member still running has voted the same attempt, the
// answer is "go" if all are quiesced, else "retry". A member whose
// checkpoint returned before the go cannot release: every later vote is
// answered "abort", and the others roll back at their tool's timeout.
//
// Exchange: once every member still running has written its out, the
// lists are concatenated in member order and delivered once to all of
// them. A member whose checkpoint returned without an out (it failed, or
// does not resume) takes no part.
func driveFabric(ctx context.Context, members []fabricMember, done func(i int) bool, log *logrus.Entry) fabricOutcome {
	var out fabricOutcome
	decided := -1 // highest attempt answered
	aborted := false
	exchanged := false
	tick := time.NewTicker(fabricPollInterval)
	defer tick.Stop()
	for {
		running := make([]int, 0, len(members))
		for i := range members {
			if !done(i) {
				running = append(running, i)
			}
		}
		if len(running) == 0 {
			return out
		}
		if !out.Went && len(running) < len(members) && !aborted {
			aborted = true
			log.Warn("gpushare fabric: a member ended before the quiesce vote passed; aborting the vote")
		}

		states := make(map[int]fabricState, len(running))
		for _, i := range running {
			st, ok, err := members[i].State(ctx)
			if err != nil {
				log.WithError(err).WithField("member", members[i].Name()).Debug("gpushare fabric: state")
				continue
			}
			if ok {
				states[i] = st
			}
		}

		// Votes.
		if !out.Went {
			attempt, all, allOK := -1, true, true
			for _, i := range running {
				st, ok := states[i]
				if !ok || st.Vote == nil {
					all = false
					break
				}
				if attempt == -1 {
					attempt = st.Vote.Attempt
				}
				if st.Vote.Attempt != attempt {
					all = false
					break
				}
				allOK = allOK && st.Vote.OK
			}
			// An abort answers each member's own attempt as it votes.
			if aborted {
				for _, i := range running {
					if st, ok := states[i]; ok && st.Vote != nil && st.Vote.Attempt > decided {
						if err := members[i].Verdict(ctx, st.Vote.Attempt, fabricVerdictAbort); err != nil {
							log.WithError(err).WithField("member", members[i].Name()).Warn("gpushare fabric: abort verdict")
						}
					}
				}
			} else if all && attempt > decided {
				verdict := "retry"
				if allOK {
					verdict = "go"
				}
				for _, i := range running {
					if err := members[i].Verdict(ctx, attempt, verdict); err != nil {
						log.WithError(err).WithField("member", members[i].Name()).Warn("gpushare fabric: verdict")
					}
				}
				decided = attempt
				out.Attempts = attempt + 1
				out.Went = verdict == "go"
				log.WithFields(logrus.Fields{"attempt": attempt, "verdict": verdict}).Info("gpushare fabric: quiesce vote")
			}
		}

		// Exchange.
		if !exchanged {
			var merged bytes.Buffer
			ready := true
			for _, i := range running {
				st, ok := states[i]
				if !ok || st.Out == nil {
					ready = false
					break
				}
				merged.WriteString(*st.Out)
				if n := merged.Len(); n > 0 && merged.Bytes()[n-1] != '\n' {
					merged.WriteByte('\n')
				}
			}
			if ready {
				for _, i := range running {
					if err := members[i].PutIn(ctx, merged.Bytes()); err != nil {
						log.WithError(err).WithField("member", members[i].Name()).Warn("gpushare fabric: deliver handle list")
					}
				}
				exchanged = true
				out.Exchanged = true
				out.Handles = strings.Count(merged.String(), "\n")
				log.WithFields(logrus.Fields{"members": len(running), "handles": out.Handles}).Info("gpushare fabric: handle lists exchanged")
			}
		}

		select {
		case <-ctx.Done():
			return out
		case <-tick.C:
		}
	}
}

// httpFabricMember reaches a member's session on its node's agent.
type httpFabricMember struct {
	client           *http.Client
	base             string // http://<node>:<port>
	session, ns, pod string
}

func (m *httpFabricMember) Name() string { return m.ns + "/" + m.pod }

func (m *httpFabricMember) url(suffix string) string {
	return fmt.Sprintf("%s/v1/gpushare/fabric/%s/%s/%s%s", m.base, url.PathEscape(m.session),
		url.PathEscape(m.ns), url.PathEscape(m.pod), suffix)
}

func (m *httpFabricMember) State(ctx context.Context) (fabricState, bool, error) {
	var st fabricState
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.url(""), http.NoBody)
	if err != nil {
		return st, false, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return st, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return st, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return st, false, fmt.Errorf("state %s: %s: %s", m.Name(), resp.Status, strings.TrimSpace(string(b)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return st, false, err
	}
	return st, true, nil
}

func (m *httpFabricMember) send(ctx context.Context, u string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s: %s", m.Name(), resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func (m *httpFabricMember) Verdict(ctx context.Context, attempt int, verdict string) error {
	return m.send(ctx, m.url(fmt.Sprintf("/verdict?attempt=%d&verdict=%s", attempt, url.QueryEscape(verdict))), nil)
}

func (m *httpFabricMember) PutIn(ctx context.Context, data []byte) error {
	return m.send(ctx, m.url("/in"), data)
}

// fabricHTTPClient carries the agent token to peers. No overall timeout: a
// member's checkpoint call runs as long as the suspend, dump and resume do,
// bounded by the caller's context.
var fabricHTTPClient = &http.Client{
	Transport: &authTransport{base: http.DefaultTransport},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// GroupCheckpointRequest checkpoints the pods of one workload whose GPU
// memory is shared across nodes, as one fabric session.
type GroupCheckpointRequest struct {
	Members []CheckpointRequest `json:"members"`
	// LeaveRunning applies to every member. The session needs it: a member
	// that stops after its dump cannot take part in the others' resume.
	LeaveRunning bool `json:"leaveRunning"`
}

// GroupMemberResult is one member's outcome.
type GroupMemberResult struct {
	Namespace string            `json:"namespace"`
	PodName   string            `json:"podName"`
	Result    *CheckpointResult `json:"result,omitempty"`
	Error     string            `json:"error,omitempty"`
}

// GroupCheckpointResult reports a group checkpoint.
type GroupCheckpointResult struct {
	Session string              `json:"session"`
	Members []GroupMemberResult `json:"members"`
	Fabric  fabricOutcome       `json:"fabric"`
}

// groupCheckpoint drives a fabric session: it asks each member's agent to
// checkpoint its pod in the session and answers the votes and the
// exchange until all of them return.
func (a *Agent) groupCheckpoint(ctx context.Context, req GroupCheckpointRequest, log *logrus.Entry) (*GroupCheckpointResult, error) {
	if len(req.Members) < 2 {
		return nil, errors.New("a group checkpoint needs at least two members")
	}
	if !req.LeaveRunning {
		return nil, errors.New("a group checkpoint requires leaveRunning: a member stopped by its dump cannot take part in the others' resume")
	}
	if a.kubeClient == nil {
		return nil, errors.New("a group checkpoint needs the kube client to find each member's node")
	}
	session := newFabricSession()
	log = log.WithField("fabricSession", session)

	members := make([]fabricMember, len(req.Members))
	bases := make([]string, len(req.Members))
	for i, m := range req.Members {
		if m.Namespace == "" || m.PodName == "" {
			return nil, fmt.Errorf("member %d: namespace and podName required", i)
		}
		pod, err := a.kubeClient.CoreV1().Pods(m.Namespace).Get(ctx, m.PodName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("member %s/%s: %w", m.Namespace, m.PodName, err)
		}
		if pod.Spec.NodeName == "" {
			return nil, fmt.Errorf("member %s/%s is not scheduled", m.Namespace, m.PodName)
		}
		base, err := a.peerAgentURL(ctx, pod.Spec.NodeName)
		if err != nil {
			return nil, fmt.Errorf("member %s/%s: %w", m.Namespace, m.PodName, err)
		}
		bases[i] = base
		members[i] = &httpFabricMember{client: fabricHTTPClient, base: base, session: session, ns: m.Namespace, pod: m.PodName}
	}

	res := &GroupCheckpointResult{Session: session, Members: make([]GroupMemberResult, len(req.Members))}
	var mu sync.Mutex
	finished := make([]bool, len(req.Members))
	var wg sync.WaitGroup
	for i, m := range req.Members {
		m.LeaveRunning = true
		m.GPUShareFabricSession = session
		wg.Add(1)
		go func(i int, m CheckpointRequest) {
			defer wg.Done()
			r, err := postCheckpoint(ctx, fabricHTTPClient, bases[i], m)
			mu.Lock()
			defer mu.Unlock()
			res.Members[i] = GroupMemberResult{Namespace: m.Namespace, PodName: m.PodName, Result: r}
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
	res.Fabric = driveFabric(ctx, members, done, log)
	wg.Wait()

	var failed []string
	for _, m := range res.Members {
		if m.Error != "" {
			failed = append(failed, m.Namespace+"/"+m.PodName+": "+m.Error)
		}
	}
	if len(failed) > 0 {
		return res, fmt.Errorf("group checkpoint %s: %d of %d members failed: %s", session, len(failed), len(res.Members), strings.Join(failed, "; "))
	}
	return res, nil
}

// postCheckpoint runs one member's checkpoint on its node's agent.
func postCheckpoint(ctx context.Context, client *http.Client, base string, req CheckpointRequest) (*CheckpointResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/checkpoint", bytes.NewReader(body))
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
	var r CheckpointResult
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("decode checkpoint result: %w", err)
	}
	return &r, nil
}

func (a *Agent) groupCheckpointHandler(w http.ResponseWriter, r *http.Request) {
	var req GroupCheckpointRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}
	res, err := a.groupCheckpoint(r.Context(), req, logrus.WithField("op", "group-checkpoint"))
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
