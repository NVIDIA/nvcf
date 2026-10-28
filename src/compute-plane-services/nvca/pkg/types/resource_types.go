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

package types //nolint:revive

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/sirupsen/logrus"
	"gopkg.in/inf.v0"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	listersv1 "k8s.io/client-go/listers/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures/sharedcluster"
)

const (
	trueVal           = "true"
	InstanceTypeLabel = "nvca.nvcf.nvidia.io/instance-type"
	GPUFamilyLabel    = "nvidia.com/gpu.family"
	GPUMachineLabel   = "nvidia.com/gpu.machine"
	nodeLogField      = "node"
)

// GPUNodeClassification identifies a GPU family/machine combination for classification-gap
// metrics, and the unclassified/total node counts seen for that combination.
type GPUNodeClassification struct {
	GPUFamily    string
	GPUMachine   string
	Unclassified uint64
	Total        uint64
}

type BackendGPUs []BackendGPU

func (bs BackendGPUs) ToRegistration(
	allowMultiNodeWorkloads bool,
	infraOverhead corev1.ResourceList,
) []RegistrationGPU {
	rgs := make([]RegistrationGPU, len(bs))
	for i, b := range bs {
		if b.Static {
			rgs[i] = RegistrationGPU{
				Name:          string(b.Name),
				Capacity:      b.Capacity,
				InstanceTypes: make([]RegistrationInstanceType, len(b.InstanceTypes)),
			}
			for j, it := range b.InstanceTypes {
				rgs[i].InstanceTypes[j] = RegistrationInstanceType{
					Name:          string(it.Name),
					Value:         it.FullName,
					Description:   it.Description,
					Default:       it.Default,
					CPUCores:      roundUpCPUToInteger(it.CPU),
					CPU:           it.CPU.String(),
					SystemMemory:  it.SystemMemory.String(),
					GPUCount:      it.GPUCount,
					GPUMemory:     it.GPUMemoryPerGPU.String(),
					OS:            it.OS,
					CPUArch:       it.CPUArch,
					DriverVersion: it.DriverVersion,
					Storage:       it.Storage.String(),
					// NodeType is not set for static GPUs, default to SINGLE
					NodeType: RegistrationInstanceTypeNodeTypeSingle,
				}
			}
		} else {
			// Dynamic GPUs need to calculate the infra overhead
			// Overhead of infrastructure (ex. utils) containers should be removed from instance type resources,
			// otherwise workloads + infra would use more than allotted resources.
			rgs[i] = b.toDynamicRegistration(allowMultiNodeWorkloads, infraOverhead)
		}
	}
	return rgs
}

