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

package nvca

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcaclientv2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/typed/nvca/v2beta1"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
)

var (
	errICMSRequestDeletingDuringBinding = errors.New(
		"ICMS request began deleting while persisting model cache binding")
	errICMSRequestReplacedDuringBinding = errors.New(
		"ICMS request was replaced while persisting model cache binding")
	errRegularModelCacheBindingRetiring = errors.New(
		"regular model cache binding is Retiring")
)

type modelCacheBindingInput struct {
	selection       *nvcastorage.PersistedModelCacheStorageSelection
	sharingDomain   string
	cacheHandle     string
	writerNamespace string
}

func (c *BackendK8sCache) modelCacheBindingInput(
	req *nvcav2beta1.ICMSRequest,
) (*modelCacheBindingInput, error) {
	raw := req.Annotations[nvcastorage.ModelCacheStorageSelectionAnnotationKey]
	if raw == "" {
		return nil, nil
	}
	selection, err := nvcastorage.ParsePersistedModelCacheStorageSelection(raw)
	if err != nil {
		return nil, fmt.Errorf("parse persisted model cache storage selection: %w", err)
	}
	if selection.Mode != nvcastorage.ModelCacheSelectionDurable {
		return nil, nil
	}

	cacheSpec, workflow := cacheSelectionInput(req)
	if cacheSpec == nil || cacheSpec.CacheHandle == "" {
		return nil, fmt.Errorf("durable model cache selection has no cache handle")
	}
	if workflow != selection.Workflow {
		return nil, fmt.Errorf("durable model cache workflow changed from %q to %q",
			selection.Workflow, workflow)
	}
	if req.Spec.NCAId == "" {
		return nil, fmt.Errorf("durable model cache selection has no sharing domain")
	}
	if req.Namespace == "" || req.Name == "" || req.UID == "" {
		return nil, fmt.Errorf("durable model cache request requires namespace, name, and UID")
	}

	writerNamespace := nvcastorage.ModelCacheInitNamespace
	if workflow == nvcastorage.ModelCacheWorkflowRegular {
		writerNamespace = c.podInstanceNamespace
	}
	if writerNamespace == "" {
		return nil, fmt.Errorf("durable model cache writer namespace is empty")
	}
	return &modelCacheBindingInput{
		selection:       selection,
		sharingDomain:   req.Spec.NCAId,
		cacheHandle:     cacheSpec.CacheHandle,
		writerNamespace: writerNamespace,
	}, nil
}

