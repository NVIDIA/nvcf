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
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	translateutil "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/util"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/imagecredential"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	runtimejson "k8s.io/apimachinery/pkg/runtime/serializer/json"
	utilerror "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/workloadtypes"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcak8sutil "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcainternaltranslate "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/translate"
	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kaischeduler"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	UnexpectedAdmissionErrReason  = "UnexpectedAdmissionError"
	ImagePullIssueReason          = "ErrImagePull"
	ImagePullIssueAlternateReason = "ImagePullBackOff"
	InferenceContainerName        = "inference"
	InitContainerName             = "init"
	RWPVCSuffix                   = "rw-pvc"
	ROPVCSuffix                   = "ro-pvc"
	ModelVolumeName               = "model-data"
)

type K8sComputeBackend struct {
	clients        *kubeclients.KubeClients
	bk8s           *BackendK8sCache
	dynClient      dynamic.Interface
	discRestMapper meta.RESTMapper
	scheme         *runtime.Scheme
	enabledAttrs   featureflag.Attributes
}

// These are mocked in tests.
var (
	newDynamicClient = func(_ *runtime.Scheme, config *rest.Config) (dynamic.Interface, error) {
		rc, err := rest.RESTClientFor(config)
		if err != nil {
			return nil, err
		}
		return dynamic.New(rc), nil
	}
)

func NewK8sComputeBackend(clients *kubeclients.KubeClients, bk8s *BackendK8sCache) (ICMSRequestHelper, K8sArtifactHelper) {
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	appsv1.AddToScheme(scheme)
	batchv1.AddToScheme(scheme)
	nvcav1new.AddToScheme(scheme)

	// Neither of these calls should fail in a serviceable k8s environment.
	dc, err := newDynamicClient(scheme, clients.Config)
	if err != nil {
		panic("create rest client for config: " + err.Error())
	}
	grs, err := restmapper.GetAPIGroupResources(clients.DiscoveryClient)
	if err != nil {
		panic("get API group-resources: " + err.Error())
	}

	newK8sBE := K8sComputeBackend{
		clients:        clients,
		bk8s:           bk8s,
		dynClient:      dc,
		discRestMapper: restmapper.NewDiscoveryRESTMapper(grs),
		scheme:         scheme,
		enabledAttrs:   featureflag.GetEnabledAttributes(),
	}
	return newK8sBE, newK8sBE
}

func getArtifactsFromReq(req *nvcav2beta1.ICMSRequest) (
	podArts, cmArts, secretArts, svcArts []function.LaunchArtifact,
	initCacheJob, bdCreate function.LaunchArtifact,
) {
	// Get the artifacts and perform type checks.
	for _, a := range req.Spec.CreationMsgInfo.LaunchArtifacts {
		a := a
		switch a.Type {
		case function.LaunchArtifactTypePod:
			podArts = append(podArts, a)
		case function.LaunchArtifactTypeConfigmap:
			cmArts = append(cmArts, a)
		case function.LaunchArtifactTypeSecret:
			secretArts = append(secretArts, a)
		case nvcatypes.LaunchArtifactTypeBYOOOTelCollectorService:
			svcArts = append(svcArts, a)
		case function.LaunchArtifactTypeInitCacheJob:
			initCacheJob = a
		case function.LaunchArtifactTypeBlockDevice:
			bdCreate = a
		}
	}
	return
}

func (c K8sComputeBackend) ApplyCreationMessage(ctx context.Context, req *nvcav2beta1.ICMSRequest) error {
	log := core.GetLogger(ctx)

	metrics := nvcametrics.FromContext(ctx)

	switch req.Spec.Action.Normalize() {
	case common.FunctionCreationAction:
		switch c.detectRequestType(req) {
		case ftMiniService:
			return c.applyMiniServiceCreationMessage(ctx, req)
		default:
			// Translate launch spec into artifacts for minimal code changes during migration
			// to the translation lib for functions. This only happens once.
			if c.bk8s.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.UseFunctionTranslator) &&
				len(req.Spec.CreationMsgInfo.LaunchArtifacts) == 0 &&
				req.Spec.CreationMsgInfo.FunctionLaunchSpecification != nil &&
				req.Spec.CreationMsgInfo.FunctionLaunchSpecification.EnvironmentB64 != "" {
				launchArts, err := c.translateFunctionLaunchSpecification(ctx, req)
				if err != nil {
					metricLabels := metrics.WithDefaultLabelValues(EventTranslateFunctionError)
					metrics.EventErrorTotal.WithLabelValues(metricLabels...).Inc()
					return fmt.Errorf("translate function launch specification: %w", err)
				}

				if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
					ureq, err := c.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})

					// Track K8s API call metrics
					if metrics := nvcametrics.FromContext(ctx); metrics != nil {
						metrics.TrackK8sAPICall("icmsrequest", err)
					}

					if err != nil {
						return err
					}

					ureq.Spec.CreationMsgInfo.LaunchArtifacts = launchArts

					_, err = c.clients.BART.NvcaV2beta1().ICMSRequests(ureq.Namespace).Update(ctx, ureq, metav1.UpdateOptions{})
					return err
				}); err != nil {
					log.WithError(err).Error("Failed to update launch artifacts")
					return err
				}
				// The update will be requeued by the informer.
				return nil
			}
			return c.applyFunctionCreationMessage(ctx, req)
		}
	case common.TaskCreationAction:
		switch c.detectRequestType(req) {
		case ftMiniService:
			return c.applyMiniServiceCreationMessage(ctx, req)
		default:
			return c.applyContainerTaskCreationMessage(ctx, req)
		}
	}
	return nvcaerrors.TerminalError(fmt.Errorf("request %s is neither function nor task", req.Name))
}

const (
	ftContainer   = "container"
	ftMiniService = "miniservice"
)

const (
	legacyFunctionCreationAction common.MessageAction = "RequestSpotInstances"
	legacyTaskCreationAction     common.MessageAction = "RequestSpotInstancesForTask"
)

// toLegacyAction converts new ICMS action names to legacy action names
// for backward compatibility with the ICMS service API.
func toLegacyAction(action common.MessageAction) common.MessageAction {
	switch action.Normalize() {
	case common.RequestICMSInstances:
		return legacyFunctionCreationAction
	case common.RequestICMSInstancesForTask:
		return legacyTaskCreationAction
	default:
		return action
	}
}

func (c K8sComputeBackend) detectRequestType(req *nvcav2beta1.ICMSRequest) string {
	if ls := req.Spec.CreationMsgInfo.FunctionLaunchSpecification; ls != nil {
		if ls.HelmChartLaunchSpecification != nil {
			return ftMiniService
		}
		return ftContainer
	}
	if ls := req.Spec.CreationMsgInfo.TaskLaunchSpecification; ls != nil {
		if ls.HelmChartLaunchSpecification != nil {
			return ftMiniService
		}
		return ftContainer
	}
	// Detect by artifact presence in case launch spec is not set.
	for _, art := range req.Spec.CreationMsgInfo.LaunchArtifacts {
		if art.Type == function.LaunchArtifactTypeHelmChart {
			return ftMiniService
		}
	}
	return ftContainer
}

func (c K8sComputeBackend) translateFunctionLaunchSpecification(
	ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
) (arts function.LaunchArtifacts, err error) {
	reqType := c.detectRequestType(req)
	namespace := c.bk8s.podInstanceNamespace
	if reqType != ftContainer {
		namespace = req.Name
	}
	tcfg := function.TranslateConfig{
		TranslateConfig: common.TranslateConfig{
			Namespace:                    namespace,
			ObjectNameBase:               req.Name,
			InstanceTypeLabelSelectorKey: nodefeatures.UniformInstanceTypeLabelKey,
			WorkloadResources:            corev1.ResourceRequirements{},
			Tolerations:                  append([]corev1.Toleration(nil), c.bk8s.cfg.Workload.Tolerations...),
			OTelResources:                k8sutil.GetContainerResourcesBYOO(c.bk8s.cfg),
			FluentbitResources:           k8sutil.GetContainerResourcesFluentBit(c.bk8s.cfg),
			FluentbitEnabled:             c.bk8s.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.BYOOFluentBit),
			ClusterRegion:                c.bk8s.clusterRegion,
			ClusterName:                  c.bk8s.clusterName,
		},
		DefaultStargateAddress: c.bk8s.cfg.Workload.DefaultStargateAddress,
		StargateQUICInsecure:   c.bk8s.cfg.Workload.StargateQUICInsecure,
		LLMSidecarRunAsUser:    c.bk8s.cfg.Workload.LLMSidecarRunAsUser,
	}

	if reqType == ftContainer {
		// Overhead is not needed in reqs/lims because they are only used for the inference container here.
		reqs, lims, err := c.calculatePodInstanceResourcesForInstanceType(req.Spec.CreationMsgInfo, nil)
		if err != nil {
			return nil, nvcaerrors.TerminalError(err)
		}
		if !c.bk8s.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.EnforceContainerFunctionResourceLimits) {
			// Remove non-GPU resources when enforcement is off.
			for _, l := range []corev1.ResourceList{reqs, lims} {
				delete(l, corev1.ResourceCPU)
				delete(l, corev1.ResourceMemory)
				delete(l, corev1.ResourceEphemeralStorage)
			}
		}

		tcfg.WorkloadResources.Requests = reqs
		tcfg.WorkloadResources.Limits = lims
	}

	msg := function.CreationQueueMessage{
		Details:                      req.Spec.FunctionDetails,
		CreationQueueMessageMetadata: req.Spec.CreationMsgInfo.CreationQueueMessageMetadata,
		LaunchSpecification:          req.Spec.CreationMsgInfo.FunctionLaunchSpecification,
	}

	objs, err := function.Translate(msg, tcfg)
	if err != nil {
		return nil, nvcaerrors.TerminalError(err)
	}
	if reqType == ftContainer {
		envs := c.bk8s.cfg.Agent.BYOOOTelCollectorEnvVars()
		for _, obj := range objs {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				continue
			}
			k8sutil.AddBYOOOTelCollectorEnvVarsToPodSpec(&pod.Spec, envs)
		}
	}

	// Ignore pod instances following the first, since NVCA multiplies them by instance count internally.
	addedFirstInstancePod := false
	encode := runtimejson.NewSerializer(runtimejson.DefaultMetaFactory, c.scheme, c.scheme, false).Encode
	out := &bytes.Buffer{}
	for _, obj := range objs {
		robj, ok := obj.(runtime.Object)
		if !ok {
			return nil, nvcaerrors.TerminalError(fmt.Errorf("code bug: %T is not a runtime.Object", obj))
		}
		var artType function.LaunchArtifactType
		switch t := obj.(type) {
		case *corev1.Pod:
			artType = function.LaunchArtifactTypePod
			if reqType == ftContainer && strings.HasSuffix(t.Name, req.Name) {
				if addedFirstInstancePod {
					continue
				}
				addedFirstInstancePod = true
			}
		case *corev1.Service:
			artType = types.LaunchArtifactTypeBYOOOTelCollectorService
		case *corev1.Secret:
			if t.StringData["password"] != "" {
				// Encode an additional artifact for creds.
				artData, err := json.Marshal(map[string]string{
					"apiKey": t.StringData["password"],
				})
				if err != nil {
					return nil, nvcaerrors.TerminalError(fmt.Errorf("encoded helm creds artifact: %v", err))
				}
				arts = append(arts, function.LaunchArtifact{
					Type:          function.LaunchArtifactTypeHelmCreds,
					Specification: base64.StdEncoding.EncodeToString(artData),
				})
			}
			artType = function.LaunchArtifactTypeSecret
		case *corev1.ConfigMap:
			artType = function.LaunchArtifactTypeConfigmap
		case *corev1.PersistentVolumeClaim:
			artType = function.LaunchArtifactTypeBlockDevice
		case *batchv1.Job:
			artType = function.LaunchArtifactTypeInitCacheJob
		default:
			return nil, nvcaerrors.TerminalError(fmt.Errorf("code bug: unknown translated function type %T", obj))
		}

		if err := encode(robj, out); err != nil {
			return nil, nvcaerrors.TerminalError(fmt.Errorf("encode translated object: %w", err))
		}

		arts = append(arts, function.LaunchArtifact{
			Type:          artType,
			Specification: base64.StdEncoding.EncodeToString(out.Bytes()),
		})

		// Reset buffer for reuse.
		out.Reset()
	}

	if msg.LaunchSpecification.HelmChartLaunchSpecification != nil {
		hchartArtSpec := nvcatypes.HelmChartArtifactSpec{}
		hcCfg, err := common.ExtractHelmConfiguration(
			msg.LaunchSpecification.EnvironmentB64,
			msg.LaunchSpecification.HelmChartLaunchSpecification,
		)
		if err != nil {
			return nil, nvcaerrors.TerminalError(fmt.Errorf("extract helm config: %v", err))
		}
		hchartArtSpec.HelmChartURL = hcCfg.URL
		if hchartArtSpec.RepositoryURL, hchartArtSpec.ChartName, hchartArtSpec.Version,
			err = parseHelmChartURL(hcCfg.URL); err != nil {
			return nil, nvcaerrors.TerminalError(fmt.Errorf("parse helm chart url: %v", err))
		}
		hchartArtSpec.ValuesJSON = hcCfg.Values
		hchartArtSpec.HelmChartServiceName = hcCfg.ServiceName
		hchartArtSpec.HelmChartServicePort = hcCfg.ServicePort
		hchartArtSpecBytes, err := json.Marshal(hchartArtSpec)
		if err != nil {
			return nil, err
		}

		arts = append(arts, function.LaunchArtifact{
			Type:          function.LaunchArtifactTypeHelmChart,
			Specification: base64.StdEncoding.EncodeToString(hchartArtSpecBytes),
		})
	}

	return arts, nil
}

