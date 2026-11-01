// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The plan applies the captured address only when the pod's address changed
// and no other pod uses the captured one.
func TestPlanCapturedAddress(t *testing.T) {
	none := func(string) (string, error) { return "", nil }
	for name, c := range map[string]struct {
		captured, pod string
		instance      []string
		holder        func(string) (string, error)
		want          string
	}{
		"applied":                  {"10.1.0.5", "10.1.9.9", []string{"10.1.9.9", "10.1.9.8"}, none, capturedAddrApplied},
		"applied, IPv6":            {"fd00::5", "fd00::9", nil, none, capturedAddrApplied},
		"applied without a lookup": {"10.1.0.5", "10.1.9.9", nil, nil, capturedAddrApplied},
		"address unchanged":        {"10.1.0.5", "10.1.0.5", nil, none, capturedAddrUnchanged},
		"no captured address":      {"", "10.1.9.9", nil, none, capturedAddrSkipUnusable},
		"no pod address":           {"10.1.0.5", "", nil, none, capturedAddrSkipUnusable},
		"families differ":          {"10.1.0.5", "fd00::9", nil, none, capturedAddrSkipUnusable},
		"a member's new address":   {"10.1.0.5", "10.1.9.9", []string{"10.1.9.9", " 10.1.0.5"}, none, capturedAddrSkipMember},
		"held by a pod": {"10.1.0.5", "10.1.9.9", nil,
			func(ip string) (string, error) { return "other/web-0", nil }, capturedAddrSkipHeld},
		"cannot tell who holds it": {"10.1.0.5", "10.1.9.9", nil,
			func(string) (string, error) { return "", errors.New("forbidden") }, capturedAddrSkipUnknown},
	} {
		p := planCapturedAddress(c.captured, c.pod, c.instance, c.holder)
		if p.result != c.want {
			t.Errorf("%s: result %q (%s), want %q", name, p.result, p.reason, c.want)
		}
		if p.result != capturedAddrApplied && p.result != capturedAddrUnchanged && p.reason == "" {
			t.Errorf("%s: a skip without a reason", name)
		}
	}
}

func ruleText(steps []iptablesStep) string {
	var b strings.Builder
	for _, s := range steps {
		b.WriteString(strings.Join(s.args, " "))
		b.WriteString("\n")
	}
	return b.String()
}

// The edge translation: the claim of sockets bound to the pod address or the
// wildcard, the DNAT of what no socket claims to the captured address, and
// the SNAT of what leaves from it, in nvsnap's own chains.
func TestCapturedAddressRules(t *testing.T) {
	for _, c := range []struct{ captured, pod, tool, host string }{
		{"10.1.0.5", "10.1.9.9", "iptables", "/32"},
		{"fd00::5", "fd00::9", "ip6tables", "/128"},
	} {
		p := planCapturedAddress(c.captured, c.pod, nil, nil)
		tool, steps := capturedAddressRules(p)
		if tool != c.tool {
			t.Fatalf("%s: tool %s, want %s", c.captured, tool, c.tool)
		}
		text := ruleText(steps)
		for _, want := range []string{
			"-t mangle -A " + capturedAddrMangleClaimed + " -d " + c.pod + c.host + " -p tcp -m socket --nowildcard -j MARK --set-xmark " + capturedAddrClaimMark,
			"-t nat -A " + capturedAddrNATIn + " -d " + c.pod + c.host + " -p tcp -m mark ! --mark " + capturedAddrClaimMark + " -j DNAT --to-destination " + c.captured,
			"-t nat -A " + capturedAddrNATOut + " -s " + c.captured + c.host + " ! -o lo -j SNAT --to-source " + c.pod,
			"-t mangle -I PREROUTING 1 -j " + capturedAddrMangleClaimed,
			"-t nat -I PREROUTING 1 -j " + capturedAddrNATIn,
			"-t nat -I POSTROUTING 1 -j " + capturedAddrNATOut,
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: no rule %q in\n%s", c.captured, want, text)
			}
		}
		// The pod's own connections to its captured address (NCCL's
		// connection to its proxy) stay untranslated: an SNAT of them
		// resets them (seen on a GB300 cluster, 2026-10-26).
		for _, s := range steps {
			if slices.Contains(s.args, "SNAT") && !strings.Contains(strings.Join(s.args, " "), "! -o lo") {
				t.Errorf("%s: SNAT also rewrites the pod's traffic to itself: %v", c.captured, s.args)
			}
		}
		if strings.Contains(text, "--syn") {
			t.Errorf("%s: a SYN-only rule misses restored connections whose peer speaks first:\n%s", c.captured, text)
		}
	}
}