// ensureModelCacheBinding creates or adopts the immutable binding and adds the
// exact ICMSRequest UID as a reference before persisting the binding reference
// on the request. A true return value means the request annotation changed and
// the caller must stop this reconcile before creating any side effect.
func (c *BackendK8sCache) ensureModelCacheBinding(
	ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
) (bool, error) {
	input, err := c.modelCacheBindingInput(req)
	if err != nil {
		return false, nvcaerrors.TerminalError(err)
	}
	if input == nil {
		return false, nil
	}
	if c.clients == nil || c.clients.BART == nil || c.clients.K8s == nil {
		return false, fmt.Errorf("model cache binding clients are not configured")
	}

	expected, err := nvcastorage.NewModelCacheBinding(
		input.selection, input.sharingDomain, input.cacheHandle, input.writerNamespace)
	if err != nil {
		return false, nvcaerrors.TerminalError(fmt.Errorf("build model cache binding intent: %w", err))
	}
	bindings := c.clients.BART.NvcaV2beta1().ModelCacheBindings(expected.Namespace)

	var binding *nvcav2beta1.ModelCacheBinding
	if input.selection.BindingName == "" {
		binding, err = bindings.Get(ctx, expected.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if err := nvcastorage.ValidateModelCacheStorageSelectionInputsWithClientset(
				ctx, c.clients.K8s, c.systemNamespace, input.selection); err != nil {
				if errors.Is(err, nvcastorage.ErrModelCacheStorageSelectionDrift) || apierrors.IsNotFound(err) {
					return false, nvcaerrors.TerminalError(
						fmt.Errorf("validate model cache selection before binding creation: %w", err))
				}
				return false, fmt.Errorf("validate model cache selection before binding creation: %w", err)
			}
			binding, err = bindings.Create(ctx, expected, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				binding, err = bindings.Get(ctx, expected.Name, metav1.GetOptions{})
			}
		}
		if err != nil {
			return false, fmt.Errorf("get or create model cache binding %s/%s: %w",
				expected.Namespace, expected.Name, err)
		}
	} else {
		if input.selection.BindingName != expected.Name {
			return false, nvcaerrors.TerminalError(fmt.Errorf(
				"persisted model cache binding name %q does not match expected %q",
				input.selection.BindingName, expected.Name))
		}
		binding, err = bindings.Get(ctx, input.selection.BindingName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nvcaerrors.TerminalError(fmt.Errorf(
					"persisted model cache binding %s/%s is missing",
					expected.Namespace, input.selection.BindingName))
			}
			return false, fmt.Errorf("get persisted model cache binding %s/%s: %w",
				expected.Namespace, input.selection.BindingName, err)
		}
	}

	if binding.UID == "" {
		return false, nvcaerrors.TerminalError(fmt.Errorf(
			"model cache binding %s/%s has no API-assigned UID", binding.Namespace, binding.Name))
	}
	if err := nvcastorage.ValidateModelCacheBindingIntent(
		binding, input.selection, input.sharingDomain, input.cacheHandle, input.writerNamespace); err != nil {
		return false, nvcaerrors.TerminalError(err)
	}
	if input.selection.BindingName != "" {
		if err := validateModelCacheBindingForEnsure(binding, input, req); err != nil {
			return false, nvcaerrors.TerminalError(err)
		}
		return false, nil
	}

	binding, err = c.ensureActiveModelCacheBindingReference(ctx, bindings, input, req)
	if err != nil {
		return false, err
	}
	changed, err := c.persistModelCacheBindingReference(ctx, req, input.selection, binding)
	if errors.Is(err, errICMSRequestDeletingDuringBinding) ||
		errors.Is(err, errICMSRequestReplacedDuringBinding) {
		if releaseErr := c.removeModelCacheBindingReference(
			ctx, bindings, binding.Name, input, req.Namespace, req.Name, req.UID); releaseErr != nil {
			return false, fmt.Errorf("%w; release newly-added binding reference: %v", err, releaseErr)
		}
	}
	return changed, err
}

func (c *BackendK8sCache) ensureActiveModelCacheBindingReference(
	ctx context.Context,
	bindings nvcaclientv2beta1.ModelCacheBindingInterface,
	input *modelCacheBindingInput,
	req *nvcav2beta1.ICMSRequest,
) (*nvcav2beta1.ModelCacheBinding, error) {
	var result *nvcav2beta1.ModelCacheBinding
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		binding, err := bindings.Get(ctx, nvcastorage.ModelCacheBindingName(input.cacheHandle), metav1.GetOptions{})
		if err != nil {
			return err
		}
		if err := nvcastorage.ValidateModelCacheBindingIntent(
			binding, input.selection, input.sharingDomain, input.cacheHandle, input.writerNamespace); err != nil {
			return nvcaerrors.TerminalError(err)
		}

		changed := false
		switch binding.Status.Phase {
		case "":
			if binding.Status.LastPhaseTransitionTime != nil || len(binding.Status.RequestReferences) != 0 ||
				binding.Status.Realized != nil || len(binding.Status.Conditions) != 0 {
				return nvcaerrors.TerminalError(fmt.Errorf(
					"model cache binding %s/%s has a partially initialized status",
					binding.Namespace, binding.Name))
			}
			now := metav1.Now()
			binding.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseActive
			binding.Status.LastPhaseTransitionTime = &now
			changed = true
		case nvcav2beta1.ModelCacheBindingPhaseActive:
		case nvcav2beta1.ModelCacheBindingPhaseRetiring:
			return nvcaerrors.TerminalError(fmt.Errorf(
				"model cache binding %s/%s is Retiring", binding.Namespace, binding.Name))
		default:
			return nvcaerrors.TerminalError(fmt.Errorf(
				"model cache binding %s/%s has unknown phase %q",
				binding.Namespace, binding.Name, binding.Status.Phase))
		}

		originalRefs := append([]nvcav2beta1.ModelCacheBindingRequestReference(nil),
			binding.Status.RequestReferences...)
		refs, exactFound, err := c.referencesWithCurrentRequest(ctx, binding, req)
		if err != nil {
			return err
		}
		if !exactFound {
			refs = append(refs, nvcav2beta1.ModelCacheBindingRequestReference{
				Namespace: req.Namespace,
				Name:      req.Name,
				UID:       req.UID,
			})
			changed = true
		}
		sort.Slice(refs, func(i, j int) bool {
			if refs[i].Namespace != refs[j].Namespace {
				return refs[i].Namespace < refs[j].Namespace
			}
			if refs[i].Name != refs[j].Name {
				return refs[i].Name < refs[j].Name
			}
			return refs[i].UID < refs[j].UID
		})
		if !reflect.DeepEqual(refs, originalRefs) {
			changed = true
		}
		binding.Status.RequestReferences = refs
		if !changed {
			result = binding
			return nil
		}
		result, err = bindings.UpdateStatus(ctx, binding, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("activate model cache binding for request %s/%s: %w",
			req.Namespace, req.Name, err)
	}
	if result == nil {
		return nil, fmt.Errorf("activate model cache binding for request %s/%s returned no binding",
			req.Namespace, req.Name)
	}
	if err := validateActiveModelCacheBindingForRequest(result, input, req); err != nil {
		return nil, nvcaerrors.TerminalError(err)
	}
	return result, nil
}