// Use the same logic ICMS does to parse helm chart details from a chart URL.
// to avoid drift from what occurs upstream.
// This is necessary until Helm ReVal is used to render charts,
// since the new launch spec only has "helmChart" in it.
var parseChartRE = regexp.MustCompile(`^(.+)/charts/(.+)-(.+)\.tgz$`)

func parseHelmChartURL(hcURL string) (repoURL, chartName, version string, err error) {
	submatches := parseChartRE.FindStringSubmatch(hcURL)
	if len(submatches) != 4 {
		return "", "", "", fmt.Errorf("helm chart url %s does not match regexp %q",
			hcURL, parseChartRE.String())
	}

	repoURL = submatches[1]
	chartName = submatches[2]
	version = submatches[3]

	return repoURL, chartName, version, nil
}

func (c K8sComputeBackend) applyFunctionCreationMessage(ctx context.Context, req *nvcav2beta1.ICMSRequest) error { //nolint:gocyclo
	log := core.GetLogger(ctx)
	var roPVCName string

	activeInstances := map[string]nvcav2beta1.InstanceStatus{}
	if len(req.Status.Instances) != 0 {
		activeInstances = req.Status.Instances
	}

	// getArtifacts categorized from req
	podArts, cmArts, secretArts, svcArts, initCacheJob, bdCreate := getArtifactsFromReq(req)

	instCount := req.Spec.CreationMsgInfo.InstanceCount

	// if requested instances are already created, return
	if int(instCount) == len(activeInstances) {
		return c.bk8s.ApplyICMSRequestStatusChange(ctx, req)
	}

	c.bk8s.eventRecorder.Eventf(req, corev1.EventTypeNormal, string(types.EventCategoryInstanceCreation), "Creating %v requested instances", instCount)

	labelsForReq := nvcatypes.GetLabelsForRequest(req, c.bk8s.featureFlagFetcher)
	annosForReq := nvcatypes.GetAnnotationsForRequest(req)
	ownerRefsForReq := getOwnerRefForRequest(req)
	instanceNamespace := c.bk8s.podInstanceNamespace

	// mf applies mutations to obj with data from the request.
	mf := func(obj client.Object) {
		obj.SetNamespace(instanceNamespace)
		obj.SetLabels(mergeMaps(obj.GetLabels(), labelsForReq))
		obj.SetAnnotations(mergeMaps(obj.GetAnnotations(), annosForReq))
		obj.SetOwnerReferences(ownerRefsForReq)
	}

	// Create secrets for the function before creating anything else
	for _, secretArt := range secretArts {
		if err := c.CreateSecretArtifact(ctx, secretArt, mf); err != nil {
			return err
		}
	}
	if len(secretArts) != 0 {
		log.Debugf("successfully created %v Secrets", len(secretArts))
	}

	for _, cmArt := range cmArts {
		if err := c.CreateConfigMapArtifact(ctx, cmArt, mf); err != nil {
			return err
		}
	}

	if len(cmArts) != 0 {
		log.Debugf("successfully created %v ConfigMaps", len(cmArts))
	}

	for _, svcArt := range svcArts {
		if err := c.CreateServiceArtifact(ctx, svcArt, mf); err != nil {
			return err
		}
	}

	if len(svcArts) != 0 {
		log.Debugf("successfully created %v Services", len(svcArts))
	}

	launchSpec := req.Spec.CreationMsgInfo.FunctionLaunchSpecification

	needsBYOO := launchSpec != nil && launchSpec.Telemetries != nil &&
		c.bk8s.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.BYOObservability)

	for _, podArt := range podArts {
		// decode the Pod Worker spec
		obj, err := nvcak8sutil.GetObjectFromEncodedString(podArt.Specification, reflect.TypeOf(&corev1.Pod{}))
		if err != nil {
			return nvcaerrors.TerminalError(fmt.Errorf("error while decoding YAML object: %w", err))
		}

		pod := obj.(*corev1.Pod)

		// Pods for a function with BYOO enabled must be targeted by the BYOO metrics egress netpol
		// in this namespace, which uses a specific label.
		if needsBYOO {
			pod.Labels[k8sutil.BYOOMetricsEgressTargetLabelKey] =
				k8sutil.BYOOMetricsEgressTargetLabelValue

			// Add BYOO OTel env vars to the inference container.
			// Use 127.0.0.1 since the OTel collector runs as a sidecar in the same pod.
			byooOTelEnvs := common.MakeOTelEnvSet(launchSpec.Telemetries, "127.0.0.1")
			for i := range pod.Spec.Containers {
				// Add to inference containers, preserving customer-provided env vars.
				switch pod.Spec.Containers[i].Name {
				case common.UtilsContainerName, function.LLMWorkerContainerName,
					common.ByooOTelCollectorPodNameBase, common.ESSContainerName:
				default:
					c := []corev1.Container{pod.Spec.Containers[i]}
					common.AddOptionalEnvsToContainers(c, byooOTelEnvs...)
					pod.Spec.Containers[i] = c[0]
				}
			}
		}

		// setup the InitCacheJob & BlockDevice if requested
		if c.bk8s.cachingSupportEnabled && (initCacheJob.Specification != "" && bdCreate.Specification != "") {
			cacheMF, cachePVCName, err := c.setupContainerFunctionModelCaching(ctx, req, bdCreate, initCacheJob,
				func(obj client.Object) {
					// for caching mf we need to skip OwnerRefs
					obj.SetNamespace(instanceNamespace)
					obj.SetLabels(mergeMaps(obj.GetLabels(), labelsForReq))
					obj.SetAnnotations(mergeMaps(obj.GetAnnotations(), annosForReq))
				})
			if err != nil {
				return err
			}
			roPVCName = cachePVCName
			cacheMF(pod)
		} else if !c.bk8s.cachingSupportEnabled {
			log.Debugf("ModelCaching support is disabled, creating instance without caching")
		} else {
			log.Debug("InitCacheJob / BDCreate spec was not specified, skipping model caching")
		}

		// Container function utils and init resource limits are toggled by feature flag.
		setResourceLimits := c.bk8s.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.EnforceContainerFunctionResourceLimits)
		k8sutil.SetNVCFInfraContainerResources(corev1.ResourceList(c.bk8s.cfg.Agent.UtilsResources), pod, setResourceLimits)
		// Only validate the whole pod if limits are required, since other containers may not have them
		// when the feature flag is disabled.
		if setResourceLimits {
			if err := k8sutil.ValidateAllContainerResourcesSet(pod); err != nil {
				log.WithError(err).Error("Container function pod resources are invalid")
				return nvcaerrors.TerminalError(err)
			}
		}

		if c.bk8s.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.KAIScheduler) {
			pod.Spec.SchedulerName = kaischeduler.SchedulerName
			pod.Labels[kaischeduler.SchedulerQueueLabel] = kaischeduler.DefaultQueue
		}

		// Enable NVIDIA Nsight GPU profiling for this pod when its function is in the
		// profiling allowlist. The label must be present at pod creation for the Nsight
		// Operator's admission webhook to inject profiling.
		if c.bk8s.nsightProfilingAllowlist.ShouldProfile(req.Spec.FunctionDetails.FunctionID) {
			if pod.Labels == nil {
				pod.Labels = map[string]string{}
			}
			pod.Labels[c.bk8s.nsightProfilingAllowlist.LabelKey()] = c.bk8s.nsightProfilingAllowlist.LabelValue()
		}

		if requeue, err := c.initializeImageCredentialHelper(ctx, req, mf); err != nil || requeue {
			if err != nil {
				err = fmt.Errorf("ensure image credential updater objects: %w", err)
			}
			return err
		}

		newActiveInstances, err := c.CreatePodArtifactInstances(ctx, pod, req, mf)
		if err != nil {
			return err
		}
		for _, activeInstance := range newActiveInstances {
			activeInstance := activeInstance
			if _, ok := activeInstances[activeInstance.ID]; !ok {
				activeInstances[activeInstance.ID] = activeInstance
			}
		}
	}

	// TODO(estroczynski): set owner references on other artifacts to clean up when Pod is deleted.

	// update timestamp only once for InProgress
	if req.Status.RequestStatus != nvcav2beta1.ICMSRequestStatusInProgress {
		req.Status.LastStatusUpdated = &metav1.Time{Time: core.GetCurrentTime(ctx)}
	}
	req.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusInProgress

	modify := func(_ context.Context, sr *nvcav2beta1.ICMSRequest) {
		if roPVCName != "" {
			sr.Status.CacheReferenceName = roPVCName
		}
		sr.Status.Instances = activeInstances
		sr.Status.RequestStatus = req.Status.RequestStatus
		sr.Status.LastStatusUpdated = req.Status.LastStatusUpdated
	}
	if !c.bk8s.applyICMSRequestStatusChange(ctx, req, modify) {
		log.Errorf("failed to update status for Request %v/%v", req.Namespace, req.Name)
	}
	if int(instCount) != len(activeInstances) {
		return fmt.Errorf("created only %v of requested %v instances for request %v/%v", len(activeInstances), instCount, req.Namespace, req.Name)
	}
	log.Debugf("successfully created %v of requested %v instances for request %v/%v", len(activeInstances), instCount, req.Namespace, req.Name)
	return nil
}

func (c K8sComputeBackend) setupContainerFunctionModelCaching(ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
	bdCreate, initCacheJob function.LaunchArtifact,
	cachemf mutateFunc,
) (func(*corev1.Pod), string, error) {
	log := core.GetLogger(ctx)
	rwPVC, initJob, err := getModelCacheK8sArtifacts(ctx, bdCreate, initCacheJob, cachemf)
	if err != nil {
		log.WithError(err).Error("failed getModelCacheK8sArtifacts, model caching will be disabled")
		return func(*corev1.Pod) {}, "", nil
	}
	return c.setupContainerModelCaching(ctx, req, rwPVC, initJob, cachemf)
}

func (c K8sComputeBackend) setupContainerModelCaching(ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
	rwPVC *corev1.PersistentVolumeClaim,
	initJob *batchv1.Job,
	cachemf mutateFunc,
) (mf func(*corev1.Pod), roPVCName string, err error) {
	log := core.GetLogger(ctx)
	// setup init cache Job writer and RWMany PVC
	mc, roPVCName := c.SetupModelCachingForRequest(ctx, rwPVC, initJob, req, cachemf)
	switch mc {
	case ModelCachingCompleted:
		log.Infof("model caching completed, starting worker creation")
		c.bk8s.eventRecorder.Eventf(req, corev1.EventTypeNormal, string(types.EventCategoryModelCaching), "%v ready for instance", roPVCName)
		// Modify the pod volume to be that of the ROPVCName
		mf = func(pod *corev1.Pod) {
			for id := range pod.Spec.Volumes {
				if pod.Spec.Volumes[id].Name == ModelVolumeName {
					pod.Spec.Volumes[id].VolumeSource = corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: roPVCName,
							ReadOnly:  true,
						},
					}
				}
			}
		}
		return mf, roPVCName, nil
	case ModelCachingInProgress:
		modify := func(ctx context.Context, sr *nvcav2beta1.ICMSRequest) {
			if roPVCName != "" {
				sr.Status.CacheReferenceName = roPVCName
			}
			sr.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusCachingInProgress
			sr.Status.LastStatusUpdated = &metav1.Time{Time: core.GetCurrentTime(ctx)}
		}
		if !c.bk8s.applyICMSRequestStatusChange(ctx, req, modify) {
			log.Errorf("failed to update status for Request %v/%v", req.Namespace, req.Name)
		}
		return nil, "", fmt.Errorf("model caching is still in progress")
	case ModelCachingFailed:
		c.bk8s.eventRecorder.Event(req, corev1.EventTypeWarning,
			string(types.EventCategoryModelCaching), "Caching setup failed, resort to non-cached workers")
		log.Warnf("model caching failed, NVCA will create non-cached workers")
	}
	return func(*corev1.Pod) {}, "", nil
}

