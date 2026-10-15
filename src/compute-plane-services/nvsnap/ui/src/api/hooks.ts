// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  fetchNodes,
  fetchPods,
  fetchCheckpoints,
  fetchCheckpoint,
  createCheckpoint,
  deleteCheckpoint,
  fetchRestores,
  createRestore,
  fetchHealth,
  fetchDemoState,
  demoDeploy,
  demoInference,
  demoCheckpoint,
  demoRestore,
  demoCleanup,
  demoCleanTestPods,
  fetchRetentionPolicies,
  createRetentionPolicy,
  deleteRetentionPolicy,
  fetchAuditLog,
  fetchBlobstoreStats,
  fetchBlobstoreCaptures,
  type RetentionPolicy,
} from './client';

export function useNodes() {
  return useQuery({ queryKey: ['nodes'], queryFn: fetchNodes });
}

export function useBlobstoreStats() {
  return useQuery({ queryKey: ['blobstore', 'stats'], queryFn: fetchBlobstoreStats, refetchInterval: 5000 });
}

export function useBlobstoreCaptures() {
  return useQuery({ queryKey: ['blobstore', 'captures'], queryFn: fetchBlobstoreCaptures, refetchInterval: 5000 });
}

export function usePods(namespace?: string) {
  return useQuery({ queryKey: ['pods', namespace], queryFn: () => fetchPods(namespace) });
}

export function useCheckpoints(namespace?: string, source?: string) {
  return useQuery({ queryKey: ['checkpoints', namespace, source], queryFn: () => fetchCheckpoints(namespace, source) });
}

export function useCheckpoint(id: string, namespace?: string) {
  return useQuery({
    queryKey: ['checkpoint', id, namespace],
    queryFn: () => fetchCheckpoint(id, namespace),
    refetchInterval: 3000, // Poll for status updates
  });
}

export function useCreateCheckpoint() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: createCheckpoint,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['checkpoints'] });
    },
  });
}

export function useDeleteCheckpoint() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, namespace }: { id: string; namespace?: string }) => deleteCheckpoint(id, namespace),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['checkpoints'] });
    },
  });
}

export function useRestores(namespace?: string) {
  return useQuery({ queryKey: ['restores', namespace], queryFn: () => fetchRestores(namespace) });
}

export function useCreateRestore() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: createRestore,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['restores'] });
    },
  });
}

export function useHealth() {
  return useQuery({ queryKey: ['health'], queryFn: fetchHealth, refetchInterval: 30000 });
}

// --- Demo hooks ---

export function useDemoState() {
  return useQuery({
    queryKey: ['demo-state'],
    queryFn: fetchDemoState,
    refetchInterval: 2000,
  });
}

export function useDemoDeploy() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (workloadType: string) => demoDeploy(workloadType),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['demo-state'] }),
  });
}

export function useDemoInference() {
  return useMutation({
    mutationFn: ({ prompt, maxTokens }: { prompt: string; maxTokens?: number }) =>
      demoInference(prompt, maxTokens),
  });
}

export function useDemoCheckpoint() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => demoCheckpoint(),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['demo-state'] }),
  });
}

export function useDemoRestore() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (args?: { checkpointId?: string; targetNode?: string }) =>
      demoRestore(args?.checkpointId, args?.targetNode),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['demo-state'] }),
  });
}

export function useDemoCleanup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => demoCleanup(),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['demo-state'] }),
  });
}

export function useDemoCleanTestPods() {
  return useMutation({
    mutationFn: () => demoCleanTestPods(),
  });
}

// --- Retention Policies ---

export function useRetentionPolicies() {
  return useQuery({ queryKey: ['retention-policies'], queryFn: fetchRetentionPolicies });
}

export function useCreateRetentionPolicy() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (policy: Partial<RetentionPolicy>) => createRetentionPolicy(policy),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['retention-policies'] }),
  });
}

export function useDeleteRetentionPolicy() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => deleteRetentionPolicy(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['retention-policies'] }),
  });
}

// --- Audit Log ---

export function useAuditLog(params?: { action?: string; resource?: string; limit?: number }) {
  return useQuery({ queryKey: ['audit', params], queryFn: () => fetchAuditLog(params) });
}
