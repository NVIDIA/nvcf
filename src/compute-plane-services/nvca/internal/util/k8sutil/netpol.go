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

package k8sutil

import (
	"context"
	"errors"
	"fmt"
	"strings"

	k8sclient "k8s.io/client-go/kubernetes"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
)

const (
	K8sNameLabelKey = "kubernetes.io/metadata.name"

	// These NetworkPolicies will be created by NVCA operator in the requests namespace.
	// They must be copied to each new namespace for a pod or mini service instance.
	NetworkPoliciesConfigMapName = "nvca-namespace-networkpolicies"
	// General
	AllPodsEgressNetworkPolicyName             = "allow-egress-internet-no-internal-no-api"
	MonitoringIngressNetworkPolicyName         = "allow-ingress-monitoring"
	AllowEgressIntraNamespaceNetworkPolicyName = "allow-egress-intra-namespace"
	// GXCache
	EgressGXCacheNetworkPolicyName  = "allow-egress-gxcache"
	IngressGXCacheNetworkPolicyName = "allow-ingress-monitoring-gxcache"
	// DCGM
	IngressMonitoringDCGMNetworkPolicyNameKey = "allow-ingress-monitoring-dcgm"
	// Caching
	EgressNVCFCacheNetworkPolicyNameKey = "allow-egress-nvcf-cache"
	ContainerCachingNamespace           = "container-caching"
	ProxyCacheNamespace                 = "dns-proxy"
	// Crowdstrike namespace (only install Crowdstrike policies if this namespace exists)
	// (moved to a package-level variable below so it can be overridden at runtime)
	// Crowdstrike
	EgressCrowdstrikeNetworkPolicyName  = "allow-egress-crowdstrike"
	IngressCrowdstrikeNetworkPolicyName = "allow-ingress-crowdstrike"
	// BYOO
	EgressBYOOOTelPrometheusNetworkPolicyName = "allow-egress-prometheus-nvcf-byoo"
	// This label k/v must be set on all pods containing the byoo-otel-collector container
	// so metrics can be forwarded to the local prometheus instance running in a separate namespace.
	BYOOMetricsEgressTargetLabelKey   = "nvca.nvcf.nvidia.io/byoo-metrics-egress-target"
	BYOOMetricsEgressTargetLabelValue = "byoo-otel-collector"

	// This prefix is used to identify custom network policies in the configmap
	nvcfCustomNetworkPolicyPrefix = "nvcf-custom-"
	nvcfCustomNetworkPolicyLabel  = "nvca.nvcf.nvidia.io/network-policy-customization"
)

// CrowdstrikeNamespace is the namespace that, when present in the cluster,
// enables installation of Crowdstrike-related network policies. It is a
// variable so it can be overridden from the CLI at startup.
var CrowdstrikeNamespace = "crowdstrike-injector"

func EnsureNetworkPoliciesFunctionNamespace(
	ctx context.Context,
	namespace string,
	nps map[string]string,
	ffFetcher featureflag.Fetcher,
	k8sClient k8sclient.Interface,
	crClient client.Client,
	extraNPs ...*netv1.NetworkPolicy,
) error {
	var allExtraNPs []*netv1.NetworkPolicy
	allExtraNPs = append(allExtraNPs, createIntraNamespaceEgressPolicy(namespace))
	allExtraNPs = append(allExtraNPs, extraNPs...)
	return ensureNetworkPolicies(ctx,
		namespace,
		nps,
		ffFetcher,
		k8sClient,
		crClient,
		allExtraNPs...,
	)
}

func EnsureNetworkPoliciesSharedPodInstanceNamespace(
	ctx context.Context,
	namespace string,
	nps map[string]string,
	ffFetcher featureflag.Fetcher,
	k8sClient k8sclient.Interface,
	crClient client.Client,
) error {
	return ensureNetworkPolicies(ctx,
		namespace,
		nps,
		ffFetcher,
		k8sClient,
		crClient,
	)
}

