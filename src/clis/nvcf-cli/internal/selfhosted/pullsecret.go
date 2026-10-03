/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package selfhosted

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// deleteExactly pins a delete to the object that was inspected. The ownership
// guards read an object and then delete it by name, and in that window another
// actor can relabel it, or delete and recreate it under the same name. Without
// preconditions the delete lands on whatever holds the name by then; with them
// the apiserver rejects it as a conflict instead.
func deleteExactly(o metav1.Object) metav1.DeleteOptions {
	// UID only. UID already pins identity, which is the whole goal: a
	// delete-and-recreate under the same name changes it. ResourceVersion
	// additionally pins the object's version, so any concurrent write turns
	// the delete into a 409 that every sweep then swallows. That bites hardest
	// on a Job, whose .status the Job controller mutates continuously.
	uid := o.GetUID()
	return metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	}
}

const (
	// Project-specific name so the auto-created / mirrored pull secret
	// can never collide with the conventional `nvcr-pull-secret` that
	// operators or the install flow may already manage in 'default'.
	//
	// The name is suffixed per role by validatorPullSecretRoleName: ModeSplit
	// runs both validators concurrently, and the two kubecontexts can resolve
	// to the same cluster. One shared Secret means the role that finishes first
	// deletes it while the other Job is still pulling, which the kubelet
	// reports as FailedToRetrieveImagePullSecret.
	validatorPullSecretName = "nvcf-preflight-pull-secret"
)

// validatorPullSecretRoleName returns the managed pull-secret name for a role.
// A Secret cannot be co-owned, so each role needs its own rather than a shared
// object with a role label.
func validatorPullSecretRoleName(role string) string {
	if role == "" {
		return validatorPullSecretName
	}
	return validatorPullSecretName + "-" + role
}

// validatorPullSecretRunName is the name this run mints under. It carries the
// run's unguessable suffix because the managed labels are three public
// constants: with a predictable name, anyone able to create a Secret in this
// namespace could pre-create it wearing those labels, pass the ownership check,
// and have the NGC credential written into an object they control. Same
// reasoning as the per-run RBAC names.
func validatorPullSecretRunName(role, runID string) string {
	base := validatorPullSecretRoleName(role)
	if runID == "" {
		return base
	}
	return base + "-" + runID
}

// The NGC API key's environment variables, in the order
// ensureLocalImagePullSecrets in cmd/self_hosted_up.go reads them.
var ngcAPIKeyEnvNames = []string{
	"NGC_IMAGE_PULL_API_KEY",
	"NVCF_NGCR_API_KEY",
	"NVCF_NGC_API_KEY",
	"NGC_API_KEY",
}

// Namespaces scanned, in order, for an existing docker-registry secret with
// credentials for the validator image's registry. "default" wins first so we
// avoid an unnecessary mirror; "nvcf" is where the chart-installed secret
// lands post-up; the remainder match the install flow's secret targets.
var validatorPullSecretSearchNamespaces = []string{
	clusterValidatorNamespace,
	"nvcf",
	"cassandra-system",
	"nats-system",
	"api-keys",
	"ess",
	"sis",
	"vault-system",
	"nvca-operator",
	"nvca-system",
	"nvcf-backend",
}

// validatorPullSecretListTimeout bounds one namespace's List in the scan, so
// a slow namespace cannot starve the rest; validatorPullSecretWriteTimeout
// bounds the Create of the Secret this run attaches, which gets its own time
// rather than whatever the scan left.
var (
	validatorPullSecretListTimeout  = 5 * time.Second
	validatorPullSecretWriteTimeout = 10 * time.Second
)