func getPullSecretsFromArtifacts(ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
) (workerImagePullSecrets, workloadImagePullSecrets []*corev1.Secret, err error) {
	log := core.GetLogger(ctx)

	var secretObjs []metav1.Object
	for _, v := range req.Spec.CreationMsgInfo.LaunchArtifacts {
		if v.Type != function.LaunchArtifactTypeSecret {
			continue
		}
		sObj, err := nvcak8sutil.GetObjectFromEncodedString(v.Specification, reflect.TypeOf(&corev1.Secret{}))
		if err != nil {
			log.WithError(err).Errorf("failed to decode ICMS request artifact of type %s", v.Type)
			return nil, nil, fmt.Errorf("failed to decode ICMS request artifact of type %s, error: %w", v.Type, err)
		}
		s := sObj.(*corev1.Secret)
		secretObjs = append(secretObjs, s)
	}

	return k8sutil.FindNVCFImagePullSecretObjects(secretObjs...)
}

// getWorkerPullSecretsFromArtifacts returns only worker image pull secrets from LaunchArtifacts.
// Use this for container function paths where workload pull secrets are not required
// (e.g., ECR images authenticated via node IAM role).
func getWorkerPullSecretsFromArtifacts(ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
) (workerImagePullSecrets []*corev1.Secret, err error) {
	log := core.GetLogger(ctx)

	var secretObjs []metav1.Object
	for _, v := range req.Spec.CreationMsgInfo.LaunchArtifacts {
		if v.Type != function.LaunchArtifactTypeSecret {
			continue
		}
		sObj, err := nvcak8sutil.GetObjectFromEncodedString(v.Specification, reflect.TypeOf(&corev1.Secret{}))
		if err != nil {
			log.WithError(err).Errorf("failed to decode ICMS request artifact of type %s", v.Type)
			return nil, fmt.Errorf("failed to decode ICMS request artifact of type %s, error: %w", v.Type, err)
		}
		s := sObj.(*corev1.Secret)
		secretObjs = append(secretObjs, s)
	}

	return k8sutil.FindNVCFWorkerImagePullSecretObjects(secretObjs...)
}

func (c K8sComputeBackend) doHelmChartStorageRequests(ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
	instanceNamespace string,
	workerImagePullSecrets []*corev1.Secret,
	needsCaching bool,
) (instanceAnnos, utilsAnnos map[string]string, err error) {
	log := core.GetLogger(ctx)
	metrics := nvcametrics.FromContext(ctx)

	var sts []*nvcav2beta1.StorageRequest
	// NOTE: doHelmChartStorageRequests has no callers (dead code); the active
	// helm storage-request path is the miniservice reconciler. The
	// HelmCachingSupport sub-gate is dropped here only so this compiles after
	// the flag is removed; backend selection lives in makeStorageRequests.
	if needsCaching && c.bk8s.cachingSupportEnabled {
		st, err := nvcastorage.NewModelCacheStorageRequest(req, c.bk8s.featureFlagFetcher)
		if err != nil {
			return nil, nil, err
		}
		sts = append(sts, st)
	}
	if c.bk8s.helmSharedStorageEnabled {
		if len(workerImagePullSecrets) == 0 || workerImagePullSecrets[0] == nil {
			if workerImagePullSecrets, _, err = getPullSecretsFromArtifacts(ctx, req); err != nil {
				return nil, nil, nvcaerrors.TerminalError(fmt.Errorf("get worker pull secret from artifact: %v", err))
			}
		}
		sts = append(sts, nvcastorage.NewSharedStorageRequest(req, c.bk8s.featureFlagFetcher, c.bk8s.cfg,
			workerImagePullSecrets))
	}
	if c.bk8s.helmInternalPersistentStorageEnabled {
		sts = append(sts, nvcastorage.NewInternalPersistentStorageRequest(req, featureflag.HelmInternalPersistentStorage.Spec, c.bk8s.featureFlagFetcher))
	}

	srAPI := nvcastorage.NewStorageRequestAPI(c.clients.BART)
	instanceAnnos, utilsAnnos = map[string]string{}, map[string]string{}
	for _, st := range sts {
		ref, err := srAPI.Get(ctx, instanceNamespace, st.Name)
		if err != nil && !apierrors.IsNotFound(err) {
			log.WithError(err).Error("Failed to get StorageRequest")
			if metrics != nil {
				metrics.TrackK8sAPICall("storagerequests", err)
			}
			return nil, nil, err
		}
		if apierrors.IsNotFound(err) {
			var created *nvcav2beta1.StorageRequest
			if created, err = srAPI.Create(ctx, instanceNamespace, st); err != nil {
				return nil, nil, err
			}
			ref = nvcastorage.NewStorageRequestRefFromV2Beta1(created)
		}
		st := ref.V1()

		switch st.Status.Phase {
		case nvcav1new.StorageFailed:
			switch st.Spec.Type {
			case nvcav1new.ModelCacheRequest:
				c.bk8s.eventRecorder.Event(req, corev1.EventTypeWarning,
					string(types.EventCategoryModelCaching), "Caching setup failed, resort to non-cached workers")
				log.Error("Model cache storage failed, model caching will be disabled")
				metrics.EventErrorTotal.WithLabelValues(metrics.WithDefaultLabelValues(EventPVCModelCachingError)...).Inc()
				metrics.EventErrorTotal.WithLabelValues(metrics.WithDefaultLabelValues(EventModelCachingFailed)...).Inc()
			case nvcav1new.SharedStorageRequest:
				log.Error("Shared storage failed")
				return nil, nil, nvcaerrors.TerminalError(fmt.Errorf("shared storage failed"))
			case nvcav1new.InternalPersistentStorageRequest:
				log.Error("Internal persistent storage failed")
				return nil, nil, nvcaerrors.TerminalError(fmt.Errorf("internal persistent storage failed"))
			}
		case nvcav1new.StorageReady:
			log.Debugf("Storage %s succeeded", st.Spec.Type)
			switch st.Spec.Type {
			case nvcav1new.ModelCacheRequest:
				// Model caching is allowed to fail.
				if st.Status.ModelCache != nil {
					roPVCName := st.Status.ModelCache.ROPVCName
					instanceAnnos[nvcastorage.WebhookModelCachePVCNameAnnotationKey] = roPVCName
					utilsAnnos[nvcastorage.WebhookModelCachePVCNameAnnotationKey] = roPVCName
				}
			case nvcav1new.SharedStorageRequest:
				instanceAnnos[nvcastorage.HelmWebhookSharedStorageSecretsReadOnlyPVCNameAnnotationKey] =
					st.Status.SharedStorage.Secrets.ReadOnlyPVCName
				utilsAnnos[nvcastorage.HelmWebhookSharedStorageSecretsReadWritePVCNameAnnotationKey] =
					st.Status.SharedStorage.Secrets.ReadWritePVCName
				instanceAnnos[nvcastorage.HelmWebhookSharedStorageKNSReadOnlyPVCNameAnnotationKey] =
					st.Status.SharedStorage.KNS.ReadOnlyPVCName
				utilsAnnos[nvcastorage.HelmWebhookSharedStorageKNSReadWritePVCNameAnnotationKey] =
					st.Status.SharedStorage.KNS.ReadWritePVCName
			case nvcav1new.InternalPersistentStorageRequest:
				instanceAnnos[nvcastorage.HelmWebhookInternalPersistentStorageStorageClassNameAnnotationKey] =
					st.Status.InternalPersistentStorage.StorageClassName
			}
		default:
			if st.Spec.Type == nvcav1new.ModelCacheRequest {
				modify := func(ctx context.Context, sr *nvcav2beta1.ICMSRequest) {
					sr.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusCachingInProgress
					sr.Status.LastStatusUpdated = &metav1.Time{Time: core.GetCurrentTime(ctx)}
				}
				if !c.bk8s.applyICMSRequestStatusChange(ctx, req, modify) {
					log.Errorf("Failed to update model cache status for Request")
				}
			}
			log.Debugf("Storage %s is in phase %s, requeueing", st.Spec.Type, st.Status.Phase)
			return nil, nil, nvcastorage.NewRequeueableStorageError(fmt.Sprintf("storage is %s for helm chart, requeue", st.Status.Phase))
		}
	}

	return instanceAnnos, utilsAnnos, nil
}

func (c K8sComputeBackend) initializeImageCredentialHelper(
	ctx context.Context,
	icmsReq *nvcav2beta1.ICMSRequest,
	mf mutateFunc,
) (bool, error) {
	log := core.GetLogger(ctx)

	if c.bk8s.imageCredentialHelperImage == "" {
		log.Debug("Third party registry image credential helper image not configured, skipping image credential update setup")
		return false, nil
	}

	var envB64 string
	if icmsReq.Spec.CreationMsgInfo.FunctionLaunchSpecification != nil {
		envB64 = icmsReq.Spec.CreationMsgInfo.FunctionLaunchSpecification.EnvironmentB64
	} else {
		envB64 = icmsReq.Spec.CreationMsgInfo.TaskLaunchSpecification.EnvironmentB64
	}
	allEnvSet, err := common.DecodeEnvironmentB64(envB64, common.EnvDecoderText)
	if err != nil {
		log.WithError(err).Error("Failed to decode workload env set")
		return false, nvcaerrors.TerminalError(err)
	}

	if allEnvSet[common.ContainerRegistriesCredentialsEnv] == "" || allEnvSet[common.SidecarRegistryCredentialEnv] == "" {
		log.Debug("Third party registry support not configured for this function, skipping image credential update setup")
		return false, nil
	}

	targetNamespace := c.bk8s.podInstanceNamespace
	jobNamespace := c.bk8s.systemNamespace

	// A selector is needed to target pod instance secrets specifically.
	secretSel, err := getICMSRequestIDLabelSelector(ctx, icmsReq.Spec.RequestID, icmsReq.Spec.MessageBatchID)
	if err != nil {
		log.WithError(err).Error("Failed to create ICMSRequest label selector")
		return false, nvcaerrors.TerminalError(err)
	}

	tprCredSecret, err := imagecredential.NewImageCredsSecret(icmsReq.Name, allEnvSet)
	if err != nil {
		log.WithError(err).Error("Failed to create third party registry cred helper objects")
		return false, nvcaerrors.TerminalError(err)
	}

	mf(tprCredSecret)

	if _, err := c.clients.K8s.CoreV1().Secrets(targetNamespace).Get(ctx, tprCredSecret.Name, metav1.GetOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			return false, err
		}
		_, err := c.clients.K8s.CoreV1().Secrets(targetNamespace).Create(ctx, tprCredSecret, metav1.CreateOptions{})
		if err != nil {
			return false, err
		}
	}

	tprUpdaterInitJob := imagecredential.NewInitJob(icmsReq.Name+"-cred-init", c.bk8s.imageCredentialHelperImage,
		targetNamespace, secretSel.String())

	// Use NVCA's service account to run the job for API access and image pull secrets.
	tprUpdaterInitJob.Namespace = jobNamespace
	tprUpdaterInitJob.Spec.Template.Spec.ServiceAccountName = "nvca"
	k8sutil.MergePodSpecTolerations(&tprUpdaterInitJob.Spec.Template.Spec, c.bk8s.cfg.Workload.Tolerations...)

	// Set TTL to 1 hour so jobs are cleaned up in case of terminal failure
	// or the instance is cleaned up before this check completes.
	tprUpdaterInitJob.Spec.TTLSecondsAfterFinished = new(int32)
	*tprUpdaterInitJob.Spec.TTLSecondsAfterFinished = 3600

	// These jobs should complete in seconds. If some issue results in jobs running for > 10 minutes,
	// their pods should be terminated and the job should be marked failed.
	tprUpdaterInitJob.Spec.ActiveDeadlineSeconds = new(int64)
	*tprUpdaterInitJob.Spec.ActiveDeadlineSeconds = 600

	jobClient := c.clients.K8s.BatchV1().Jobs(jobNamespace)

	if tmpJob, err := jobClient.Get(ctx, tprUpdaterInitJob.Name, metav1.GetOptions{}); err == nil {
		log.Debug("Checking image pull updater init job status")
		objStatus, reason := checkImageCredentialUpdaterJobStatus(tmpJob)
		switch objStatus {
		case InitCacheJobCompleted:
			log.Debug("Image pull updater init job succeeded")
			if tmpJob.Annotations == nil {
				tmpJob.Annotations = map[string]string{}
			}
			tmpJob.Annotations[k8sutil.ImageCredUpdaterInitJobCompletedAnnotationKey] = "true"
			if _, err := jobClient.Update(ctx, tmpJob, metav1.UpdateOptions{}); err != nil {
				return false, err
			}
		case InitCacheJobFailed:
			err := fmt.Errorf("image pull updater init job failed: %s", reason)
			log.WithError(err).Error("Image pull updater init job in terminal state")
			return false, nvcaerrors.TerminalError(err)
		default:
			log.Debug("Image pull updater init job is pending or running, requeuing")
			return true, nil
		}
	} else if apierrors.IsNotFound(err) {
		if _, err := jobClient.Create(ctx, tprUpdaterInitJob, metav1.CreateOptions{}); err != nil {
			log.WithError(err).Error("Failed to apply image pull updater init job")
			return false, err
		}
		log.Debug("Image pull updater init job has been applied, requeuing")
		return true, nil
	} else {
		log.WithError(err).Error("Failed to get image pull updater init job")
		return false, err
	}

	return false, nil
}

