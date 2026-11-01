// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// A restored pod's captured address (docs/design/RESTORE-POD-ADDRESS.md).
// The restored processes remember the pod IP of the captured pod. CRIU moves
// the sockets that existed at the dump; a socket opened after the restore
// (NCCL opens peer connections lazily, on first use) binds the captured
// address, which the new pod does not have. With the switch on, the restore
// makes the captured address local on the pod's loopback (never advertised)
// and translates it at the pod's edge:
//
//   - what leaves from the captured address carries the pod IP (SNAT);
//   - a connection to the pod IP that no socket bound to the pod IP or to
//     the wildcard claims goes to the captured address (DNAT): the pod's
//     own sockets, restored or opened after the restore, keep the captured
//     address, so CRIU is not asked to move them (the pod's entry leaves its
//     inet address map) and the pod reaches its own captured address
//     locally.
//
// A local captured address is unsafe when another pod uses it: guards skip
// it when it is a new address of the restored instance or the address of a
// pod in the cluster, and the restore continues as without the switch.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/metrics"
)

// Results of a captured-address plan (the metric's result label).
const (
	capturedAddrApplied       = "applied"
	capturedAddrUnchanged     = "unchanged"
	capturedAddrSkipMember    = "skipped_member"
	capturedAddrSkipHeld      = "skipped_held"
	capturedAddrSkipUnknown   = "skipped_unknown"
	capturedAddrSkipUnusable  = "skipped_unusable"
	capturedAddrFailed        = "failed"
	capturedAddrClaimMark     = "0x40000000/0x40000000"
	capturedAddrChainPrefix   = "NVSNAP-RESTORE-ADDR"
	capturedAddrMangleClaimed = capturedAddrChainPrefix + "-CLAIM"
	capturedAddrNATIn         = capturedAddrChainPrefix + "-IN"
	capturedAddrNATOut        = capturedAddrChainPrefix + "-OUT"
)

// capturedAddressPlan is what a restore does with the captured address: apply
// it (result capturedAddrApplied) or not, and why.
type capturedAddressPlan struct {
	captured, podIP net.IP
	result          string
	reason          string
}

// planCapturedAddress decides whether the captured address can be made local
// in the restored pod whose address is podIP. instanceNew are the new
// addresses of the restored instance's pods (the new side of its inet
// address map); holder names the pod that has an address in the cluster ("",
// nil when none).
func planCapturedAddress(captured, podIP string, instanceNew []string, holder func(ip string) (string, error)) capturedAddressPlan {
	p := capturedAddressPlan{captured: net.ParseIP(strings.TrimSpace(captured)), podIP: net.ParseIP(strings.TrimSpace(podIP))}
	switch {
	case p.captured == nil || p.podIP == nil:
		p.result, p.reason = capturedAddrSkipUnusable, fmt.Sprintf("captured address %q or pod address %q is not an IP", captured, podIP)
	case p.captured.Equal(p.podIP):
		p.result, p.reason = capturedAddrUnchanged, "the pod has the captured address"
	case (p.captured.To4() == nil) != (p.podIP.To4() == nil):
		p.result, p.reason = capturedAddrSkipUnusable, "the captured address and the pod address are of different families"
	}
	if p.result != "" {
		return p
	}
	for _, n := range instanceNew {
		if ip := net.ParseIP(strings.TrimSpace(n)); ip != nil && ip.Equal(p.captured) {
			p.result, p.reason = capturedAddrSkipMember, "the captured address is now the address of a pod of the restored instance"
			return p
		}
	}
	if holder != nil {
		name, err := holder(p.captured.String())
		if err != nil {
			p.result, p.reason = capturedAddrSkipUnknown, fmt.Sprintf("cannot tell whether a pod has the captured address: %v", err)
			return p
		}
		if name != "" {
			p.result, p.reason = capturedAddrSkipHeld, "the captured address is the address of pod "+name
			return p
		}
	}
	p.result = capturedAddrApplied
	return p
}

