// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package fnstatus names the function version a capture record belongs to
// and projects the records onto one status per function version: what
// nvsnap holds for it and what admission does with its next pod. The
// agents stamp the names (Identity); nvsnap-server keeps the NvsnapFunction
// objects (Project). The records stay the only source of truth.
package fnstatus

import (
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// The labels the agents put on a group record. A record's key is scoped
// by the function version (or the namespace, outside NVCF), so a record
// belongs to exactly one.
const (
	LabelFunctionVersion = "nvsnap.io/function-version-id"
	LabelFunction        = "nvsnap.io/function-id"
	LabelTenant          = "nvsnap.io/tenant"
	LabelNamespace       = "nvsnap.io/namespace"
)

// Record names and states, as the agent writes them (internal/agent).
const (
	GroupRecordPrefix    = "nvsnap-criu-group-"
	CachedirRecordPrefix = "nvsnap-cachedir-"

	StateAlias          = "alias"
	StateCapturing      = "capturing"
	StateVerifying      = "verifying"
	StateComplete       = "complete"
	StateFailed         = "failed"
	StateRestoreFailed  = "restore-failed"
	StateGPUShareFailed = "gpushare-failed"
	// StateStalled is a complete capture whose restores stopped getting to
	// run MaxStalls times: admission starts its pods cold, keeping it.
	StateStalled = "stalled"

	// MaxStalls is the agent's criuGroupMaxStalls.
	MaxStalls = 3

	MethodCRIU     = "criu"
	MethodCachedir = "cachedir"
)

// Namespace labels the platform puts on a function's namespaces, in the
// order they are read (the agent's criuScopeLabels for the version).
var (
	versionLabels  = []string{"function-version-id", "FUNCTION_VERSION_ID"}
	functionLabels = []string{"function-id", "FUNCTION_ID"}
)

// Identity returns the labels naming the function version of namespace ns,
// from its labels nsLabels; tenantLabel is the namespace label naming its
// tenant. Outside NVCF only the namespace is named. A value that is not a
// valid label value is left out.
func Identity(ns string, nsLabels map[string]string, tenantLabel string) map[string]string {
	out := map[string]string{}
	set := func(k, v string) {
		if v != "" && len(validation.IsValidLabelValue(v)) == 0 {
			out[k] = v
		}
	}
	set(LabelNamespace, ns)
	set(LabelFunctionVersion, first(nsLabels, versionLabels))
	set(LabelFunction, first(nsLabels, functionLabels))
	if tenantLabel != "" {
		set(LabelTenant, nsLabels[tenantLabel])
	}
	return out
}

func first(labels map[string]string, keys []string) string {
	for _, k := range keys {
		if v := labels[k]; v != "" {
			return v
		}
	}
	return ""
}

// ObjectName is the name of the status of the function version a record
// with labels belongs to: the version id, or "ns-<namespace>" outside
// NVCF; "" for a record not named yet.
func ObjectName(labels map[string]string) string {
	if v := labels[LabelFunctionVersion]; v != "" {
		return v
	}
	if ns := labels[LabelNamespace]; ns != "" {
		return "ns-" + ns
	}
	return ""
}

// Configuration is one configuration (group record) of a function version.
type Configuration struct {
	Key string `json:"key"`
	// State is the record's state; StateStalled for a complete one
	// admission no longer restores.
	State string `json:"state"`
	// Method is how admission starts the configuration's next pod:
	// MethodCRIU (restore), MethodCachedir (seeded caches), or "" (cold).
	Method     string   `json:"method,omitempty"`
	Ranks      int      `json:"ranks"`
	Nodes      []string `json:"nodes,omitempty"`
	Captures   int      `json:"captures,omitempty"`
	StartedAt  string   `json:"startedAt,omitempty"`
	VerifiedAt string   `json:"verifiedAt,omitempty"`
	// Restores counts the recent restores the record keeps (at most 10);
	// LastRestoreAt and LastRestoreSeconds are the newest.
	Restores           int     `json:"restores,omitempty"`
	LastRestoreAt      string  `json:"lastRestoreAt,omitempty"`
	LastRestoreSeconds float64 `json:"lastRestoreSeconds,omitempty"`
	RestoreFailures    int     `json:"restoreFailures,omitempty"`
	GPUFaults          int     `json:"gpuFaults,omitempty"`
	Stalls             int     `json:"stalls,omitempty"`
	// Cachedir is "complete/ranks" of the cachedir capture, when there is
	// one.
	Cachedir string `json:"cachedir,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// Status is what nvsnap holds for one function version. The top fields
// summarise its best configuration.
type Status struct {
	FunctionVersionID string          `json:"functionVersionID,omitempty"`
	FunctionID        string          `json:"functionID,omitempty"`
	Tenant            string          `json:"tenant,omitempty"`
	Namespace         string          `json:"namespace,omitempty"`
	State             string          `json:"state"`
	Method            string          `json:"method,omitempty"`
	Ranks             int             `json:"ranks"`
	VerifiedAt        string          `json:"verifiedAt,omitempty"`
	LastRestoreSecs   float64         `json:"lastRestoreSeconds,omitempty"`
	Reason            string          `json:"reason,omitempty"`
	Configurations    []Configuration `json:"configurations"`
}

// reasonMax bounds a reason in the status.
const reasonMax = 200

// Project returns the status of every function version the records name,
// by object name. records are the ConfigMaps of the records' namespace;
// aliases, records not named yet and anything else are skipped.
func Project(records []corev1.ConfigMap) map[string]Status {
	cachedir := map[string]map[string]string{}
	for i := range records {
		if key, ok := strings.CutPrefix(records[i].Name, CachedirRecordPrefix); ok {
			cachedir[key] = records[i].Data
		}
	}
	out := map[string]Status{}
	for i := range records {
		r := &records[i]
		key, ok := strings.CutPrefix(r.Name, GroupRecordPrefix)
		if !ok || r.Data["state"] == StateAlias {
			continue
		}
		name := ObjectName(r.Labels)
		if name == "" {
			continue
		}
		s := out[name]
		s.FunctionVersionID = r.Labels[LabelFunctionVersion]
		s.FunctionID = r.Labels[LabelFunction]
		s.Tenant = r.Labels[LabelTenant]
		s.Namespace = r.Labels[LabelNamespace]
		s.Configurations = append(s.Configurations, configuration(key, r.Data, cachedir[key]))
		out[name] = s
	}
	for name := range out {
		s := out[name]
		summarise(&s)
		out[name] = s
	}
	return out
}

// configuration reads group record d of key and its cachedir record c.
func configuration(key string, d, c map[string]string) Configuration {
	cfg := Configuration{Key: key, State: d["state"], StartedAt: d["startedAt"], VerifiedAt: d["verifiedAt"]}
	cfg.Ranks, _ = strconv.Atoi(d["size"])
	cfg.Captures, _ = strconv.Atoi(d["captures"])
	cfg.RestoreFailures, _ = strconv.Atoi(d["restoreFailures"])
	cfg.GPUFaults, _ = strconv.Atoi(d["gpuFaults"])
	cfg.Stalls, _ = strconv.Atoi(d["stalls"])
	for i := range cfg.Ranks {
		if n := d["node."+strconv.Itoa(i)]; n != "" {
			cfg.Nodes = append(cfg.Nodes, n)
		}
	}
	if h := strings.Fields(d["restores"]); len(h) > 0 {
		cfg.Restores = len(h)
		at, secs, _ := strings.Cut(h[len(h)-1], "=")
		cfg.LastRestoreAt = at
		cfg.LastRestoreSeconds, _ = strconv.ParseFloat(secs, 64)
	}
	if c != nil {
		cfg.Cachedir = cachedirRanks(c, cfg.Ranks)
	}
	switch {
	case Restorable(d):
		cfg.Method = MethodCRIU
	case cfg.State == StateComplete && cfg.Stalls >= MaxStalls:
		cfg.State = StateStalled
	}
	if cfg.Method == "" && fallsBack(cfg.State) && cfg.Cachedir != "" {
		if done, total, _ := strings.Cut(cfg.Cachedir, "/"); done == total {
			cfg.Method = MethodCachedir
		}
	}
	cfg.Reason = truncate(firstOf(d["reason"], d["lastRestoreFailure"], d["gpushareFailure"]))
	return cfg
}

// Restorable reports whether admission restores the configuration of group
// record d (the agent's parseCRIUGroup): a complete capture, or one being
// verified (for its own instance only), that has not stalled, with a
// checkpoint for every rank.
func Restorable(d map[string]string) bool {
	if d["state"] != StateComplete && (d["state"] != StateVerifying || d["sourceNamespace"] == "") {
		return false
	}
	if n, _ := strconv.Atoi(d["stalls"]); n >= MaxStalls {
		return false
	}
	size, err := strconv.Atoi(d["size"])
	if err != nil || size < 1 {
		return false
	}
	for i := range size {
		if d["checkpoint."+strconv.Itoa(i)] == "" {
			return false
		}
	}
	return true
}

// fallsBack reports a state whose pods admission seeds from the
// cachedir capture, when there is a complete one.
func fallsBack(state string) bool {
	return state == StateFailed || state == StateRestoreFailed || state == StateGPUShareFailed
}

// cachedirRanks is "complete/ranks" of cachedir record c, for a
// configuration of ranks ranks (0: as many as the record names).
func cachedirRanks(c map[string]string, ranks int) string {
	done, named := 0, 0
	for k, v := range c {
		if strings.HasPrefix(k, "rank.") {
			named++
			if v == StateComplete {
				done++
			}
		}
	}
	if ranks < named {
		ranks = named
	}
	return strconv.Itoa(done) + "/" + strconv.Itoa(ranks)
}

// stateOrder ranks states from the most useful to the least: the summary
// is the best configuration's.
var stateOrder = map[string]int{
	StateComplete: 0, StateVerifying: 1, StateCapturing: 2, StateStalled: 3,
	StateRestoreFailed: 4, StateFailed: 5, StateGPUShareFailed: 6,
}

func rank(c Configuration) int {
	r, ok := stateOrder[c.State]
	if !ok {
		r = len(stateOrder)
	}
	// A configuration that falls back to a complete cachedir capture
	// still starts faster than a cold one.
	if c.Method == MethodCachedir {
		r -= len(stateOrder)
	}
	if c.Method == MethodCRIU {
		r -= 2 * len(stateOrder)
	}
	return r
}

// summarise orders s's configurations, best first, and sets the summary
// from the first.
func summarise(s *Status) {
	sort.SliceStable(s.Configurations, func(i, j int) bool {
		ri, rj := rank(s.Configurations[i]), rank(s.Configurations[j])
		if ri != rj {
			return ri < rj
		}
		return s.Configurations[i].Key < s.Configurations[j].Key
	})
	best := s.Configurations[0]
	s.State, s.Method, s.Ranks = best.State, best.Method, best.Ranks
	s.VerifiedAt, s.LastRestoreSecs = best.VerifiedAt, best.LastRestoreSeconds
	s.Reason = ""
	if best.State != StateComplete {
		s.Reason = best.Reason
	}
}

func firstOf(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string) string {
	if len(s) <= reasonMax {
		return s
	}
	return s[:reasonMax]
}