func checkImageCredentialUpdaterJobStatus(job *batchv1.Job) (state InitCacheJobState, reason string) {
	if job.Status.CompletionTime != nil && job.Status.Succeeded > 0 {
		return InitCacheJobCompleted, ""
	}
	for _, jobCond := range job.Status.Conditions {
		if jobCond.Type == batchv1.JobFailed && jobCond.Status == corev1.ConditionTrue {
			return InitCacheJobFailed, jobCond.Reason
		}
		if jobCond.Type == batchv1.JobComplete && jobCond.Status == corev1.ConditionTrue {
			return InitCacheJobCompleted, jobCond.Reason
		}
	}
	return InitCacheJobInProgress, ""
}

func reqStatusPtr(rs nvcav2beta1.RequestStatus) *nvcav2beta1.RequestStatus { return &rs }

func (c K8sComputeBackend) CreatePodArtifactInstances(ctx context.Context, pod *corev1.Pod,
	req *nvcav2beta1.ICMSRequest, mf mutateFunc) ([]nvcav2beta1.InstanceStatus, error) {
	log := core.GetLogger(ctx)

	baseName := req.Name
	instCount := req.Spec.CreationMsgInfo.InstanceCount

	mf(pod)

	// Pod Specs have default Pod affinities set by ICMS not relevant to BYOC.
	if pod.Spec.Affinity != nil {
		pod.Spec.Affinity.PodAffinity = nil
		pod.Spec.Affinity.PodAntiAffinity = nil
	}

	if err := c.prepareTransportTLSForPod(ctx, pod); err != nil {
		return nil, err
	}

	needsEnforcement := !c.enabledAttrs.Empty()
	if needsEnforcement {
		enforce.SetMetadata(pod, c.enabledAttrs)
	}

	// Add the INFERENCE_READY_TIMEOUT env var to the utils
	// container in the worker pod.
	addEnvsToUtilsContainers(pod, corev1.EnvVar{
		Name:  nvcatypes.InferenceReadyTimeoutEnvKey,
		Value: c.bk8s.k8sTimeConfig.WorkerStartupTimeout.String(),
	})

	// use the req object Name as PodID
	var newActiveInstances []nvcav2beta1.InstanceStatus
	for i := len(req.Status.Instances); i < int(instCount); i++ {
		pod := pod.DeepCopy()
		pod.Name = getPodName(fmt.Sprintf("%d-%s", i, baseName))
		pod = setEnvInPod(pod, map[string]string{nvcatypes.InstanceIDEnvKey: pod.Name, nvcatypes.NVCFInstIDEnvKey: pod.Name})

		plog := log.WithField("pod_name", pod.Name)
		plog.Debug("Creating Pod from artifact")

		setTerminationGracePeriodIfNotSet(pod)
		k8sutil.ApplyCustomAnnotations(pod, c.bk8s.customAnnotations)

		if _, err := c.clients.K8s.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return nil, fmt.Errorf("failed to create instance for Request %v/%v, err: %v", req.Namespace, req.Name, err)
			}
			plog.Debugln("Pod instance already exists")

			if existingPod, err := c.clients.K8s.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{}); err == nil {
				// Track K8s API call metrics (success case)
				if metrics := nvcametrics.FromContext(ctx); metrics != nil {
					metrics.TrackK8sAPICall("pod", err)
				}

				if needsEnforcement && !enforce.IsEnforcementLabelSet(existingPod) {
					plog.Info("Updating Pod instance with enforcement labels")

					enforce.SetMetadata(existingPod, c.enabledAttrs)
					_, err := c.clients.K8s.CoreV1().Pods(pod.Namespace).Update(ctx, existingPod, metav1.UpdateOptions{})
					if err != nil {
						return nil, fmt.Errorf("update pod from ICMSRequest %s/%s with enforcement labels: %v",
							req.Name, req.Namespace, err)
					}
				}
			} else {
				plog.WithError(err).Error("Get existing Pod instance")
				return nil, err
			}
		}
		newActiveInstances = append(newActiveInstances, nvcav2beta1.InstanceStatus{
			ID:                    pod.Name,
			Type:                  nvcav2beta1.InstanceTypePod,
			Status:                string(types.ICMSInstanceStarted),
			LastReportedStatus:    string(types.ICMSInstanceStateNoStatus),
			LastReportedTimestamp: nil,
		})

		c.bk8s.eventRecorder.Eventf(req, corev1.EventTypeNormal,
			string(types.EventCategoryInstanceCreation), "Created %v Instance %v", nvcav2beta1.InstanceTypePod, pod.Name)
	}

	if len(newActiveInstances) != 0 {
		log.Debugf("successfully created %v Pod instances", len(newActiveInstances))
	}

	return newActiveInstances, nil
}

func (c K8sComputeBackend) CreatePodArtifact(ctx context.Context, podArt function.LaunchArtifact, mf mutateFunc) error {
	obj, err := nvcak8sutil.GetObjectFromEncodedString(podArt.Specification, reflect.TypeOf(&corev1.Pod{}))
	if err != nil {
		return nvcaerrors.TerminalError(fmt.Errorf("error while decoding Pod object: %w", err))
	}

	pod := obj.(*corev1.Pod)

	mf(pod)

	_, err = c.clients.K8s.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create Pod %s/%s from artifact: %v", pod.Namespace, pod.Name, err)
	}
	return nil
}

func (c K8sComputeBackend) purgeInstanceID(ctx context.Context, req *nvcav2beta1.ICMSRequest,
	terminatedInstances map[string]nvcav2beta1.InstanceStatus, id string) bool {
	log := core.GetLogger(ctx).WithField("instance_id", id)

	instanceTerminated := false
	if isMiniServiceInstance(id) {
		// The miniservice controller will handle function object and namespace deletion.
		ms := &v1alpha1.MiniService{}
		ms.Name = id
		if err := c.clients.HelmV2.Get(ctx, client.ObjectKeyFromObject(ms), ms); err != nil {
			if !apierrors.IsNotFound(err) {
				c.bk8s.eventRecorder.Eventf(req, corev1.EventTypeWarning,
					string(types.EventCategoryInstanceTermination), "Failed to get instance %v", id)
				log.WithError(err).Errorf("failed to get miniservice instance %v, for request %v/%v",
					id, req.Namespace, req.Name)
				return false
			}
			log.Debug("Miniservice not found, assuming instance is terminated")
		} else if ms.DeletionTimestamp == nil {
			if err := c.clients.HelmV2.Delete(ctx, ms); err != nil {
				if !apierrors.IsNotFound(err) {
					c.bk8s.eventRecorder.Eventf(req, corev1.EventTypeWarning,
						string(types.EventCategoryInstanceTermination), "Failed to stop instance %v", id)
					log.WithError(err).Errorf("failed to terminate miniservice instance %v, for request %v/%v",
						id, req.Namespace, req.Name)
					return false
				}
				log.Debug("Miniservice not found, report as terminated")
			} else {
				log.Debug("Terminated miniservice")
				c.bk8s.eventRecorder.Eventf(req, corev1.EventTypeNormal, string(types.EventCategoryInstanceTermination),
					"Stopped instance %v", id)
			}
		}

		if _, ok := terminatedInstances[id]; !ok {
			terminatedInstances[id] = nvcav2beta1.InstanceStatus{
				ID:                    id,
				Type:                  nvcav2beta1.InstanceTypeMiniService,
				Status:                string(types.ICMSInstanceTerminated),
				LastReportedStatus:    string(types.ICMSInstanceStateNoStatus),
				LastReportedTimestamp: nil,
			}
		}
		instanceTerminated = true
	} else {
		err := c.clients.K8s.CoreV1().Pods(c.bk8s.podInstanceNamespace).Delete(ctx, id, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			log.WithError(err).Errorf("failed to terminate instance %v, for request %v/%v", id, req.Namespace, req.Name)
			c.bk8s.eventRecorder.Eventf(req, corev1.EventTypeWarning,
				string(types.EventCategoryInstanceTermination), "Failed to stop instance %v/%v", c.bk8s.podInstanceNamespace, id)
			return false
		} else if err != nil && apierrors.IsNotFound(err) {
			log.Debug("Pod not found, report as terminated")
		} else {
			log.Debug("Terminated Pod")
			c.bk8s.eventRecorder.Eventf(req, corev1.EventTypeNormal,
				string(types.EventCategoryInstanceTermination), "Stopped instance %v/%v", c.bk8s.podInstanceNamespace, id)
		}

		if _, ok := terminatedInstances[id]; !ok {
			terminatedInstances[id] = nvcav2beta1.InstanceStatus{
				ID:                    id,
				Type:                  nvcav2beta1.InstanceTypePod,
				Status:                string(types.ICMSInstanceTerminated),
				LastReportedStatus:    string(types.ICMSInstanceStateNoStatus),
				LastReportedTimestamp: nil,
			}
			instanceTerminated = true
		}
	}

	return instanceTerminated
}

// deleteStorageRequestsInNamespace removes finalizers from and deletes all StorageRequests in the
// namespace. This unblocks namespace deletion when StorageRequests would otherwise block due to
// finalizers. Best-effort: logs warnings but does not fail on individual errors.
//
// Safeguard: only deletes StorageRequests when the namespace is already Terminating
// (DeletionTimestamp set) or when there are no running pods in the namespace. This prevents
// prematurely removing storage while workload pods may still be using PVCs created by the
// StorageRequest.
func (c K8sComputeBackend) deleteStorageRequestsInNamespace(ctx context.Context, namespace string) {
	log := core.GetLogger(ctx).WithField("namespace", namespace)

	ns, err := c.clients.K8s.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return
		}
		log.WithError(err).Warn("Failed to get namespace for StorageRequest cleanup")
		return
	}

	// Safeguard: if namespace is not yet Terminating, check for running pods.
	// Do not delete StorageRequests if workload pods may still be using the storage.
	if ns.DeletionTimestamp == nil {
		podList, err := c.clients.K8s.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			log.WithError(err).Warn("Failed to list pods for StorageRequest cleanup safeguard")
			return
		}
		for i := range podList.Items {
			if podList.Items[i].DeletionTimestamp == nil {
				log.Debug("Skipping StorageRequest cleanup: namespace has running pods that may use storage")
				return
			}
		}
	}

	stList, err := c.clients.BART.NvcaV2beta1().StorageRequests(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return
		}
		log.WithError(err).Warn("Failed to list StorageRequests in namespace")
		return
	}

	srAPI := c.clients.BART.NvcaV2beta1().StorageRequests(namespace)
	for i := range stList.Items {
		st := &stList.Items[i]
		if len(st.Finalizers) > 0 {
			if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				latest, err := srAPI.Get(ctx, st.Name, metav1.GetOptions{})
				if err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return err
				}
				if len(latest.Finalizers) == 0 {
					return nil
				}
				latest.Finalizers = nil
				_, err = srAPI.Update(ctx, latest, metav1.UpdateOptions{})
				return err
			}); err != nil && !apierrors.IsNotFound(err) {
				log.WithError(err).Warnf("Failed to remove finalizers from StorageRequest %s", st.Name)
				continue
			}
		}
		if err := srAPI.Delete(ctx, st.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			log.WithError(err).Warnf("Failed to delete StorageRequest %s", st.Name)
		}
	}
}

// PurgeInstanceID implements the ICMSRequestHelper interface by delegating to the private purgeInstanceID method
func (c K8sComputeBackend) PurgeInstanceID(ctx context.Context, req *nvcav2beta1.ICMSRequest,
	terminatedInstances map[string]nvcav2beta1.InstanceStatus, instanceID string) bool {
	return c.purgeInstanceID(ctx, req, terminatedInstances, instanceID)
}