// AddInstanceCapacity computes per-instance-type capacity from live node state and returns the
// updated gpus along with, per GPU family/machine combination, the count of GPU-bearing nodes
// that could not be attributed to any instance type (missing or unrecognized
// nvca.nvcf.nvidia.io/instance-type label) and the total count of GPU-bearing nodes seen
// (classified and unclassified). The unclassified count signals a GPU discovery/labeling gap:
// hardware NVCA can see on the node but cannot classify, so it is silently excluded from every
// nvca_instance_type_* metric.
func AddInstanceCapacity(
	ctx context.Context,
	gpus []RegistrationGPU,
	nodeLister listersv1.NodeLister,
	sharedClusterOn *atomic.Bool,
) ([]RegistrationGPU, []GPUNodeClassification, error) {
	log := core.GetLogger(ctx)

	goodNodes := map[string][]*corev1.Node{}
	unschedulableNodes := map[string][]*corev1.Node{}
	nodes, err := nodeLister.List(labels.Everything())
	if err != nil {
		return nil, nil, fmt.Errorf("list nodes: %v", err)
	}

	log.WithField("count", len(nodes)).Debug("Adding instance capacity from all nodes")

	for _, node := range nodes {
		log := log.WithField(nodeLogField, node.Name)
		// Non-ready nodes should not be in published capacity.
		// Once they become ready, this method will pick them up.
		if !IsNodeReady(node) {
			unschedulableNodes[node.Labels[InstanceTypeLabel]] = append(unschedulableNodes[node.Labels[InstanceTypeLabel]], node)
			log.Debug("Node is not ready or cordoned, adding to unschedulable")
			continue
		}

		// Check for shared-cluster nodes when the mode is turned on.
		if sharedClusterOn != nil && sharedClusterOn.Load() &&
			(node.Labels == nil || node.Labels[sharedcluster.ScheduleLabelKey] != trueVal) {
			unschedulableNodes[node.Labels[InstanceTypeLabel]] = append(unschedulableNodes[node.Labels[InstanceTypeLabel]], node)
			log.Debug("Node is not in shared cluster pool, adding to unschedulable")
			continue
		}

		key := node.Labels[InstanceTypeLabel]
		goodNodes[key] = append(goodNodes[key], node)
	}

	consumedLabels := map[string]struct{}{}
	for i, gpu := range gpus {
		for j, it := range gpu.InstanceTypes {
			// Multinode instance types are a multiple of the max instance type,
			// so use max/unschedulable of those for metrics.
			if it.NodeType == RegistrationInstanceTypeNodeTypeMulti {
				continue
			}
			var (
				instanceCapacity      uint64
				unschedulableCapacity uint64
			)

			instGPU := *resource.NewQuantity(int64(it.GPUCount), resource.DecimalSI) //nolint:gosec

			itName := InstanceName(it.Name).WithoutMultiplierMultiNode()
			consumedLabels[itName] = struct{}{}
			log := log.WithFields(logrus.Fields{
				"instance_type":  it.Name,
				"instance_label": itName,
			})
			for _, node := range goodNodes[itName] {
				capacity := calculateAvailability(node.Status.Allocatable, instGPU)

				log.WithFields(logrus.Fields{
					nodeLogField: node.Name,
					"capacity":   capacity,
				}).Debug("Node capacity")

				instanceCapacity += capacity
			}
			for _, node := range unschedulableNodes[itName] {
				capacity := calculateAvailability(node.Status.Allocatable, instGPU)

				log.WithFields(logrus.Fields{
					nodeLogField: node.Name,
					"capacity":   capacity,
				}).Debug("Node unschedulable capacity")

				unschedulableCapacity += capacity
			}
			gpus[i].InstanceTypes[j].MaxInstances = instanceCapacity
			gpus[i].InstanceTypes[j].UnschedulableCapacity = unschedulableCapacity
		}
	}

	type classificationKey struct {
		gpuFamily  string
		gpuMachine string
	}
	classifications := map[classificationKey]*GPUNodeClassification{}
	for _, nodeSet := range []map[string][]*corev1.Node{goodNodes, unschedulableNodes} {
		for label, nodesForLabel := range nodeSet {
			for _, node := range nodesForLabel {
				if _, anyFound := ParseGPUResourceList(node.Status.Allocatable).GPU(); !anyFound {
					continue
				}
				key := classificationKey{gpuFamily: node.Labels[GPUFamilyLabel], gpuMachine: node.Labels[GPUMachineLabel]}
				c, ok := classifications[key]
				if !ok {
					c = &GPUNodeClassification{GPUFamily: key.gpuFamily, GPUMachine: key.gpuMachine}
					classifications[key] = c
				}
				c.Total++
				if _, ok := consumedLabels[label]; ok {
					continue
				}
				// Debug, not Warn: this fires per unclassified node on every reconcile, and the
				// gap is already surfaced (and alertable) via nvca_gpu_node_unclassified_count.
				log.WithFields(logrus.Fields{
					nodeLogField:     node.Name,
					"instance_label": label,
					GPUFamilyLabel:   key.gpuFamily,
					GPUMachineLabel:  key.gpuMachine,
				}).Debug("Node has GPU resources but no recognized instance-type label")
				c.Unclassified++
			}
		}
	}

	gpuNodeClassifications := make([]GPUNodeClassification, 0, len(classifications))
	for _, c := range classifications {
		gpuNodeClassifications = append(gpuNodeClassifications, *c)
	}

	return gpus, gpuNodeClassifications, nil
}