// fakeIptables keeps the state the steps change: chains and their rules, and
// the hooks' jumps.
type fakeIptables struct {
	chains map[string][]string
	jumps  map[string][]string
	calls  int
}

func (f *fakeIptables) run(_ string, args ...string) error {
	f.calls++
	table, op, chain := args[1], args[2], args[3]
	key := table + "/" + chain
	switch op {
	case "-N":
		if _, ok := f.chains[key]; ok {
			return errors.New("chain already exists")
		}
		f.chains[key] = nil
	case "-F":
		if _, ok := f.chains[key]; !ok {
			return errors.New("no chain")
		}
		f.chains[key] = nil
	case "-A":
		f.chains[key] = append(f.chains[key], strings.Join(args[4:], " "))
	case "-C":
		if slices.Contains(f.jumps[key], args[5]) {
			return nil
		}
		return errors.New("no such rule")
	case "-I":
		f.jumps[key] = append([]string{args[6]}, f.jumps[key]...)
	}
	return nil
}

// Running the steps again (a retried restore) leaves the same rules: no
// second jump, no duplicated rule.
func TestRunIptablesStepsIsIdempotent(t *testing.T) {
	f := &fakeIptables{chains: map[string][]string{}, jumps: map[string][]string{}}
	tool, steps := capturedAddressRules(planCapturedAddress("10.1.0.5", "10.1.9.9", nil, nil))
	for i := 0; i < 2; i++ {
		if err := runIptablesSteps(tool, steps, f.run); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	for hook, jumps := range f.jumps {
		if len(jumps) != 1 {
			t.Errorf("%s: jumps %v, want one", hook, jumps)
		}
	}
	for chain, rules := range f.chains {
		if len(rules) != 1 {
			t.Errorf("%s: rules %v, want one", chain, rules)
		}
	}
	if len(f.jumps) != 3 || len(f.chains) != 3 {
		t.Errorf("jumps %v chains %v", f.jumps, f.chains)
	}
}

// A failing step that is not tolerated stops the run and names the rule.
func TestRunIptablesStepsFails(t *testing.T) {
	tool, steps := capturedAddressRules(planCapturedAddress("10.1.0.5", "10.1.9.9", nil, nil))
	err := runIptablesSteps(tool, steps, func(_ string, args ...string) error {
		if slices.Contains(args, "DNAT") {
			return errors.New("no xt_socket")
		}
		if slices.Contains(args, "-C") {
			return errors.New("no such rule")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "DNAT") {
		t.Fatalf("err %v", err)
	}
}

// The address goes on before any rule, and a failure to add it installs
// none.
func TestApplyCapturedAddress(t *testing.T) {
	var order []string
	d := capturedAddressDeps{
		alias: func(pid int, ip string) error { order = append(order, "alias "+ip); return nil },
		run: func(pid int, tool string, args ...string) error {
			if pid != 42 {
				t.Errorf("rules in pid %d", pid)
			}
			order = append(order, "rule")
			if slices.Contains(args, "-C") {
				return errors.New("no such rule")
			}
			return nil
		},
	}
	p := planCapturedAddress("10.1.0.5", "10.1.9.9", nil, nil)
	if err := applyCapturedAddress(42, p, d); err != nil {
		t.Fatal(err)
	}
	if len(order) < 2 || order[0] != "alias 10.1.0.5" {
		t.Fatalf("order %v", order)
	}
	order = nil
	d.alias = func(int, string) error { return errors.New("EPERM") }
	if err := applyCapturedAddress(42, p, d); err == nil || len(order) != 0 {
		t.Fatalf("err %v, rules %v", err, order)
	}
	if err := applyCapturedAddress(42, planCapturedAddress("10.1.0.5", "10.1.0.5", nil, nil), d); err == nil {
		t.Fatal("applied a plan that does not apply")
	}
}

// For a pod that keeps its captured address, its own entry leaves the map
// and a Service of that pod alone leads to the captured address; the other
// members' entries stay.
func TestInetAddrMapKeeping(t *testing.T) {
	m := "10.1.0.5=10.1.9.9, 10.1.0.6=10.1.9.8,172.20.0.10=10.1.9.9,172.20.0.11=10.1.9.8"
	got := inetAddrMapKeeping(m, "10.1.0.5", "10.1.9.9")
	if want := "10.1.0.6=10.1.9.8,172.20.0.10=10.1.0.5,172.20.0.11=10.1.9.8"; got != want {
		t.Errorf("map %q, want %q", got, want)
	}
	if got := inetAddrMapKeeping("", "10.1.0.5", "10.1.9.9"); got != "" {
		t.Errorf("empty map became %q", got)
	}
	if got := inetAddrMapKeeping("fd00::5=fd00::9,fd00::6=fd00::8", "fd00::5", "fd00::9"); got != "fd00::6=fd00::8" {
		t.Errorf("IPv6 map %q", got)
	}
	if got := inetAddrMapNew(m); !slices.Equal(got, []string{"10.1.9.9", "10.1.9.8", "10.1.9.9", "10.1.9.8"}) {
		t.Errorf("new addresses %v", got)
	}
}

// The restored pod's own captured address stays a peer when the dead peers
// are closed.
func TestKeepAddressesKeepsTheCapturedAddress(t *testing.T) {
	keep := keepAddresses("10.1.0.5", "10.1.0.6=10.1.9.8")
	if !keep["10.1.0.5"] || !keep["10.1.9.8"] || keep["10.1.0.6"] {
		t.Fatalf("keep %v", keep)
	}
}

func pod(ns, name, ip string, mod func(*corev1.Pod)) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Status: corev1.PodStatus{PodIP: ip, PodIPs: []corev1.PodIP{{IP: ip}}}}
	if mod != nil {
		mod(p)
	}
	return p
}

// The pod that has an address: never the placeholder itself, a pod being
// deleted, a host-network pod, or one whose address does not match (the
// selector is checked again).
func TestPodWithIP(t *testing.T) {
	now := metav1.Now()
	kc := fake.NewSimpleClientset(
		pod("fn", "placeholder", "10.1.0.5", nil),
		pod("fn", "going", "10.1.0.5", func(p *corev1.Pod) { p.DeletionTimestamp = &now; p.Finalizers = []string{"x"} }),
		pod("kube-system", "node-agent", "10.1.0.5", func(p *corev1.Pod) { p.Spec.HostNetwork = true }),
		pod("other", "unrelated", "10.1.7.7", nil),
	)
	a := &Agent{kubeClient: kc}
	if name, err := a.podWithIP(context.Background(), "10.1.0.5", "fn/placeholder"); err != nil || name != "" {
		t.Fatalf("holder %q, %v; want none", name, err)
	}
	if _, err := kc.CoreV1().Pods("other").Create(context.Background(), pod("other", "web-0", "10.1.0.5", nil), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if name, err := a.podWithIP(context.Background(), "10.1.0.5", "fn/placeholder"); err != nil || name != "other/web-0" {
		t.Fatalf("holder %q, %v; want other/web-0", name, err)
	}
}

// The placeholder's address comes from the group's map when the map moves the
// captured address, else from the pod.
func TestPlaceholderPodIP(t *testing.T) {
	a := &Agent{kubeClient: fake.NewSimpleClientset(pod("fn", "p-0", "10.1.9.9", nil))}
	if got := a.placeholderPodIP(context.Background(), "fn", "p-0", "10.1.0.5=10.1.4.4", "10.1.0.5"); got != "10.1.4.4" {
		t.Errorf("from the map: %q", got)
	}
	if got := a.placeholderPodIP(context.Background(), "fn", "p-0", "", "10.1.0.5"); got != "10.1.9.9" {
		t.Errorf("from the pod: %q", got)
	}
	if got := a.placeholderPodIP(context.Background(), "fn", "gone", "", "10.1.0.5"); got != "" {
		t.Errorf("missing pod: %q", got)
	}
}

// restoreCapturedAddress applies what the plan allows and never applies a
// skipped plan; a failed apply is not reported as applied.
func TestRestoreCapturedAddress(t *testing.T) {
	log := logrus.NewEntry(logrus.New())
	log.Logger.SetOutput(io.Discard)
	saved := capturedAddressApply
	t.Cleanup(func() { capturedAddressApply = saved })
	applied := 0
	capturedAddressApply = capturedAddressDeps{
		alias: func(int, string) error { applied++; return nil },
		run: func(_ int, _ string, args ...string) error {
			if slices.Contains(args, "-C") {
				return errors.New("no such rule")
			}
			return nil
		},
	}
	a := &Agent{kubeClient: fake.NewSimpleClientset(pod("other", "web-0", "10.1.0.7", nil))}
	if !a.restoreCapturedAddress(context.Background(), 1, "fn/p-0", "10.1.0.5", "10.1.9.9", "", log) || applied != 1 {
		t.Fatalf("not applied (%d)", applied)
	}
	if a.restoreCapturedAddress(context.Background(), 1, "fn/p-0", "10.1.0.7", "10.1.9.9", "", log) || applied != 1 {
		t.Fatal("applied an address another pod holds")
	}
	capturedAddressApply.alias = func(int, string) error { return errors.New("EPERM") }
	if a.restoreCapturedAddress(context.Background(), 1, "fn/p-0", "10.1.0.5", "10.1.9.9", "", log) {
		t.Fatal("a failed apply reported as applied")
	}
}

// A captured address the restored pod cannot keep leaves the restore as it
// is without the switch: the sockets CRIU re-binds still find their
// address. A conformance run restored a lone pod whose captured address
// another pod had taken; nothing aliased it and every restore failed with
// "Can't bind inet socket back: Cannot assign requested address".
func TestRestoreSourceAddress(t *testing.T) {
	log := logrus.NewEntry(logrus.New())
	log.Logger.SetOutput(io.Discard)
	saved := capturedAddressApply
	t.Cleanup(func() { capturedAddressApply = saved })
	var aliased []string
	aliasErr := error(nil)
	capturedAddressApply = capturedAddressDeps{
		alias: func(_ int, ip string) error { aliased = append(aliased, ip); return aliasErr },
		run: func(_ int, _ string, args ...string) error {
			if slices.Contains(args, "-C") {
				return errors.New("no such rule")
			}
			return nil
		},
	}
	kc := fake.NewSimpleClientset(
		pod("fn", "p-0", "10.1.9.9", nil),      // the placeholder
		pod("other", "web-0", "10.1.0.7", nil), // holds a captured address
	)
	const moved = "10.1.0.7=10.1.9.9,10.1.0.8=10.1.9.8"
	for name, tc := range map[string]struct {
		keep      bool
		captured  string
		inetMap   string
		aliasErr  error
		wantMap   string
		wantAlias []string
	}{
		"off: aliased":                       {false, "10.1.0.5", "", nil, "", []string{"10.1.0.5"}},
		"off: moved by the map":              {false, "10.1.0.7", moved, nil, moved, nil},
		"off: no captured address":           {false, "", "", nil, "", nil},
		"on: kept":                           {true, "10.1.0.5", "", nil, "", []string{"10.1.0.5"}},
		"on: kept, own entry dropped":        {true, "10.1.0.5", "10.1.0.5=10.1.9.9,10.1.0.8=10.1.9.8", nil, "10.1.0.8=10.1.9.8", []string{"10.1.0.5"}},
		"on: held by another pod, aliased":   {true, "10.1.0.7", "", nil, "", []string{"10.1.0.7"}},
		"on: held by another pod, moved":     {true, "10.1.0.7", moved, nil, moved, nil},
		"on: apply fails, alias tried again": {true, "10.1.0.5", "", errors.New("EPERM"), "", []string{"10.1.0.5", "10.1.0.5"}},
	} {
		t.Run(name, func(t *testing.T) {
			aliased, aliasErr = nil, tc.aliasErr
			a := &Agent{kubeClient: kc, config: Config{RestoreCapturedAddress: tc.keep}}
			got := a.restoreSourceAddress(context.Background(), 1, "fn", "p-0", tc.captured, tc.inetMap, log)
			if got != tc.wantMap {
				t.Errorf("inet address map %q, want %q", got, tc.wantMap)
			}
			if !slices.Equal(aliased, tc.wantAlias) {
				t.Errorf("aliased %v, want %v", aliased, tc.wantAlias)
			}
		})
	}
}