// TODO: Harden logic on instance termination failures
func (c K8sComputeBackend) ApplyTerminationMessage(ctx context.Context, req *nvcav2beta1.ICMSRequest) error {
	log := core.GetLogger(ctx)
	instanceIDs := req.Spec.TerminationMsgInfo.InstanceIds
	terminatedInstances := map[string]nvcav2beta1.InstanceStatus{}
	if len(req.Status.Instances) != 0 {
		terminatedInstances = req.Status.Instances
	}

	var instanceIDsToTerminate []string
	for _, id := range instanceIDs {
		if _, ok := terminatedInstances[id]; !ok {
			instanceIDsToTerminate = append(instanceIDsToTerminate, id)
		}
	}

	if len(instanceIDsToTerminate) == 0 {
		// Cannot mutate the object since it is in the cache, we must first perform a deep copy on it
		req = req.DeepCopy()
		modify := func(ctx context.Context, sr *nvcav2beta1.ICMSRequest) {
			sr.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusCompleted
			sr.Status.LastStatusUpdated = &metav1.Time{Time: core.GetCurrentTime(ctx)}
		}
		if !c.bk8s.applyICMSRequestStatusChange(ctx, req, modify) {
			return fmt.Errorf("failed to update status for Request %v/%v", req.Namespace, req.Name)
		}
		return nil
	}

	// Check the lastStatusUpdatedTimeStamp and then if all instances Terminated
	// * Mark Request as Completed
	// * Keep the Request in Progress and fall through
	it := 0
	for _, id := range instanceIDs {
		id := id
		if c.purgeInstanceID(ctx, req, terminatedInstances, id) {
			it++
		}
	}

	// Cannot mutate the object since it is in the cache, we must first perform a deep copy on it
	req = req.DeepCopy()
	req.Status = nvcav2beta1.ICMSRequestStatus{
		Instances: terminatedInstances,
	}
	// update timestamp only once for InProgress
	if req.Status.RequestStatus != nvcav2beta1.ICMSRequestStatusInProgress {
		req.Status.LastStatusUpdated = &metav1.Time{Time: core.GetCurrentTime(ctx)}
	}
	req.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusInProgress

	modify := func(_ context.Context, sr *nvcav2beta1.ICMSRequest) {
		sr.Status.RequestStatus = req.Status.RequestStatus
		sr.Status.LastStatusUpdated = req.Status.LastStatusUpdated
		sr.Status.Instances = req.Status.Instances
	}
	if !c.bk8s.applyICMSRequestStatusChange(ctx, req, modify) {
		log.Errorf("failed to update status for Request %v/%v", req.Namespace, req.Name)
	}

	if len(terminatedInstances) != len(instanceIDs) {
		return fmt.Errorf("terminated only %v of requested %v instances for request %v/%v", it, len(instanceIDs),
			req.Namespace, req.Name)
	}
	log.Debugf("successfully terminated %v of requested %v instances for request %v/%v", it, len(instanceIDs), req.Namespace, req.Name)
	return nil
}

func IsPodStuckInitializing(pod *corev1.Pod, k8sTimeConfig *k8sutil.TimeConfig) (bool, types.ICMSInstanceState) {
	podStatus := pod.Status
	initConditionTrue := false
	for _, cond := range podStatus.Conditions {
		if cond.Type == corev1.PodInitialized && cond.Status == corev1.ConditionTrue {
			initConditionTrue = true
			break
		}
	}
	if initConditionTrue {
		for _, containerStatus := range podStatus.ContainerStatuses {
			if containerStatus.RestartCount >= k8sutil.RestartCountToFailInstance &&
				k8sutil.IsTimeSincePodLaunchedLaterThan(pod, k8sTimeConfig.PodLaunchThresholdSecondsOnFailedRestarts) {
				return true, types.ICMSInstanceFailedContainerRestartLoop
			}
		}
	} else {
		for _, containerStatus := range podStatus.InitContainerStatuses {
			if containerStatus.RestartCount >= k8sutil.RestartCountToFailInstance &&
				k8sutil.IsTimeSincePodLaunchedLaterThan(pod, k8sTimeConfig.PodLaunchThresholdSecondsOnFailedRestarts) {
				return true, types.ICMSInstanceFailedInitContainerRestartLoop
			}
		}
	}
	// Pod seems to be good otherwise
	// Just check if the Pod's init container is stuck for > 120minutes
	if !initConditionTrue {
		if k8sutil.IsTimeSincePodLaunchedLaterThan(pod, k8sTimeConfig.PodLaunchThresholdMinutesOnInitFailure) {
			return true, types.ICMSInstanceFailedInitContainerStuck
		}
	}
	return false, types.ICMSInstanceStateNoStatus
}

func (c K8sComputeBackend) GetErroredPodLogs(ctx context.Context, pod *corev1.Pod, prepend string, writeMaxBytes int64) (string, int64, error) {
	log := core.GetLogger(ctx)

	// Image pull issues should be directly reported to users for debugging.
	if imagePullIssues, ok := k8sutil.ImagePullIssuesReported(pod.Status); ok {
		for _, imagePullIssue := range imagePullIssues {
			prepend = fmt.Sprintf("%s (%s)\n", prepend, imagePullIssue.Message)
		}
	}

	totalBytesWritten := int64(0)
	sb := &strings.Builder{}
	sb.WriteString(prepend)
	bytesWritten := int64(len(prepend))
	if len(prepend) > 0 {
		sb.WriteString("\n---\n")
	}
	totalBytesWritten += bytesWritten
	if writeMaxBytes >= bytesWritten {
		writeMaxBytes -= bytesWritten
	} else {
		writeMaxBytes = 0
	}
	for _, cs := range pod.Status.InitContainerStatuses {
		bytesWritten, err := c.writeContainerLogs(ctx, pod, cs, true, writeMaxBytes, sb)
		totalBytesWritten += bytesWritten
		if writeMaxBytes >= bytesWritten {
			writeMaxBytes -= bytesWritten
		} else {
			writeMaxBytes = 0
		}
		if err != nil {
			log.WithError(err).Errorf("Write Pod %s init container %s logs", pod.Name, cs.Name)
			continue
		}
	}
	for _, cs := range pod.Status.ContainerStatuses {
		bytesWritten, err := c.writeContainerLogs(ctx, pod, cs, false, writeMaxBytes, sb)
		totalBytesWritten += bytesWritten
		if writeMaxBytes >= bytesWritten {
			writeMaxBytes -= bytesWritten
		} else {
			writeMaxBytes = 0
		}
		if err != nil {
			log.WithError(err).Errorf("Write Pod %s container %s logs", pod.Name, cs.Name)
			continue
		}
	}

	return sb.String(), totalBytesWritten, nil
}

var (
	infraContainerNames = map[string]bool{
		common.UtilsContainerName:       true,
		function.LLMWorkerContainerName: true,
		"init":                          true,
		"ess":                           true,
		"ess-init":                      true,
	}
	publicStrRE = regexp.MustCompile(`(\\)?"public(\\)?":(\s)?true`)
)

func (c K8sComputeBackend) writeContainerLogs(ctx context.Context,
	pod *corev1.Pod, cs corev1.ContainerStatus, isInit bool,
	writeMaxBytes int64,
	sb *strings.Builder,
) (int64, error) {
	if isInit {
		sb.WriteString("INIT ")
	}
	sb.WriteString("CONTAINER ")
	sb.WriteString(cs.Name)

	switch {
	case cs.State.Running != nil:
		sb.WriteString(" RUNNING")
	case cs.State.Waiting != nil:
		sb.WriteString(" WAITING")
	case cs.State.Terminated != nil:
		sb.WriteString(fmt.Sprintf(" EXITED, CODE=%d", cs.State.Terminated.ExitCode))
	}

	var (
		lr            io.ReadCloser
		errs          []error
		inRestartLoop = cs.RestartCount > 0
	)
	shouldGetLogs := c.bk8s.logPostingEnabled &&
		((cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0) || inRestartLoop)
	if shouldGetLogs && writeMaxBytes > 0 {
		tl := MaxFailedPodLogLines
		var err error
		lr, err = c.clients.K8s.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
			Container:  cs.Name,
			TailLines:  &tl,
			LimitBytes: &writeMaxBytes,
			Previous:   true,
		}).Stream(ctx)
		errs = append(errs, err)
	}

	logBytesWritten := int64(0)
	if shouldGetLogs || lr != nil || inRestartLoop {
		sb.WriteString(":\n")
		if inRestartLoop {
			sb.WriteString("(CONTAINER IN RESTART LOOP)")
		}
		if lr != nil {
			if inRestartLoop {
				sb.WriteByte('\n')
			}
			defer lr.Close()
			if infraContainerNames[cs.Name] {
				n, lerrs := writePublicLogsOnly(sb, lr)
				errs = append(errs, lerrs...)
				logBytesWritten += n
			} else {
				n, err := io.Copy(sb, lr)
				errs = append(errs, err)
				logBytesWritten = n
			}
		} else if shouldGetLogs && writeMaxBytes == 0 {
			if inRestartLoop {
				sb.WriteByte('\n')
			}
			sb.WriteString("<pod log limit reached, see detailed logs>")
		}
	}
	sb.WriteString("\n---\n")

	return logBytesWritten, utilerror.NewAggregate(errs)
}

// Only write public logs from infra containers.
func writePublicLogsOnly(sb *strings.Builder, lr io.Reader) (logBytesWritten int64, errs []error) {
	scanner := bufio.NewScanner(lr)
	for scanner.Scan() {
		line := scanner.Text()
		if publicStrRE.MatchString(line) {
			n, err := sb.WriteString(line)
			errs = append(errs, err)
			logBytesWritten += int64(n)
		}
	}
	return logBytesWritten, errs
}

var badICMSInstanceStates = map[types.ICMSInstanceState]struct{}{
	types.ICMSInstanceFailed:                         {},
	types.ICMSInstanceKilledNoCapacity:               {},
	types.ICMSInstanceKilledAdmissionError:           {},
	types.ICMSInstanceFailedInitContainerStuck:       {},
	types.ICMSInstanceFailedContainerRestartLoop:     {},
	types.ICMSInstanceFailedInitContainerRestartLoop: {},
	types.ICMSInstanceFailedImagePullIssues:          {},
	types.ICMSInstanceFailedCreateContainerError:     {},
	types.ICMSInstanceDegradedWorker:                 {},
}

func (c K8sComputeBackend) GetICMSRequestUpdatesForCreateRequest(ctx context.Context, req *nvcav2beta1.ICMSRequest) []types.ICMSRequestUpdateInfo {
	log := core.GetLogger(ctx)

	var icmsRequestUpdates []types.ICMSRequestUpdateInfo
	for id, st := range req.Status.Instances {
		switch st.Type {
		case nvcav2beta1.InstanceTypePod:
			statusUpdate, err := c.GetICMSRequestUpdatesForCreatePodRequest(ctx, st, req)
			if err != nil {
				log.WithError(err).Warnf("failed to get Pod instance ID %v updates on the backend", id)
				continue
			}
			if !reflect.DeepEqual(statusUpdate, types.ICMSRequestUpdateInfo{}) {
				icmsRequestUpdates = append(icmsRequestUpdates, statusUpdate)
			}
		case nvcav2beta1.InstanceTypeMiniService:
			statusUpdate, err := c.GetICMSRequestUpdatesForMiniServiceRequest(ctx, req, st)
			if err != nil {
				log.WithError(err).Warnf("failed to get MiniService instance ID %v updates", id)
				continue
			}
			if !reflect.DeepEqual(statusUpdate, types.ICMSRequestUpdateInfo{}) {
				icmsRequestUpdates = append(icmsRequestUpdates, statusUpdate)
			}
		}
	}
	log.Debugf("ICMSRequestStatusUpdate: %v updates for CreationRequest", len(icmsRequestUpdates))
	return icmsRequestUpdates
}

