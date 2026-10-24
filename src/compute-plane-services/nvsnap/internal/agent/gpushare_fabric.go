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
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
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
func (a *Agent) openFabricSession(session, namespace, pod, podUID string) (*fabricSessionDir, func(), error) {
	if !validFabricSession(session) {
		return nil, nil, fmt.Errorf("gpushare: invalid fabric session %q", session)
	}
	if podUID == "" {
		return nil, nil, errors.New("gpushare: a fabric session needs the pod uid to find its store")
	}
	s, closeFn, err := a.openFabricSessionAt(session, namespace, pod, a.fabricHostDir(podUID, session))
	if err != nil {
		return nil, nil, err
	}
	s.container = fabricContainerDir(session)
	return s, closeFn, nil
}

// fabricSessionDir is a session directory, worked in through its parent: the
// pod's chunk store, or a checkpoint's, which the workload can write
// (confined_fs.go).
type fabricSessionDir struct {
	parent *confinedDir
	name   string
	// container is the directory as the workload and the tool see it.
	container string
}

// own gives the directory to the user process pid runs as: the tool and
// the restored processes write their handle lists there as that user.
func (s *fabricSessionDir) own(procBase string, pid int) error {
	uid, gid, err := procOwner(procBase, pid)
	if err != nil {
		return err
	}
	return s.parent.root.Lchown(s.name, uid, gid)
}

func (s *fabricSessionDir) read(name string, limit int64) ([]byte, error) {
	return s.parent.readFile(s.name+"/"+name, limit)
}

func (s *fabricSessionDir) exists(name string) bool { return s.parent.exists(s.name + "/" + name) }

// write writes name by rename, so the tool never reads a partial file.
func (s *fabricSessionDir) write(name string, data []byte) error {
	return s.parent.writeFileAtomic(s.name+"/"+name, data) // read by the tool inside the workload
}

// fabricSmallLimit bounds the files of a session other than the handle
// lists: a vote, a verdict.
const fabricSmallLimit = 4 << 10

// openFabricSessionAt creates host (fresh) and registers it for the pod.
// host's parent is created when missing; host itself is worked in only
// through its parent (fabricSessionDir).
func (a *Agent) openFabricSessionAt(session, namespace, pod, host string) (*fabricSessionDir, func(), error) {
	if !validFabricSession(session) {
		return nil, nil, fmt.Errorf("gpushare: invalid fabric session %q", session)
	}
	parentDir, name := filepath.Dir(host), filepath.Base(host)
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("gpushare: fabric dir parent: %w", err)
	}
	parent, err := openConfinedDir(parentDir)
	if err != nil {
		return nil, nil, fmt.Errorf("gpushare: fabric dir parent: %w", err)
	}
	s, closeFn, err := a.openFabricSessionIn(session, namespace, pod, parent, name)
	if err != nil {
		_ = parent.Close()
		return nil, nil, err
	}
	return s, func() { closeFn(); _ = parent.Close() }, nil
}

// openFabricSessionIn creates name (fresh) in parent and registers it for
// the pod. The returned func removes both; parent stays open.
func (a *Agent) openFabricSessionIn(session, namespace, pod string, parent *confinedDir, name string) (*fabricSessionDir, func(), error) {
	if err := parent.root.RemoveAll(name); err != nil {
		return nil, nil, fmt.Errorf("gpushare: clear fabric dir: %w", err)
	}
	if err := parent.root.Mkdir(name, 0o755); err != nil {
		return nil, nil, fmt.Errorf("gpushare: create fabric dir: %w", err)
	}
	s := &fabricSessionDir{parent: parent, name: name}
	key := fabricKey(session, namespace, pod)
	a.fabricSessions.Store(key, s)
	return s, func() {
		a.fabricSessions.Delete(key)
		_ = parent.root.RemoveAll(name)
	}, nil
}

