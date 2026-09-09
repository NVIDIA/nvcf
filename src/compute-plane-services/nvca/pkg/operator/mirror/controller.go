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

package mirror

import (
	"context"
	"fmt"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/controlplane"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
)

const (
	// AdditionalImagePullSecretLabelKey is the label key used to identify secrets managed by this controller
	AdditionalImagePullSecretLabelKey = "nvca.nvcf.nvidia.io/additional-image-pull-secret" //nolint:gosec
	ManagedbyLabelKey                 = "app.kubernetes.io/managed-by"
	ManagedByValue                    = "nvca-mirror"
	DefaultResyncPeriod               = 1 * time.Hour
)

var (
	// NamespaceBackoff is the backoff configuration for retrying operations when namespace is not found
	// 15 second intervals, up to 20 retries (5 minutes total)
	NamespaceBackoff = wait.Backoff{
		Steps:    20,
		Duration: 15 * time.Second,
		Factor:   1.0,
		Jitter:   0.1,
	}
)

// Controller watches secrets in a source namespace and copies them to a target namespace
type Controller struct {
	clientset            kubernetes.Interface
	sourceNamespace      string
	targetNamespace      string
	secretNames          []string
	resyncPeriod         time.Duration
	controlPlaneIdentity controlplane.Identity
	informerFactory      informers.SharedInformerFactory
}

// NewController creates a new MirrorController
func NewController(
	clientset kubernetes.Interface,
	sourceNamespace string,
	targetNamespace string,
	secretNames []string,
	resyncPeriod time.Duration,
) *Controller {
	controller, err := NewControllerForControlPlane(
		clientset,
		sourceNamespace,
		targetNamespace,
		secretNames,
		resyncPeriod,
		controlplane.DefaultIdentity(),
	)
	if err != nil {
		panic(err)
	}
	return controller
}

// NewControllerForControlPlane creates a MirrorController for a specific control plane.
func NewControllerForControlPlane(
	clientset kubernetes.Interface,
	sourceNamespace string,
	targetNamespace string,
	secretNames []string,
	resyncPeriod time.Duration,
	identity controlplane.Identity,
) (*Controller, error) {
	if !identity.Valid() {
		return nil, fmt.Errorf("invalid control plane identity %q", identity.String())
	}

	return &Controller{
		clientset:            clientset,
		sourceNamespace:      sourceNamespace,
		targetNamespace:      targetNamespace,
		secretNames:          secretNames,
		resyncPeriod:         resyncPeriod,
		controlPlaneIdentity: identity,
	}, nil
}

// Run starts the controller and blocks until the context is canceled
func (c *Controller) Run(ctx context.Context) error {
	log := core.GetLogger(ctx)
	log.WithField("resyncPeriod", c.resyncPeriod).Info("Starting MirrorController")

	// Validate that all configured secrets exist in the source namespace before starting
	if err := c.validateSecretsExist(ctx); err != nil {
		return fmt.Errorf("secret validation failed: %w", err)
	}

	// Create informer factory for the source namespace
	c.informerFactory = informers.NewSharedInformerFactoryWithOptions(
		c.clientset,
		c.resyncPeriod,
		informers.WithNamespace(c.sourceNamespace),
	)

	// Get the secret informer
	secretInformer := c.informerFactory.Core().V1().Secrets()
	informer := secretInformer.Informer()

	// Add event handlers
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			secret, ok := obj.(*corev1.Secret)
			if !ok {
				log.Errorf("Expected *corev1.Secret but got %T", obj)
				return
			}
			if err := c.handleSecretAdd(ctx, secret); err != nil {
				log.WithError(err).Errorf("Failed to handle secret add: %s", secret.Name)
			}
		},
		UpdateFunc: func(oldObj, newObj any) {
			oldSecret, ok := oldObj.(*corev1.Secret)
			if !ok {
				log.Errorf("Expected *corev1.Secret but got %T", oldObj)
				return
			}
			newSecret, ok := newObj.(*corev1.Secret)
			if !ok {
				log.Errorf("Expected *corev1.Secret but got %T", newObj)
				return
			}
			if err := c.handleSecretUpdate(ctx, oldSecret, newSecret); err != nil {
				log.WithError(err).Errorf("Failed to handle secret update: %s", newSecret.Name)
			}
		},
	}); err != nil {
		return fmt.Errorf("failed to add event handler: %w", err)
	}

	// Start the informer
	c.informerFactory.Start(ctx.Done())

	// Wait for cache sync
	log.Info("Waiting for informer cache to sync")
	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		return fmt.Errorf("failed to sync informer cache")
	}
	log.Info("Informer cache synced")

	if err := c.adoptLegacyMirroredSecrets(ctx); err != nil {
		return fmt.Errorf("failed to adopt legacy mirrored secrets: %w", err)
	}

	if err := c.replayConfiguredSecrets(ctx, informer); err != nil {
		return fmt.Errorf("failed to replay configured secrets: %w", err)
	}

	// Run initial cleanup
	if err := c.cleanupOrphanedSecrets(ctx); err != nil {
		log.WithError(err).Error("Failed to cleanup orphaned secrets")
		// Don't fail on cleanup errors, just log them
	}

	// Block until context is canceled
	<-ctx.Done()
	log.Info("Context canceled, stopping controller")

	return nil
}

