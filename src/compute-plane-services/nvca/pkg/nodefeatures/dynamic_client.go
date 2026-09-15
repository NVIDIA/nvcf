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

package nodefeatures

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	k8sinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/informers/internalinterfaces"
	listersv1 "k8s.io/client-go/listers/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures/sharedcluster"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// TODO: enforce nvidia.com/{p}gpu request on Pods
// https://github.com/NVIDIA/k8s-device-plugin/tree/226dc5d7f3dbdc2522ad9bc0aeedeef753d4aea6?tab=readme-ov-file#running-gpu-jobs
const (
	GPUResourceKey       = "nvidia.com/gpu"
	PGPUResourceKey      = "nvidia.com/pgpu"
	GPUSharedResourceKey = "nvidia.com/gpu.shared"
)

// GPUResourceNames contains all GPU resource names for detecting GPU workloads.
var GPUResourceNames = []v1.ResourceName{
	v1.ResourceName(GPUResourceKey),
	v1.ResourceName(GPUSharedResourceKey),
	v1.ResourceName(PGPUResourceKey),
}

type dynamicClient struct {
	DynamicClientOptions

	// Retrieves GPU allocation.
	gpuAllocGetter GPUAllocationGetter
	// nodeLister is backed by a cache so computed backend GPU data
	// does not need to be explicitly cached.
	nodeLister listersv1.NodeLister
	// Used to construct instance-type label when not present on a node.
	clusterProvider string
	// A node may be marked as vm-passthrough, but the cluster may not be configured
	// to run in kata isolation mode.
	isKataEnabled bool
	// Used to determine if GPU passthrough is enabled in the cluster.
	isPassthroughGPUEnabled bool
}

type DynamicClientOptions struct {
	// By default, only one GPU type is allowed.
	MultipleGPUTypesAllowed bool
	// By default, use the 'node.kubernetes.io/instance-type' label AS IS
	// if enabled, have uniform labels across all clusters of a CSP
	UniformInstanceLabels bool
	// When true, operate in shared-cluster mode.
	SharedClusterOn *atomic.Bool

	AttributeFetcher featureflag.AttributeFetcher
}

func NewDynamicClient(
	ag GPUAllocationGetter,
	nodeLister listersv1.NodeLister,
	clusterProvider string,
	opts DynamicClientOptions,
) Client {
	if opts.AttributeFetcher == nil {
		opts.AttributeFetcher = featureflag.DefaultFetcher
	}
	c := &dynamicClient{
		DynamicClientOptions:    opts,
		gpuAllocGetter:          ag,
		nodeLister:              nodeLister,
		clusterProvider:         clusterProvider,
		isKataEnabled:           opts.AttributeFetcher.IsAttributeEnabled(featureflag.AttrKataRuntimeIsolation),
		isPassthroughGPUEnabled: opts.AttributeFetcher.IsAttributeEnabled(featureflag.AttrPassthroughGPUEnabled),
	}
	return c
}

const (
	// Kata runtime isolation configuration on nodes.
	gpuWorkloadConfigLabel = "nvidia.com/gpu.workload.config"
	vmPassthroughVal       = "vm-passthrough"
	trueVal                = "true"
)

var (
	reqGPUPresent, reqVMPassthrough labels.Requirement
)

func init() {
	reqGPUPresent = mustNewReq(gpuPresentLabelKey, selection.Equals, []string{trueVal})
	reqVMPassthrough = mustNewReq(gpuWorkloadConfigLabel, selection.Equals, []string{"vm-passthrough"})
}

func mustNewReq(label string, op selection.Operator, vals []string) labels.Requirement {
	req, err := labels.NewRequirement(label, op, vals)
	if err != nil {
		panic(err)
	}
	return *req
}