func (c K8sComputeBackend) GetICMSRequestUpdatesForCreatePodRequest(ctx context.Context, st nvcav2beta1.InstanceStatus,
	req *nvcav2beta1.ICMSRequest) (types.ICMSRequestUpdateInfo, error) {
	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"instanceID": st.ID,
	})
	metrics := nvcametrics.FromContext(ctx)

	id := st.ID
	rs := reqStatusToState(req.Status.RequestStatus)
	srs := types.ICMSRequestFulfilled
	tc := types.ICMSInstanceStateNoStatus
	fPL := ""
	action := toLegacyAction(req.Spec.Action)
	var instanceIPs []string
	p, err := c.clients.K8s.CoreV1().Pods(c.bk8s.podInstanceNamespace).Get(ctx, id, metav1.GetOptions{})

	// Track K8s API call metrics
	if metrics := nvcametrics.FromContext(ctx); metrics != nil {
		metrics.TrackK8sAPICall("pod", err)
	}

	if err != nil {
		if !apierrors.IsNotFound(err) {
			return types.ICMSRequestUpdateInfo{}, err
		}

		if !isStartupArgsMalformed(req.Status.LastReconcileError) {
			log.WithError(err).Warn("Pod is not running, report it as killed, if not reported before")
		}

		if c.bk8s.shouldReportInstanceStatusHeartbeat(ctx, req, st.ID,
			string(types.ICMSInstanceTerminated), st.LastReportedStatus, st.LastReportedTimestamp) {

			srUpdateInfo := types.ICMSRequestUpdateInfo{
				RequestID:  req.Spec.RequestID,
				InstanceID: id,
				Payload: types.ICMSInstanceStatusUpdateRequest{
					Status:           types.ICMSRequestInstanceTerminatedByService,
					InstanceState:    types.ICMSInstanceTerminated,
					Action:           common.TerminationAction,
					RequestState:     types.ICMSInstanceRequestClosed,
					TerminationCause: types.ICMSInstanceFailedNotFound,
					SystemFailure:    string(types.ICMSInstanceFailedNotFound),
				},
			}

			if isStartupArgsMalformed(req.Status.LastReconcileError) {
				srUpdateInfo.Payload.TerminationCause = types.ICMSInstanceTerminatedTerminalError
				srUpdateInfo.Payload.SystemFailure = string(types.ICMSInstanceTerminatedTerminalError)
				srUpdateInfo.Payload.HealthInfo.ErrorLog = "Container arguments are malformed: " + errMalformedArgsSubstring
			}

			if m := nvcametrics.FromContext(ctx); m != nil {
				m.RecordWorkloadStatus(
					workloadtypes.WorkloadTypeContainer,
					nvcametrics.ActionToWorkloadKind(req.Spec.Action),
					workloadtypes.WorkloadStatusFailure,
					nvcametrics.ICMSInstanceStateToFailureCategory(srUpdateInfo.Payload.TerminationCause),
				)
			}

			return srUpdateInfo, nil
		}
		return types.ICMSRequestUpdateInfo{}, nil
	}

	if p.ObjectMeta.DeletionTimestamp != nil {
		// terminationRequest will report this instance state to ICMS.
		log.WithFields(logrus.Fields{"node-name": p.Spec.NodeName}).
			Warnf("Pod is being terminated, it was previously reported as %v", st.LastReportedStatus)
		return types.ICMSRequestUpdateInfo{}, nil
	}

	needsPurge := false
	var gracePeriodSeconds *int64
	is, dReason := podPhaseToInstanceState(p, c.bk8s.k8sTimeConfig)
	// NVCF-125 if InstanceStatus Failed, we mark the request closed
	_, isBadState := badICMSInstanceStates[is]
	if isBadState && req.Spec.Action != common.RequestICMSInstancesForTask && req.Spec.Action != common.TaskCreationAction {
		if is == types.ICMSInstanceDegradedWorker && !c.bk8s.autoPurgeDegradedWorkers {
			log.Warnf("pod %v is in state %v, reason: %v, skip purge since autoPurgeDegradedWorkers is disabled",
				id, is, dReason)
		} else {
			needsPurge = true
			log.Warnf("pod %v is in state %v, it will be force-purged", id, is)
		}
	} else if req.Spec.Action == common.RequestICMSInstancesForTask || req.Spec.Action == common.TaskCreationAction {
		maxRuntimeDurationStr := req.Spec.CreationMsgInfo.TaskLaunchSpecification.MaxRuntimeDuration
		maxQueuedDurationStr := req.Spec.CreationMsgInfo.TaskLaunchSpecification.MaxQueuedDuration
		// Wait for the utils container to exit, or for a max duration to have passed.
		mrd, err := nvcainternaltranslate.ParseMaxRuntimeDuration(maxRuntimeDurationStr)
		if err != nil {
			log.WithError(err).WithField("maxRuntimeDurationStr", maxRuntimeDurationStr).
				Error("Error parsing max runtime duration for container task")
			return types.ICMSRequestUpdateInfo{}, nvcaerrors.TerminalError(fmt.Errorf("parse max runtime duration: %w", err))
		}
		mqd, err := translateutil.ParseISO8601Duration(maxQueuedDurationStr)
		if err != nil {
			log.WithError(err).WithField("maxQueuedDurationStr", maxQueuedDurationStr).
				Error("Error parsing max queued duration for container task")
			return types.ICMSRequestUpdateInfo{}, nvcaerrors.TerminalError(fmt.Errorf("parse max queued duration: %w", err))
		}
		gracePeriodSeconds, needsPurge = c.reconcileContainerTaskPodState(ctx, p, is, mqd, mrd, time.Now())
	}

	var errSource string
	if needsPurge {
		srs = types.ICMSRequestInstanceTerminatedByService
		tc = is
		is = types.ICMSInstanceTerminated
		action = common.TerminationAction
		rs = types.ICMSInstanceRequestClosed
		var prependLog string
		if (req.Spec.Action == common.RequestICMSInstancesForTask || req.Spec.Action == common.TaskCreationAction) && !isBadState {
			prependLog = fmt.Sprintf("pod terminated while in state %s due to unexpected task failure, check task logs for more information", is)
		} else {
			if dReason != "" {
				prependLog = fmt.Sprintf("pod terminated due to state %s: %s", tc, dReason)
			} else {
				prependLog = fmt.Sprintf("pod terminated due to state %s", tc)
			}
		}
		// For CreateContainerError, fetch the actual K8s event message(s) for HealthInfo.ErrorLog.
		if tc == types.ICMSInstanceFailedCreateContainerError {
			eventLog, eventErr := getCreateContainerErrorEventLog(ctx, c.clients, c.bk8s.podInstanceNamespace, id)
			if eventErr != nil {
				log.WithError(eventErr).WithField("pod", id).Debug("Failed to get CreateContainerError events, using container status message")
			}
			if eventLog != "" {
				fPL = prependLog + "\n---\nKubernetes events:\n" + eventLog
			}
		}
		if fPL == "" {
			if fPL, _, err = c.GetErroredPodLogs(ctx, p, prependLog, MaxBytesForPodLogs); err != nil {
				log.WithError(err).WithField("pod", id).Error("Failed to get failed pod logs")
			}
		} else {
			// Append container logs when we already have event log
			podLogs, _, _ := c.GetErroredPodLogs(ctx, p, "", MaxBytesForPodLogs)
			if podLogs != "" {
				fPL += "\n---\nContainer/init logs:\n" + podLogs
			}
		}
		err := c.clients.K8s.CoreV1().Pods(c.bk8s.podInstanceNamespace).Delete(ctx, id, metav1.DeleteOptions{
			GracePeriodSeconds: gracePeriodSeconds,
		})
		if err != nil && !apierrors.IsNotFound(err) {
			log.WithError(err).WithField("pod", id).
				Errorf("failed to delete pod stuck due to %v, status will still be reported to ICMS", tc)
		}

		// Only set error source if terminating a task.
		if req.Spec.TaskDetails.TaskID != "" {
			errSource = types.ErrorSourceTaskContainer
		}
	}

	if c.bk8s.lowLatencyStreamingEnabled {
		instanceIPs = []string{p.Status.HostIP, p.Status.PodIP}
	}

	if c.bk8s.shouldReportInstanceStatusHeartbeat(ctx, req, st.ID,
		string(is), st.LastReportedStatus, st.LastReportedTimestamp) {

		// Only increment metric once.
		if imagePullIssues, ok := k8sutil.ImagePullIssuesReported(p.Status); ok {
			for _, imagePullIssue := range imagePullIssues {
				reg := k8sutil.ParseImageRegistry(imagePullIssue.Image)
				metrics.ImagePullIssueTotal.WithLabelValues(metrics.WithDefaultLabelValues(reg)...).Inc()
			}
		}

		// Record workload result metric on terminal state transitions.
		if m := nvcametrics.FromContext(ctx); m != nil {
			if needsPurge {
				m.RecordWorkloadStatus(
					workloadtypes.WorkloadTypeContainer,
					nvcametrics.ActionToWorkloadKind(req.Spec.Action),
					workloadtypes.WorkloadStatusFailure,
					nvcametrics.ICMSInstanceStateToFailureCategory(tc),
				)
			} else if is == types.ICMSInstanceRunning && st.LastReportedStatus != string(types.ICMSInstanceRunning) {
				m.RecordWorkloadStatus(
					workloadtypes.WorkloadTypeContainer,
					nvcametrics.ActionToWorkloadKind(req.Spec.Action),
					workloadtypes.WorkloadStatusSuccess,
					workloadtypes.FailureCategoryNone,
				)
			}
		}

		return types.ICMSRequestUpdateInfo{
			RequestID:  req.Spec.RequestID,
			InstanceID: id,
			Payload: types.ICMSInstanceStatusUpdateRequest{
				Status:           srs,
				InstanceState:    is,
				Action:           toLegacyAction(action),
				RequestState:     rs,
				TerminationCause: tc,
				HealthInfo: types.HealthInfo{
					ErrorLog:    fPL,
					ErrorSource: errSource,
				},
				SystemFailure: string(tc),
				InstanceIPs:   instanceIPs,
			},
		}, nil
	}

	return types.ICMSRequestUpdateInfo{}, nil
}

// errMalformedArgsSubstring is the error substring produced by shlex (via nvcf-icms-translate)
// when container args contain unmatched quotes.
const errMalformedArgsSubstring = "EOF found when expecting closing quote"

func isStartupArgsMalformed(lastReconcileErr string) bool {
	return strings.Contains(lastReconcileErr, errMalformedArgsSubstring)
}

func IsPodAdmissionRejected(ps corev1.PodStatus) bool {
	return strings.EqualFold(ps.Reason, UnexpectedAdmissionErrReason)
}

// createContainerErrorReasons are K8s event reasons for container create failures.
var createContainerErrorReasons = []string{"CreateContainerError", "CreateContainerConfigError"}

func podHasCreateContainerError(pod *corev1.Pod) (msg string, ok bool) {
	if pod == nil {
		return "", false
	}
	for _, cs := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
		if cs.State.Waiting == nil {
			continue
		}
		// CreateContainerError is set when a container fails to be created on the node.
		// CreateContainerConfigError is similar but indicates a configuration issue.
		// Both are terminal for the current pod spec and should trigger instance termination.
		if cs.State.Waiting.Reason == "CreateContainerError" || cs.State.Waiting.Reason == "CreateContainerConfigError" {
			return cs.State.Waiting.Message, true
		}
	}
	return "", false
}

// getCreateContainerErrorEventLog fetches K8s events for the pod whose Reason is
// CreateContainerError or CreateContainerConfigError and returns their messages
// for use in HealthInfo.ErrorLog. Events are returned newest first; multiple
// messages are joined with newlines.
func getCreateContainerErrorEventLog(ctx context.Context, clients *kubeclients.KubeClients, namespace, podName string) (string, error) {
	fieldSelector := fmt.Sprintf("involvedObject.kind=Pod,involvedObject.name=%s,involvedObject.namespace=%s",
		podName, namespace)
	list, err := clients.K8s.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: fieldSelector})
	if err != nil {
		return "", err
	}
	var lines []string
	for i := len(list.Items) - 1; i >= 0; i-- {
		ev := &list.Items[i]
		for _, r := range createContainerErrorReasons {
			if ev.Reason == r {
				msg := strings.TrimSpace(ev.Message)
				if msg != "" {
					lines = append(lines, fmt.Sprintf("[%s] %s", ev.Reason, msg))
				}
				break
			}
		}
	}
	return strings.Join(lines, "\n"), nil
}

