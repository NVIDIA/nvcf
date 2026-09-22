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
	"os"
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
	// on the singleton Job sweep, whose only target is an active Job whose
	// .status the Job controller mutates continuously.
	uid := o.GetUID()
	return metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	}
}

const (
	// Project-specific name so the auto-created / mirrored pull secret
	// can never collide with the conventional `nvcr-pull-secret` that
	// operators or the install flow may already manage in 'default'.
	// Pairs with isManagedByValidatorCLI label guard for defense-in-depth.
	//
	// The name is suffixed per role by validatorPullSecretRoleName: ModeSplit
	// runs both validators concurrently, and the two kubecontexts can resolve
	// to the same cluster. One shared Secret means the role that finishes first
	// deletes it while the other Job is still pulling, which the kubelet
	// reports as FailedToRetrieveImagePullSecret.
	validatorPullSecretName        = "nvcf-preflight-pull-secret"
	validatorPullSecretScanTimeout = 10 * time.Second
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

// Mirrors the chain used by ensureLocalImagePullSecrets in cmd/self_hosted_up.go.
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

// resolveValidatorPullSecret picks the imagePullSecret name to attach to the
// validator Job. Precedence:
//
//  1. provided != "" -> use as-is (--cluster-validator-pull-secret flag).
//  2. Cluster scan -> mirror a matching secret into the validator namespace.
//  3. NGC env var -> mint a secret in the validator namespace.
//  4. "" -> kubelet will surface ImagePullBackOff if the image is private.
//
// Only hard mirror/create failures propagate; read-side failures fall through
// to the next layer.
func resolveValidatorPullSecret(
	ctx context.Context,
	client kubernetes.Interface,
	provided, image, role string,
) (string, error) {
	if provided != "" {
		return provided, nil
	}
	registry := parseRegistryFromImage(image)
	if registry == "" {
		return "", nil
	}

	scanCtx, cancel := context.WithTimeout(ctx, validatorPullSecretScanTimeout)
	defer cancel()

	if name, err := scanAndMirrorPullSecret(scanCtx, client, registry, role); err != nil {
		return "", err
	} else if name != "" {
		return name, nil
	}

	if name, err := autoCreatePullSecretFromEnv(scanCtx, client, registry, role); err != nil {
		return "", err
	} else if name != "" {
		return name, nil
	}

	return "", nil
}

// Returns "" for Docker Hub shorthand (no host segment), since the scan
// needs an auths.<host> key to match against.
func parseRegistryFromImage(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}
	slash := strings.Index(image, "/")
	if slash <= 0 {
		return ""
	}
	head := image[:slash]
	if !strings.ContainsAny(head, ".:") && head != "localhost" {
		return ""
	}
	return head
}

// Walks validatorPullSecretSearchNamespaces and returns the first
// docker-registry secret whose dockerconfigjson has an auths entry for
// registry. Mirrors the body into clusterValidatorNamespace when the match
// lives elsewhere, so the Job can reference the secret without cross-namespace
// lookups.
func scanAndMirrorPullSecret(ctx context.Context, client kubernetes.Interface, registry, role string) (string, error) {
	for _, ns := range validatorPullSecretSearchNamespaces {
		// Filter client-side rather than via FieldSelector: server-side
		// type= selector on Secrets is only honored from k8s 1.27 onward.
		secrets, err := client.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue
		}
		for _, s := range secrets.Items {
			if s.Type != corev1.SecretTypeDockerConfigJson {
				continue
			}
			cfg, ok := s.Data[corev1.DockerConfigJsonKey]
			if !ok || !dockerConfigHasRegistry(cfg, registry) {
				continue
			}
			if s.Namespace == clusterValidatorNamespace {
				return s.Name, nil
			}
			// Mirror under the validator's well-known name rather than the
			// source secret's name. Using the source name risks colliding
			// with an unrelated operator/chart secret of the same name in
			// the destination namespace, and writeDockerConfigSecret's
			// delete-and-recreate path would otherwise destroy that
			// secret on type mismatch.
			if err := writeDockerConfigSecret(ctx, client, clusterValidatorNamespace, validatorPullSecretRoleName(role), role, cfg); err != nil {
				return "", fmt.Errorf("mirror pull secret %s/%s to %s/%s: %w",
					s.Namespace, s.Name, clusterValidatorNamespace, validatorPullSecretRoleName(role), err)
			}
			return validatorPullSecretRoleName(role), nil
		}
	}
	return "", nil
}

func dockerConfigHasRegistry(cfg []byte, registry string) bool {
	var doc struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	if err := json.Unmarshal(cfg, &doc); err != nil {
		return false
	}
	_, ok := doc.Auths[registry]
	return ok
}