func NewNodeInformerOptions(attrFetcher featureflag.AttributeFetcher) []k8sinformers.SharedInformerOption {
	if attrFetcher == nil {
		attrFetcher = featureflag.DefaultFetcher
	}
	var reqs labels.Requirements

	// Either all nodes with GPUs or only those with the available key should be parsed.
	reqs = append(reqs, reqGPUPresent)

	if attrFetcher.IsAttributeEnabled(featureflag.AttrKataRuntimeIsolation) ||
		attrFetcher.IsAttributeEnabled(featureflag.AttrPassthroughGPUEnabled) {
		reqs = append(reqs, reqVMPassthrough)
	}

	selStr := labels.NewSelector().Add(reqs...).String()
	tlof := internalinterfaces.TweakListOptionsFunc(func(lo *metav1.ListOptions) {
		lo.LabelSelector = selStr
	})

	return []k8sinformers.SharedInformerOption{
		k8sinformers.WithTweakListOptions(tlof),
	}
}

// These labels are from other node feature discovery mechanisms.
const (
	// InstanceType Label key used when UniformInstanceLabels is
	// disabled (DEPRECATED)
	DeprecatedInstanceTypeLabelKey = "node.kubernetes.io/instance-type"

	// Instance type set by NVCA (or admin to override).
	//
	// Ex. "nvca.nvcf.nvidia.io/instance-type": "ON-PREM.GPU.A100"
	UniformInstanceTypeLabelKey = types.InstanceTypeLabel
)

// These labels hold information about a node's GPU hardware capabilities.
// They are managed by GPU feature discovery.
//
// https://github.com/NVIDIA/gpu-feature-discovery/blob/main/README.md
// All these labels are required for DynamicGPUDiscovery and will raise a
// warning log entry, if a warning log is Not desired, add the label to the
// doNotWarnLabelSet below
const (
	// "nvidia.com/gpu.present": "true",
	gpuPresentLabelKey = "nvidia.com/gpu.present"
	// "nvidia.com/gpu.family": "ampere",
	gpuFamilyLabelKey = "nvidia.com/gpu.family"
	// "nvidia.com/gpu.machine": "Google-Compute-Engine",
	gpuMachineLabelKey = "nvidia.com/gpu.machine"
	// "nvidia.com/gpu.memory": "40960",
	gpuMemoryLabelKey = "nvidia.com/gpu.memory"
	// "nvidia.com/gpu.product": "A100-SXM4-40GB",
	gpuProductLabelKey = "nvidia.com/gpu.product"
	// nvidia.com/cuda.driver.major: "535"
	gpuDriverMajorLabelKey = "nvidia.com/cuda.driver.major"
	// nvidia.com/cuda.driver.minor: "154"
	gpuDriverMinorLabelKey = "nvidia.com/cuda.driver.minor"
	// nvidia.com/cuda.driver.rev: "05"
	gpuDriverRevisionLabelKey = "nvidia.com/cuda.driver.rev"
	// kubernetes.io/arch: amd64
	cpuArchLabelKey = "kubernetes.io/arch"
	// kubernetes.io/os: linux
	osLabelKey = "kubernetes.io/os"
	// "nvca.nvcf.nvidia.io/gpu.product" : "AD102GL"
	// this label if present will change the GPU reported as such
	gpuProductLabelOverrideKey = "nvca.nvcf.nvidia.io/gpu.product"
	// unknown string
	UnknownString = "unknown"
)

// List of node labels above which are not a Must
var doNotWarnLabelSet = map[string]bool{
	gpuProductLabelOverrideKey: true,
}