// inetAddrMapNew returns the new addresses of an inet address map
// ("OLD=NEW,...").
func inetAddrMapNew(inetAddrMap string) []string {
	var out []string
	for _, pair := range strings.Split(inetAddrMap, ",") {
		if _, nw, ok := strings.Cut(strings.TrimSpace(pair), "="); ok && strings.TrimSpace(nw) != "" {
			out = append(out, strings.TrimSpace(nw))
		}
	}
	return out
}

// iptablesStep is one command of a rule set. A step with check runs only when
// the check fails (the rule is not there yet); a tolerant step's failure is
// ignored (a chain that exists already).
type iptablesStep struct {
	args     []string
	check    []string
	tolerant bool
}

// capturedAddressRules returns the tool (iptables or ip6tables) and the steps
// that translate a plan's captured address at the pod's edge. Every rule lives
// in nvsnap's own chains, flushed and refilled on each run, so running them
// again (a retried restore) leaves the same rules.
func capturedAddressRules(p capturedAddressPlan) (string, []iptablesStep) {
	tool, host := "iptables", "/32"
	if p.captured.To4() == nil {
		tool, host = "ip6tables", "/128"
	}
	captured, pod := p.captured.String(), p.podIP.String()
	var steps []iptablesStep
	chain := func(table, name, hook string, rules ...[]string) {
		steps = append(steps,
			iptablesStep{args: []string{"-t", table, "-N", name}, tolerant: true},
			iptablesStep{args: []string{"-t", table, "-F", name}})
		for _, r := range rules {
			steps = append(steps, iptablesStep{args: append([]string{"-t", table, "-A", name}, r...)})
		}
		steps = append(steps, iptablesStep{
			check: []string{"-t", table, "-C", hook, "-j", name},
			args:  []string{"-t", table, "-I", hook, "1", "-j", name},
		})
	}
	// A packet a socket bound to the pod address or to the wildcard accepts
	// keeps its destination: such a listener (the engine's API, a probe's
	// port) gets its connections as before.
	chain("mangle", capturedAddrMangleClaimed, "PREROUTING",
		[]string{"-d", pod + host, "-p", "tcp", "-m", "socket", "--nowildcard", "-j", "MARK", "--set-xmark", capturedAddrClaimMark})
	// A connection no socket accepts goes to the captured address, where the
	// pod's sockets are: a listener opened after the restore, or a restored
	// connection whose first packet after the restore comes from its peer
	// (the nat table sees a connection's first packet only).
	chain("nat", capturedAddrNATIn, "PREROUTING",
		[]string{"-d", pod + host, "-p", "tcp", "-m", "mark", "!", "--mark", capturedAddrClaimMark, "-j", "DNAT", "--to-destination", captured})
	// What leaves the pod from the captured address carries the pod
	// address. Not what stays in it (out through the loopback): the pod's
	// own connections to its captured address, such as NCCL's connection to
	// its proxy, keep the captured address at both ends, as when they were
	// captured.
	chain("nat", capturedAddrNATOut, "POSTROUTING",
		[]string{"-s", captured + host, "!", "-o", "lo", "-j", "SNAT", "--to-source", pod})
	return tool, steps
}

// runIptablesSteps runs steps with tool through run.
func runIptablesSteps(tool string, steps []iptablesStep, run func(tool string, args ...string) error) error {
	for _, s := range steps {
		if s.check != nil && run(tool, s.check...) == nil {
			continue
		}
		if err := run(tool, s.args...); err != nil && !s.tolerant {
			return fmt.Errorf("%s %s: %w", tool, strings.Join(s.args, " "), err)
		}
	}
	return nil
}

// capturedAddressDeps are what applying a plan touches, replaced in tests.
type capturedAddressDeps struct {
	alias func(pid int, ip string) error
	run   func(pid int, tool string, args ...string) error
}

