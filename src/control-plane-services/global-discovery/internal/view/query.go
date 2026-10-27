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

package view

import (
	"maps"
	"slices"

	"google.golang.org/protobuf/proto"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
)

// Filter selects derived objects of one kind for a list or a watch.
type Filter interface {
	Kind() Kind
	// Match reports whether the full message matches.
	Match(m proto.Message) bool
	// IncludeEndpoints selects Full instead of Lite messages.
	IncludeEndpoints() bool
}

// ClusterFilter selects clusters.
type ClusterFilter struct {
	IncludeExpired bool
}

// Kind implements Filter.
func (ClusterFilter) Kind() Kind { return KindCluster }

// IncludeEndpoints implements Filter.
func (ClusterFilter) IncludeEndpoints() bool { return false }

// Match implements Filter.
func (f ClusterFilter) Match(m proto.Message) bool {
	c := m.(*gdsv1.Cluster)
	return f.IncludeExpired || c.GetLease().GetState() != gdsv1.LeaseState_LEASE_STATE_EXPIRED
}

// ImportFilter selects ServiceImports.
type ImportFilter struct {
	Namespace string
	Selector  map[string]string
	Endpoints bool
}

// Kind implements Filter.
func (ImportFilter) Kind() Kind { return KindServiceImport }

// IncludeEndpoints implements Filter.
func (f ImportFilter) IncludeEndpoints() bool { return f.Endpoints }

// Match implements Filter.
func (f ImportFilter) Match(m proto.Message) bool {
	si := m.(*gdsv1.ServiceImport)
	return (f.Namespace == "" || si.GetNamespace() == f.Namespace) && selects(f.Selector, si.GetLabels())
}

// PoolFilter selects pools. Fields are combined with AND; empty fields match
// everything. An empty States matches every state except GONE.
type PoolFilter struct {
	Region        string
	DC            string
	Cluster       string
	HardwareClass string
	// Model matches a model id or an alias.
	Model     string
	States    []gdsv1.PoolState
	Selector  map[string]string
	Endpoints bool
}

// Kind implements Filter.
func (PoolFilter) Kind() Kind { return KindPool }

// IncludeEndpoints implements Filter.
func (f PoolFilter) IncludeEndpoints() bool { return f.Endpoints }

// Match implements Filter.
func (f PoolFilter) Match(m proto.Message) bool {
	p := m.(*gdsv1.Pool)
	loc := p.GetLocation()
	switch {
	case f.Region != "" && loc.GetRegion() != f.Region,
		f.DC != "" && loc.GetDc() != f.DC,
		f.Cluster != "" && loc.GetCluster() != f.Cluster,
		f.HardwareClass != "" && p.GetHardwareClass() != f.HardwareClass:
		return false
	}
	if len(f.States) == 0 {
		if p.GetState() == gdsv1.PoolState_POOL_STATE_GONE {
			return false
		}
	} else if !slices.Contains(f.States, p.GetState()) {
		return false
	}
	if f.Model != "" && !servesModel(p, f.Model) {
		return false
	}
	return selects(f.Selector, p.GetLabels())
}

func servesModel(p *gdsv1.Pool, model string) bool {
	for _, m := range p.GetModels() {
		if m.GetModelId() == model || slices.Contains(m.GetAliases(), model) {
			return true
		}
	}
	return false
}

// RouterFilter selects routers.
type RouterFilter struct {
	Region  string
	Cluster string
}

// Kind implements Filter.
func (RouterFilter) Kind() Kind { return KindRouter }

// IncludeEndpoints implements Filter.
func (RouterFilter) IncludeEndpoints() bool { return false }

// Match implements Filter.
func (f RouterFilter) Match(m proto.Message) bool {
	r := m.(*gdsv1.Router)
	return (f.Region == "" || r.GetRegion() == f.Region) && (f.Cluster == "" || r.GetCluster() == f.Cluster)
}

func selects(selector, labels map[string]string) bool {
	for k, v := range selector {
		if got, ok := labels[k]; !ok || got != v {
			return false
		}
	}
	return true
}

// indexes speed up selector and model lookups. Values are object keys.
type indexes struct {
	models       map[string]map[string]struct{}
	importLabels map[string]map[string]struct{}
	poolLabels   map[string]map[string]struct{}
}