func (c *dynamicClient) GetAllBackendGPUs(ctx context.Context) ([]types.BackendGPU, error) {
	log := core.GetLogger(ctx)

	log.Debug("Gathering backend GPU info from Node features")

	backendGPUSet, _, err := c.getBackendGPUSet(ctx)
	if err != nil {
		return nil, err
	}

	// If the MultipleGPUTypesAllowed feature is not enabled,
	// only one GPU type per cluster is allowed.
	switch l := len(backendGPUSet); l {
	case 0:
		return nil, nvcaerrors.NotExistError(fmt.Errorf("no backend GPUs were found. Ensure gpu-operator is installed and at least one node "+
			"has GPU resources (%s). See "+
			"https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/getting-started.html#verification-running-sample-gpu-applications",
			GetGPUResourceNameFetcher(c.AttributeFetcher)))
	case 1:
	default:
		if !c.MultipleGPUTypesAllowed {
			return nil, fmt.Errorf("exactly 1 backend GPU type is allowed, got: %d", l)
		}
	}

	// Dedup by names.
	backendGPUs := make([]types.BackendGPU, len(backendGPUSet))
	i := 0
	for gpuName, instTypeSet := range backendGPUSet {
		backendGPU := types.BackendGPU{
			Name:          gpuName,
			InstanceTypes: make([]types.InstanceType, len(instTypeSet)),
		}

		j := 0
		for _, instType := range instTypeSet {
			backendGPU.InstanceTypes[j] = instType
			j++
		}
		sort.Slice(backendGPU.InstanceTypes, func(i, j int) bool {
			return backendGPU.InstanceTypes[i].String() <
				backendGPU.InstanceTypes[j].String()
		})

		backendGPUs[i] = backendGPU
		i++
	}
	sort.Slice(backendGPUs, func(i, j int) bool {
		return backendGPUs[i].Name < backendGPUs[j].Name
	})

	return backendGPUs, nil
}

func (c *dynamicClient) GetGPUResources(ctx context.Context, gn types.GPUName) (types.GPUResource, error) {
	_, gpuResourceSet, err := c.getBackendGPUSet(ctx)
	if err != nil {
		return types.GPUResource{}, err
	}
	alloc, err := c.gpuAllocGetter.GetForGPU(ctx, gn)
	if err != nil {
		return types.GPUResource{}, err
	}
	capacity, ok := gpuResourceSet[gn]
	if !ok {
		return types.GPUResource{}, fmt.Errorf("gpu %s not found in capacity set", gn)
	}
	return types.GPUResource{
		Allocated: alloc,
		Capacity:  capacity,
	}, nil
}

// Set of GPU name -> InstanceType.String() mappings.
// The string form is used since two instances
// may have the same Name but different specs (ex. 1 vs 2 GPUs).
type backendGPUSet map[types.GPUName]map[string]types.InstanceType

func (c *dynamicClient) getBackendGPUSet(ctx context.Context) (backendGPUSet, map[types.GPUName]uint64, error) {
	log := core.GetLogger(ctx)

	nodes, err := c.nodeLister.List(labels.Everything())
	if err != nil {
		return nil, nil, fmt.Errorf("list nodes: %v", err)
	}

	log.WithField("count", len(nodes)).Debug("Getting backend GPU set for all nodes")

	// Since backendGPUSet is only the shape of cluster GPUs,
	// the GPU capacity must be updated for each node.
	gpuCapacities := map[types.GPUName]uint64{}
	gpuSet := backendGPUSet{}
	nodeToGPUName := map[string]types.GPUName{}
	nodeCounts := map[string]uint64{}
	for _, node := range nodes {
		node := node

		log := log.WithField("node", node.Name)

		// Non-ready nodes should not be in published capacity.
		// Once they become ready, this method will pick them up.
		if !types.IsNodeReady(node) {
			log.Debug("Node is not ready or cordoned, skipping")
			continue
		}

		// Check for shared-cluster nodes when the mode is turned on.
		if c.SharedClusterOn != nil && c.SharedClusterOn.Load() &&
			(node.Labels == nil || node.Labels[sharedcluster.ScheduleLabelKey] != trueVal) {
			log.Debug("Node is not in shared cluster pool, skipping")
			continue
		}

		gpuName, instanceType, gpuRes, isGPUEnabled := c.parseNodeFeatures(ctx, node)
		if !isGPUEnabled {
			log.Debug("Node does not have GPU enabled, skipping")
			continue
		}

		nodeToGPUName[node.Name] = gpuName

		gpuCapacities[gpuName] += gpuRes.Capacity

		existingInstances, ok := gpuSet[gpuName]
		if !ok {
			existingInstances = map[string]types.InstanceType{}
			gpuSet[gpuName] = existingInstances
		}
		// Take max of non-GPU resources for consistency across node allocatable resources.
		// GPU and GPU memory resources are handled by deduplicating with String().
		if existingInstanceType, ok := existingInstances[instanceType.String()]; ok {
			instanceType.CPU = maxQuantities(existingInstanceType.CPU, instanceType.CPU)
			instanceType.SystemMemory = maxQuantities(existingInstanceType.SystemMemory, instanceType.SystemMemory)
			instanceType.Storage = maxQuantities(existingInstanceType.Storage, instanceType.Storage)
		}
		// Track node count for this instance type for multi-node.
		instanceType.NodeCount = nodeCounts[instanceType.String()] + 1
		nodeCounts[instanceType.String()] = instanceType.NodeCount

		existingInstances[instanceType.String()] = instanceType
	}

	return gpuSet, gpuCapacities, nil
}