func (c *BackendK8sCache) referencesWithCurrentRequest(
	ctx context.Context,
	binding *nvcav2beta1.ModelCacheBinding,
	req *nvcav2beta1.ICMSRequest,
) ([]nvcav2beta1.ModelCacheBindingRequestReference, bool, error) {
	refs := make([]nvcav2beta1.ModelCacheBindingRequestReference, 0,
		len(binding.Status.RequestReferences)+1)
	exactFound := false
	for _, ref := range binding.Status.RequestReferences {
		if ref.Namespace != req.Namespace || ref.Name != req.Name {
			refs = append(refs, ref)
			continue
		}
		if ref.UID == req.UID {
			if exactFound {
				return nil, false, nvcaerrors.TerminalError(fmt.Errorf(
					"model cache binding %s/%s contains a duplicate request reference for %s/%s",
					binding.Namespace, binding.Name, req.Namespace, req.Name))
			}
			exactFound = true
			refs = append(refs, ref)
			continue
		}

		live, err := c.clients.BART.NvcaV2beta1().ICMSRequests(ref.Namespace).
			Get(ctx, ref.Name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return nil, false, fmt.Errorf("validate stale model cache binding reference %s/%s: %w",
				ref.Namespace, ref.Name, err)
		case live.UID == ref.UID:
			return nil, false, nvcaerrors.TerminalError(fmt.Errorf(
				"model cache binding %s/%s is already referenced by live request %s/%s UID %s",
				binding.Namespace, binding.Name, ref.Namespace, ref.Name, ref.UID))
		default:
			// The namespaced request name was reused after the recorded UID
			// disappeared. The stale reference is safe to replace.
			continue
		}
	}
	return refs, exactFound, nil
}