// handleSecretAdd handles the addition of a secret
func (c *Controller) handleSecretAdd(ctx context.Context, secret *corev1.Secret) error {
	log := core.GetLogger(ctx)

	// Check if this is a secret we care about
	if !c.isTrackedSecret(secret.Name) {
		return nil
	}

	log.WithField("secret", secret.Name).Debug("Handling secret add")
	return c.syncSecret(ctx, secret)
}

// handleSecretUpdate handles the update of a secret
func (c *Controller) handleSecretUpdate(ctx context.Context, oldSecret, newSecret *corev1.Secret) error {
	log := core.GetLogger(ctx)

	// Check if this is a secret we care about
	if !c.isTrackedSecret(newSecret.Name) {
		return nil
	}

	// Check if the secret data has actually changed
	if equality.Semantic.DeepEqual(oldSecret.Data, newSecret.Data) &&
		equality.Semantic.DeepEqual(oldSecret.Type, newSecret.Type) {
		log.WithField("secret", newSecret.Name).Debug("Secret data unchanged, skipping sync")
		return nil
	}

	log.WithField("secret", newSecret.Name).Info("Handling secret update")
	return c.syncSecret(ctx, newSecret)
}

// syncSecret copies the secret from source namespace to target namespace
func (c *Controller) syncSecret(ctx context.Context, secret *corev1.Secret) error {
	log := core.GetLogger(ctx)
	log.WithField("secret", secret.Name).Info("Syncing secret to target namespace")

	// First, wait for the target namespace to exist
	err := retry.OnError(NamespaceBackoff, k8serrors.IsNotFound, func() error {
		_, err := c.clientset.CoreV1().Namespaces().Get(ctx, c.targetNamespace, metav1.GetOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("target namespace %q does not exist: %w", c.targetNamespace, err)
	}

	targetLabels, err := c.mirroredSecretLabels()
	if err != nil {
		return err
	}

	// Create the target secret template
	targetSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secret.Name,
			Namespace: c.targetNamespace,
			Labels:    targetLabels,
		},
		Type: secret.Type,
		Data: secret.Data,
	}

	// Now use RetryOnConflict to handle conflicts during secret operations
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		// Try to get the existing secret
		existing, err := c.clientset.CoreV1().Secrets(c.targetNamespace).Get(
			ctx,
			secret.Name,
			metav1.GetOptions{},
		)

		if err != nil {
			if k8serrors.IsNotFound(err) {
				// Secret doesn't exist, create it
				_, err := c.clientset.CoreV1().Secrets(c.targetNamespace).Create(
					ctx,
					targetSecret,
					metav1.CreateOptions{},
				)
				if err != nil {
					return err
				}
				log.WithField("secret", secret.Name).Info("Created secret in target namespace")
				return nil
			}
			// Other error
			return err
		}

		if !c.canManageTargetSecret(existing) {
			log.WithFields(map[string]interface{}{
				"secret":                secret.Name,
				"targetNamespace":       c.targetNamespace,
				"controlPlaneIdentity":  c.controlPlaneIdentity.String(),
				"existingSecretOwner":   existing.Labels[controlplane.OwnerLabel],
				"existingSecretManaged": existing.Labels[AdditionalImagePullSecretLabelKey],
			}).Warn("Skipping existing mirrored secret that is not owned by this control plane")
			return nil
		}

		// Secret exists and belongs to this controller, update it.
		existing.Data = targetSecret.Data
		existing.Type = targetSecret.Type
		if existing.Labels == nil {
			existing.Labels = make(map[string]string)
		}
		for key, value := range targetLabels {
			existing.Labels[key] = value
		}

		_, err = c.clientset.CoreV1().Secrets(c.targetNamespace).Update(
			ctx,
			existing,
			metav1.UpdateOptions{},
		)
		if err != nil {
			return err
		}
		log.WithField("secret", secret.Name).Info("Updated secret in target namespace")
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to sync secret to target namespace: %w", err)
	}
	return nil
}