func maxQuantities(a, b resource.Quantity) resource.Quantity {
	if a.Cmp(b) == -1 {
		return b
	}
	return a
}

var (
	zeroQ = resource.MustParse("0")

	// Allowed GPU resource name for generic GPU resource allocation.
	allowedGPUResKeyGeneric = "gpu"
	// Allowed GPU resource name for time-sliced GPUs.
	allowedGPUResKeyTimeSliced = "gpu.shared"
	// Allowed GPU resource name for passthrough GPUs (eg. Kata).
	allowedGPUResKeyPassthrough = "pgpu"
)

//nolint:gocyclo
func (c *dynamicClient) parseNodeFeatures(ctx context.Context,
	node *v1.Node,
) (types.GPUName, types.InstanceType, types.GPUResource, bool) {
	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"node": node.GetName(),
	})
	labels := node.GetLabels()
	if labels == nil {
		return "", types.InstanceType{}, types.GPUResource{}, false
	}

	if labels[gpuPresentLabelKey] != trueVal {
		return "", types.InstanceType{}, types.GPUResource{}, false
	}

	var (
		// MIG (only generic case supported for now)
		isTimeSlicedNode     = false
		driverVersion        = UnknownString
		isPassthroughGPUNode = false
	)
	if (c.isKataEnabled || c.isPassthroughGPUEnabled) &&
		labels[gpuWorkloadConfigLabel] == vmPassthroughVal {
		isPassthroughGPUNode = true
	}

	nlg := nodeLabelGetter{
		log:    log,
		labels: labels,
	}

	instanceType := types.InstanceType{}
	var gpuName types.GPUName
	var err error

	// honor the gpuProductLabelOverrideKey
	gpuFullProductName := nlg.get(gpuProductLabelOverrideKey)
	if gpuFullProductName == "" {
		gpuFullProductName = nlg.get(gpuProductLabelKey)
		if gpuFullProductName == "" {
			log.Warn("No GPU name available")
			return "", types.InstanceType{}, types.GPUResource{}, false
		}
		gpuName, err = types.ParseGPUName(gpuFullProductName)
		if err != nil {
			log.WithError(err).Error("Map full GPU product name to standardized name")
			return "", types.InstanceType{}, types.GPUResource{}, false
		}
	} else {
		log.Debugf("Using override GPU product label: %s=%s", gpuProductLabelOverrideKey, gpuFullProductName)
		gpuName = types.GPUName(gpuFullProductName)
	}

	instanceType.FullName = gpuFullProductName

	// https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/gpu-sharing.html#about-configuring-gpu-time-slicing
	if strings.HasSuffix(strings.ToUpper(gpuFullProductName), "-SHARED") {
		isTimeSlicedNode = true
	}

	// Allocatable contains all resources allocatable by workload pods.
	nodeResources := node.Status.Allocatable

	gpuResource := types.GPUResource{}
	gpuCap := types.ParseGPUResourceList(nodeResources)

	// Use a single key here to ensure both NVCA and the node are configured correctly.
	// Multiple GPU resource names may be present, but only one quantity must be used.
	var allowedGPUResKey string
	switch {
	case isPassthroughGPUNode:
		allowedGPUResKey = allowedGPUResKeyPassthrough
	case isTimeSlicedNode:
		allowedGPUResKey = allowedGPUResKeyTimeSliced
	default:
		allowedGPUResKey = allowedGPUResKeyGeneric
	}

	gpuCount, foundGPUCound := gpuCap.GetGPUCount(allowedGPUResKey)
	if !foundGPUCound || gpuCount == 0 {
		if gpuCap.HasMIG() {
			log.Warn("MIG resources were found on node capacity. NVCA only supports the 'single' MIG strategy")
		} else if isPassthroughGPUNode && !gpuCap.HasGenericPGPU() {
			if isPassthroughGPUNode {
				log.Warn("Node is marked as passthrough-enabled but no pGPU capacity detected")
			} else {
				log.Warn("Passthrough GPUs is enabled but no pGPU capacity detected")
			}
		} else {
			log.Warnf("No GPU capacity detected on node for resource name: %s", allowedGPUResKey)
		}
		return "", types.InstanceType{}, types.GPUResource{}, false
	}

	gpuResource.Capacity = gpuCount
	instanceType.GPUCount = gpuCount

	// CPUs will be rounded down (floor integer conversion) to the nearest whole CPU.
	instanceType.CPU = *nodeResources.Cpu()
	instanceType.SystemMemory = *nodeResources.Memory()
	instanceType.Storage = *nodeResources.StorageEphemeral()

	if gpuMemoryStr := nlg.get(gpuMemoryLabelKey); gpuMemoryStr != "" {
		// All GPU memory numbers are in mebibytes.
		if instanceType.GPUMemoryPerGPU, err = resource.ParseQuantity(gpuMemoryStr + "Mi"); err != nil {
			log.WithError(err).WithField(gpuMemoryLabelKey, gpuMemoryStr).
				Error("Parse GPU memory")
		}
	} else {
		instanceType.GPUMemoryPerGPU = zeroQ
	}

	gpuFamilyStr := nlg.get(gpuFamilyLabelKey)
	if gpuFamilyStr == "" {
		gpuFamilyStr = UnknownString
	}
	gpuMachineStr := nlg.get(gpuMachineLabelKey)
	if gpuMachineStr == "" {
		gpuMachineStr = UnknownString
	}
	cpuArchStr := nlg.get(cpuArchLabelKey)
	if cpuArchStr == "" {
		cpuArchStr = UnknownString
	}
	osStr := nlg.get(osLabelKey)
	if osStr == "" {
		osStr = UnknownString
	}
	drvMajorVerion := nlg.get(gpuDriverMajorLabelKey)
	if drvMajorVerion != "" {
		drvMinorVersion := nlg.get(gpuDriverMinorLabelKey)
		if drvMinorVersion != "" {
			drvRevision := nlg.get(gpuDriverRevisionLabelKey)
			if drvRevision != "" {
				driverVersion = fmt.Sprintf("%v.%v.%v", drvMajorVerion, drvMinorVersion, drvRevision)
			}
		}
	} else {
		driverVersion = UnknownString
	}
	instanceType.OS = osStr
	instanceType.CPUArch = cpuArchStr
	instanceType.DriverVersion = driverVersion
	instanceType.Description = fmt.Sprintf("%s (%s family) on a %s machine",
		gpuFullProductName, gpuFamilyStr, gpuMachineStr)
	if isPassthroughGPUNode {
		if c.isKataEnabled {
			instanceType.Description += " (Kata-enabled)"
		} else {
			instanceType.Description += " (Passthrough GPU)"
		}
	}

	// use the node.kubernetes.io/instance-type
	// if UniformLabels is disabled
	// this is to ensure backward compatibility
	if c.UniformInstanceLabels {
		instanceType.Name = types.InstanceName(nlg.get(UniformInstanceTypeLabelKey))
	} else {
		instanceType.Name = types.InstanceName(nlg.get(DeprecatedInstanceTypeLabelKey))
	}
	if instanceType.Name == "" {
		// Construct this on the fly if the label is not present
		// in case the node label updater has not run on this node yet.
		instanceType.Name = types.MakeInstanceName(c.clusterProvider, gpuName)
	}

	return gpuName, instanceType, gpuResource, true
}

type nodeLabelGetter struct {
	log    *logrus.Entry
	labels map[string]string
}

func (g nodeLabelGetter) get(lk string) string {
	lv := g.labels[lk]
	if lv == "" {
		if !doNotWarnLabelSet[lk] {
			g.log.WithField("label", lk).Warn("Missing label or value on node")
		}
	}
	return lv
}