// resolveValidatorPullSecret picks the imagePullSecret name to attach to the
// validator Job. Precedence:
//
//  1. provided != "" -> use as-is (--cluster-validator-pull-secret flag).
//  2. Cluster scan -> adopt an operator's Secret in the validator namespace,
//     or copy the matching registry's entry of one found elsewhere.
//  3. This machine's credential for the image's registry, the one the local
//     credential check probed -> mint a Secret for this run. The NGC key
//     goes only to nvcr.io.
//  4. "" -> the image is pulled without a secret.
//
// note says what the run copied or created, for the operator to see. err is
// a copy or create that failed; read-side failures fall through to the next
// layer.
//
// A Secret this run mints or mirrors is recorded in created, the only record
// its cleanup trusts.
func resolveValidatorPullSecret(
	ctx context.Context,
	client kubernetes.Interface,
	provided, image, role, runID string,
	preserve bool,
	created runObjects,
) (name, note string, err error) {
	if provided != "" {
		return provided, "", nil
	}
	registry, repo, _, ok := parseImageRef(image)
	if !ok {
		return "", "", nil
	}

	runName := validatorPullSecretRunName(role, runID)
	write := func(cfg []byte) error {
		wctx, cancel := context.WithTimeout(ctx, validatorPullSecretWriteTimeout)
		defer cancel()
		return writeDockerConfigSecret(wctx, client, clusterValidatorNamespace, runName, role, runID, cfg, preserve,
			created)
	}

	if src, cfg := findClusterPullSecret(ctx, client, registry, repo); src != nil {
		if src.Namespace == clusterValidatorNamespace {
			return src.Name, "", nil
		}
		// Mirror under the validator's run-scoped name rather than the source
		// secret's name, which could collide with an unrelated Secret in the
		// destination namespace.
		if err := write(cfg); err != nil {
			return "", "", fmt.Errorf("copy pull secret %s/%s to %s/%s: %w",
				src.Namespace, src.Name, clusterValidatorNamespace, runName, err)
		}
		return runName, fmt.Sprintf("copied the pull credential for %s from %s/%s into %s/%s for this run",
			registry, src.Namespace, src.Name, clusterValidatorNamespace, runName), nil
	}

	cred, ok, lookupErr := registryCredentialsFrom(ctx).lookup(ctx, registry)
	if !ok {
		if lookupErr != nil {
			return "", "", fmt.Errorf("no pull secret for %s in the cluster, and reading this machine's "+
				"credential failed: %w", registry, lookupErr)
		}
		return "", "", nil
	}
	cfg, err := buildDockerConfigJSON(dockerConfigKey(registry), cred.user, cred.pass)
	if err != nil {
		return "", "", fmt.Errorf("encode dockerconfigjson for %s: %w", registry, err)
	}
	if err := write(cfg); err != nil {
		return "", "", fmt.Errorf("create pull secret %s/%s: %w", clusterValidatorNamespace, runName, err)
	}
	return runName, fmt.Sprintf("created pull secret %s/%s for %s from %s for this run",
		clusterValidatorNamespace, runName, registry, cred.source), nil
}

// findClusterPullSecret walks validatorPullSecretSearchNamespaces and returns
// the first docker-registry Secret with credentials for registry, with a
// dockerconfigjson holding only that registry's entry. An operator's Secret
// in the validator namespace is returned to be used as is; one this CLI
// minted for another run is skipped (see isAnyValidatorSecret).
func findClusterPullSecret(
	ctx context.Context, client kubernetes.Interface, registry, repo string,
) (*corev1.Secret, []byte) {
	for _, ns := range validatorPullSecretSearchNamespaces {
		secrets, err := listDockerConfigSecrets(ctx, client, ns)
		if err != nil {
			continue
		}
		for i := range secrets {
			s := &secrets[i]
			if s.Type != corev1.SecretTypeDockerConfigJson {
				continue
			}
			// Adopt only an operator-supplied Secret (no managed labels, so
			// no sweep touches it). Never one another run minted: adopting
			// one reuses a possibly stale credential, can outlive this run,
			// and can be deleted by its own run's sweep mid-pull, which the
			// kubelet reports as FailedToRetrieveImagePullSecret.
			if s.Namespace == clusterValidatorNamespace && isAnyValidatorSecret(s) {
				continue
			}
			if cfg, ok := filterDockerConfig(s.Data[corev1.DockerConfigJsonKey], registry, repo); ok {
				return s, cfg
			}
		}
	}
	return nil, nil
}