// capturedAddressApply is what restoreCapturedAddress applies a plan with;
// tests replace it.
var capturedAddressApply = capturedAddressDeps{alias: addSourcePodIPToPlaceholderLo, run: nsenterIptables}

// applyCapturedAddress makes the plan's captured address local in the network
// namespace of host pid pid and installs its edge translation.
func applyCapturedAddress(pid int, p capturedAddressPlan, d capturedAddressDeps) error {
	if p.result != capturedAddrApplied {
		return errors.New("the plan does not apply the captured address")
	}
	if err := d.alias(pid, p.captured.String()); err != nil {
		return fmt.Errorf("make the captured address local: %w", err)
	}
	tool, steps := capturedAddressRules(p)
	return runIptablesSteps(tool, steps, func(tool string, args ...string) error { return d.run(pid, tool, args...) })
}

// iptablesStepTimeout bounds one iptables command: a lock held elsewhere in
// the namespace must not stall the restore.
const iptablesStepTimeout = 30 * time.Second

// nsenterIptables runs tool with args in the network namespace of host pid
// pid.
func nsenterIptables(pid int, tool string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), iptablesStepTimeout)
	defer cancel()
	netns := "--net=" + hostProcBase() + "/" + strconv.Itoa(pid) + "/ns/net"
	out, err := exec.CommandContext(ctx, "nsenter", append([]string{netns, "--", tool, "-w"}, args...)...).CombinedOutput() //nolint:gosec // fixed tool; addresses parsed as IPs
	if err != nil {
		return fmt.Errorf("%w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// podWithIP names the pod of the cluster, other than skip ("namespace/name"),
// that has address ip and is not being deleted, or "".
func (a *Agent) podWithIP(ctx context.Context, ip, skip string) (string, error) {
	if a.kubeClient == nil {
		return "", errors.New("no kube client")
	}
	pods, err := a.kubeClient.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{FieldSelector: "status.podIP=" + ip})
	if err != nil {
		return "", err
	}
	want := net.ParseIP(ip)
	for i := range pods.Items {
		p := &pods.Items[i]
		name := p.Namespace + "/" + p.Name
		if name == skip || p.DeletionTimestamp != nil || p.Spec.HostNetwork || !podHasIP(p, want) {
			continue
		}
		return name, nil
	}
	return "", nil
}

// podHasIP reports whether pod p has address ip (the selector is checked
// again here: what the API returns is not trusted to match it).
func podHasIP(p *corev1.Pod, ip net.IP) bool {
	if ip == nil {
		return false
	}
	if net.ParseIP(p.Status.PodIP).Equal(ip) {
		return true
	}
	for _, a := range p.Status.PodIPs {
		if net.ParseIP(a.IP).Equal(ip) {
			return true
		}
	}
	return false
}

// restoreCapturedAddress gives the placeholder (host pid pid, pod
// namespace/name placeholder, address podIP) the captured address when the
// guards allow it, and records the outcome. It never fails the restore: what
// it does not apply leaves the restore as without the switch. It reports
// whether the captured address was applied.
func (a *Agent) restoreCapturedAddress(ctx context.Context, pid int, placeholder, captured, podIP, inetAddrMap string, log *logrus.Entry) bool {
	p := planCapturedAddress(captured, podIP, inetAddrMapNew(inetAddrMap), func(ip string) (string, error) {
		return a.podWithIP(ctx, ip, placeholder)
	})
	log = log.WithFields(logrus.Fields{"capturedAddress": captured, "podAddress": podIP, "result": p.result})
	if p.result == capturedAddrApplied {
		if err := applyCapturedAddress(pid, p, capturedAddressApply); err != nil {
			p.result = capturedAddrFailed
			log = log.WithField("result", p.result)
			log.WithError(err).Warn("CRIU restore: cannot give the restored pod its captured address; sockets opened after the restore cannot use it")
		} else {
			log.Info("CRIU restore: the restored pod keeps its captured address, translated to its pod address at its edge")
		}
	} else if p.result != capturedAddrUnchanged {
		log.WithField("reason", p.reason).Warn("CRIU restore: the captured address stays off the restored pod; sockets opened after the restore cannot use it")
	}
	metrics.RestoreCapturedAddressTotal.WithLabelValues(p.result).Inc()
	return p.result == capturedAddrApplied
}

// restoreSourceAddress readies the network namespace of host pid pid (the
// placeholder namespace/name) for the sockets CRIU re-binds to the
// captured pod address captured, and returns the inet address map CRIU is
// given.
//
// With RestoreCapturedAddress the captured address stays local, translated
// at the pod's edge, and CRIU leaves the pod's sockets on it. When it
// cannot be kept (another pod holds it, it is unknown, applying failed),
// the restore proceeds as without the switch: an inet address map that
// moves the address re-binds the sockets to the new one; otherwise the
// address is aliased on the loopback, so the re-bind finds it.
func (a *Agent) restoreSourceAddress(ctx context.Context, pid int, namespace, name, captured, inetAddrMap string, log *logrus.Entry) string {
	if a.config.RestoreCapturedAddress {
		podIP := a.placeholderPodIP(ctx, namespace, name, inetAddrMap, captured)
		if a.restoreCapturedAddress(ctx, pid, namespace+"/"+name, captured, podIP, inetAddrMap, log) {
			return inetAddrMapKeeping(inetAddrMap, captured, podIP)
		}
	}
	switch {
	case inetAddrMapMoves(inetAddrMap, captured):
		// No alias: it would make the old address local and capture the
		// traffic of the instance's pod that has it now.
		log.WithField("sourcePodIP", captured).Info("Source pod IP moved by the inet address map; no alias on placeholder lo")
	case captured != "":
		if err := capturedAddressApply.alias(pid, captured); err != nil {
			log.WithError(err).Warn("Failed to alias source pod IP on placeholder lo; TCP restore may fail")
		} else {
			log.WithField("sourcePodIP", captured).Info("Aliased source pod IP on placeholder lo")
		}
	}
	return inetAddrMap
}

// inetAddrMapKeeping returns inetAddrMap ("OLD=NEW,...") for a pod that
// keeps its captured address captured, its pod address podIP: the pod's own
// entry goes (CRIU leaves its sockets on the captured address and no rule
// sends its traffic away from it), and an entry that leads to the pod's new
// address (a Service of the pod alone) leads to the captured address, where
// its sockets are.
func inetAddrMapKeeping(inetAddrMap, captured, podIP string) string {
	c, p := net.ParseIP(strings.TrimSpace(captured)), net.ParseIP(strings.TrimSpace(podIP))
	var out []string
	for _, pair := range strings.Split(inetAddrMap, ",") {
		pair = strings.TrimSpace(pair)
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		old, nw := net.ParseIP(strings.TrimSpace(k)), net.ParseIP(strings.TrimSpace(v))
		switch {
		case c != nil && old != nil && old.Equal(c):
			continue
		case c != nil && p != nil && nw != nil && nw.Equal(p):
			pair = strings.TrimSpace(k) + "=" + c.String()
		}
		out = append(out, pair)
	}
	return strings.Join(out, ",")
}

// placeholderPodIP is the placeholder's pod address: the new side of the
// captured address in the inet address map of a group restore, else the
// pod's status; "" when unknown (the plan then skips).
func (a *Agent) placeholderPodIP(ctx context.Context, namespace, name, inetAddrMap, captured string) string {
	if c := net.ParseIP(strings.TrimSpace(captured)); c != nil {
		for _, pair := range strings.Split(inetAddrMap, ",") {
			o, nw, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if ok && c.Equal(net.ParseIP(strings.TrimSpace(o))) {
				return strings.TrimSpace(nw)
			}
		}
	}
	if a.kubeClient == nil || namespace == "" || name == "" {
		return ""
	}
	pod, err := a.kubeClient.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	return pod.Status.PodIP
}