func addIndex(idx map[string]map[string]struct{}, term, key string) {
	set, ok := idx[term]
	if !ok {
		set = map[string]struct{}{}
		idx[term] = set
	}
	set[key] = struct{}{}
}

func labelTerm(k, v string) string { return k + "=" + v }

func buildIndexes(objects [numKinds]map[string]*Object) indexes {
	idx := indexes{
		models:       map[string]map[string]struct{}{},
		importLabels: map[string]map[string]struct{}{},
		poolLabels:   map[string]map[string]struct{}{},
	}
	for key, o := range objects[KindServiceImport] {
		for k, v := range o.Full.(*gdsv1.ServiceImport).GetLabels() {
			addIndex(idx.importLabels, labelTerm(k, v), key)
		}
	}
	for key, o := range objects[KindPool] {
		p := o.Full.(*gdsv1.Pool)
		for k, v := range p.GetLabels() {
			addIndex(idx.poolLabels, labelTerm(k, v), key)
		}
		for _, m := range p.GetModels() {
			addIndex(idx.models, m.GetModelId(), key)
			for _, a := range m.GetAliases() {
				addIndex(idx.models, a, key)
			}
		}
	}
	return idx
}

// candidates returns the keys an index narrows a filter to, or nil and false
// when no index applies.
func (cs *clusterSet) candidates(f Filter) (map[string]struct{}, bool) {
	var terms []map[string]struct{}
	switch f := f.(type) {
	case ImportFilter:
		for k, v := range f.Selector {
			terms = append(terms, cs.index.importLabels[labelTerm(k, v)])
		}
	case PoolFilter:
		if f.Model != "" {
			terms = append(terms, cs.index.models[f.Model])
		}
		for k, v := range f.Selector {
			terms = append(terms, cs.index.poolLabels[labelTerm(k, v)])
		}
	}
	if len(terms) == 0 {
		return nil, false
	}
	slices.SortFunc(terms, func(a, b map[string]struct{}) int { return len(a) - len(b) })
	out := maps.Clone(terms[0])
	for _, t := range terms[1:] {
		for k := range out {
			if _, ok := t[k]; !ok {
				delete(out, k)
			}
		}
	}
	return out, true
}

// matching returns the objects of the filter's kind that match, sorted by
// key. cs.mu must be held.
func (cs *clusterSet) matching(f Filter) []*Object {
	objects := cs.objects[f.Kind()]
	var keys []string
	if cand, ok := cs.candidates(f); ok {
		keys = slices.Collect(maps.Keys(cand))
	} else {
		keys = slices.Collect(maps.Keys(objects))
	}
	slices.Sort(keys)
	out := make([]*Object, 0, len(keys))
	for _, k := range keys {
		if o, ok := objects[k]; ok && f.Match(o.Full) {
			out = append(out, o)
		}
	}
	return out
}

// List returns the messages of the objects matching f and the revision of
// the view they were taken from. An unknown clusterset yields nothing and the
// revision of an empty view.
func (v *View) List(clusterSet string, f Filter) ([]proto.Message, string) {
	cs := v.get(clusterSet)
	if cs == nil {
		return nil, Revision(v.epoch, 0)
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	objs := cs.matching(f)
	out := make([]proto.Message, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.Message(f.IncludeEndpoints()))
	}
	return out, Revision(v.epoch, cs.seq)
}