// listDockerConfigSecrets lists namespace's docker-registry Secrets, so the
// scan does not fetch every other Secret's key material. A server that
// refuses the type field selector is asked for everything instead, and the
// caller filters.
func listDockerConfigSecrets(
	ctx context.Context, client kubernetes.Interface, namespace string,
) ([]corev1.Secret, error) {
	lctx, cancel := context.WithTimeout(ctx, validatorPullSecretListTimeout)
	defer cancel()
	list, err := client.CoreV1().Secrets(namespace).List(lctx, metav1.ListOptions{
		FieldSelector: "type=" + string(corev1.SecretTypeDockerConfigJson),
	})
	if apierrors.IsBadRequest(err) {
		list, err = client.CoreV1().Secrets(namespace).List(lctx, metav1.ListOptions{})
	}
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// filterDockerConfig returns a dockerconfigjson holding only cfg's entry for
// registry, and whether it has one. A copy carries no other registry's
// credential. Keys match the way the kubelet matches them: scheme and a
// /v1/ or /v2/ prefix ignored, and a key with a path only for repositories
// under it.
func filterDockerConfig(cfg []byte, registry, repo string) ([]byte, bool) {
	var doc struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	if err := json.Unmarshal(cfg, &doc); err != nil {
		return nil, false
	}
	key, ok := "", false
	if _, exact := doc.Auths[dockerConfigKey(registry)]; exact {
		key, ok = dockerConfigKey(registry), true
	} else {
		for k := range doc.Auths {
			if dockerConfigKeyMatches(k, registry, repo) && (!ok || k < key) {
				key, ok = k, true
			}
		}
	}
	if !ok {
		return nil, false
	}
	out, err := json.Marshal(map[string]any{"auths": map[string]json.RawMessage{key: doc.Auths[key]}})
	return out, err == nil
}

// dockerConfigKeyMatches reports whether a dockerconfigjson auths key covers
// an image in repo on registry, as the kubelet reads the key.
func dockerConfigKeyMatches(key, registry, repo string) bool {
	rest := strings.TrimPrefix(strings.TrimPrefix(key, "https://"), "http://")
	host, path, _ := strings.Cut(rest, "/")
	if !sameDockerHost(host, registry) {
		return false
	}
	path = strings.TrimSuffix(path, "/")
	if p, ok := strings.CutPrefix(path, "v1"); ok && (p == "" || strings.HasPrefix(p, "/")) {
		path = strings.TrimPrefix(p, "/")
	} else if p, ok := strings.CutPrefix(path, "v2"); ok && (p == "" || strings.HasPrefix(p, "/")) {
		path = strings.TrimPrefix(p, "/")
	}
	return path == "" || repo == path || strings.HasPrefix(repo, path+"/")
}

// Mirrors cmd/self_hosted_up.go's dockerConfigJSON.
func buildDockerConfigJSON(registry, username, password string) ([]byte, error) {
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	return json.Marshal(map[string]any{
		"auths": map[string]any{
			registry: map[string]string{
				"username": username,
				"password": password,
				"auth":     auth,
			},
		},
	})
}

// writeDockerConfigSecret creates the pull Secret this run will reference from
// a .dockerconfigjson body, and records it in created.
//
// Create-only. The name carries this run's unguessable suffix, so nothing this
// CLI created can already hold it and any collision is another object.
// Adopting one would write the NGC credential into something we do not own,
// which label-based ownership cannot prevent: the managed labels are three
// public constants anyone can copy onto a Secret they pre-create under a
// predictable name.
//
// The Secret is the legacy kubernetes.io/dockercfg type, which the kubelet
// pulls with just as well. Released CLIs adopt any kubernetes.io/dockerconfigjson
// Secret in the namespace whose registry matches, whatever its labels, so with
// that type they would reuse this run's NGC key and lose it mid-pull when this
// run's cleanup deletes it.
func writeDockerConfigSecret(
	ctx context.Context, client kubernetes.Interface, namespace, name, role, runID string, dockerConfig []byte,
	preserve bool, created runObjects,
) error {
	dockercfg, err := dockerCfgFromConfigJSON(dockerConfig)
	if err != nil {
		return fmt.Errorf("convert docker config: %w", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    clusterValidatorRunLabels(role, runID, preserve),
		},
		Type: corev1.SecretTypeDockercfg,
		Data: map[string][]byte{corev1.DockerConfigKey: dockercfg},
	}
	got, err := client.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf(
				"refusing to overwrite existing secret %s/%s: this run generated that name, so "+
					"another object already holds it; pass --cluster-validator-pull-secret to "+
					"choose an explicit secret name",
				namespace, name)
		}
		return fmt.Errorf("create: %w", err)
	}
	created.add(kindSecret, got)
	return nil
}

// dockerCfgFromConfigJSON turns a .dockerconfigjson body into a .dockercfg
// one: the same per-registry entries without the "auths" wrapper.
func dockerCfgFromConfigJSON(cfg []byte) ([]byte, error) {
	var doc struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	if err := json.Unmarshal(cfg, &doc); err != nil {
		return nil, err
	}
	if len(doc.Auths) == 0 {
		return nil, fmt.Errorf("no auths entries")
	}
	return json.Marshal(doc.Auths)
}

// isAnyValidatorSecret reports whether a Secret was minted by any version of
// this CLI's validator, including released ones that label with
// managed-by=nvcf-cli and use the bare nvcf-preflight-pull-secret name. Such a
// Secret belongs to another run: its CLI deletes it on exit, possibly while
// this run's pod is still pulling, and a stale one would be reused forever.
func isAnyValidatorSecret(s *corev1.Secret) bool {
	return hasValidatorManagedLabels(s.Labels) ||
		s.Labels["app.kubernetes.io/name"] == clusterValidatorAppLabel ||
		strings.HasPrefix(s.Name, validatorPullSecretName)
}

// hasValidatorManagedLabels reports whether an object carries the labels this
// CLI stamps on everything it creates. It is the ownership test for every
// resource the validator writes to or deletes by name, not just Secrets.
func hasValidatorManagedLabels(labels map[string]string) bool {
	for k, v := range clusterValidatorLabels() {
		if labels[k] != v {
			return false
		}
	}
	return true
}