func IsNodeReady(node *corev1.Node) bool {
	// Cordoned nodes should not be parsed.
	if node.Spec.Unschedulable {
		return false
	}
	for _, nc := range node.Status.Conditions {
		if nc.Type == corev1.NodeReady && nc.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func calculateAvailability(capacity corev1.ResourceList, instGPU resource.Quantity) uint64 {
	gpuCapacity, ok := ParseGPUResourceList(capacity).GPU()
	if !ok {
		return 0
	}

	if instGPU.IsZero() {
		return 0
	}

	var availability uint64
	for gpuCapacity.Cmp(instGPU) >= 0 {
		availability++
		gpuCapacity.Sub(instGPU)
	}
	return availability
}

type BackendGPU struct {
	Name          GPUName
	Capacity      uint64
	InstanceTypes []InstanceType
	Static        bool
}

func CalculateResourcesForInstanceType(
	instanceType RegistrationInstanceType,
	gpuResourceName corev1.ResourceName,
	overhead corev1.ResourceList,
) (reqs, lims corev1.ResourceList, err error) {
	if reqs, lims, err = CalculateNonGPUResourcesForInstanceType(instanceType, overhead); err != nil {
		return nil, nil, err
	}

	//nolint:gosec
	gpuResQuantity := *resource.NewQuantity(int64(instanceType.GPUCount), resource.DecimalSI)
	reqs[gpuResourceName] = gpuResQuantity
	lims[gpuResourceName] = gpuResQuantity

	return reqs, lims, nil
}

func CalculateNonGPUResourcesForInstanceType(
	instanceType RegistrationInstanceType,
	overhead corev1.ResourceList,
) (reqs, lims corev1.ResourceList, err error) {
	reqs, lims = corev1.ResourceList{}, corev1.ResourceList{}

	if lims[corev1.ResourceCPU], err = resource.ParseQuantity(instanceType.CPU); err != nil {
		return nil, nil, fmt.Errorf("parse internal instance type resource %s: %w", corev1.ResourceCPU, err)
	}
	reqs[corev1.ResourceCPU] = lims[corev1.ResourceCPU].DeepCopy()
	if lims[corev1.ResourceMemory], err = resource.ParseQuantity(instanceType.SystemMemory); err != nil {
		return nil, nil, fmt.Errorf("parse internal instance type resource %s: %w", corev1.ResourceMemory, err)
	}
	reqs[corev1.ResourceMemory] = lims[corev1.ResourceMemory].DeepCopy()
	if lims[corev1.ResourceEphemeralStorage], err = resource.ParseQuantity(instanceType.Storage); err != nil {
		return nil, nil, fmt.Errorf("parse internal instance type resource %s: %w", corev1.ResourceEphemeralStorage, err)
	}
	reqs[corev1.ResourceEphemeralStorage] = lims[corev1.ResourceEphemeralStorage].DeepCopy()

	if overhead != nil {
		for _, rl := range []corev1.ResourceList{reqs, lims} {
			for rn, rq := range rl {
				if ro, ok := overhead[rn]; ok {
					rq.Add(ro)
					rl[rn] = rq
				}
			}
		}
	}

	return reqs, lims, nil
}

const byteSuffixes = "KMGTPE"

var (
	binaryUnit  = inf.NewDec(1024, 0)
	decimalUnit = inf.NewDec(1000, 0)
	tenThousand = inf.NewDec(10000, 0)
)

// byteCountSIDec implements an algorithm to reduce b to a human-readable
// byte count with a byte suffix (kibi, mebi, etc.), potentially losing
// precision by rounding down. Rounding is ok because node resources vs.
// instance type resources will never exactly match in the limit.
func byteCountSIDec(unit, b *inf.Dec) string {
	// Reduce scale if possible to normalize quotient scale.
	b.Round(b, 0, inf.RoundFloor)
	// Return small and negative values as-is.
	if b.Cmp(unit) < 1 {
		return b.String()
	}
	exp := 0
	div := inf.NewDec(0, 0).Set(unit)
	n := inf.NewDec(0, 0).Set(b)
	tmp := inf.NewDec(0, 0).Set(b)
	for ; n.Cmp(unit) >= 0; n.QuoRound(n, unit, 0, inf.RoundFloor) {
		div.Mul(div, unit)
		if exp++; exp == len(byteSuffixes)-1 {
			// Break early if the exponent index is not specified (8Ei+).
			exp--
			break
		}
		tmp.QuoRound(b, div, 0, inf.RoundFloor)
		// To avoid reducing the human-readable byte size by too much
		// and losing precision unnecessarily, keep any size below 10000.
		// The value will be reduced without precision loss by tmp.String.
		if tmp.Cmp(tenThousand) == -1 {
			break
		}
	}
	return fmt.Sprintf("%s%c", tmp.String(), byteSuffixes[exp])
}

func byteCountBinarySIQuantity(b resource.Quantity) string {
	if s := canonicalizeQuantity(b); s != "" {
		return s
	}
	s := byteCountSIDec(binaryUnit, b.AsDec())
	if b.AsDec().Cmp(binaryUnit) == -1 {
		return b.String()
	}
	return fmt.Sprintf("%si", s)
}

func byteCountDecimalSIQuantity(b resource.Quantity) string {
	if s := canonicalizeQuantity(b); s != "" {
		return s
	}
	return byteCountSIDec(decimalUnit, b.AsDec())
}

// canonicalizeQuantity returns a quantity as a canonical string if
// its integer value is under 5 sigfigs.
// This function is a fast path for converting an already-reduced quantity
// to an easily human-readable value.
func canonicalizeQuantity(q resource.Quantity) string {
	if res, suff := q.CanonicalizeBytes(nil); len(res) < 5 {
		return string(append(res, suff...))
	}
	return ""
}

func roundToNearestByteSize(q resource.Quantity) string {
	if q.Format == resource.BinarySI {
		return byteCountBinarySIQuantity(q)
	}
	return byteCountDecimalSIQuantity(q)
}

func roundUpCPUToInteger(cpu resource.Quantity) uint64 {
	//nolint:gosec
	return uint64(cpu.Value())
}

func (g BackendGPU) toDynamicRegistration(allowMultiNodeWorkloads bool, infraOverhead corev1.ResourceList) (rg RegistrationGPU) {
	rg.Name = string(g.Name)
	// use the Capacity AS IS from BackendGPU for
	// total capacity
	rg.Capacity = g.Capacity

	// Reverse the instance type list by GPU count to calculate subdivisions by the largest one first.
	// This prevents an instance type from a node with a bad GPU from creating unexpected subdivisions,
	// ex. using 7 as the divisor instead of 8.
	//
	// NOTE: this may cause subtle misrepresentations of non-GPU resource quantities between two instance types
	// created from different nodes with different GPU counts and non-GPU resource quantities,
	// ex. node-1 with 6 A100 GPUs, 32 CPUs vs. node-2 with 8 A100 GPUs, 64 CPUs will yield "GPU.NCP.A100_1x"
	// instances with more collective CPUs than exist in the cluster.
	// One way to fix this is to implement static configuration for the cluster that defines a
	// machine configuration that results in instance types being properly differentiated on machine type,
	// ex. node-1 should generate the "GPU.NCP.A100-6_1x" instance type and node-2 should generate the "GPU.NCP.A100-8_1x"
	// instance type. This doesn't fix the cross-cluster instance type compatibility issue
	// (what if "GPU.NCP.A100-6_1x" looks different between two target-able clusters), but does fix the problem
	// on individual clusters with intentionally different machine sizes for the same GPU.
	sort.Slice(g.InstanceTypes, func(i, j int) bool {
		return g.InstanceTypes[i].GPUCount >= g.InstanceTypes[j].GPUCount
	})

	instSetByIDStr := map[string]RegistrationInstanceType{}
	for _, it := range g.InstanceTypes {
		// NB(estroczynski): this needs to be updated if NVCA is to support CPU-only workload nodes.
		if it.GPUCount == 0 {
			continue
		}

		// Calculate the per-GPU memory count for instance multiples.
		for i := uint64(1); i < it.GPUCount; i *= 2 {
			// Use the full GPU name to dedup entries, since some features
			// are captured in the full name only.
			instIDStr := fmt.Sprintf("%s-%dx", it.FullName, i)

			if _, ok := instSetByIDStr[instIDStr]; ok {
				continue
			}

			instType, include := calcDynamicInstanceType(it, i, infraOverhead)
			if !include {
				continue
			}

			instSetByIDStr[instIDStr] = instType
			rg.InstanceTypes = append(rg.InstanceTypes, instType)
		}

		lastInstIDStr := fmt.Sprintf("%s-%dx", it.FullName, it.GPUCount)
		if _, ok := instSetByIDStr[lastInstIDStr]; ok {
			continue
		}

		// Add the last one, which may or may not be a power of 2.
		if lastInstType, include := calcDynamicInstanceType(it, it.GPUCount, infraOverhead); include {
			instSetByIDStr[lastInstIDStr] = lastInstType
			rg.InstanceTypes = append(rg.InstanceTypes, lastInstType)
		}

		if allowMultiNodeWorkloads {
			// Calculate the multinode instances, starting from 2 nodes.
			for currNodeCount := uint64(2); currNodeCount <= it.NodeCount; currNodeCount++ {
				instIDStr := it.Name.WithMultiNode(it.GPUCount, currNodeCount)

				if _, ok := instSetByIDStr[instIDStr]; ok {
					continue
				}

				instType, include := calcDynamicInstanceTypeMultiNode(it, currNodeCount, infraOverhead)
				if !include {
					continue
				}

				instSetByIDStr[instIDStr] = instType
				rg.InstanceTypes = append(rg.InstanceTypes, instType)
			}
		}
	}

	sort.Slice(rg.InstanceTypes, func(i, j int) bool {
		return rg.InstanceTypes[i].Name < rg.InstanceTypes[j].Name
	})

	// Make the first instance type the default, which is always a single GPU instance.
	if len(rg.InstanceTypes) != 0 {
		rg.InstanceTypes[0].Default = true
	}

	return rg
}

func calcDynamicInstanceType(baseIT InstanceType, subGPUCount uint64, infraOverhead corev1.ResourceList) (RegistrationInstanceType, bool) {
	regIT := RegistrationInstanceType{
		Name:          baseIT.Name.WithMultiplier(subGPUCount),
		Value:         string(baseIT.Name),
		Description:   baseIT.Description,
		Default:       false,
		OS:            baseIT.OS,
		GPUCount:      subGPUCount,
		CPUArch:       baseIT.CPUArch,
		DriverVersion: baseIT.DriverVersion,
		NodeType:      RegistrationInstanceTypeNodeTypeSingle,
	}

	var (
		rl     = corev1.ResourceList{}
		gpuMem resource.Quantity
	)
	if subGPUCount == baseIT.GPUCount {
		gpuMem = baseIT.GPUMemoryPerGPU.DeepCopy()
		//nolint:gosec
		gpuMem.Mul(int64(baseIT.GPUCount))

		rl[corev1.ResourceCPU] = baseIT.CPU
		rl[corev1.ResourceMemory] = baseIT.SystemMemory.DeepCopy()
		rl[corev1.ResourceEphemeralStorage] = baseIT.Storage.DeepCopy()
	} else {
		gpuMem = baseIT.GPUMemoryPerGPU.DeepCopy()
		//nolint:gosec
		gpuMem.Mul(int64(subGPUCount))

		// Gosec assumes overflow can happen, but values are not big enough to cause this.
		//nolint:gosec
		fullGPUCountDec := inf.NewDec(int64(baseIT.GPUCount), 0)

		rl[corev1.ResourceCPU] = calcFractionCPU(baseIT.CPU.DeepCopy(), subGPUCount, fullGPUCountDec)
		rl[corev1.ResourceMemory] = calcFraction(baseIT.SystemMemory.DeepCopy(), subGPUCount, fullGPUCountDec, 0)
		rl[corev1.ResourceEphemeralStorage] = calcFraction(baseIT.Storage.DeepCopy(), subGPUCount, fullGPUCountDec, 0)
	}

	if !subOverheadResources(rl, infraOverhead) {
		return RegistrationInstanceType{}, false
	}

	cpu := rl[corev1.ResourceCPU]
	regIT.CPU = cpu.String()
	regIT.CPUCores = roundUpCPUToInteger(cpu)
	regIT.SystemMemory = roundToNearestByteSize(rl[corev1.ResourceMemory])
	regIT.Storage = roundToNearestByteSize(rl[corev1.ResourceEphemeralStorage])
	regIT.GPUMemory = roundToNearestByteSize(gpuMem)

	return regIT, true
}

func calcFraction(q resource.Quantity, numFactor uint64, denom *inf.Dec, scale inf.Scale) resource.Quantity {
	quo(&q, denom, scale)
	// Multiplication cannot result in significant lost precision
	// because each is sub/full <= 1.
	q.Mul(int64(numFactor)) //nolint:gosec
	return q
}

func calcFractionCPU(q resource.Quantity, numFactor uint64, denom *inf.Dec) resource.Quantity {
	q.SetScaled(q.MilliValue(), 0)
	qf := calcFraction(q, numFactor, denom, 0)
	qf.SetScaled(qf.Value(), resource.Milli)
	return qf
}

func quo(num *resource.Quantity, denom *inf.Dec, scale inf.Scale) {
	num.AsDec().QuoRound(num.AsDec(), denom, scale, inf.RoundFloor)
}

func calcDynamicInstanceTypeMultiNode(baseIT InstanceType, nodeCount uint64, infraOverhead corev1.ResourceList) (RegistrationInstanceType, bool) {
	// Gosec assumes overflow can happen, but values are not big enough to cause this.
	//nolint:gosec
	nodeCountI64 := int64(nodeCount)
	//nolint:gosec
	gpuCountI64 := int64(baseIT.GPUCount)

	gpuMemValue := baseIT.GPUMemoryPerGPU.DeepCopy()
	gpuMemValue.Mul(nodeCountI64 * gpuCountI64)

	rl := corev1.ResourceList{}

	storageValue := baseIT.Storage.DeepCopy()
	storageValue.Mul(nodeCountI64)
	rl[corev1.ResourceEphemeralStorage] = storageValue

	systemMemValue := baseIT.SystemMemory.DeepCopy()
	systemMemValue.Mul(nodeCountI64)
	rl[corev1.ResourceMemory] = systemMemValue

	cpuCoresValue := baseIT.CPU.DeepCopy()
	cpuCoresValue.Mul(nodeCountI64)
	rl[corev1.ResourceCPU] = cpuCoresValue

	if !subOverheadResources(rl, infraOverhead) {
		return RegistrationInstanceType{}, false
	}
	// Update after subtracting overhead.
	cpuCoresValue = rl[corev1.ResourceCPU]
	systemMemValue = rl[corev1.ResourceMemory]
	storageValue = rl[corev1.ResourceEphemeralStorage]

	// Max instances can be calculated here by dividing the number of nodes parsed for this instance type (baseIT.NodeCount)
	// by the desired node count for this instance type. For example, if baseIT was derived from 24 nodes and nodeCount == 5,
	// then MaxInstances is 4. This calculation handles the case where nodeCount < baseIT/2 by integer floor division (MaxInstances == 1).
	maxInstances := baseIT.NodeCount / nodeCount

	regIT := RegistrationInstanceType{
		Name:          baseIT.Name.WithMultiNode(baseIT.GPUCount, nodeCount),
		Value:         string(baseIT.Name),
		Description:   fmt.Sprintf("%s, %d nodes", baseIT.Description, nodeCount),
		Default:       false,
		CPU:           cpuCoresValue.String(),
		CPUCores:      roundUpCPUToInteger(cpuCoresValue),
		SystemMemory:  roundToNearestByteSize(systemMemValue),
		Storage:       roundToNearestByteSize(storageValue),
		GPUCount:      baseIT.GPUCount * nodeCount,
		GPUMemory:     roundToNearestByteSize(gpuMemValue),
		OS:            baseIT.OS,
		CPUArch:       baseIT.CPUArch,
		DriverVersion: baseIT.DriverVersion,
		NodeType:      RegistrationInstanceTypeNodeTypeMulti,
		MaxInstances:  maxInstances,
	}

	return regIT, true
}

// If any overhead resource quantity is greater than some instance type quantity,
// the instance type cannot satisfy a workload and overhead so should be created.
func subOverheadResources(rl, overhead corev1.ResourceList) bool {
	for overheadRN, overheadRQ := range overhead {
		rq, ok := rl[overheadRN]
		if !ok {
			continue
		}
		// rq cannot be 0 or negative.
		if rq.Cmp(overheadRQ) != 1 {
			return false
		}
		rq.Sub(overheadRQ)
		rl[overheadRN] = rq
	}
	return true
}

type InstanceType struct {
	Name            InstanceName
	FullName        string
	Description     string
	Default         bool
	CPU             resource.Quantity
	SystemMemory    resource.Quantity
	GPUCount        uint64
	GPUMemoryPerGPU resource.Quantity
	Storage         resource.Quantity
	CPUArch         string
	OS              string
	DriverVersion   string
	NodeCount       uint64
}

func (t *InstanceType) String() string {
	return fmt.Sprintf("%s|gpuc=%d|gpum=%s",
		t.Name,
		t.GPUCount, t.GPUMemoryPerGPU.String())
}

var clusterProviderNorm = map[string]string{
	"ON-PREM":   "ON-PREM",
	"AWS":       "AWS",
	"GCP":       "GCP",
	"OCI":       "OCI",
	"AZURE":     "AZURE",
	"DGX-CLOUD": "DGX-CLOUD",
	"NCP":       "NCP",
}

type InstanceName string

func (n InstanceName) WithMultiplier(m uint64) string {
	return fmt.Sprintf("%s_%dx", n, m)
}

func (n InstanceName) WithMultiNode(m, count uint64) string {
	return fmt.Sprintf("%s_%dx.x%d", n, m, count)
}

var multiNodeInstanceTypeRE = regexp.MustCompile(`_\d+x(\.x\d+)?$`)

func (n InstanceName) WithoutMultiplierMultiNode() string {
	return multiNodeInstanceTypeRE.ReplaceAllString(string(n), "")
}

func (n InstanceName) GetNodeCount() int {
	matches := multiNodeInstanceTypeRE.FindStringSubmatch(string(n))
	if len(matches) < 2 {
		return 1
	}
	nodeCount, _ := strconv.Atoi(strings.TrimPrefix(matches[1], ".x"))
	if nodeCount == 0 {
		nodeCount = 1
	}
	return nodeCount
}

func MakeInstanceName(clusterProvider string, gpuName GPUName) InstanceName {
	return InstanceName(fmt.Sprintf("%s.GPU.%s", clusterProvider, gpuName))
}

func NormalizeClusterProvider(clusterProvider string) (string, error) {
	name, ok := clusterProviderNorm[strings.ToUpper(clusterProvider)]
	if !ok {
		return "", fmt.Errorf("unknown cluster provider %q", clusterProvider)
	}
	return name, nil
}
