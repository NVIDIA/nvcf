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

package api

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	gds "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/convert"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/view"
)

func validateExportKey(v *validator, clusterSet, cluster, namespace, name string) store.ExportKey {
	v.dnsLabel("clusterset", clusterSet)
	v.dnsLabel("cluster", cluster)
	v.dnsLabel("namespace", namespace)
	v.dnsLabel("name", name)
	return store.ExportKey{ClusterSet: clusterSet, Cluster: cluster, Namespace: namespace, Name: name}
}

// normalizeSpec applies the Kubernetes defaults: TCP ports and no session
// affinity.
func normalizeSpec(s *store.ExportSpec) {
	for i := range s.Ports {
		if s.Ports[i].Protocol == "" {
			s.Ports[i].Protocol = store.ProtocolTCP
		}
	}
	if s.SessionAffinity == "" {
		s.SessionAffinity = store.SessionAffinityNone
	}
}

func exportToProto(st view.ExportStatus) *gdsv1.ServiceExport {
	r := st.Record
	return &gdsv1.ServiceExport{
		Clusterset:          r.Key.ClusterSet,
		Cluster:             r.Key.Cluster,
		Namespace:           r.Key.Namespace,
		Name:                r.Key.Name,
		Spec:                convert.ExportSpecToProto(r.Spec),
		Generation:          r.Generation,
		UpdatedAt:           convert.Timestamp(r.UpdatedAt),
		EndpointsGeneration: st.EndpointsGeneration,
		EndpointCount:       st.EndpointCount,
		ReadyEndpointCount:  st.ReadyEndpointCount,
	}
}

// PutServiceExport implements gdsv1.ServiceExportServiceServer.
func (s *Server) PutServiceExport(ctx context.Context, req *gdsv1.PutServiceExportRequest) (*gdsv1.PutServiceExportResponse, error) {
	var v validator
	key := validateExportKey(&v, req.GetClusterset(), req.GetCluster(), req.GetNamespace(), req.GetName())
	v.exportSpec("spec", req.GetSpec())
	if err := v.err(); err != nil {
		return nil, err
	}
	if err := s.requireCluster(ctx, key.ClusterKey()); err != nil {
		return nil, err
	}
	now := s.now()
	spec := convert.ExportSpecFromProto(req.GetSpec())
	normalizeSpec(&spec)
	stored, hasStored := s.view.ExportRecord(key)
	if spec.ExportCreationTimestamp.IsZero() {
		spec.ExportCreationTimestamp = now
		if hasStored {
			spec.ExportCreationTimestamp = stored.Spec.ExportCreationTimestamp
		}
	}
	gen, assigned := generation(req.GetGeneration(), stored.Generation, now)
	rec := store.ExportRecord{Key: key, Spec: spec, Generation: gen, UpdatedAt: now}
	err := s.store.PutExport(ctx, rec)
	if cur, stale := store.StaleGeneration(err); stale && assigned {
		rec.Generation = registry.NextGeneration(cur, now)
		err = s.store.PutExport(ctx, rec)
	}
	if cur, stale := store.StaleGeneration(err); stale {
		return nil, staleGeneration("export", rec.Generation, cur)
	}
	if err != nil {
		return nil, storeError(err, "put export")
	}
	s.view.ApplyExport(rec)
	s.log.Debug("export stored", zap.String("clusterset", key.ClusterSet), zap.String("cluster", key.Cluster),
		zap.String("namespace", key.Namespace), zap.String("name", key.Name), zap.Uint64("generation", rec.Generation))
	st, _ := s.view.ExportStatusOf(key)
	return &gdsv1.PutServiceExportResponse{Export: exportToProto(st)}, nil
}

// DeleteServiceExport implements gdsv1.ServiceExportServiceServer.
func (s *Server) DeleteServiceExport(ctx context.Context, req *gdsv1.DeleteServiceExportRequest) (*gdsv1.DeleteServiceExportResponse, error) {
	var v validator
	key := validateExportKey(&v, req.GetClusterset(), req.GetCluster(), req.GetNamespace(), req.GetName())
	if err := v.err(); err != nil {
		return nil, err
	}
	if err := s.store.DeleteExport(ctx, key); err != nil {
		return nil, storeError(err, "delete export")
	}
	s.view.ApplyExportDelete(key)
	s.log.Debug("export deleted", zap.String("clusterset", key.ClusterSet), zap.String("cluster", key.Cluster),
		zap.String("namespace", key.Namespace), zap.String("name", key.Name))
	return &gdsv1.DeleteServiceExportResponse{}, nil
}

// ListServiceExports implements gdsv1.ServiceExportServiceServer.
func (s *Server) ListServiceExports(_ context.Context, req *gdsv1.ListServiceExportsRequest) (*gdsv1.ListServiceExportsResponse, error) {
	var v validator
	key := validateClusterKey(&v, req.GetClusterset(), req.GetCluster())
	if err := v.err(); err != nil {
		return nil, err
	}
	resp := &gdsv1.ListServiceExportsResponse{}
	for _, st := range s.view.Exports(key) {
		resp.Exports = append(resp.Exports, exportToProto(st))
	}
	return resp, nil
}

