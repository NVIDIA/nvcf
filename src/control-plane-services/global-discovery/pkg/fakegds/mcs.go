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

package fakegds

import (
	"cmp"
	"context"
	"slices"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
)

type exportService struct {
	gdsv1.UnimplementedServiceExportServiceServer
	s *Server
}

func exportIDs(clusterset, cluster, namespace, name string) error {
	return require("clusterset", clusterset, "cluster", cluster, "namespace", namespace, "name", name)
}

// exportViewLocked returns a stored export with its endpoint counts.
func exportViewLocked(cs *clusterSet, key exportKey) *gdsv1.ServiceExport {
	e := proto.CloneOf(cs.exports[key])
	if rec := cs.endpoints[key]; rec != nil {
		e.EndpointsGeneration = rec.generation
		e.EndpointCount, e.ReadyEndpointCount = countEndpoints(rec.endpoints)
	}
	return e
}

func (x *exportService) PutServiceExport(_ context.Context, req *gdsv1.PutServiceExportRequest) (*gdsv1.PutServiceExportResponse, error) {
	if err := exportIDs(req.GetClusterset(), req.GetCluster(), req.GetNamespace(), req.GetName()); err != nil {
		return nil, err
	}
	s := x.s
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.setLocked(req.GetClusterset())
	if cs.clusters[req.GetCluster()] == nil {
		return nil, errClusterNotRegistered(codes.FailedPrecondition, req.GetClusterset(), req.GetCluster())
	}
	key := exportKey{req.GetCluster(), req.GetNamespace(), req.GetName()}
	if err := s.putExportLocked(cs, key, req.GetSpec(), req.GetGeneration()); err != nil {
		return nil, err
	}
	return &gdsv1.PutServiceExportResponse{Export: exportViewLocked(cs, key)}, nil
}

// putExportLocked stores an export when generation exceeds the stored one.
// A spec without export_creation_timestamp keeps the stored one, or gets
// the server time for a new export.
func (s *Server) putExportLocked(cs *clusterSet, key exportKey, spec *gdsv1.ServiceExportSpec, generation uint64) error {
	now := s.now()
	old := cs.exports[key]
	if old != nil && generation <= old.GetGeneration() {
		return errStaleGeneration("export", generation, old.GetGeneration())
	}
	spec = proto.CloneOf(spec)
	if spec == nil {
		spec = &gdsv1.ServiceExportSpec{}
	}
	if spec.ExportCreationTimestamp == nil {
		if old != nil && old.GetSpec().GetExportCreationTimestamp() != nil {
			spec.ExportCreationTimestamp = old.GetSpec().GetExportCreationTimestamp()
		} else {
			spec.ExportCreationTimestamp = timestamppb.New(now)
		}
	}
	cs.exports[key] = &gdsv1.ServiceExport{
		Clusterset: cs.name,
		Cluster:    key.cluster,
		Namespace:  key.namespace,
		Name:       key.name,
		Spec:       spec,
		Generation: generation,
		UpdatedAt:  timestamppb.New(now),
	}
	s.commitLocked(cs)
	return nil
}

func (x *exportService) DeleteServiceExport(_ context.Context, req *gdsv1.DeleteServiceExportRequest) (*gdsv1.DeleteServiceExportResponse, error) {
	if err := exportIDs(req.GetClusterset(), req.GetCluster(), req.GetNamespace(), req.GetName()); err != nil {
		return nil, err
	}
	x.s.DeleteExport(req.GetClusterset(), req.GetCluster(), req.GetNamespace(), req.GetName())
	return &gdsv1.DeleteServiceExportResponse{}, nil
}

func (x *exportService) ListServiceExports(_ context.Context, req *gdsv1.ListServiceExportsRequest) (*gdsv1.ListServiceExportsResponse, error) {
	if err := require("clusterset", req.GetClusterset(), "cluster", req.GetCluster()); err != nil {
		return nil, err
	}
	s := x.s
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.setLocked(req.GetClusterset())
	var keys []exportKey
	for k := range cs.exports {
		if k.cluster == req.GetCluster() {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b exportKey) int {
		return cmp.Or(cmp.Compare(a.namespace, b.namespace), cmp.Compare(a.name, b.name))
	})
	resp := &gdsv1.ListServiceExportsResponse{}
	for _, k := range keys {
		resp.Exports = append(resp.Exports, exportViewLocked(cs, k))
	}
	return resp, nil
}