func ensureNetworkPolicies(
	ctx context.Context,
	namespace string,
	nps map[string]string,
	ffFetcher featureflag.Fetcher,
	k8sClient k8sclient.Interface,
	crClient client.Client,
	extraNPs ...*netv1.NetworkPolicy,
) error {
	log := core.GetLogger(ctx)

	if k8sClient == nil && crClient == nil {
		return fmt.Errorf("code bug: both clients are nil")
	}
	if ffFetcher == nil {
		ffFetcher = featureflag.DefaultFetcher
	}

	var errs []error

	// Prune all network policies in the namespace
	if ffFetcher.IsFeatureFlagEnabled(featureflag.SelfHosted) {
		if k8sClient != nil {
			if err := k8sClient.NetworkingV1().NetworkPolicies(namespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{}); err != nil {
				log.WithError(err).Errorf("delete all network policies in namespace %s", namespace)
				return fmt.Errorf("delete all network policies in namespace %s: %v", namespace, err)
			}
		} else if crClient != nil {
			if err := crClient.DeleteAllOf(ctx, &netv1.NetworkPolicy{}, client.InNamespace(namespace)); err != nil {
				log.WithError(err).Errorf("delete all network policies in namespace %s", namespace)
				return fmt.Errorf("delete all network policies in namespace %s: %v", namespace, err)
			}
		}
		return nil
	}

	netpols, err := getValidNetworkPolicies(ctx, namespace, nps, ffFetcher)
	if err != nil {
		log.WithError(err).Errorf("getValidNetworkPolicies failed in namespace %s", namespace)
		return fmt.Errorf("get valid network policies: %w", err)
	}

	newNetworkPolicies := map[string]*netv1.NetworkPolicy{}
	netpols = append(netpols, extraNPs...)
	for _, newNP := range netpols {
		newNetworkPolicies[newNP.Name] = newNP
		newNP.Namespace = namespace

		switch newNP.Name {
		case MonitoringIngressNetworkPolicyName:
			snRule := netv1.NetworkPolicyIngressRule{
				From: []netv1.NetworkPolicyPeer{
					{
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{K8sNameLabelKey: namespace},
						},
					},
				},
			}
			newNP.Spec.Ingress = append(newNP.Spec.Ingress, snRule)
		case EgressNVCFCacheNetworkPolicyNameKey:
			// check if both container-caching & dns-proxy namespaces are present
			var ccnsErr, pcnsErr error
			if k8sClient != nil {
				nsClient := k8sClient.CoreV1().Namespaces()
				_, ccnsErr = nsClient.Get(ctx, ContainerCachingNamespace, metav1.GetOptions{})
				_, pcnsErr = nsClient.Get(ctx, ProxyCacheNamespace, metav1.GetOptions{})
			} else {
				ccnsErr = crClient.Get(ctx, client.ObjectKey{Name: ContainerCachingNamespace}, &corev1.Namespace{})
				pcnsErr = crClient.Get(ctx, client.ObjectKey{Name: ProxyCacheNamespace}, &corev1.Namespace{})
			}
			if apierrors.IsNotFound(ccnsErr) || apierrors.IsNotFound(pcnsErr) {
				var nsNotExist []string
				if apierrors.IsNotFound(ccnsErr) {
					nsNotExist = append(nsNotExist, ContainerCachingNamespace)
				}
				if apierrors.IsNotFound(pcnsErr) {
					nsNotExist = append(nsNotExist, ProxyCacheNamespace)
				}
				log.Debugf("%q namespaces don't exist, skipping installing %v policy",
					nsNotExist, EgressNVCFCacheNetworkPolicyNameKey)
				continue
			}
		case EgressCrowdstrikeNetworkPolicyName, IngressCrowdstrikeNetworkPolicyName:
			// Only install Crowdstrike-related policies if the cluster contains the
			// crowdstrike-injector namespace.
			var csErr error
			if k8sClient != nil {
				nsClient := k8sClient.CoreV1().Namespaces()
				_, csErr = nsClient.Get(ctx, CrowdstrikeNamespace, metav1.GetOptions{})
			} else {
				csErr = crClient.Get(ctx, client.ObjectKey{Name: CrowdstrikeNamespace}, &corev1.Namespace{})
			}
			if apierrors.IsNotFound(csErr) {
				log.Debugf("%q namespace doesn't exist, skipping installing %v policy", CrowdstrikeNamespace, newNP.Name)
				continue
			}
		case EgressBYOOOTelPrometheusNetworkPolicyName:
			if newNP.Spec.PodSelector.MatchLabels == nil {
				newNP.Spec.PodSelector.MatchLabels = map[string]string{}
			}
			newNP.Spec.PodSelector.MatchLabels[BYOOMetricsEgressTargetLabelKey] = BYOOMetricsEgressTargetLabelValue
		}

		if err := createOrUpdateNetworkPolicy(ctx, namespace, newNP, k8sClient, crClient); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}

	// Prune existing network policies that are not in the configmap and are custom network policies
	return pruneCustomNetworkPolicies(ctx, namespace, k8sClient, crClient, newNetworkPolicies)
}