// GPUShareGroupInfo places a checkpoint in its instance.
type GPUShareGroupInfo struct {
	Session string `json:"session"`
	Index   int    `json:"index"`
	Size    int    `json:"size"`
	// Companion marks a member without GPUs: restored stopped and
	// continued once the GPU ranks resume (criu_companion.go).
	Companion bool `json:"companion,omitempty"`
}

func gpushareGroupInfo(req CheckpointRequest) *GPUShareGroupInfo {
	if req.Companion && req.GroupSession != "" {
		return &GPUShareGroupInfo{Session: req.GroupSession, Index: req.GPUShareGroupIndex, Size: req.GPUShareGroupSize, Companion: true}
	}
	if req.GPUShareFabricSession == "" {
		return nil
	}
	return &GPUShareGroupInfo{Session: req.GPUShareFabricSession, Index: req.GPUShareGroupIndex, Size: req.GPUShareGroupSize}
}

// restoreV2Group is a restore's part in a group restore.
type restoreV2Group struct {
	Session, InetAddrMap string
	Namespace, Pod       string
}

// restoreSession is one pod's directory in a group restore.
type restoreSession struct {
	dir *fabricSessionDir
	// containerDir is where the gpushare tool sees it ("" without gpushare).
	containerDir string
	close        func()
}

// openRestoreSession creates the pod's session directory in the
// placeholder's work volume (webhook.RestoreWorkMountPath), which the
// placeholder mounts writable: the tool's resume uses it as --fabric-map,
// and the checkpoint itself is read-only there.
func (a *Agent) openRestoreSession(g restoreV2Group, work *confinedDir, gpushare bool) (*restoreSession, error) {
	if !validFabricSession(g.Session) {
		return nil, fmt.Errorf("gpushare: invalid fabric session %q", g.Session)
	}
	name := fabricDirPrefix + g.Session
	cdir := ""
	if gpushare {
		cdir = webhook.RestoreWorkMountPath + "/" + name
	}
	dir, closeFn, err := a.openFabricSessionIn(g.Session, g.Namespace, g.Pod, work, name)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	return &restoreSession{dir: dir, containerDir: cdir, close: func() { once.Do(closeFn) }}, nil
}

// fabricUnlockWait bounds the hold: TCP gives up on a connection held
// longer than about 13 minutes.
const fabricUnlockWait = 10 * time.Minute

// awaitUnlock reports this pod restored and waits for the driver's
// verdict, "unlock" or "abort".
func (s *restoreSession) awaitUnlock(ctx context.Context, log *logrus.Entry) (string, error) {
	if err := s.dir.write("restored", []byte("1\n")); err != nil {
		return "", fmt.Errorf("group restore: report restored: %w", err)
	}
	log.Info("group restore: CRIU restore done, holding the network lock until every pod is restored")
	return waitFabricVerdictFile(ctx, s.dir, "u", fabricUnlockWait)
}

// waitFabricVerdictFile polls name in the session for "unlock" or "abort".
func waitFabricVerdictFile(ctx context.Context, s *fabricSessionDir, name string, limit time.Duration) (string, error) {
	t0 := time.Now()
	for {
		if b, err := s.read(name, fabricSmallLimit); err == nil {
			v := strings.TrimSpace(string(b))
			if v == "unlock" || v == fabricVerdictAbort {
				return v, nil
			}
			return "", fmt.Errorf("group restore: unexpected verdict %q", v)
		}
		if time.Since(t0) > limit {
			return "", fmt.Errorf("group restore: no unlock after %s; the other pods did not finish restoring", limit)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(fabricPollInterval):
		}
	}
}

// netUnlockArgs releases a group restore's network lock: CRIU reads the
// lock's name from the images and removes it in the pod's network
// namespace (through libnftables; no nft binary needed). The agent's own
// CRIU runs it, with only the network namespace entered, so the binary and
// the images are the agent's paths.
func netUnlockArgs(hostPID int, imagesDir string) []string {
	return []string{"-t", strconv.Itoa(hostPID), "-n", "--",
		v2BinDirInContainer + "/criu", "net-unlock", "-D", imagesDir, "-o", "net-unlock.log"}
}