func (c *BackendK8sCache) persistModelCacheBindingReference(
	ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
	selection *nvcastorage.PersistedModelCacheStorageSelection,
	binding *nvcav2beta1.ModelCacheBinding,
) (bool, error) {
	boundSelection := *selection
	boundSelection.RequiredAccessModes = append(
		[]corev1.PersistentVolumeAccessMode(nil), selection.RequiredAccessModes...)
	boundSelection.BindingName = binding.Name
	boundSelection.BindingUID = binding.UID
	payload, err := boundSelection.Marshal()
	if err != nil {
		return false, nvcaerrors.TerminalError(fmt.Errorf("marshal bound model cache selection: %w", err))
	}

	changed := false
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest, err := c.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).
			Get(ctx, req.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if latest.UID != req.UID {
			return fmt.Errorf("%w: expected UID %s, found %s",
				errICMSRequestReplacedDuringBinding, req.UID, latest.UID)
		}
		if !latest.DeletionTimestamp.IsZero() {
			return errICMSRequestDeletingDuringBinding
		}
		current, err := nvcastorage.ParsePersistedModelCacheStorageSelection(
			latest.Annotations[nvcastorage.ModelCacheStorageSelectionAnnotationKey])
		if err != nil {
			return nvcaerrors.TerminalError(fmt.Errorf(
				"parse latest persisted model cache storage selection: %w", err))
		}
		if reflect.DeepEqual(current, &boundSelection) {
			// Another reconcile may have committed the binding while this
			// caller still holds the unbound request snapshot. Force that
			// stale caller to stop before runtime side effects.
			if !reflect.DeepEqual(selection, &boundSelection) {
				changed = true
			}
			return nil
		}
		if !reflect.DeepEqual(current, selection) {
			return nvcaerrors.TerminalError(fmt.Errorf(
				"model cache storage selection changed while binding request %s/%s",
				req.Namespace, req.Name))
		}
		if latest.Annotations == nil {
			latest.Annotations = map[string]string{}
		}
		latest.Annotations[nvcastorage.ModelCacheStorageSelectionAnnotationKey] = payload
		_, err = c.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).
			Update(ctx, latest, metav1.UpdateOptions{})
		if err == nil {
			changed = true
		}
		return err
	})
	if err != nil {
		return false, fmt.Errorf("persist model cache binding reference on request %s/%s: %w",
			req.Namespace, req.Name, err)
	}
	return changed, nil
}

// validateModelCacheBindingForRuntime makes the binding, not the mutable live
// StorageClass or catalog, authoritative after selection has been committed.
func (c *BackendK8sCache) validateModelCacheBindingForRuntime(
	ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
) error {
	_, err := c.activeModelCacheBindingForRuntime(ctx, req)
	return err
}

func (c *BackendK8sCache) activeModelCacheBindingForRuntime(
	ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
) (*nvcav2beta1.ModelCacheBinding, error) {
	input, err := c.modelCacheBindingInput(req)
	if err != nil {
		return nil, nvcaerrors.TerminalError(err)
	}
	if input == nil {
		return nil, nil
	}
	if input.selection.BindingName == "" || input.selection.BindingUID == "" {
		return nil, nvcaerrors.TerminalError(fmt.Errorf(
			"durable model cache selection has no committed binding reference"))
	}
	if c.clients == nil || c.clients.BART == nil {
		return nil, fmt.Errorf("model cache binding client is not configured")
	}
	binding, err := c.clients.BART.NvcaV2beta1().
		ModelCacheBindings(nvcastorage.ModelCacheInitNamespace).
		Get(ctx, input.selection.BindingName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nvcaerrors.TerminalError(fmt.Errorf(
				"persisted model cache binding %s/%s is missing",
				nvcastorage.ModelCacheInitNamespace, input.selection.BindingName))
		}
		return nil, fmt.Errorf("get persisted model cache binding %s/%s: %w",
			nvcastorage.ModelCacheInitNamespace, input.selection.BindingName, err)
	}
	if binding.Status.Phase == nvcav2beta1.ModelCacheBindingPhaseRetiring &&
		input.selection.Workflow == nvcastorage.ModelCacheWorkflowRegular {
		if err := validateModelCacheBindingForEnsure(binding, input, req); err != nil {
			return nil, nvcaerrors.TerminalError(err)
		}
		return nil, nvcaerrors.TerminalError(fmt.Errorf("%w: %s/%s",
			errRegularModelCacheBindingRetiring, binding.Namespace, binding.Name))
	}
	if err := validateActiveModelCacheBindingForRequest(binding, input, req); err != nil {
		return nil, nvcaerrors.TerminalError(err)
	}
	return binding, nil
}