func pruneCustomNetworkPolicies(ctx context.Context,
	namespace string,
	k8sClient k8sclient.Interface,
	crClient client.Client,
	newNetworkPolicies map[string]*netv1.NetworkPolicy,
) error {
	log := core.GetLogger(ctx)

	if k8sClient == nil && crClient == nil {
		return fmt.Errorf("code bug: both clients are nil")
	}

	// List existing custom network policies in the namespace
	var existingCustomNps *netv1.NetworkPolicyList
	if k8sClient != nil {
		var err error
		existingCustomNps, err = k8sClient.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: nvcfCustomNetworkPolicyLabel,
		})
		if err != nil {
			log.WithError(err).Errorf("list existing network policies in namespace %s", namespace)
			return fmt.Errorf("list existing network policies in namespace %s: %v", namespace, err)
		}
	} else if crClient != nil {
		var err error
		existingCustomNps = &netv1.NetworkPolicyList{}
		err = crClient.List(ctx,
			existingCustomNps,
			client.InNamespace(namespace),
			client.MatchingLabels(map[string]string{nvcfCustomNetworkPolicyLabel: "enabled"}))
		if err != nil {
			log.WithError(err).Errorf("list existing network policies in namespace %s", namespace)
			return fmt.Errorf("list existing network policies in namespace %s: %v", namespace, err)
		}
	}

	// Delete custom network policies that are not in the full list of network policies
	var errs []error
	for _, existingCustomNP := range existingCustomNps.Items {
		if _, ok := newNetworkPolicies[existingCustomNP.Name]; !ok {
			if k8sClient != nil {
				if err := k8sClient.NetworkingV1().NetworkPolicies(namespace).Delete(ctx, existingCustomNP.Name, metav1.DeleteOptions{}); err != nil {
					errs = append(errs, fmt.Errorf("delete existing network policy %s in namespace %s: %v", existingCustomNP.Name, namespace, err))
				}
			} else if crClient != nil {
				if err := crClient.Delete(ctx, &existingCustomNP); err != nil {
					errs = append(errs, fmt.Errorf("delete existing network policy %s in namespace %s: %v", existingCustomNP.Name, namespace, err))
				}
			}
		}
	}

	return errors.Join(errs...)
}

func getValidNetworkPolicies(
	ctx context.Context,
	namespace string,
	nps map[string]string,
	ffFetcher featureflag.Fetcher,
) ([]*netv1.NetworkPolicy, error) {
	log := core.GetLogger(ctx)
	var netpols []*netv1.NetworkPolicy
	for _, npName := range getValidNetworkPolicyNames(nps) {
		// skip deploying GXCache network policy if it is NOT enabled
		if (npName == EgressGXCacheNetworkPolicyName || npName == IngressGXCacheNetworkPolicyName) &&
			!ffFetcher.IsFeatureFlagEnabled(featureflag.GXCache) &&
			!ffFetcher.IsAttributeEnabled(featureflag.AttrOVCSecurityEnforcements) {
			log.Debugf("Skip deploying NetworkPolicy %s in namespace %s, since feature disabled", npName, namespace)
			continue
		}
		if npName == EgressBYOOOTelPrometheusNetworkPolicyName && !ffFetcher.IsFeatureFlagEnabled(featureflag.BYOObservability) {
			log.Debugf("Skip deploying NetworkPolicy %s in namespace %s, since feature disabled", npName, namespace)
			continue
		}

		log.Debugf("Creating or updating NetworkPolicy %s in namespace %s", npName, namespace)

		npYAML, ok := nps[npName]
		if !ok {
			return nil, fmt.Errorf("expected ConfigMap key %s to exist, was empty", npName)
		}

		newNP := &netv1.NetworkPolicy{}
		if err := yaml.Unmarshal([]byte(npYAML), newNP); err != nil {
			return nil, fmt.Errorf("unmarshal NetworkPolicy %s: %v", npName, err)
		}
		newNP.Name = npName

		// If the network policy name starts with nvcf-custom-, add the label
		// to indicate that it is a custom network policy.
		if strings.HasPrefix(npName, nvcfCustomNetworkPolicyPrefix) {
			if newNP.Labels == nil {
				newNP.Labels = map[string]string{}
			}
			newNP.Labels[nvcfCustomNetworkPolicyLabel] = "enabled"
			newNP.Name = npName
		}

		netpols = append(netpols, newNP)
	}
	return netpols, nil
}