func podPhaseToInstanceState(pod *corev1.Pod, k8sTimeConfig *k8sutil.TimeConfig) (types.ICMSInstanceState, string) {
	switch ps := pod.Status; ps.Phase {
	case corev1.PodPending, corev1.PodUnknown:
		if nvcak8sutil.IsPodScheduled(ps) {
			if msg, ok := podHasCreateContainerError(pod); ok {
				return types.ICMSInstanceFailedCreateContainerError, msg
			}
			if k8sutil.IsTimeSincePodLaunchedLaterThan(pod, k8sTimeConfig.MaxImagePullErrorThreshold) {
				if _, ok := nvcak8sutil.ImagePullIssuesReported(ps); ok {
					return types.ICMSInstanceFailedImagePullIssues, ""
				}
			}
			stuck, reason := IsPodStuckInitializing(pod, k8sTimeConfig)
			if stuck {
				return reason, ""
			}
			if k8sutil.IsTimeSincePodLaunchedLaterThan(pod, k8sTimeConfig.PodLaunchThresholdMinutesOnInitFailure) {
				return types.ICMSInstanceFailedInitContainerStuck, ""
			}
			return types.ICMSInstanceStarted, ""
		}
		// Pod is getting stuck for > 10mins and not getting scheduled it will be killed
		if k8sutil.IsTimeSincePodLaunchedLaterThan(pod, k8sTimeConfig.PodScheduledThreshold) {
			return types.ICMSInstanceKilledNoCapacity, ""
		}
		return types.ICMSInstanceStarted, ""
	case corev1.PodRunning:
		// NVCF-125 Check PodReady & Not in Stuck Initializing state
		if nvcak8sutil.IsPodReady(ps) {
			return types.ICMSInstanceRunning, ""
		}
		if msg, ok := podHasCreateContainerError(pod); ok {
			return types.ICMSInstanceFailedCreateContainerError, msg
		}
		stuck, reason := IsPodStuckInitializing(pod, k8sTimeConfig)
		if stuck {
			return reason, ""
		}
		degradedStatus, degraded := nvcak8sutil.IsPodDegraded(pod, k8sTimeConfig)
		if degraded {
			return types.ICMSInstanceDegradedWorker, degradedStatus.Reason
		}
		return types.ICMSInstanceStarted, ""
	case corev1.PodSucceeded:
		// Report Succeeded as Running
		return types.ICMSInstanceRunning, ""
	case corev1.PodFailed:
		// Pod is getting rejected for admission it will be killed
		if IsPodAdmissionRejected(ps) {
			return types.ICMSInstanceKilledAdmissionError, ""
		}
		return types.ICMSInstanceFailed, ""
	}
	return types.ICMSInstanceStarted, ""
}

func reqStatusToState(reqStatus nvcav2beta1.RequestStatus) types.ICMSInstanceRequestState {
	switch reqStatus {
	case nvcav2beta1.ICMSRequestStatusFailed:
		return types.ICMSInstanceRequestClosed
	case nvcav2beta1.ICMSRequestStatusCompleted, nvcav2beta1.ICMSRequestStatusPending, nvcav2beta1.ICMSRequestStatusInProgress:
		return types.ICMSInstanceRequestActive
	default:
		return types.ICMSInstanceRequestActive
	}
}

func (c K8sComputeBackend) GetICMSRequestUpdatesForTerminationRequest(ctx context.Context, req *nvcav2beta1.ICMSRequest) []types.ICMSRequestUpdateInfo {
	log := core.GetLogger(ctx)
	var icmsRequestUpdates []types.ICMSRequestUpdateInfo

	// Check if this is an eviction maintenance termination request
	isEvictionMaintenance := false
	if req.Labels != nil {
		if msgID, exists := req.Labels[SQSMessageIDKey]; exists && strings.HasPrefix(msgID, "evict-maint-") {
			isEvictionMaintenance = true
		}
	}

	for id, st := range req.Status.Instances {
		if !strings.EqualFold(string(types.ICMSInstanceTerminated), st.LastReportedStatus) {
			updateInfo := types.ICMSRequestUpdateInfo{
				RequestID:  req.Spec.RequestID,
				InstanceID: id,
				Payload: types.ICMSInstanceStatusUpdateRequest{
					Status:        types.ICMSRequestInstanceTerminatedByUser,
					InstanceState: types.ICMSInstanceTerminated,
					Action:        toLegacyAction(req.Spec.Action),
					RequestState:  types.ICMSInstanceRequestClosed,
				},
			}

			// Set maintenance termination cause for eviction maintenance requests
			if isEvictionMaintenance {
				updateInfo.Payload.Status = types.ICMSRequestInstanceTerminatedByService
				updateInfo.Payload.TerminationCause = types.ICMSInstanceTerminatedServiceMaintenance
				updateInfo.Payload.SystemFailure = string(types.ICMSInstanceTerminatedServiceMaintenance)
			}

			icmsRequestUpdates = append(icmsRequestUpdates, updateInfo)
		}
	}
	log.Debugf("ICMSRequestStatusUpdate: %v updates for TerminationRequest", len(icmsRequestUpdates))
	return icmsRequestUpdates
}

func (c K8sComputeBackend) GetICMSRequestStatusUpdatesForRequest(ctx context.Context,
	req *nvcav2beta1.ICMSRequest) ([]types.ICMSRequestUpdateInfo, error) {
	log := core.GetLogger(ctx)
	var icmsRequestUpdates []types.ICMSRequestUpdateInfo

	switch req.Spec.Action {
	case common.FunctionCreationAction, common.TaskCreationAction:
		icmsRequestUpdates = append(icmsRequestUpdates, c.GetICMSRequestUpdatesForCreateRequest(ctx, req)...)
	case common.TerminationAction:
		icmsRequestUpdates = append(icmsRequestUpdates, c.GetICMSRequestUpdatesForTerminationRequest(ctx, req)...)
	}

	log.Debugf("ICMS request status update: %v updates will be posted to ICMS", len(icmsRequestUpdates))
	return icmsRequestUpdates, nil
}

func (c K8sComputeBackend) AggregateInstanceStatuses(ctx context.Context, req *nvcav2beta1.ICMSRequest) AggregatedInstanceStatus {
	log := core.GetLogger(ctx)
	var aggStatuses []AggregatedInstanceStatus
	for id, inst := range req.Status.Instances {
		switch inst.Type {
		case nvcav2beta1.InstanceTypePod:
			aggStatuses = append(aggStatuses, c.AggregatePodInstanceStatus(ctx, req, id))
		case nvcav2beta1.InstanceTypeMiniService:
			ms := &v1alpha1.MiniService{}
			if err := c.clients.HelmV2.Get(ctx, client.ObjectKey{Name: id}, ms); err != nil {
				if apierrors.IsNotFound(err) {
					log.Warnf("Instance %v not found", id)
				} else {
					log.WithError(err).Errorf("Failed to get activeInstance %v", id)
				}
				aggStatuses = append(aggStatuses, AggregatedInstanceStatusUnknown)
				continue
			}

			switch ms.Status.Phase {
			case "":
				aggStatuses = append(aggStatuses, AggregatedInstanceStatusUnknown)
			case v1alpha1.MiniServiceCacheInProgress:
				aggStatuses = append(aggStatuses, AggregatedInstanceStatusModelCachingInProgress)
			case v1alpha1.MiniServiceInstalling, v1alpha1.MiniServiceInstalled:
				aggStatuses = append(aggStatuses, AggregatedInstanceStatusScheduling)
			case v1alpha1.MiniServiceStarting:
				aggStatuses = append(aggStatuses, AggregatedInstanceStatusPending)
			case v1alpha1.MiniServiceRunning, v1alpha1.MiniServiceCompleted:
				aggStatuses = append(aggStatuses, AggregatedInstanceStatusSucceeded)
			case v1alpha1.MiniServiceInstallFailed, v1alpha1.MiniServiceFailed:
				aggStatuses = append(aggStatuses, AggregatedInstanceStatusFailed)
			}

			// Verify all storage requests that exist
			aggStatuses = append(aggStatuses, getStorageRequestsAggregatedInstanceStatus(ctx,
				func() ([]*nvcav2beta1.StorageRequest, error) {
					return c.bk8s.storageRequestLister.StorageRequests(ms.Spec.Namespace).List(labels.Everything())
				}),
			)
		default:
			log.Warnf("Unknown instance type %s for id %s", inst.Type, id)
		}
	}

	var anyInstPending, anyInstScheduling, anyInstUnknown, anyInstModelCaching bool
	for _, status := range aggStatuses {
		switch status {
		case AggregatedInstanceStatusFailed:
			// If any instances are failing, then not all are successful.
			log.Debug("At least one instance for Request is Failed")
			return status
		case AggregatedInstanceStatusModelCachingInProgress:
			anyInstModelCaching = true
		case AggregatedInstanceStatusScheduling:
			anyInstScheduling = true
		case AggregatedInstanceStatusPending:
			anyInstPending = true
		case AggregatedInstanceStatusSucceeded:
		default:
			anyInstUnknown = true
		}
	}

	switch {
	case anyInstModelCaching:
		log.Debug("At least one instance for Request is ModelCaching")
		return AggregatedInstanceStatusModelCachingInProgress
	case anyInstUnknown:
		log.Debug("At least one instance for Request is Unknown")
		return AggregatedInstanceStatusUnknown
	case anyInstScheduling:
		log.Debug("At least one instance for Request is Scheduling")
		return AggregatedInstanceStatusScheduling
	case anyInstPending:
		log.Debug("At least one instance for Request is Pending")
		return AggregatedInstanceStatusPending
	}

	log.Debug("All instances for Request are in Running / Completed State")
	return AggregatedInstanceStatusSucceeded
}

func getStorageRequestsAggregatedInstanceStatus(
	ctx context.Context,
	storageReqLister func() ([]*nvcav2beta1.StorageRequest, error),
) AggregatedInstanceStatus {
	log := core.GetLogger(ctx)
	storageRequests, err := storageReqLister()
	if err != nil {
		log.WithError(err).Error("failed to list StorageRequest resources")
		return AggregatedInstanceStatusUnknown
	}
	anyPending := false
	for _, req := range storageRequests {
		// Model cache requests are optional so terminal states mean ready.
		if req.Spec.Type == nvcav2beta1.ModelCacheRequest {
			switch req.Status.Phase {
			case nvcav2beta1.StorageFailed, nvcav2beta1.StorageRuntimeError:
				log.Error("Modelcache failed, continuing with deployment")
			case nvcav2beta1.StorageReady:
			default:
				anyPending = true
			}
		} else {
			switch req.Status.Phase {
			case nvcav2beta1.StorageFailed, nvcav2beta1.StorageRuntimeError:
				return AggregatedInstanceStatusFailed
			case nvcav2beta1.StorageReady:
			default:
				anyPending = true
			}
		}
	}
	if anyPending {
		return AggregatedInstanceStatusPending
	}
	// If this is empty or all succeeded
	return AggregatedInstanceStatusSucceeded
}

func (c K8sComputeBackend) AggregatePodInstanceStatus(ctx context.Context, req *nvcav2beta1.ICMSRequest, instanceID string) AggregatedInstanceStatus {
	log := core.GetLogger(ctx)
	p, err := c.bk8s.podSpecLister.Get(instanceID)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Warnf("Instance %v not found", instanceID)
		} else {
			log.WithError(err).Errorf("Failed to get activeInstance %v", instanceID)
		}
		return AggregatedInstanceStatusUnknown
	}
	return podPhaseToAggregatedInstanceStatus(p, c.bk8s.k8sTimeConfig, getPodSchedulingTimeout(ctx, req, c.bk8s.k8sTimeConfig))
}

// Tasks can remain enqueued up to their max queued duration, so a non-default value must be used
// to check for a scheduling timeout.
func getPodSchedulingTimeout(ctx context.Context, req *nvcav2beta1.ICMSRequest, k8sTimeConfig *k8sutil.TimeConfig) time.Duration {
	schedulingTimeout := k8sTimeConfig.PodScheduledThreshold
	if req.Spec.Action == common.RequestICMSInstancesForTask || req.Spec.Action == common.TaskCreationAction {
		mqd, err := translateutil.ParseISO8601Duration(req.Spec.CreationMsgInfo.TaskLaunchSpecification.MaxQueuedDuration)
		if err != nil {
			core.GetLogger(ctx).WithError(err).Error("Failed to parse max queued duration for task Pod")
		} else {
			schedulingTimeout = mqd
		}
	}
	return schedulingTimeout
}

func podPhaseToAggregatedInstanceStatus(p *corev1.Pod, k8sTimeConfig *k8sutil.TimeConfig, schedulingTimeout time.Duration) AggregatedInstanceStatus {
	ps := p.Status
	switch ps.Phase {
	case corev1.PodRunning:
		// NVCF-125 Check PodReady & Not in Stuck Initializing state
		if !nvcak8sutil.IsPodReady(ps) {
			if stuck, _ := IsPodStuckInitializing(p, k8sTimeConfig); stuck {
				return AggregatedInstanceStatusFailed
			}
			if _, degraded := nvcak8sutil.IsPodDegraded(p, k8sTimeConfig); degraded {
				return AggregatedInstanceStatusFailed
			}
			return AggregatedInstanceStatusPending
		}
		return AggregatedInstanceStatusSucceeded
	case corev1.PodSucceeded:
		return AggregatedInstanceStatusSucceeded
	case corev1.PodFailed:
		// Pod is getting rejected for admission it will be killed
		if IsPodAdmissionRejected(ps) {
			return AggregatedInstanceStatusFailed
		}
		// This means the Pod was able to be bound to a Node,
		// which is what matters for this function.
		return AggregatedInstanceStatusSucceeded
	default:
		if nvcak8sutil.IsPodScheduled(ps) {
			overImagePullTimeout := k8sutil.IsTimeSincePodLaunchedLaterThan(p, k8sTimeConfig.MaxImagePullErrorThreshold)
			if _, ok := nvcak8sutil.ImagePullIssuesReported(ps); overImagePullTimeout && ok {
				return AggregatedInstanceStatusFailed
			}
			if stuck, _ := IsPodStuckInitializing(p, k8sTimeConfig); stuck {
				return AggregatedInstanceStatusFailed
			}
			if k8sutil.IsTimeSincePodLaunchedLaterThan(p, k8sTimeConfig.PodLaunchThresholdMinutesOnInitFailure) {
				return AggregatedInstanceStatusFailed
			}
			return AggregatedInstanceStatusPending
		}
		// Pod is getting stuck scheduling, and should be killed
		if k8sutil.IsTimeSincePodLaunchedLaterThan(p, schedulingTimeout) {
			return AggregatedInstanceStatusFailed
		}
		return AggregatedInstanceStatusScheduling
	}
}