func validateModelCacheBindingForEnsure(
	binding *nvcav2beta1.ModelCacheBinding,
	input *modelCacheBindingInput,
	req *nvcav2beta1.ICMSRequest,
) error {
	if binding.Status.Phase != nvcav2beta1.ModelCacheBindingPhaseRetiring {
		return validateActiveModelCacheBindingForRequest(binding, input, req)
	}
	if input.selection.Workflow != nvcastorage.ModelCacheWorkflowRegular {
		return fmt.Errorf("model cache binding %s/%s is Retiring and cannot serve %q workflow",
			binding.Namespace, binding.Name, input.selection.Workflow)
	}
	if err := nvcastorage.ValidateModelCacheBindingIntent(
		binding, input.selection, input.sharingDomain, input.cacheHandle, input.writerNamespace); err != nil {
		return err
	}
	sole, err := validateExactModelCacheBindingRequestReference(binding, req)
	if err != nil {
		return fmt.Errorf("model cache binding %s/%s is Retiring: %w",
			binding.Namespace, binding.Name, err)
	}
	if !sole {
		return fmt.Errorf("model cache binding %s/%s is Retiring with other request references",
			binding.Namespace, binding.Name)
	}
	return nil
}

func validateActiveModelCacheBindingForRequest(
	binding *nvcav2beta1.ModelCacheBinding,
	input *modelCacheBindingInput,
	req *nvcav2beta1.ICMSRequest,
) error {
	if err := nvcastorage.ValidateModelCacheBinding(
		binding, input.selection, input.sharingDomain, input.cacheHandle, input.writerNamespace); err != nil {
		return err
	}
	if !nvcastorage.ModelCacheBindingHasRequestReference(
		binding, req.Namespace, req.Name, req.UID) {
		return fmt.Errorf("model cache binding %s/%s has no reference to request %s/%s UID %s",
			binding.Namespace, binding.Name, req.Namespace, req.Name, req.UID)
	}
	return nil
}

// beginRegularModelCacheBindingRetirement atomically closes a durable regular
// cache binding to new users before destructive cleanup. It authorizes cleanup
// only when the exact request UID is the sole reference. An already-Retiring
// binding is adopted only by that same request so interrupted cleanup can
// resume without exposing the binding to the Active runtime path.
func (c *BackendK8sCache) beginRegularModelCacheBindingRetirement(
	ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
) (*nvcav2beta1.ModelCacheBinding, bool, error) {
	input, err := c.modelCacheBindingInput(req)
	if err != nil {
		return nil, false, nvcaerrors.TerminalError(err)
	}
	if input == nil || input.selection.Workflow != nvcastorage.ModelCacheWorkflowRegular ||
		input.selection.BindingName == "" || input.selection.BindingUID == "" {
		return nil, false, nvcaerrors.TerminalError(fmt.Errorf(
			"regular model cache retirement requires a committed durable regular binding reference"))
	}
	if c.clients == nil || c.clients.BART == nil {
		return nil, false, fmt.Errorf("model cache binding client is not configured")
	}

	bindings := c.clients.BART.NvcaV2beta1().ModelCacheBindings(nvcastorage.ModelCacheInitNamespace)
	var result *nvcav2beta1.ModelCacheBinding
	authorized := false
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		result = nil
		authorized = false
		binding, err := bindings.Get(ctx, input.selection.BindingName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if err := nvcastorage.ValidateModelCacheBindingIntent(
			binding, input.selection, input.sharingDomain, input.cacheHandle, input.writerNamespace); err != nil {
			return nvcaerrors.TerminalError(err)
		}
		sole, err := validateExactModelCacheBindingRequestReference(binding, req)
		if err != nil {
			return nvcaerrors.TerminalError(err)
		}

		switch binding.Status.Phase {
		case nvcav2beta1.ModelCacheBindingPhaseActive:
			if !sole {
				result = binding
				return nil
			}
			now := metav1.Now()
			binding.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseRetiring
			binding.Status.LastPhaseTransitionTime = &now
			updated, updateErr := bindings.UpdateStatus(ctx, binding, metav1.UpdateOptions{})
			if updateErr != nil {
				return updateErr
			}
			result = updated
			authorized = true
			return nil
		case nvcav2beta1.ModelCacheBindingPhaseRetiring:
			if !sole {
				return nvcaerrors.TerminalError(fmt.Errorf(
					"Retiring model cache binding %s/%s has other request references",
					binding.Namespace, binding.Name))
			}
			result = binding
			authorized = true
			return nil
		default:
			return nvcaerrors.TerminalError(fmt.Errorf(
				"model cache binding %s/%s cannot retire from phase %q",
				binding.Namespace, binding.Name, binding.Status.Phase))
		}
	})
	if err != nil {
		return nil, false, fmt.Errorf("retire regular model cache binding %s for request %s/%s: %w",
			input.selection.BindingName, req.Namespace, req.Name, err)
	}
	if result == nil {
		return nil, false, fmt.Errorf("retire regular model cache binding %s returned no binding",
			input.selection.BindingName)
	}
	return result, authorized, nil
}