// ReplaceEndpoints implements gdsv1.ServiceExportServiceServer.
func (s *Server) ReplaceEndpoints(ctx context.Context, req *gdsv1.ReplaceEndpointsRequest) (*gdsv1.ReplaceEndpointsResponse, error) {
	var v validator
	key := validateExportKey(&v, req.GetClusterset(), req.GetCluster(), req.GetNamespace(), req.GetName())
	if err := v.err(); err != nil {
		return nil, err
	}
	if n := len(req.GetEndpoints()); n > gds.MaxEndpointsPerExport {
		return nil, tooManyEndpoints(n)
	}
	eps := make([]store.Endpoint, 0, len(req.GetEndpoints()))
	var ready int32
	for i, e := range req.GetEndpoints() {
		v.endpoint(fmt.Sprintf("endpoints[%d]", i), e)
		ep := convert.EndpointFromProto(e)
		if ep.AddressType == "" {
			ep.AddressType = convert.AddressTypeFromProto(addressType(ep.Addresses))
		}
		for j := range ep.Ports {
			if ep.Ports[j].Protocol == "" {
				ep.Ports[j].Protocol = store.ProtocolTCP
			}
		}
		if ep.Conditions.Ready {
			ready++
		}
		eps = append(eps, ep)
	}
	if err := v.err(); err != nil {
		return nil, err
	}
	if err := s.requireCluster(ctx, key.ClusterKey()); err != nil {
		return nil, err
	}
	now := s.now()
	storedGen, _ := s.view.EndpointSetGeneration(key)
	gen, assigned := generation(req.GetGeneration(), storedGen, now)
	rec := store.EndpointSetRecord{Key: key, Endpoints: eps, Generation: gen, UpdatedAt: now}
	err := s.store.PutEndpointSet(ctx, rec)
	if cur, stale := store.StaleGeneration(err); stale && assigned {
		rec.Generation = registry.NextGeneration(cur, now)
		err = s.store.PutEndpointSet(ctx, rec)
	}
	if cur, stale := store.StaleGeneration(err); stale {
		return nil, staleGeneration("endpoint set", rec.Generation, cur)
	}
	if err != nil {
		return nil, storeError(err, "replace endpoints")
	}
	s.view.ApplyEndpointSet(rec)
	return &gdsv1.ReplaceEndpointsResponse{
		Generation:         rec.Generation,
		EndpointCount:      int32(len(eps)),
		ReadyEndpointCount: ready,
	}, nil
}

// GetServiceImport implements gdsv1.ServiceImportServiceServer.
func (s *Server) GetServiceImport(_ context.Context, req *gdsv1.GetServiceImportRequest) (*gdsv1.GetServiceImportResponse, error) {
	var v validator
	v.dnsLabel("clusterset", req.GetClusterset())
	v.dnsLabel("namespace", req.GetNamespace())
	v.dnsLabel("name", req.GetName())
	if err := v.err(); err != nil {
		return nil, err
	}
	o, ok := s.view.Get(req.GetClusterset(), view.KindServiceImport, view.ImportKey(req.GetNamespace(), req.GetName()))
	if !ok {
		return nil, notFound("no ServiceImport %s/%s in clusterset %q", req.GetNamespace(), req.GetName(), req.GetClusterset())
	}
	return &gdsv1.GetServiceImportResponse{ServiceImport: o.Message(req.GetIncludeEndpoints()).(*gdsv1.ServiceImport)}, nil
}

func importFilter(v *validator, namespace string, selector *gdsv1.LabelSelector, endpoints bool) view.ImportFilter {
	v.optionalDNSLabel("namespace", namespace)
	v.selector("selector", selector)
	return view.ImportFilter{Namespace: namespace, Selector: selector.GetMatchLabels(), Endpoints: endpoints}
}

// ListServiceImports implements gdsv1.ServiceImportServiceServer.
func (s *Server) ListServiceImports(_ context.Context, req *gdsv1.ListServiceImportsRequest) (*gdsv1.ListServiceImportsResponse, error) {
	var v validator
	v.dnsLabel("clusterset", req.GetClusterset())
	f := importFilter(&v, req.GetNamespace(), req.GetSelector(), req.GetIncludeEndpoints())
	if err := v.err(); err != nil {
		return nil, err
	}
	msgs, rev := s.view.List(req.GetClusterset(), f)
	resp := &gdsv1.ListServiceImportsResponse{Revision: rev}
	for _, m := range msgs {
		resp.ServiceImports = append(resp.ServiceImports, m.(*gdsv1.ServiceImport))
	}
	return resp, nil
}

// WatchServiceImports implements gdsv1.ServiceImportServiceServer.
func (s *Server) WatchServiceImports(req *gdsv1.WatchServiceImportsRequest, stream grpc.ServerStreamingServer[gdsv1.WatchServiceImportsResponse]) error {
	var v validator
	v.dnsLabel("clusterset", req.GetClusterset())
	f := importFilter(&v, req.GetNamespace(), req.GetSelector(), req.GetIncludeEndpoints())
	if err := v.err(); err != nil {
		return err
	}
	return watchError(s.hub.Watch(stream.Context(), req.GetClusterset(), f, req.GetResumeRevision(), func(ev view.WatchEvent) error {
		resp := &gdsv1.WatchServiceImportsResponse{Type: ev.Type, Revision: ev.Revision}
		if ev.Object != nil {
			resp.ServiceImport = ev.Object.(*gdsv1.ServiceImport)
		}
		return stream.Send(resp)
	}))
}
