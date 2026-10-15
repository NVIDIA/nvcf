// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// The engine's own view of its size is the check on how nvsnap grouped its
// pods. Workloads come in shapes nvsnap does not know (a new operator, a
// hand-written chart, an engine started from a script); checkpointing part
// of an engine that spans more pods would stall the rest of it. So an
// instance is captured only when its pods hold every GPU the engine says
// it uses, and an engine that says it spans nodes is never captured as a
// single pod.

import (
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

var (
	tensorParallelFlags   = []string{"--tensor-parallel-size", "--tensor_parallel_size", "--tp-size", "--tp_size", "--tp", "-tp"}
	pipelineParallelFlags = []string{"--pipeline-parallel-size", "--pipeline_parallel_size", "--pp-size", "--pp_size", "--pp", "-pp"}
	tensorParallelEnv     = []string{"NIM_TENSOR_PARALLEL_SIZE", "TENSOR_PARALLEL_SIZE"}
	pipelineParallelEnv   = []string{"NIM_PIPELINE_PARALLEL_SIZE", "PIPELINE_PARALLEL_SIZE"}
	// multiNodeFlags only appear on an engine that spans nodes.
	multiNodeFlags = []string{"--nnodes", "--node-rank", "--node_rank", "--dist-init-addr", "--master-addr", "--master_addr",
		"--data-parallel-address", "--headless"}
)

// engineWords are the words of the container's command line; a shell
// script given inline is split into its words too.
func engineWords(c *corev1.Container) []string {
	var words []string
	for _, a := range append(append([]string{}, c.Command...), c.Args...) {
		words = append(words, strings.Fields(a)...)
	}
	return words
}

// flagValue returns the integer value of the first of flags on the
// command line ("--f N" or "--f=N").
func flagValue(words, flags []string) (int, bool) {
	for i, w := range words {
		for _, f := range flags {
			v := ""
			switch {
			case w == f && i+1 < len(words):
				v = words[i+1]
			case strings.HasPrefix(w, f+"="):
				v = strings.TrimPrefix(w, f+"=")
			default:
				continue
			}
			if n, err := strconv.Atoi(strings.Trim(v, `"'`)); err == nil && n > 0 {
				return n, true
			}
		}
	}
	return 0, false
}

func envValue(c *corev1.Container, names []string) (int, bool) {
	for _, e := range c.Env {
		for _, n := range names {
			if e.Name == n {
				if v, err := strconv.Atoi(e.Value); err == nil && v > 0 {
					return v, true
				}
			}
		}
	}
	return 0, false
}

func gpuLimit(c *corev1.Container) int64 {
	q, ok := c.Resources.Limits["nvidia.com/gpu"]
	if !ok {
		return 0
	}
	return q.Value()
}

// instanceCoversEngine returns why the ranks are not a whole engine, or nil.
func instanceCoversEngine(ranks []*corev1.Pod) error {
	var gpus int64
	need := 1
	multiNode := ""
	for _, p := range ranks {
		for i := range p.Spec.Containers {
			c := &p.Spec.Containers[i]
			g := gpuLimit(c)
			if g == 0 {
				continue
			}
			gpus += g
			words := engineWords(c)
			tp, ok := flagValue(words, tensorParallelFlags)
			if !ok {
				tp, _ = envValue(c, tensorParallelEnv)
			}
			pp, ok := flagValue(words, pipelineParallelFlags)
			if !ok {
				pp, _ = envValue(c, pipelineParallelEnv)
			}
			need = max(need, max(tp, 1)*max(pp, 1))
			for _, w := range words {
				for _, f := range multiNodeFlags {
					if w == f || strings.HasPrefix(w, f+"=") {
						multiNode = f
					}
				}
			}
		}
	}
	if int64(need) > gpus {
		return fmt.Errorf("the engine uses %d GPUs (tensor x pipeline parallel) but the %d pod(s) nvsnap found hold %d", need, len(ranks), gpus)
	}
	if multiNode != "" && len(ranks) == 1 {
		return fmt.Errorf("the engine spans nodes (%s) but nvsnap found it as one pod", multiNode)
	}
	return nil
}
