/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package rootfsonly

import (
	"regexp"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// HashInputComposer builds a checkpointstore.HashInput from a pod spec.
// The composed HashInput drives content-addressing of the captured cache:
// two pods that produce the same HashInput must have semantically
// interchangeable caches (same model weights, same compiled engines).
//
// Composition strategy is deliberately conservative — when in doubt, hash
// it. False negatives (different hashes for compatible pods) just cause a
// cold start; false positives (same hash for incompatible pods) would
// silently load wrong weights into a customer's pod, which is worse.
type HashInputComposer struct {
	// CUDADriverMajor is the node's NVIDIA driver major version (e.g.
	// 580 for "580.95.05"). Read from the node and supplied per-pod by
	// the trigger. Required: a capture made on driver 580.x is not
	// compatible on a 555.x node because the rootfs upperdir contains
	// driver-versioned firmware paths.
	CUDADriverMajor int
}

// Compose returns the HashInput for the given pod. mainContainer is the
// index in pod.spec.containers whose image / args / env drive composition.
// nil pod or out-of-range index yields a HashInput with CaptureFormatVersion
// + CUDADriverMajor populated and nothing else — safe to hash but unlikely
// to match any real capture.
func (c *HashInputComposer) Compose(pod *corev1.Pod, mainContainer int) checkpointstore.HashInput {
	out := checkpointstore.HashInput{
		CUDADriverMajor:      c.CUDADriverMajor,
		CaptureFormatVersion: checkpointstore.CaptureFormatVersion,
	}
	if pod == nil || mainContainer < 0 || mainContainer >= len(pod.Spec.Containers) {
		return out
	}
	main := pod.Spec.Containers[mainContainer]

	out.ImageDigest = resolvedImageDigest(pod, mainContainer)
	out.ModelID = inferModelID(main)
	out.EngineCompatFlags = composeEngineCompatFlags(main)
	return out
}

// resolvedImageDigest prefers status.containerStatuses[].imageID (the
// post-pull resolved digest like "nvcr.io/nim/...@sha256:abc..." or
// "docker.io/vllm/vllm-openai@sha256:def..."). Falls back to the spec
// image string if status isn't populated yet (shouldn't happen for a
// "warm" pod, but this is defensive).
func resolvedImageDigest(pod *corev1.Pod, mainContainer int) string {
	mainName := ""
	if mainContainer < len(pod.Spec.Containers) {
		mainName = pod.Spec.Containers[mainContainer].Name
	}
	for i := range pod.Status.ContainerStatuses {
		cs := &pod.Status.ContainerStatuses[i]
		if cs.Name == mainName && cs.ImageID != "" {
			return cs.ImageID
		}
	}
	if mainContainer < len(pod.Spec.Containers) {
		return pod.Spec.Containers[mainContainer].Image
	}
	return ""
}

// composeEngineCompatFlags returns the discriminating flags for cache
// compatibility: container Command + Args (preserved order) and
// cache-relevant env (sorted). Each entry is prefixed with "cmd:",
// "arg:", or "env:" to namespace them; ComputeHash will sort the final
// slice but we keep the per-namespace order via the prefix sort.
func composeEngineCompatFlags(c corev1.Container) []string {
	flags := make([]string, 0, len(c.Command)+len(c.Args))
	for i, cmd := range c.Command {
		flags = append(flags, "cmd["+itoa(i)+"]:"+cmd)
	}
	for i, a := range stripRoleFlags(c.Args) {
		flags = append(flags, "arg["+itoa(i)+"]:"+a)
	}
	for _, e := range cacheRelevantEnv(c.Env) {
		flags = append(flags, "env:"+e.Name+"="+e.Value)
	}
	return flags
}

// itoa is a small fmt-free integer formatter for the flag prefix.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// cacheRelevantEnv filters out env vars that don't affect cache compatibility:
//   - nvsnap's own intercept-library / log knobs (LD_PRELOAD, LD_LIBRARY_PATH,
//     NVSNAP_*) are set the same way for every nvsnap-managed pod and don't
//     change what's in the cache.
//   - Common debug / observability flags (PYTHONFAULTHANDLER,
//     PYTHONUNBUFFERED, NCCL_DEBUG, TORCH_*, *_DEBUG_*) are turned on
//     for diagnostics and shouldn't cause cache misses.
//
// Result is sorted by name for determinism.
//
// When in doubt, INCLUDE the env var. False positives (cache miss) are
// always safe; false negatives (wrong cache loaded) are not.
func cacheRelevantEnv(envs []corev1.EnvVar) []corev1.EnvVar {
	skipExact := map[string]struct{}{
		"LD_PRELOAD":                 {},
		"LD_LIBRARY_PATH":            {},
		"PYTHONFAULTHANDLER":         {},
		"PYTHONUNBUFFERED":           {},
		"NCCL_DEBUG":                 {},
		"NCCL_DEBUG_SUBSYS":          {},
		"NCCL_DEBUG_FILE":            {},
		"TORCH_CPP_LOG_LEVEL":        {},
		"TORCH_SHOW_CPP_STACKTRACES": {},
		"TORCH_DISTRIBUTED_DEBUG":    {},
		"VLLM_LOGGING_LEVEL":         {},
	}
	// Disaggregated-serving wiring (Dynamo runtime env, etcd and NATS
	// endpoints) says where a worker plugs in, not what it downloads or
	// compiles; see stripRoleFlags.
	for _, name := range roleEnvExact {
		skipExact[name] = struct{}{}
	}
	skipPrefix := append([]string{"NVSNAP_"}, roleEnvPrefixes...)
	out := make([]corev1.EnvVar, 0, len(envs))
	for _, e := range envs {
		if _, ok := skipExact[e.Name]; ok {
			continue
		}
		skip := false
		for _, p := range skipPrefix {
			if strings.HasPrefix(e.Name, p) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		// EnvVars that reference secrets/configMaps via ValueFrom don't
		// have a literal Value — record their presence by name only,
		// not their resolved value (which we don't have at compose time).
		if e.ValueFrom != nil && e.Value == "" {
			out = append(out, corev1.EnvVar{Name: e.Name, Value: "<from-ref>"})
			continue
		}
		out = append(out, corev1.EnvVar{Name: e.Name, Value: e.Value})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// modelFlagPattern matches `--model foo`, `--model=foo`, `--model-path foo`
// or `--model-path=foo` inside a multi-line shell script (Args[0] for
// engines that wrap the launch command in /bin/bash -c).
var modelFlagPattern = regexp.MustCompile(`--model(?:-path)?(?:=|\s+)([^\s\\]+)`)

// modelEnvNames are the env vars engines and NIMs read the model identity
// from when it is not on the command line, in precedence order.
var modelEnvNames = []string{"HF_MODEL_ID", "MODEL_ID", "NIM_MODEL_NAME"}

// InferModelID is a best-effort extractor for the model identifier the
// container will download. It looks, in order, at:
//
//   - command+args as a token list: "--model X", "--model-path X",
//     "--model=X". Dynamo workers (python3 -m dynamo.vllm --model X) and
//     any chart that lists args one per item land here.
//   - each arg as free text, for the bash -lc "vllm serve --model X ..."
//     wrapper convention.
//   - the env vars in modelEnvNames.
//   - a NIM image, whose identity is the image itself.
//
// Empty means no model identity: the pod is not a downloader.
func InferModelID(c corev1.Container) string {
	argv := append(append([]string{}, c.Command...), c.Args...)
	for i, tok := range argv {
		switch {
		case tok == "--model" || tok == "--model-path":
			if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
				return argv[i+1]
			}
		case strings.HasPrefix(tok, "--model=") || strings.HasPrefix(tok, "--model-path="):
			if v := tok[strings.IndexByte(tok, '=')+1:]; v != "" {
				return v
			}
		}
	}
	for _, a := range c.Args {
		if m := modelFlagPattern.FindStringSubmatch(a); m != nil {
			return m[1]
		}
	}
	for _, name := range modelEnvNames {
		for _, e := range c.Env {
			if e.Name == name && e.Value != "" {
				return e.Value
			}
		}
	}
	if IsNIMImage(c.Image) {
		return c.Image
	}
	return ""
}

// inferModelID keeps the historical unexported name for the composer.
func inferModelID(c corev1.Container) string { return InferModelID(c) }

// IsModelWorkload reports whether the pod is a model downloader: it asks
// for a GPU and its main container names a model. Supporting pods in a
// chart (frontend, router, planner, etcd, nats) fail one or both tests
// and are left alone by the election.
func IsModelWorkload(pod *corev1.Pod, mainContainer int) (modelID string, ok bool) {
	if pod == nil || mainContainer < 0 || mainContainer >= len(pod.Spec.Containers) {
		return "", false
	}
	if podGPURequest(pod) == 0 {
		return "", false
	}
	modelID = InferModelID(pod.Spec.Containers[mainContainer])
	return modelID, modelID != ""
}

// Role flags. In a disaggregated deployment (Dynamo over vLLM, SGLang or
// TRT-LLM) the prefill and decode workers run the same image, download
// the same model and differ only in a flag that names their role and in
// the KV-transfer plumbing between them. Hashing those flags would give
// each role its own cache and its own download, which is the duplication
// the election exists to remove. The model tree is identical across roles
// and mounts read-only; compile caches live in a per-pod writable shadow,
// so a role that needs different kernels recompiles into it and nothing
// is corrupted. Flags that change what is downloaded (--model, --revision,
// --tokenizer, quantization) stay in the hash.
var (
	roleFlagsNoValue = map[string]bool{
		"--is-prefill-worker": true, // dynamo.vllm
		"--is-decode-worker":  true,
	}
	roleFlagsWithValue = map[string]bool{
		"--disaggregation-mode":             true, // dynamo.sglang, dynamo.trtllm
		"--disaggregation-strategy":         true,
		"--disaggregation-bootstrap-port":   true,
		"--disaggregation-transfer-backend": true,
		"--kv-transfer-config":              true, // vLLM connector JSON, carries kv_role
		"--kv-events-config":                true, // KV event publishing, prefill side only
	}
	// GROVE_*: Grove gang-scheduling env the Dynamo operator injects, with
	// the component name and the pod index in it.
	roleEnvPrefixes = []string{"DYN_", "DYNAMO_", "GROVE_"}
	roleEnvExact    = []string{"ETCD_ENDPOINTS", "NATS_SERVER", "NATS_URL"}

	// roleFlagInString removes the same flags from a shell-script arg (the
	// bash -lc "vllm serve ..." convention), value quoted or bare.
	roleFlagInString = regexp.MustCompile(
		`\s--(?:is-prefill-worker|is-decode-worker)\b` +
			`|\s--(?:disaggregation-mode|disaggregation-strategy|disaggregation-bootstrap-port|disaggregation-transfer-backend|kv-transfer-config|kv-events-config)(?:=|\s+)(?:'[^']*'|"[^"]*"|\S+)`)
)

// stripRoleFlags returns args without the role flags, in both the
// one-token-per-item form and the single shell-string form.
func stripRoleFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if roleFlagsNoValue[a] {
			continue
		}
		if roleFlagsWithValue[a] {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
			continue
		}
		if eq := strings.IndexByte(a, '='); eq > 0 && roleFlagsWithValue[a[:eq]] {
			continue
		}
		if strings.ContainsAny(a, " \n\t") {
			a = roleFlagInString.ReplaceAllString(a, "")
		}
		out = append(out, a)
	}
	return out
}