// cleanupOrphanedSecrets removes secrets in the target namespace that are no longer configured
func (c *Controller) cleanupOrphanedSecrets(ctx context.Context) error {
	log := core.GetLogger(ctx)
	log.Info("Cleaning up orphaned secrets")

	if err := c.adoptLegacyMirroredSecrets(ctx); err != nil {
		return err
	}

	labelSelector, err := c.mirroredSecretSelector()
	if err != nil {
		return err
	}
	secrets, err := c.clientset.CoreV1().Secrets(c.targetNamespace).List(
		ctx,
		metav1.ListOptions{
			LabelSelector: labelSelector,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to list secrets in target namespace: %w", err)
	}

	// Build a set of tracked secret names for quick lookup
	trackedSecrets := make(map[string]bool, len(c.secretNames))
	for _, name := range c.secretNames {
		trackedSecrets[name] = true
	}

	// Delete secrets that are no longer tracked
	for _, secret := range secrets.Items {
		if !trackedSecrets[secret.Name] {
			log.WithField("secret", secret.Name).Info("Deleting orphaned secret from target namespace")
			err := c.clientset.CoreV1().Secrets(c.targetNamespace).Delete(
				ctx,
				secret.Name,
				metav1.DeleteOptions{},
			)
			if err != nil && !k8serrors.IsNotFound(err) {
				log.WithError(err).Errorf("Failed to delete orphaned secret: %s", secret.Name)
				// Continue with other secrets even if one fails
			}
		}
	}

	log.Info("Cleanup complete")
	return nil
}

// isTrackedSecret checks if the given secret name is in the list of secrets we're tracking
func (c *Controller) isTrackedSecret(name string) bool {
	for _, secretName := range c.secretNames {
		if secretName == name {
			return true
		}
	}
	return false
}

func (c *Controller) replayConfiguredSecrets(ctx context.Context, informer cache.SharedIndexInformer) error {
	if len(c.secretNames) == 0 {
		return nil
	}

	for _, obj := range informer.GetStore().List() {
		secret, ok := obj.(*corev1.Secret)
		if !ok {
			return fmt.Errorf("expected *corev1.Secret but got %T", obj)
		}
		if !c.isTrackedSecret(secret.Name) {
			continue
		}
		if err := c.syncSecret(ctx, secret); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) mirroredSecretLabels() (map[string]string, error) {
	return controlplane.AddOwnerLabel(map[string]string{
		AdditionalImagePullSecretLabelKey: "true",
		ManagedbyLabelKey:                 ManagedByValue,
	}, c.controlPlaneIdentity)
}

func (c *Controller) mirroredSecretSelector() (string, error) {
	return mirroredSecretSelector(c.controlPlaneIdentity)
}

func mirroredSecretSelector(identity controlplane.Identity) (string, error) {
	if !identity.Valid() {
		return "", fmt.Errorf("invalid control plane identity %q", identity.String())
	}
	return labels.SelectorFromSet(labels.Set{
		AdditionalImagePullSecretLabelKey: "true",
		controlplane.OwnerLabel:           identity.String(),
	}).String(), nil
}

func (c *Controller) adoptLegacyMirroredSecrets(ctx context.Context) error {
	return adoptLegacyMirroredSecrets(ctx, c.clientset, c.targetNamespace, c.controlPlaneIdentity)
}

func adoptLegacyMirroredSecrets(
	ctx context.Context,
	clientset kubernetes.Interface,
	targetNamespace string,
	identity controlplane.Identity,
) error {
	if !identity.Valid() {
		return fmt.Errorf("invalid control plane identity %q", identity.String())
	}
	if !identity.IsDefault() {
		return nil
	}

	legacySelector := labels.SelectorFromSet(labels.Set{
		AdditionalImagePullSecretLabelKey: "true",
	}).String()
	secrets, err := clientset.CoreV1().Secrets(targetNamespace).List(
		ctx,
		metav1.ListOptions{
			LabelSelector: legacySelector,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to list legacy mirrored secrets in target namespace: %w", err)
	}

	for _, secret := range secrets.Items {
		if secret.Labels[controlplane.OwnerLabel] != "" {
			continue
		}
		secretToUpdate := secret.DeepCopy()
		if secretToUpdate.Labels == nil {
			secretToUpdate.Labels = map[string]string{}
		}
		secretToUpdate.Labels[controlplane.OwnerLabel] = identity.String()
		if _, err := clientset.CoreV1().Secrets(targetNamespace).Update(
			ctx,
			secretToUpdate,
			metav1.UpdateOptions{},
		); err != nil {
			return fmt.Errorf("failed to adopt legacy mirrored secret %q: %w", secret.Name, err)
		}
	}
	return nil
}

func (c *Controller) canManageTargetSecret(secret *corev1.Secret) bool {
	if controlplane.IsOwnedBy(secret.Labels, c.controlPlaneIdentity) {
		return true
	}
	if !c.controlPlaneIdentity.IsDefault() {
		return false
	}
	if secret.Labels[controlplane.OwnerLabel] != "" {
		return false
	}
	return secret.Labels[AdditionalImagePullSecretLabelKey] == "true" ||
		secret.Labels[ManagedbyLabelKey] == ManagedByValue
}

// validateSecretsExist validates that all configured secrets exist in the source namespace
// This is called once at startup to fail fast if secrets are missing
func (c *Controller) validateSecretsExist(ctx context.Context) error {
	log := core.GetLogger(ctx)

	if len(c.secretNames) == 0 {
		log.Debug("No secrets configured to validate")
		return nil
	}

	log.WithFields(map[string]interface{}{
		"sourceNamespace": c.sourceNamespace,
		"secretCount":     len(c.secretNames),
	}).Info("Validating that all configured secrets exist in source namespace")

	for _, secretName := range c.secretNames {
		_, err := c.clientset.CoreV1().Secrets(c.sourceNamespace).Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			log.WithError(err).Errorf("Secret %q does not exist in namespace %q", secretName, c.sourceNamespace)
			return fmt.Errorf("secret %q does not exist in namespace %q: %w", secretName, c.sourceNamespace, err)
		}
		log.WithField("secret", secretName).Infof("Validated secret exists in namespace %q", c.sourceNamespace)
	}

	log.Info("All configured secrets validated successfully")
	return nil
}

// CleanupAllAdditionalSecrets removes all secrets with the additional-image-pull-secret label
// from the target namespace. This is used when no secrets are configured to ensure cleanup.
// All errors are non-fatal and logged.
func CleanupAllAdditionalSecrets(ctx context.Context, clientset kubernetes.Interface, targetNamespace string) error {
	return CleanupAllAdditionalSecretsForControlPlane(ctx, clientset, targetNamespace, controlplane.DefaultIdentity())
}

// CleanupAllAdditionalSecretsForControlPlane removes all additional image pull secrets
// owned by the given control plane from the target namespace.
func CleanupAllAdditionalSecretsForControlPlane(
	ctx context.Context,
	clientset kubernetes.Interface,
	targetNamespace string,
	identity controlplane.Identity,
) error {
	log := core.GetLogger(ctx)
	log.WithField("targetNamespace", targetNamespace).Info("Cleaning up all additional image pull secrets")

	labelSelector, err := mirroredSecretSelector(identity)
	if err != nil {
		return err
	}

	// Check if target namespace exists
	_, err = clientset.CoreV1().Namespaces().Get(ctx, targetNamespace, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			log.WithField("targetNamespace", targetNamespace).Info("Target namespace does not exist, nothing to cleanup")
			return nil
		}
		// Other errors getting namespace - log but don't fail
		log.WithError(err).Warnf("Failed to get target namespace %q, skipping cleanup", targetNamespace)
		return nil
	}

	if err := adoptLegacyMirroredSecrets(ctx, clientset, targetNamespace, identity); err != nil {
		log.WithError(err).Warnf("Failed to adopt legacy additional image pull secrets, skipping cleanup")
		return nil
	}

	err = clientset.CoreV1().Secrets(targetNamespace).DeleteCollection(
		ctx,
		metav1.DeleteOptions{},
		metav1.ListOptions{
			LabelSelector: labelSelector,
		},
	)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			log.Info("No additional image pull secrets found or already deleted")
			return nil
		}
		log.WithError(err).Warnf("Failed to delete additional image pull secrets, skipping cleanup")
		return nil
	}

	log.Info("Successfully cleaned up all additional image pull secrets")
	return nil
}