func addEnvsToUtilsContainers(pod *corev1.Pod, envs ...corev1.EnvVar) {
	for i, container := range pod.Spec.Containers {
		if container.Name == common.UtilsContainerName || container.Name == function.LLMWorkerContainerName {
			k8sutil.AddEnvsToContainer(&pod.Spec.Containers[i], envs...)
		}
	}
}

func getOwnerRefForRequest(req *nvcav2beta1.ICMSRequest) []metav1.OwnerReference {
	return []metav1.OwnerReference{
		{
			APIVersion: nvcav2beta1.SchemeGroupVersion.Identifier(),
			Kind:       "ICMSRequest",
			Name:       req.Name,
			UID:        req.UID,
		},
	}
}

type mutateFunc func(client.Object)

func (c K8sComputeBackend) CreateSecretArtifact(ctx context.Context, a function.LaunchArtifact, mf mutateFunc) error {
	obj, err := nvcak8sutil.GetObjectFromEncodedString(a.Specification, reflect.TypeOf(&corev1.Secret{}))
	if err != nil {
		return fmt.Errorf("failed to get secret from artifact spec, err: %v", err)
	}
	secret := obj.(*corev1.Secret)

	mf(secret)

	_, err = c.clients.K8s.CoreV1().Secrets(secret.Namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create Secret %v/%v, err: %v", secret.Namespace, secret.Name, err)
	}

	return nil
}

func (c K8sComputeBackend) CreateConfigMapArtifact(ctx context.Context, a function.LaunchArtifact, mf mutateFunc) error {
	obj, err := nvcak8sutil.GetObjectFromEncodedString(a.Specification, reflect.TypeOf(&corev1.ConfigMap{}))
	if err != nil {
		return fmt.Errorf("failed to get configmap from artifact spec, err: %v", err)
	}
	cm := obj.(*corev1.ConfigMap)

	mf(cm)

	_, err = c.clients.K8s.CoreV1().ConfigMaps(cm.Namespace).Create(ctx, cm, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create ConfigMap %v/%v, err: %v", cm.Namespace, cm.Name, err)
	}

	return nil
}

func (c K8sComputeBackend) CreateServiceArtifact(ctx context.Context, a function.LaunchArtifact, mf mutateFunc) error {
	obj, err := nvcak8sutil.GetObjectFromEncodedString(a.Specification, reflect.TypeOf(&corev1.Service{}))
	if err != nil {
		return fmt.Errorf("failed to get Service from artifact spec, err: %v", err)
	}
	cm := obj.(*corev1.Service)

	mf(cm)

	_, err = c.clients.K8s.CoreV1().Services(cm.Namespace).Create(ctx, cm, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create Service %v/%v, err: %v", cm.Namespace, cm.Name, err)
	}

	return nil
}

func containerHasEnv(c *corev1.Container, key, value string) bool {
	for i := range c.Env {
		if c.Env[i].Name == key {
			c.Env[i].Value = value
			return true
		}
	}
	return false
}

func setTerminationGracePeriodIfNotSet(p *corev1.Pod) {
	defTGP := int64(DefaultTerminationGracePeriodSeconds)
	if p.Spec.TerminationGracePeriodSeconds == nil {
		p.Spec.TerminationGracePeriodSeconds = &defTGP
	}
}

func setEnvInPod(pod *corev1.Pod, envMap map[string]string) *corev1.Pod {
	for key, value := range envMap {
		// set env on all initContainers
		for i := range pod.Spec.InitContainers {
			if !containerHasEnv(&pod.Spec.InitContainers[i], key, value) {
				pod.Spec.InitContainers[i].Env = append(pod.Spec.InitContainers[i].Env, corev1.EnvVar{Name: key, Value: value})
			}
		}

		// set env on all containers
		for i := range pod.Spec.Containers {
			if !containerHasEnv(&pod.Spec.Containers[i], key, value) {
				pod.Spec.Containers[i].Env = append(pod.Spec.Containers[i].Env, corev1.EnvVar{Name: key, Value: value})
			}
		}
	}
	return pod
}

func (c K8sComputeBackend) AllInstancesTerminatedAndReported(ctx context.Context, req *nvcav2beta1.ICMSRequest) bool {
	log := core.GetLogger(ctx)
	if len(req.Status.Instances) == 0 {
		// request failed on ACK and hence no instances created, so run cleanup,
		// otherwise request is just created, retain
		if req.Status.RequestStatus == nvcav2beta1.ICMSRequestStatusFailureAcknowledged {
			return true
		} else {
			// allow cleanup if status stuck in Pending
			// for more than FailedRequestCleanupWindow
			t := req.Status.LastStatusUpdated
			cw := FailedRequestCleanupWindow
			if req.Status.RequestStatus == nvcav2beta1.ICMSRequestStatusPending &&
				t != nil && time.Since(t.Time) > cw {
				return true
			}
		}
		return false
	}
	for _, inst := range req.Status.Instances {
		switch inst.Type {
		case nvcav2beta1.InstanceTypePod:
			_, err := c.bk8s.podSpecLister.Get(inst.ID)
			if err == nil || !apierrors.IsNotFound(err) {
				// if pod is found or any other error, consider not terminated
				return false
			}
			log.Debugf("Pod %s not running", inst.ID)
		case nvcav2beta1.InstanceTypeMiniService:
			msKey := client.ObjectKey{Name: inst.ID}
			err := c.clients.HelmV2.Get(ctx, msKey, &v1alpha1.MiniService{})
			if err == nil || !apierrors.IsNotFound(err) {
				return false
			}
			log.Debugf("Miniservice %s does not exist", inst.ID)
		}
		// if the termination was reported to ICMS
		if inst.LastReportedStatus != string(types.ICMSInstanceTerminated) {
			return false
		}
	}

	// TODO: Add event for all instances terminated when we transition to controller-runtime
	// presently this log spams, and is unhelpful
	// c.bk8s.eventRecorder.Event(req, v1.EventTypeNormal,
	// 	string(types.EventCategoryInstanceTermination), "All instances terminated, request will be cleaned-up")
	log.Debugf("All instances are terminated, request %s will be cleaned-up", req.Name)

	return true
}

// cleanupNamespace deletes a namespace if it is considered terminated.
// It returns true if the namespace was deleted, false otherwise.
func cleanupNamespace(ctx context.Context,
	getNamespace func(name string) (*corev1.Namespace, error),
	deleteNamespace func(ctx context.Context, name string, options metav1.DeleteOptions) error,
	namespace string,
) (bool, error) {
	log := core.GetLogger(ctx)
	ns, err := getNamespace(namespace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Debugf("Namespace %s not found, reporting as cleaned up", namespace)
			return false, nil
		}
		return false, fmt.Errorf("failed to get namespace %s, err: %w", namespace, err)
	}
	if ns != nil && ns.DeletionTimestamp == nil {
		err := deleteNamespace(ctx, namespace, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			log.WithError(err).Errorf("failed to delete namespace %s, err: %v", namespace, err)
			return false, fmt.Errorf("failed to delete namespace %s, err: %w", namespace, err)
		}
		return true, nil
	}
	return false, nil
}

// if namespace is found and not stuck terminating, or any other error is encountered,
// consider not terminated
func isNamespaceConsideredTerminated(
	ctx context.Context,
	getNamespace func(name string) (*corev1.Namespace, error),
	namespace string,
	k8sTimeConfig *k8sutil.TimeConfig,
) bool {
	log := core.GetLogger(ctx)
	ns, err := getNamespace(namespace)
	if apierrors.IsNotFound(err) {
		return true
	}
	if ns != nil && ns.DeletionTimestamp != nil {
		if reasons, isStuck := k8sutil.IsNamespaceStuckTerminating(ns, k8sTimeConfig); isStuck {
			log.WithField("reasons", reasons).Debug("Helm chart namespace is stuck terminating, reporting as cleaned up")
			return true
		}
	}
	return false
}

func (c K8sComputeBackend) updateServiceAccountImagePullSecrets(
	ctx context.Context,
	namespace, saName string,
	imagePullSecrets []*corev1.Secret,
) error {
	log := core.GetLogger(ctx).WithFields(logrus.Fields{"sa": saName, "namespace": namespace})

	updated, err := k8sutil.UpdateServiceAccountImagePullSecrets(ctx, c.clients, nil, namespace, saName, imagePullSecrets)
	if err != nil {
		log.WithError(err).Error("Failed to update instance service account with image pull secrets")
		return err
	}

	if updated {
		log.Info("Updated service account with image pull secrets")
	}

	return nil
}

func (c K8sComputeBackend) ensureNetworkPolicies(ctx context.Context, namespace string) error {
	log := core.GetLogger(ctx)

	npCM, err := c.clients.K8s.CoreV1().ConfigMaps(c.bk8s.systemNamespace).
		Get(ctx, k8sutil.NetworkPoliciesConfigMapName, metav1.GetOptions{})
	if err != nil {
		log.WithError(err).Errorf("Get NetworkPolicy ConfigMap %s in namespace %s",
			k8sutil.NetworkPoliciesConfigMapName, c.bk8s.systemNamespace)
		return err
	}
	return c.ensureNetworkPoliciesFromConfigMap(ctx, namespace, npCM)
}

func (c K8sComputeBackend) ensureNetworkPoliciesFromConfigMap(
	ctx context.Context,
	namespace string,
	npCM *corev1.ConfigMap,
) error {
	if namespace == c.bk8s.podInstanceNamespace {
		return k8sutil.EnsureNetworkPoliciesSharedPodInstanceNamespace(ctx,
			namespace,
			npCM.Data,
			c.bk8s.featureFlagFetcher,
			c.clients.K8s,
			nil,
		)
	} else {
		var extraNPs []*netv1.NetworkPolicy
		if c.bk8s.helmSharedStorageEnabled {
			extraNPs = append(extraNPs, nvcastorage.GetIngressNetworkPolicies()...)
		}
		return k8sutil.EnsureNetworkPoliciesFunctionNamespace(ctx,
			namespace,
			npCM.Data,
			c.bk8s.featureFlagFetcher,
			c.clients.K8s,
			nil,
			extraNPs...,
		)
	}
}

func (c K8sComputeBackend) ensureHelmResourceConstraints(ctx context.Context, namespace string, req *nvcav2beta1.ICMSRequest) error {
	// Overhead is added to reqs/lims to account for other containers/custom infra appended to Pods in the namespace.
	overhead, err := c.bk8s.infraOverheadGetter.GetInfraOverhead(ctx)
	if err != nil {
		return err
	}

	cmInfo := req.Spec.CreationMsgInfo
	computeReqs, computeLims, err := c.calculatePodInstanceResourcesForInstanceType(cmInfo, overhead)
	if err != nil {
		return fmt.Errorf("calculate resource constraints: %v", err)
	}

	return k8sutil.EnsureResourceQuotas(ctx,
		c.bk8s.featureFlagFetcher,
		k8sutil.NewK8sClientShim(c.clients.K8s, &corev1.ResourceQuota{}),
		req.Spec.Action,
		namespace,
		computeReqs, computeLims,
	)
}

func (c K8sComputeBackend) HandleInstanceStatusPreconditionFailure(ctx context.Context, req *nvcav2beta1.ICMSRequest, instID string) error {
	log := core.GetLogger(ctx)
	instances := req.Status.Instances
	if c.purgeInstanceID(ctx, req, instances, instID) {
		log.Debugf("instance %v purged due to HandleInstanceStatusPreconditionFailure", instID)
		modify := func(_ context.Context, sr *nvcav2beta1.ICMSRequest) {
			sr.Status.Instances = req.Status.Instances
		}
		if !c.bk8s.applyICMSRequestStatusChange(ctx, req, modify) {
			return fmt.Errorf("failed to update status for Request %v/%v", req.Namespace, req.Name)
		}
	}
	return nil
}
