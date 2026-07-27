/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Derives criu-v2 restore placeholders from their source manifests.
//
// A criu-v2 placeholder carries no workload-specific logic: it is a bash pid1
// that bumps ns_last_pid, tails the file the source redirected stdio to, and
// waits. Everything else -- prepping the rootfs, staging the bundle, running
// CRIU -- is the agent's job. So the placeholder is a pure function of a
// handful of fields on the source pod, and writing it by hand only creates
// opportunities for the two to disagree.
//
// They did disagree. The criu-v2 migration was applied per-manifest by hand
// and left three workloads on the legacy engine, which is what made a whole
// class of failures look workload-specific.
package manifests

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"sigs.k8s.io/yaml"
)

// sourcePod is the subset of a workload manifest a placeholder derives from.
type sourcePod struct {
	Metadata struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		ImagePullSecrets []struct {
			Name string `json:"name"`
		} `json:"imagePullSecrets"`
		Containers []struct {
			Name  string   `json:"name"`
			Image string   `json:"image"`
			Args  []string `json:"args"`
			Env   []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"env"`
			ReadinessProbe struct {
				FailureThreshold int `json:"failureThreshold"`
				HTTPGet          struct {
					Path string `json:"path"`
				} `json:"httpGet"`
			} `json:"readinessProbe"`
			Resources struct {
				Limits map[string]string `json:"limits"`
			} `json:"resources"`
		} `json:"containers"`
	} `json:"spec"`
}

type restoreParams struct {
	Name             string
	Namespace        string
	Image            string
	Port             string
	ProbePath        string
	FailureThreshold int
	GPUs             string
	StdoutTo         string
	PrepDirs         []string
	CarryEnv         []envVar
	PullSecrets      []string
}

type envVar struct{ Name, Value string }

// carriedEnv are the source variables a placeholder needs in its own right:
// the restored process resolves them against this pod's environment, so a
// mismatch changes where it looks for already-downloaded weights.
var carriedEnv = map[string]bool{"HF_HOME": true}

// mkdirLine matches directory prep the source does before launching. The
// restored process expects those paths to exist in the placeholder's rootfs
// too, since CRIU restores fds that may point into them.
var mkdirLine = regexp.MustCompile(`(?m)^\s*(mkdir -p [^\n&|;]+?)\s*$`)

// stdoutRedirect finds the file the source redirects the workload's stdio to.
// The setsid convention writes to a rootfs path so CRIU restores those fds as
// plain files; the placeholder tails the same path to surface logs.
var stdoutRedirect = regexp.MustCompile(`>\s*(/[^\s]+\.out)\s`)

// IsCRIUV2Source reports whether a manifest is a criu-v2 source pod, and so
// whether a placeholder should be derived for it. The rootfs capture path
// builds its target from the capture manifest instead.
func IsCRIUV2Source(src []byte) bool {
	var p sourcePod
	if err := yaml.Unmarshal(src, &p); err != nil {
		return false
	}
	return p.Metadata.Annotations["nvsnap.io/path"] == "criu"
}

// RenderRestore derives the restore placeholder for a criu-v2 source pod.
func RenderRestore(src []byte) ([]byte, error) {
	var p sourcePod
	if err := yaml.Unmarshal(src, &p); err != nil {
		return nil, fmt.Errorf("parse source pod: %w", err)
	}
	if len(p.Spec.Containers) == 0 {
		return nil, fmt.Errorf("%s: no containers", p.Metadata.Name)
	}
	c := p.Spec.Containers[0]

	out := ""
	if m := stdoutRedirect.FindStringSubmatch(strings.Join(c.Args, "\n")); m != nil {
		out = m[1]
	}
	if out == "" {
		return nil, fmt.Errorf("%s: no stdio redirect found in args; the criu-v2 "+
			"setsid convention must send the workload's stdio to a rootfs *.out file",
			p.Metadata.Name)
	}

	gpus := c.Resources.Limits["nvidia.com/gpu"]
	if gpus == "" {
		gpus = "1"
	}
	probePath := c.ReadinessProbe.HTTPGet.Path
	if probePath == "" {
		probePath = "/v1/models"
	}
	// A restore is normally much faster than a cold start, so the source's
	// own threshold is a generous default. Models that genuinely need longer
	// declare it on the source, keeping one place to look.
	threshold := c.ReadinessProbe.FailureThreshold
	if threshold == 0 {
		threshold = 60
	}
	// A malformed value is a manifest bug, not a reason to quietly use the
	// default: silently falling back to the source's threshold can make a
	// restore give up early and look like a workload failure.
	if v := p.Metadata.Annotations["nvsnap.io/restore-failure-threshold"]; v != "" {
		n, cerr := strconv.Atoi(v)
		if cerr != nil || n <= 0 {
			return nil, fmt.Errorf("%s: nvsnap.io/restore-failure-threshold=%q is not a positive integer",
				p.Metadata.Name, v)
		}
		threshold = n
	}

	var prep []string
	for _, m := range mkdirLine.FindAllStringSubmatch(strings.Join(c.Args, "\n"), -1) {
		prep = append(prep, strings.TrimSpace(m[1]))
	}
	var carried []envVar
	for _, e := range c.Env {
		if carriedEnv[e.Name] {
			carried = append(carried, envVar{Name: e.Name, Value: e.Value})
		}
	}

	// The placeholder pulls the SAME image as the source, so it needs the same
	// credentials. Hardcoding one secret meant a NIM placeholder could not pull
	// nvcr.io/nim/* at all -- the source declares nim-pull-secret alongside
	// ours, and the restore pod silently lost it.
	var pullSecrets []string
	for _, ps := range p.Spec.ImagePullSecrets {
		if ps.Name != "" {
			pullSecrets = append(pullSecrets, ps.Name)
		}
	}
	if len(pullSecrets) == 0 {
		pullSecrets = []string{"nvsnap-pull-secret"}
	}

	var buf bytes.Buffer
	err := restoreTemplate.Execute(&buf, restoreParams{
		Name:             p.Metadata.Name,
		Namespace:        p.Metadata.Namespace,
		Image:            c.Image,
		Port:             p.Metadata.Annotations["nvsnap.io/port"],
		ProbePath:        probePath,
		FailureThreshold: threshold,
		GPUs:             gpus,
		StdoutTo:         out,
		PrepDirs:         prep,
		CarryEnv:         carried,
		PullSecrets:      pullSecrets,
	})
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", p.Metadata.Name, err)
	}
	return buf.Bytes(), nil
}

var restoreTemplate = template.Must(template.New("restore").Parse(
	`# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# GENERATED by internal/manifests -- do not edit.
# Regenerate with: go generate ./internal/manifests/...
#
# Restore placeholder for a criu-v2 (in-namespace) checkpoint of {{ .Name }}.
#
# Dumb reaper: same image as the source (CRIU's path-based file checks resolve
# against an identical rootfs), bash pid1 reaps orphans, no restore-entrypoint
# and no hostPID -- the pod keeps its own fresh pid namespace, which is where
# the in-namespace CRIU restores the dumped session. The agent drives
# everything on POST /v1/restore. See internal/agent/restore_v2.go.
apiVersion: v1
kind: Pod
metadata:
  name: {{ .Name }}-restored
  namespace: {{ .Namespace }}
  labels:
    app: {{ .Name }}-restored
    nvsnap.io/demo: "true"
spec:
  automountServiceAccountToken: false
  # IMPORTANT: must run on the same node as the source pod's checkpoint.
  # test-e2e.sh substitutes __NODE_NAME__ from the source pod's status.
  nodeName: __NODE_NAME__

  imagePullSecrets:
{{- range .PullSecrets }}
    - name: {{ . }}
{{- end }}

  containers:
    - name: restore
      image: {{ .Image }}
      imagePullPolicy: IfNotPresent
      command: ["/bin/bash", "-lc"]
      args:
        - |
          set -e
{{- range .PrepDirs }}
          {{ . }}
{{- end }}
          # Push this pod's own pid allocations high so the low pid range the
          # dump captured stays free for CRIU's exact-pid forks.
          echo 100000 > /proc/sys/kernel/ns_last_pid || echo "(ns_last_pid bump failed)"
          # Restored workload stdio is a plain-file fd on {{ .StdoutTo }} (the
          # source manifest's setsid convention); surface it via kubelet.
          touch {{ .StdoutTo }}
          tail -F {{ .StdoutTo }} &
          while true; do sleep 30; done
      env:
        # CHECKPOINT_ID intentionally in block style -- test-e2e.sh's sed
        # substitution advances to the NEXT line.
        - name: CHECKPOINT_ID
          value: "__CHECKPOINT_ID__"
{{- range .CarryEnv }}
        - { name: {{ .Name }}, value: "{{ .Value }}" }
{{- end }}
{{- if .Port }}
      readinessProbe:
        httpGet:
          path: {{ .ProbePath }}
          port: {{ .Port }}
        initialDelaySeconds: 5
        periodSeconds: 5
        timeoutSeconds: 5
        failureThreshold: {{ .FailureThreshold }}
{{- end }}
      securityContext:
        privileged: true
      resources:
        limits:
          nvidia.com/gpu: "{{ .GPUs }}"
        requests:
          nvidia.com/gpu: "{{ .GPUs }}"
      volumeMounts:
        - { name: checkpoints, mountPath: /checkpoints }
        - { name: dev-shm, mountPath: /dev/shm }

  volumes:
    - name: checkpoints
      hostPath:
        path: /var/lib/containerd/nvsnap-checkpoints
        type: Directory
    - name: dev-shm
      emptyDir:
        medium: Memory
        sizeLimit: 16Gi

  restartPolicy: Never
`))