func validateExactModelCacheBindingRequestReference(
	binding *nvcav2beta1.ModelCacheBinding,
	req *nvcav2beta1.ICMSRequest,
) (bool, error) {
	exact := 0
	for _, ref := range binding.Status.RequestReferences {
		if ref.Namespace == req.Namespace && ref.Name == req.Name && ref.UID == req.UID {
			exact++
		}
	}
	if exact == 0 {
		return false, fmt.Errorf("model cache binding %s/%s has no reference to request %s/%s UID %s",
			binding.Namespace, binding.Name, req.Namespace, req.Name, req.UID)
	}
	if exact != 1 {
		return false, fmt.Errorf("model cache binding %s/%s has %d references to request %s/%s UID %s",
			binding.Namespace, binding.Name, exact, req.Namespace, req.Name, req.UID)
	}
	return len(binding.Status.RequestReferences) == 1, nil
}

func (c *BackendK8sCache) releaseModelCacheBindingReference(
	ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
) error {
	input, err := c.modelCacheBindingInput(req)
	if err != nil {
		// Deletion errors must remain retryable so the request finalizer is not
		// silently removed while binding ownership is ambiguous.
		return fmt.Errorf("resolve model cache binding during request deletion: %w", err)
	}
	if input == nil {
		return nil
	}
	name := input.selection.BindingName
	if name == "" {
		name = nvcastorage.ModelCacheBindingName(input.cacheHandle)
	}
	bindings := c.clients.BART.NvcaV2beta1().ModelCacheBindings(nvcastorage.ModelCacheInitNamespace)
	return c.removeModelCacheBindingReference(
		ctx, bindings, name, input, req.Namespace, req.Name, req.UID)
}

func (c *BackendK8sCache) removeModelCacheBindingReference(
	ctx context.Context,
	bindings nvcaclientv2beta1.ModelCacheBindingInterface,
	bindingName string,
	input *modelCacheBindingInput,
	requestNamespace string,
	requestName string,
	requestUID types.UID,
) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		binding, err := bindings.Get(ctx, bindingName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) && input.selection.BindingName == "" {
			return nil
		}
		if err != nil {
			return err
		}
		if err := nvcastorage.ValidateModelCacheBindingIntent(
			binding, input.selection, input.sharingDomain, input.cacheHandle, input.writerNamespace); err != nil {
			if input.selection.BindingName == "" {
				// An unbound request may collide with an existing handle-scoped
				// binding owned by another immutable sharing domain. It never
				// acquired that binding and must not modify it during deletion.
				return nil
			}
			return err
		}
		refs := binding.Status.RequestReferences[:0]
		found := false
		for _, ref := range binding.Status.RequestReferences {
			if ref.Namespace == requestNamespace && ref.Name == requestName && ref.UID == requestUID {
				found = true
				continue
			}
			refs = append(refs, ref)
		}
		if !found {
			return nil
		}
		binding.Status.RequestReferences = refs
		_, err = bindings.UpdateStatus(ctx, binding, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("release model cache binding %s reference for request %s/%s: %w",
			bindingName, requestNamespace, requestName, err)
	}
	return nil
}