func autoCreatePullSecretFromEnv(ctx context.Context, client kubernetes.Interface, registry, role string) (string, error) {
	apiKey := firstNonEmptyEnv(ngcAPIKeyEnvNames...)
	if apiKey == "" {
		return "", nil
	}
	// Only ever hand the NGC key to NGC. Without this an operator who mirrors
	// the validator image to ghcr.io or a corporate Harbor and still exports
	// NGC_API_KEY gets it written as that registry's password, and the kubelet
	// then sends the live key to a third party as HTTP Basic auth, where it
	// lands in their access logs. The local probe already guards this the same
	// way; the Secret path did not.
	if !isNGCRegistry(registry) {
		return "", nil
	}
	cfg, err := buildDockerConfigJSON(registry, "$oauthtoken", apiKey)
	if err != nil {
		return "", fmt.Errorf("encode dockerconfigjson for %s: %w", registry, err)
	}
	if err := writeDockerConfigSecret(ctx, client, clusterValidatorNamespace, validatorPullSecretRoleName(role), role, cfg); err != nil {
		return "", fmt.Errorf("auto-create pull secret %s/%s: %w",
			clusterValidatorNamespace, validatorPullSecretRoleName(role), err)
	}
	return validatorPullSecretRoleName(role), nil
}

// Mirrors cmd/self_hosted_up.go's firstNonEmptyEnv; kept local to avoid an
// internal->cmd import.
func firstNonEmptyEnv(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
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

// Mirrors cmd/self_hosted_up.go's ensureDockerConfigSecret. Attaches the
// CLI's managed-by labels so a future cleanup path can identify resources
// to remove.
func writeDockerConfigSecret(ctx context.Context, client kubernetes.Interface, namespace, name, role string, dockerConfig []byte) error {
	labels := clusterValidatorRoleLabels(role)
	secrets := client.CoreV1().Secrets(namespace)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: dockerConfig},
	}
	current, err := secrets.Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil && current.Type == corev1.SecretTypeDockerConfigJson:
		// Same guard as the type-mismatch branch below, for the same reason. A
		// matching type is not permission to write: an operator-owned secret
		// with a colliding name holds their registry credentials, and this
		// would replace the data and stamp our managed labels on it, after
		// which the role sweep would delete it outright.
		if err := refuseIfUnmanaged(current, namespace, name, ""); err != nil {
			return err
		}
		if current.Data == nil {
			current.Data = map[string][]byte{}
		}
		current.Data[corev1.DockerConfigJsonKey] = dockerConfig
		if current.Labels == nil {
			current.Labels = map[string]string{}
		}
		for k, v := range labels {
			current.Labels[k] = v
		}
		if _, err := secrets.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update: %w", err)
		}
		return nil
	case err == nil:
		// Secret.Type is immutable; can't Update across a type change.
		// Guard the delete: only replace secrets we previously managed.
		// Without this, an operator-owned secret with a colliding name
		// (e.g. an Opaque secret in 'default' sharing the validator's
		// pull-secret name) would be silently destroyed.
		if err := refuseIfUnmanaged(current, namespace, name,
			fmt.Sprintf("type=%s", current.Type)); err != nil {
			return err
		}
		// A conflict means the object changed between the ownership check and
		// this delete, so the thing now holding the name was never vetted.
		// Stop rather than fall through to Create, which would otherwise
		// silently adopt or overwrite it.
		if err := secrets.Delete(ctx, name, deleteExactly(current)); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete incompatible secret type %s: %w", current.Type, err)
		}
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("get: %w", err)
	}
	if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create: %w", err)
		}
		// Lost a delete->create race: another actor recreated the secret
		// in the narrow window between our Delete and Create. Refetch and
		// overwrite with our content so the caller still gets the
		// credentials it asked for.
		current, getErr := secrets.Get(ctx, name, metav1.GetOptions{})
		if getErr != nil {
			return fmt.Errorf("get after create race: %w", getErr)
		}
		if err := refuseIfUnmanaged(current, namespace, name, "lost create race"); err != nil {
			return err
		}
		if current.Type != corev1.SecretTypeDockerConfigJson {
			return fmt.Errorf("create race left secret %s/%s with type %s, want %s",
				namespace, name, current.Type, corev1.SecretTypeDockerConfigJson)
		}
		if current.Data == nil {
			current.Data = map[string][]byte{}
		}
		current.Data[corev1.DockerConfigJsonKey] = dockerConfig
		if current.Labels == nil {
			current.Labels = map[string]string{}
		}
		for k, v := range labels {
			current.Labels[k] = v
		}
		if _, err := secrets.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update after create race: %w", err)
		}
	}
	return nil
}

// isManagedByValidatorCLI reports whether the secret carries the labels
// writeDockerConfigSecret stamps on every secret it creates. Used to
// gate the delete-then-recreate branch so we never destroy an operator-
// or chart-owned secret that happens to share a name with one of ours.
// refuseIfUnmanaged rejects a Secret nvcf-cli does not own. Every path in
// writeDockerConfigSecret that mutates an existing Secret must call this:
// overwriting an operator-owned secret replaces their registry credentials and
// stamps our managed labels on it, after which the role sweep deletes it.
//
// detail is appended to the message when there is something useful to say
// (the conflicting type, the race), otherwise "".
func refuseIfUnmanaged(s *corev1.Secret, namespace, name, detail string) error {
	if isManagedByValidatorCLI(s) {
		return nil
	}
	if detail != "" {
		detail = " (" + detail + ")"
	}
	return fmt.Errorf(
		"refusing to overwrite %s/%s%s which is not managed by nvcf-cli; "+
			"pass --cluster-validator-pull-secret to choose an explicit secret name",
		namespace, name, detail)
}

func isManagedByValidatorCLI(s *corev1.Secret) bool {
	return hasValidatorManagedLabels(s.Labels)
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