func (x *exportService) ReplaceEndpoints(_ context.Context, req *gdsv1.ReplaceEndpointsRequest) (*gdsv1.ReplaceEndpointsResponse, error) {
	if err := exportIDs(req.GetClusterset(), req.GetCluster(), req.GetNamespace(), req.GetName()); err != nil {
		return nil, err
	}
	if n := len(req.GetEndpoints()); n > api.MaxEndpointsPerExport {
		return nil, errTooManyEndpoints(n)
	}
	s := x.s
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.setLocked(req.GetClusterset())
	if cs.clusters[req.GetCluster()] == nil {
		return nil, errClusterNotRegistered(codes.FailedPrecondition, req.GetClusterset(), req.GetCluster())
	}
	key := exportKey{req.GetCluster(), req.GetNamespace(), req.GetName()}
	if err := s.putEndpointsLocked(cs, key, req.GetEndpoints(), req.GetGeneration()); err != nil {
		return nil, err
	}
	total, ready := countEndpoints(req.GetEndpoints())
	return &gdsv1.ReplaceEndpointsResponse{Generation: req.GetGeneration(), EndpointCount: total, ReadyEndpointCount: ready}, nil
}

// putEndpointsLocked replaces an endpoint set when generation exceeds the
// stored one. An endpoint set may exist without its export.
func (s *Server) putEndpointsLocked(cs *clusterSet, key exportKey, endpoints []*gdsv1.Endpoint, generation uint64) error {
	if old := cs.endpoints[key]; old != nil && generation <= old.generation {
		return errStaleGeneration("endpoint set", generation, old.generation)
	}
	rec := &endpointRec{generation: generation}
	for _, ep := range endpoints {
		rec.endpoints = append(rec.endpoints, proto.CloneOf(ep))
	}
	cs.endpoints[key] = rec
	s.commitLocked(cs)
	return nil
}

type importService struct {
	gdsv1.UnimplementedServiceImportServiceServer
	s *Server
}

// selects reports whether labels contain every entry of the selector.
func selects(sel *gdsv1.LabelSelector, labels map[string]string) bool {
	for k, v := range sel.GetMatchLabels() {
		if got, ok := labels[k]; !ok || got != v {
			return false
		}
	}
	return true
}

func importMatcher(namespace string, sel *gdsv1.LabelSelector) func(*gdsv1.ServiceImport) bool {
	return func(imp *gdsv1.ServiceImport) bool {
		return (namespace == "" || imp.GetNamespace() == namespace) && selects(sel, imp.GetLabels())
	}
}

// importView returns imp without endpoint lists unless includeEndpoints;
// counts stay.
func importView(imp *gdsv1.ServiceImport, includeEndpoints bool) *gdsv1.ServiceImport {
	if includeEndpoints {
		return imp
	}
	imp = proto.CloneOf(imp)
	for _, ce := range imp.Endpoints {
		ce.Endpoints = nil
	}
	return imp
}

func (x *importService) GetServiceImport(_ context.Context, req *gdsv1.GetServiceImportRequest) (*gdsv1.GetServiceImportResponse, error) {
	if err := require("clusterset", req.GetClusterset(), "namespace", req.GetNamespace(), "name", req.GetName()); err != nil {
		return nil, err
	}
	s := x.s
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.refreshedLocked(req.GetClusterset())
	obj, ok := cs.views[kindImports][req.GetNamespace()+"/"+req.GetName()]
	if !ok {
		return nil, errNotFound("service import %s/%s not found in clusterset %q", req.GetNamespace(), req.GetName(), req.GetClusterset())
	}
	return &gdsv1.GetServiceImportResponse{ServiceImport: importView(obj.(*gdsv1.ServiceImport), req.GetIncludeEndpoints())}, nil
}

func (x *importService) ListServiceImports(_ context.Context, req *gdsv1.ListServiceImportsRequest) (*gdsv1.ListServiceImportsResponse, error) {
	if err := require("clusterset", req.GetClusterset()); err != nil {
		return nil, err
	}
	s := x.s
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.refreshedLocked(req.GetClusterset())
	resp := &gdsv1.ListServiceImportsResponse{Revision: s.revision(s.seq)}
	for _, imp := range sortedViews(cs, kindImports, importMatcher(req.GetNamespace(), req.GetSelector())) {
		resp.ServiceImports = append(resp.ServiceImports, importView(imp, req.GetIncludeEndpoints()))
	}
	return resp, nil
}

func (x *importService) WatchServiceImports(req *gdsv1.WatchServiceImportsRequest, stream grpc.ServerStreamingServer[gdsv1.WatchServiceImportsResponse]) error {
	match := importMatcher(req.GetNamespace(), req.GetSelector())
	return x.s.serveWatch(stream.Context(), &watchSpec{
		kind:   kindImports,
		cs:     req.GetClusterset(),
		resume: req.GetResumeRevision(),
		match:  func(m proto.Message) bool { return match(m.(*gdsv1.ServiceImport)) },
		view: func(m proto.Message) proto.Message {
			return importView(m.(*gdsv1.ServiceImport), req.GetIncludeEndpoints())
		},
		send: func(typ gdsv1.EventType, obj proto.Message, rev string) error {
			resp := &gdsv1.WatchServiceImportsResponse{Type: typ, Revision: rev}
			if obj != nil {
				resp.ServiceImport = obj.(*gdsv1.ServiceImport)
			}
			return stream.Send(resp)
		},
	})
}