func netUnlock(ctx context.Context, hostPID int, imagesDir string, log *logrus.Entry) error {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	t0 := time.Now()
	out, err := exec.CommandContext(cctx, "nsenter", netUnlockArgs(hostPID, imagesDir)...).CombinedOutput() //nolint:gosec // fixed binary; args are a pid and a path built here
	if err != nil {
		return fmt.Errorf("criu net-unlock: %w (output: %s; net-unlock.log tail: %s)", err,
			strings.TrimSpace(string(out)), tailOfFile(filepath.Join(imagesDir, "net-unlock.log"), 6))
	}
	log.WithField("duration", time.Since(t0).Round(time.Millisecond).String()).Info("group restore: network unlocked")
	return nil
}

func criuSupportsNetMigration(help string) bool {
	return strings.Contains(help, "--inet-addr-map") && strings.Contains(help, "net-unlock")
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
	// Restored reports a group restore member's CRIU restore done (it then
	// holds its network lock until the driver unlocks it).
	Restored bool `json:"restored,omitempty"`
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
func readFabricState(s *fabricSessionDir) (fabricState, error) {
	var st fabricState
	if b, err := s.read("q", fabricSmallLimit); err == nil {
		v, perr := parseFabricVote(string(b))
		if perr != nil {
			return st, perr
		}
		st.Vote = v
	} else if !errors.Is(err, os.ErrNotExist) {
		return st, err
	}
	if b, err := s.read("out", fabricInLimit); err == nil {
		s := string(b)
		st.Out = &s
	} else if !errors.Is(err, os.ErrNotExist) {
		return st, err
	}
	st.HaveIn, st.Restored = s.exists("in"), s.exists("restored")
	return st, nil
}

// Member endpoints.

func (a *Agent) fabricDirFor(w http.ResponseWriter, r *http.Request) (*fabricSessionDir, bool) {
	v := mux.Vars(r)
	if !validFabricSession(v["session"]) {
		http.Error(w, "invalid session", http.StatusBadRequest)
		return nil, false
	}
	d, ok := a.fabricSessions.Load(fabricKey(v["session"], v["namespace"], v["pod"]))
	if !ok {
		// Not yet registered (the member's checkpoint has not reached the
		// suspend) or already closed: the driver polls again.
		http.Error(w, "no such fabric session on this node", http.StatusNotFound)
		return nil, false
	}
	return d.(*fabricSessionDir), true
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
	if err := dir.write("d", []byte(fmt.Sprintf("%d %s\n", attempt, verdict))); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Agent) fabricUnlockHandler(w http.ResponseWriter, r *http.Request) {
	dir, ok := a.fabricDirFor(w, r)
	if !ok {
		return
	}
	verdict := r.URL.Query().Get("verdict")
	if verdict != "unlock" && verdict != fabricVerdictAbort {
		http.Error(w, "verdict (unlock, abort) required", http.StatusBadRequest)
		return
	}
	if err := dir.write("u", []byte(verdict+"\n")); err != nil {
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
	if err := dir.write("in", data); err != nil {
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
	Unlock(ctx context.Context, verdict string) error
}

// fabricOutcome records how the driver ended a session, for the result.
type fabricOutcome struct {
	Attempts  int  `json:"attempts,omitempty"`
	Went      bool `json:"went,omitempty"`
	Unlocked  bool `json:"unlocked,omitempty"`
	Exchanged bool `json:"exchanged"`
	Handles   int  `json:"handles"`
}

// fabricDriveOpts picks the phases a session runs.
type fabricDriveOpts struct {
	// Vote: a suspend's quiesce vote (group checkpoint).
	Vote bool
	// LostOther reports a member of the group outside the session (a
	// companion) that ended before the vote passed: the capture is lost,
	// so the vote is aborted too. Nil: none.
	LostOther func() bool
	// OnGo runs once, when the vote goes: the GPU ranks stop now.
	OnGo func()
	// Unlock: hold every member until all are restored, then unlock them
	// together (group restore). The exchange waits for the unlock.
	Unlock bool
	// NoExchange: this drive does not exchange handle lists (another drive
	// of the session does, over the members that have them).
	NoExchange bool
}

// driveFabric runs one session until every member's operation has returned
// (done reports that per member) or ctx ends.
//
// Vote: once every member still running has voted the same attempt, the
// answer is "go" if all are quiesced, else "retry". A member whose
// checkpoint returned before the go cannot release: every later vote is
// answered "abort", and the others roll back at their tool's timeout.
//
// Unlock: once every member has reported its CRIU restore done, all get
// "unlock". A member that returns first has failed; the rest get "abort".
//
// Exchange: once every member still running has written its out, the
// lists are concatenated in member order and delivered once to all of
// them. A member whose operation returned without an out (it failed, or
// does not resume) takes no part.
func driveFabric(ctx context.Context, members []fabricMember, done func(i int) bool, opts fabricDriveOpts, log *logrus.Entry) fabricOutcome {
	var out fabricOutcome
	vote := &fabricVoter{decided: -1, abortSent: map[int]int{}}
	aborted := false
	// Each phase is sent to every running member and retried, each poll, to
	// the ones it did not reach; it is complete only once all have it. A
	// member waits for its message, and the receivers' writes are atomic
	// and idempotent, so a repeat is harmless and a loss is not.
	var unlock, exchange *fabricDelivery
	unlockDone, exchanged := false, false
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
		lostOne := len(running) < len(members)

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

		if opts.Vote && !out.Went {
			if (lostOne || (opts.LostOther != nil && opts.LostOther())) && !aborted {
				aborted = true
				log.Warn("gpushare fabric: a member ended before the quiesce vote passed; aborting the vote")
			}
			vote.drive(ctx, members, running, states, aborted, &out, log)
			if out.Went && opts.OnGo != nil {
				opts.OnGo()
			}
		}

		if opts.Unlock && !unlockDone {
			if unlock == nil {
				verdict := ""
				if lostOne {
					verdict = fabricVerdictAbort
				} else {
					all := true
					for _, i := range running {
						if st, ok := states[i]; !ok || !st.Restored {
							all = false
							break
						}
					}
					if all {
						verdict = "unlock"
					}
				}
				if verdict != "" {
					unlock = &fabricDelivery{what: "unlock verdict", value: verdict, sent: map[int]bool{}}
				}
			}
			if unlock != nil && unlock.deliver(running, func(i int) error { return members[i].Unlock(ctx, unlock.value) }, members, log) {
				unlockDone = true
				out.Unlocked = unlock.value == "unlock"
				log.WithFields(logrus.Fields{"verdict": unlock.value, "members": len(running)}).Info("gpushare fabric: group restore network lock")
			}
		}

		if !exchanged && !opts.NoExchange && (!opts.Unlock || out.Unlocked) {
			if exchange == nil {
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
					exchange = &fabricDelivery{what: "handle list", value: merged.String(), sent: map[int]bool{}}
				}
			}
			if exchange != nil && exchange.deliver(running, func(i int) error { return members[i].PutIn(ctx, []byte(exchange.value)) }, members, log) {
				exchanged = true
				out.Exchanged = true
				out.Handles = strings.Count(exchange.value, "\n")
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

// fabricDelivery is one message to every member, tracked per member.
type fabricDelivery struct {
	what, value string
	sent        map[int]bool
}

// deliver sends to the running members not yet reached and reports whether
// every running member now has the message.
func (d *fabricDelivery) deliver(running []int, send func(i int) error, members []fabricMember, log *logrus.Entry) bool {
	all := true
	for _, i := range running {
		if d.sent[i] {
			continue
		}
		if err := send(i); err != nil {
			log.WithError(err).WithField("member", members[i].Name()).Warn("gpushare fabric: deliver " + d.what + "; retrying")
			all = false
			continue
		}
		d.sent[i] = true
	}
	return all
}

// fabricVoter answers the quiesce vote round by round. A verdict is
// decided only once every running member has received it.
type fabricVoter struct {
	decided   int // highest attempt every member has the verdict for
	pending   *fabricDelivery
	attempt   int
	abortSent map[int]int // member -> highest attempt it has the abort for
}

func (v *fabricVoter) drive(ctx context.Context, members []fabricMember, running []int, states map[int]fabricState, aborted bool, out *fabricOutcome, log *logrus.Entry) {
	if aborted {
		// An abort answers each member's own attempt as it votes.
		for _, i := range running {
			st, ok := states[i]
			if !ok || st.Vote == nil || st.Vote.Attempt <= v.decided {
				continue
			}
			if got, ok := v.abortSent[i]; ok && got >= st.Vote.Attempt {
				continue
			}
			if err := members[i].Verdict(ctx, st.Vote.Attempt, fabricVerdictAbort); err != nil {
				log.WithError(err).WithField("member", members[i].Name()).Warn("gpushare fabric: abort verdict; retrying")
				continue
			}
			v.abortSent[i] = st.Vote.Attempt
		}
		return
	}
	if v.pending == nil {
		attempt, allOK := -1, true
		for _, i := range running {
			st, ok := states[i]
			if !ok || st.Vote == nil {
				return
			}
			if attempt == -1 {
				attempt = st.Vote.Attempt
			}
			if st.Vote.Attempt != attempt {
				return
			}
			allOK = allOK && st.Vote.OK
		}
		if attempt <= v.decided {
			return
		}
		verdict := "retry"
		if allOK {
			verdict = "go"
		}
		v.pending, v.attempt = &fabricDelivery{what: "verdict", value: verdict, sent: map[int]bool{}}, attempt
	}
	p := v.pending
	if !p.deliver(running, func(i int) error { return members[i].Verdict(ctx, v.attempt, p.value) }, members, log) {
		return
	}
	v.decided, v.pending = v.attempt, nil
	out.Attempts = v.attempt + 1
	out.Went = p.value == "go"
	log.WithFields(logrus.Fields{"attempt": v.attempt, "verdict": p.value}).Info("gpushare fabric: quiesce vote")
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

func (m *httpFabricMember) Unlock(ctx context.Context, verdict string) error {
	return m.send(ctx, m.url("/unlock?verdict="+url.QueryEscape(verdict)), nil)
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
	// LeaveRunning applies to every member: true suspends and resumes the
	// instance in place around the dump; false dumps it for a move, and
	// the network lock then holds the source pods' connections until the
	// pods are deleted.
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
	if a.kubeClient == nil {
		return nil, errors.New("a group checkpoint needs the kube client to find each member's node")
	}
	session := newFabricSession()
	log = log.WithField("fabricSession", session)

	members := make([]fabricMember, len(req.Members))
	bases := make([]string, len(req.Members))
	uids := make([]types.UID, len(req.Members))
	for i, m := range req.Members {
		if m.Namespace == "" || m.PodName == "" {
			return nil, fmt.Errorf("member %d: namespace and podName required", i)
		}
		pod, err := a.kubeClient.CoreV1().Pods(m.Namespace).Get(ctx, m.PodName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("member %s/%s: %w", m.Namespace, m.PodName, err)
		}
		uids[i] = pod.UID
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
	// Companions have no GPU state: no fabric session, no vote. Only the
	// GPU members take part in the fabric.
	var fabric []fabricMember
	var fabricIdx []int
	for i, m := range req.Members {
		if !m.Companion {
			fabric = append(fabric, members[i])
			fabricIdx = append(fabricIdx, i)
		}
	}

	res := &GroupCheckpointResult{Session: session, Members: make([]GroupMemberResult, len(req.Members))}
	var mu sync.Mutex
	finished := make([]bool, len(req.Members))
	var wg sync.WaitGroup
	for i, m := range req.Members {
		m.LeaveRunning = req.LeaveRunning
		if m.Companion {
			m.GroupSession = session
		} else {
			m.GPUShareFabricSession = session
		}
		m.GPUShareGroupIndex, m.GPUShareGroupSize = i, len(req.Members)
		m.GroupFreeze = true
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
		return finished[fabricIdx[i]]
	}
	// Every member, GPU rank or companion, waits at the group's freeze
	// point once ready; released together, they stop together.
	freezeMembers := make([]freezeMember, len(req.Members))
	for i, m := range req.Members {
		i := i
		freezeMembers[i] = freezeMember{Base: bases[i], Namespace: m.Namespace, Pod: m.PodName, Done: func() bool {
			mu.Lock()
			defer mu.Unlock()
			return finished[i]
		}}
	}
	// Once the GPU ranks stop, the rest of the instance stops with them:
	// every member pod is held (pod_hold.go), a rank's engine container
	// excepted while it saves its GPU memory. The members are released
	// from the freeze point only once all are held.
	holdsDone := make(chan struct{})
	var holding atomic.Bool
	onGo := sync.OnceFunc(func() {
		holding.Store(true)
		go func() {
			defer close(holdsDone)
			holdMembers(ctx, fabricHTTPClient, session, req.Members, bases, log)
		}()
	})
	defer func() {
		// A hold still on its way would land after this release and keep
		// its pod frozen: let every hold land, or fail, first.
		if holding.Load() {
			<-holdsDone
		}
		releaseMembers(ctx, fabricHTTPClient, session, req.Members, bases, log)
	}()
	freezeDone := make(chan struct{})
	go func() {
		defer close(freezeDone)
		if driveGroupFreeze(ctx, fabricHTTPClient, session, "", freezeMembers, 200*time.Millisecond, log, holdsDone) {
			// Dumped, the members wait stopped until all are: none
			// resumes to talk to one not dumped yet.
			driveGroupFreeze(ctx, fabricHTTPClient, session, "post", freezeMembers, 100*time.Millisecond, log, nil)
		}
	}()
	// A companion that ends before the vote passed loses the capture: the
	// GPU ranks must not suspend for it.
	lostCompanion := func() bool {
		mu.Lock()
		defer mu.Unlock()
		for i := range req.Members {
			if req.Members[i].Companion && finished[i] {
				return true
			}
		}
		return false
	}
	res.Fabric = driveFabric(ctx, fabric, done, fabricDriveOpts{Vote: true, LostOther: lostCompanion, OnGo: onGo}, log)
	wg.Wait()
	<-freezeDone

	var failed []string
	replace := false
	for _, m := range res.Members {
		if m.Error != "" {
			failed = append(failed, m.Namespace+"/"+m.PodName+": "+m.Error)
			replace = replace || sourceMustBeReplacedText(m.Error)
		}
	}
	if replace {
		// A member left suspended or locked is one engine with the others:
		// its controllers replace every pod of it, cold. (A member does not
		// replace itself in a group: checkpoint.go.)
		log.Error("group checkpoint: a member was left unusable; deleting every member's pod")
		a.deleteMembers(context.WithoutCancel(ctx), req.Members, uids, log)
	}
	if len(failed) > 0 {
		return res, fmt.Errorf("group checkpoint %s: %d of %d members failed: %s", session, len(failed), len(res.Members), strings.Join(failed, "; "))
	}
	return res, nil
}

// deleteMembers deletes the members' pods, each bound to the uid the
// driver found: a pod since replaced under the same name is left alone.
func (a *Agent) deleteMembers(ctx context.Context, members []CheckpointRequest, uids []types.UID, log *logrus.Entry) {
	for i := range members {
		m := &members[i]
		opts := metav1.DeleteOptions{}
		if uids[i] != "" {
			opts.Preconditions = &metav1.Preconditions{UID: &uids[i]}
		}
		if err := a.kubeClient.CoreV1().Pods(m.Namespace).Delete(ctx, m.PodName, opts); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			log.WithError(err).WithField("pod", m.Namespace+"/"+m.PodName).Error("group checkpoint: cannot delete a member's pod")
		}
	}
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