// Get returns one derived object.
func (v *View) Get(clusterSet string, kind Kind, key string) (*Object, bool) {
	cs := v.get(clusterSet)
	if cs == nil {
		return nil, false
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	o, ok := cs.objects[kind][key]
	return o, ok
}

// Revision returns the current revision of a clusterset.
func (v *View) Revision(clusterSet string) string {
	cs := v.get(clusterSet)
	if cs == nil {
		return Revision(v.epoch, 0)
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return Revision(v.epoch, cs.seq)
}

// PoolByName returns the pool named name in cluster, if any, including a
// GONE tombstone.
func (v *View) PoolByName(clusterSet, cluster, name string) (*gdsv1.Pool, bool) {
	cs := v.get(clusterSet)
	if cs == nil {
		return nil, false
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for _, o := range cs.objects[KindPool] {
		p := o.Full.(*gdsv1.Pool)
		if p.GetName() == name && p.GetLocation().GetCluster() == cluster && p.GetState() != gdsv1.PoolState_POOL_STATE_GONE {
			return p, true
		}
	}
	return nil, false
}

// ClusterRecord returns the stored registration of a cluster.
func (v *View) ClusterRecord(key store.ClusterKey) (store.ClusterRecord, bool) {
	cs := v.get(key.ClusterSet)
	if cs == nil {
		return store.ClusterRecord{}, false
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	r, ok := cs.clusters[key.Cluster]
	return r.Clone(), ok
}

// ExportRecord returns a stored export.
func (v *View) ExportRecord(key store.ExportKey) (store.ExportRecord, bool) {
	cs := v.get(key.ClusterSet)
	if cs == nil {
		return store.ExportRecord{}, false
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	r, ok := cs.exports[key]
	return r.Clone(), ok
}

// EndpointSetGeneration returns the generation of a stored endpoint set.
func (v *View) EndpointSetGeneration(key store.ExportKey) (uint64, bool) {
	cs := v.get(key.ClusterSet)
	if cs == nil {
		return 0, false
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	r, ok := cs.endpoints[key]
	return r.Generation, ok
}

// PoolRecord returns the API record of a pool by pool id.
func (v *View) PoolRecord(clusterSet, poolID string) (store.PoolRecord, bool) {
	cs := v.get(clusterSet)
	if cs == nil {
		return store.PoolRecord{}, false
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for k, r := range cs.pools {
		if k.PoolID == poolID {
			return r.Clone(), true
		}
	}
	return store.PoolRecord{}, false
}

// ExportStatus is a stored export with the counts of its endpoint set.
type ExportStatus struct {
	Record              store.ExportRecord
	EndpointsGeneration uint64
	EndpointCount       int32
	ReadyEndpointCount  int32
}

// Exports returns the exports of one cluster sorted by key.
func (v *View) Exports(key store.ClusterKey) []ExportStatus {
	cs := v.get(key.ClusterSet)
	if cs == nil {
		return nil
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	var out []ExportStatus
	for k, r := range cs.exports {
		if k.Cluster != key.Cluster {
			continue
		}
		st := ExportStatus{Record: r.Clone()}
		if ep, ok := cs.endpointsOf(k); ok {
			st.EndpointsGeneration, st.EndpointCount, st.ReadyEndpointCount = ep.gen, ep.total, ep.ready
		}
		out = append(out, st)
	}
	slices.SortFunc(out, func(a, b ExportStatus) int { return store.CompareExportKeys(a.Record.Key, b.Record.Key) })
	return out
}

// Summary describes one clusterset.
type Summary struct {
	ClusterSet     string
	Clusters       int
	ActiveClusters int
	Exports        int
	Pools          int
}

// Summaries describes every tracked clusterset that holds records, sorted.
func (v *View) Summaries() []Summary {
	var out []Summary
	for _, name := range v.ClusterSetNames() {
		cs := v.get(name)
		if cs == nil {
			continue
		}
		cs.mu.Lock()
		s := Summary{ClusterSet: name, Clusters: len(cs.clusters), Exports: len(cs.exports)}
		for _, o := range cs.objects[KindCluster] {
			if o.Full.(*gdsv1.Cluster).GetLease().GetState() == gdsv1.LeaseState_LEASE_STATE_ACTIVE {
				s.ActiveClusters++
			}
		}
		for _, o := range cs.objects[KindPool] {
			if o.Full.(*gdsv1.Pool).GetState() != gdsv1.PoolState_POOL_STATE_GONE {
				s.Pools++
			}
		}
		empty := s.Clusters == 0 && s.Exports == 0 && s.Pools == 0 && !cs.discovered
		cs.mu.Unlock()
		if !empty {
			out = append(out, s)
		}
	}
	return out
}

// ExportStatusOf returns one stored export with its endpoint set counts.
func (v *View) ExportStatusOf(key store.ExportKey) (ExportStatus, bool) {
	cs := v.get(key.ClusterSet)
	if cs == nil {
		return ExportStatus{}, false
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	r, ok := cs.exports[key]
	if !ok {
		return ExportStatus{}, false
	}
	st := ExportStatus{Record: r.Clone()}
	if ep, ok := cs.endpointsOf(key); ok {
		st.EndpointsGeneration, st.EndpointCount, st.ReadyEndpointCount = ep.gen, ep.total, ep.ready
	}
	return st, true
}