// getValidNetworkPolicyNames returns a list of valid network policy names
// from the nvca-namespace-networkpolicies configmap
func getValidNetworkPolicyNames(nps map[string]string) []string {
	// Give me a switch statement that checks if the network policy name is valid
	validNps := []string{
		AllPodsEgressNetworkPolicyName,
		MonitoringIngressNetworkPolicyName,
		EgressGXCacheNetworkPolicyName,
		IngressGXCacheNetworkPolicyName,
		IngressMonitoringDCGMNetworkPolicyNameKey,
		EgressNVCFCacheNetworkPolicyNameKey,
		EgressBYOOOTelPrometheusNetworkPolicyName,
	}

	// Append any custom network policies present in the configmap
	for npName := range nps {
		if strings.HasPrefix(npName, nvcfCustomNetworkPolicyPrefix) {
			validNps = append(validNps, npName)
		}
	}

	// Only include Crowdstrike policies if they are present in the configmap.
	// Actual installation will still check for the presence of the
	// Crowdstrike namespace before attempting to create them.
	if _, ok := nps[EgressCrowdstrikeNetworkPolicyName]; ok {
		validNps = append(validNps, EgressCrowdstrikeNetworkPolicyName)
	}
	if _, ok := nps[IngressCrowdstrikeNetworkPolicyName]; ok {
		validNps = append(validNps, IngressCrowdstrikeNetworkPolicyName)
	}

	return validNps
}

func createIntraNamespaceEgressPolicy(namespace string) *netv1.NetworkPolicy {
	return &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: AllowEgressIntraNamespaceNetworkPolicyName,
		},
		Spec: netv1.NetworkPolicySpec{
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress},
			Egress: []netv1.NetworkPolicyEgressRule{
				{
					To: []netv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{K8sNameLabelKey: namespace},
							},
						},
					},
				},
			},
		},
	}
}

func createOrUpdateNetworkPolicy(ctx context.Context,
	namespace string,
	newNP *netv1.NetworkPolicy,
	k8sClient k8sclient.Interface,
	crClient client.Client,
) error {
	log := core.GetLogger(ctx)

	if crClient != nil {
		opRes, err := controllerutil.CreateOrUpdate(ctx, crClient, newNP, func() error { return nil })
		if err != nil {
			return fmt.Errorf("create or update instance NetworkPolicy in namespace %s: %v", namespace, err)
		}
		switch opRes {
		case controllerutil.OperationResultCreated:
			log.Debugf("Created NetworkPolicies in namespace %s", namespace)
		case controllerutil.OperationResultUpdated:
			log.Debugf("Updated NetworkPolicies in namespace %s", namespace)
		}
		return nil
	}

	nwIface := k8sClient.NetworkingV1()
	_, err := nwIface.NetworkPolicies(namespace).Create(ctx, newNP, metav1.CreateOptions{})
	if err != nil {
		if !apierrors.IsAlreadyExists(err) {
			log.WithError(err).Errorf("Create NetworkPolicy %s in namespace %s", newNP.Name, namespace)
			return fmt.Errorf("create instance NetworkPolicy in namespace %s: %v", namespace, err)
		}

		log.Debugf("NetworkPolicy %s already exists in namespace %s, updating", newNP.Name, namespace)

		_, err = nwIface.NetworkPolicies(namespace).Update(ctx, newNP, metav1.UpdateOptions{})
		if err != nil {
			log.WithError(err).Errorf("Update NetworkPolicy %s in namespace %s", newNP.Name, namespace)
			return fmt.Errorf("update existing instance NetworkPolicy in namespace %s: %v",
				namespace, err)
		}

		log.Debugf("Updated NetworkPolicies in namespace %s", namespace)
	} else {
		log.Debugf("Created NetworkPolicies in namespace %s", namespace)
	}

	return err
}
