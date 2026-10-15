// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import axios from 'axios';

const api = axios.create({
  baseURL: '/api/v1',
  headers: { 'Content-Type': 'application/json' },
});

// Attach X-User header from localStorage on every request
api.interceptors.request.use((config) => {
  const user = localStorage.getItem('nvsnap-user') || 'anonymous';
  config.headers['X-User'] = user;
  return config;
});

// --- Types ---

export interface NodeInfo {
  name: string;
  status: string;
  gpuCount: number;
  gpuModel: string;
  agentReady: boolean;
  podCount: number;
  internalIP: string;
  createdAt: string;
}

export interface PodInfo {
  name: string;
  namespace: string;
  nodeName: string;
  status: string;
  gpuCount: number;
  image: string;
  createdAt: string;
}

export interface CheckpointInfo {
  id: string;
  namespace: string;
  podName: string;
  podNamespace?: string; // from agent metadata.json
  containerName?: string;
  containerImage?: string;
  phase: string;
  checkpointPath?: string;
  checkpointSize?: number;
  nodeName?: string;
  message?: string;
  startTime?: string;
  completionTime?: string;
  createdAt: string;
  source?: string;
}

export interface RestoreInfo {
  id: string;
  namespace: string;
  checkpointName: string;
  phase: string;
  newPodName?: string;
  nodeName?: string;
  message?: string;
  startTime?: string;
  completionTime?: string;
  createdAt: string;
}

// --- API calls ---

export async function fetchNodes(): Promise<NodeInfo[]> {
  const { data } = await api.get<{ nodes: NodeInfo[]; count: number }>('/nodes');
  return data.nodes || [];
}

export async function fetchNodePods(nodeName: string): Promise<PodInfo[]> {
  const { data } = await api.get<{ pods: PodInfo[]; count: number }>(`/nodes/${nodeName}/pods`);
  return data.pods || [];
}

export async function fetchPods(namespace?: string): Promise<PodInfo[]> {
  const params = namespace ? { namespace } : {};
  const { data } = await api.get<{ pods: PodInfo[]; count: number }>('/pods', { params });
  return data.pods || [];
}

export async function fetchCheckpoints(namespace?: string, source?: string): Promise<CheckpointInfo[]> {
  const params: Record<string, string> = {};
  if (namespace) params.namespace = namespace;
  if (source) params.source = source;
  const { data } = await api.get<{ checkpoints: CheckpointInfo[]; count: number }>('/checkpoints', { params });
  // Normalize agent-source checkpoints: map metadata.json fields to UI fields
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return (data.checkpoints || []).map((c: any) => {
    // podNamespace → namespace
    if (!c.namespace && c.podNamespace) c.namespace = c.podNamespace as string;
    // Parse from ID as fallback (format: pod__ns__timestamp)
    if (!c.namespace && c.id) {
      const parts = c.id.split('__');
      if (parts.length >= 3) {
        c.podName = c.podName || parts[0];
        c.namespace = parts[1];
      }
    }
    // containerImage → image display
    if (!c.containerName && c.containerImage) c.containerName = (c.containerImage as string).split('/').pop()?.split(':')[0];
    return c as CheckpointInfo;
  });
}

export async function fetchCheckpoint(id: string, namespace?: string): Promise<CheckpointInfo> {
  const params = namespace ? { namespace } : {};
  const { data } = await api.get<CheckpointInfo>(`/checkpoints/${id}`, { params });
  return data;
}

export async function createCheckpoint(req: {
  podName: string;
  namespace: string;
  containerName?: string;
  leaveRunning?: boolean;
}): Promise<{ id: string; phase: string; message: string }> {
  const { data } = await api.post('/checkpoints', req);
  return data;
}

export async function deleteCheckpoint(id: string, namespace?: string): Promise<void> {
  const params = namespace ? { namespace } : {};
  await api.delete(`/checkpoints/${id}`, { params });
}

export async function fetchRestores(namespace?: string): Promise<RestoreInfo[]> {
  const params = namespace ? { namespace } : {};
  const { data } = await api.get<{ restores: RestoreInfo[]; count: number }>('/restores', { params });
  return data.restores || [];
}

export async function fetchRestore(id: string, namespace?: string): Promise<RestoreInfo> {
  const params = namespace ? { namespace } : {};
  const { data } = await api.get<RestoreInfo>(`/restores/${id}`, { params });
  return data;
}

export async function createRestore(req: {
  checkpointName: string;
  checkpointId?: string;
  newPodName?: string;
  nodeName?: string;
  namespace: string;
}): Promise<{ id: string; phase: string; message: string }> {
  const { data } = await api.post('/restores', req);
  return data;
}

export async function fetchHealth(): Promise<{ status: string }> {
  const { data } = await api.get<{ status: string }>('/health');
  return data;
}

// --- Blobstore types ---

export interface BlobstoreDiskStats {
  total_bytes: number;
  free_bytes: number;
  used_bytes: number;
}

export interface BlobstoreStats {
  blob_count: number;
  blob_bytes: number;
  capture_count: number;
  manifest_count: number;
  disk: BlobstoreDiskStats | null;
}

export interface BlobstoreCapture {
  hash: string;
  file_count: number;
  total_bytes: number;
  has_manifest: boolean;
  modified_at: string;
}

export async function fetchBlobstoreStats(): Promise<BlobstoreStats> {
  const { data } = await api.get<BlobstoreStats>('/blobstore/stats');
  return data;
}

export async function fetchBlobstoreCaptures(): Promise<BlobstoreCapture[]> {
  const { data } = await api.get<{ captures: BlobstoreCapture[]; total: number }>('/blobstore/captures');
  return data.captures || [];
}

// --- Demo types ---

export type DemoPhase =
  | 'IDLE'
  | 'DEPLOYING'
  | 'RUNNING'
  | 'CHECKPOINTING'
  | 'CHECKPOINTED'
  | 'RESTORING'
  | 'RESTORED';

export interface DemoCheckpoint {
  id: string;
  size: number;
  duration: number;
}

export interface DemoState {
  phase: DemoPhase;
  workloadType: string;
  podName: string;
  podStatus: string;
  nodeName: string;
  message: string;
  error?: string;
  checkpoints: DemoCheckpoint[];
  deployDuration: number;
  checkpointDuration: number;
  restoreDuration: number;
  startedAt?: string;
}

export interface InferenceResponse {
  text: string;
  tokens: number;
  latency: number;
}

// --- Demo API calls ---

export async function fetchDemoState(): Promise<DemoState> {
  const { data } = await api.get<DemoState>('/demo/state');
  return data;
}

export async function demoDeploy(workloadType: string): Promise<{ message: string }> {
  const { data } = await api.post('/demo/deploy', { workloadType });
  return data;
}

export async function demoInference(prompt: string, maxTokens?: number): Promise<InferenceResponse> {
  const { data } = await api.post<InferenceResponse>('/demo/inference', { prompt, maxTokens: maxTokens || 50 });
  return data;
}

export async function demoCheckpoint(): Promise<{ message: string }> {
  const { data } = await api.post('/demo/checkpoint', {});
  return data;
}

export async function demoRestore(checkpointId?: string, targetNode?: string): Promise<{ message: string }> {
  const { data } = await api.post('/demo/restore', { checkpointId, targetNode });
  return data;
}

export async function demoCleanup(): Promise<{ message: string }> {
  const { data } = await api.delete('/demo/workload');
  return data;
}

export async function fetchDemoManifest(workload?: string, type?: string): Promise<{ yaml: string; type: string; workload: string }> {
  const params: Record<string, string> = {};
  if (workload) params.workload = workload;
  if (type) params.type = type;
  const { data } = await api.get('/demo/manifest', { params });
  return data;
}

// DemoWorkload describes one entry in the workload catalog the server
// loads from /etc/nvsnap/workloads at startup. The Demo UI fetches this
// list on mount and renders tiles; no hardcoded set in JS.
export interface DemoWorkload {
  id: string;
  name: string;
  desc: string;
  model: string;
  port: number;
  gpus: number;
  path: 'criu' | 'rootfs';
  ckpt_size: string;
}

export async function fetchDemoWorkloads(): Promise<DemoWorkload[]> {
  const { data } = await api.get<DemoWorkload[]>('/demo/workloads');
  return data;
}

export async function demoScaleOut(replicas: number = 2): Promise<{ message: string; replicas: string[] }> {
  const { data } = await api.post('/demo/scale-out', { replicas });
  return data;
}

export async function demoCleanTestPods(): Promise<{ deleted: number; message: string }> {
  const { data } = await api.delete('/demo/test-pods');
  return data;
}

// --- Demo pods ---

export interface DemoPod {
  name: string;
  ready: string;
  status: string;
  restarts: number;
  age: string;
}

export async function fetchDemoPods(): Promise<DemoPod[]> {
  const { data } = await api.get<{ pods: DemoPod[] }>('/demo/pods');
  return data.pods || [];
}

// --- Checkpoint file browser ---

export interface CheckpointFileEntry {
  name: string;
  isDir: boolean;
  size: number;
}

export async function fetchCheckpointFiles(path?: string): Promise<{ files: CheckpointFileEntry[]; path: string }> {
  const params = path ? { path } : {};
  const { data } = await api.get('/demo/checkpoint/files', { params });
  return data;
}

export async function fetchCheckpointFileContent(path: string): Promise<string> {
  const { data } = await api.get('/demo/checkpoint/file', { params: { path }, responseType: 'text' });
  return typeof data === 'string' ? data : JSON.stringify(data, null, 2);
}

// --- Retention Policies ---

export interface RetentionPolicy {
  id: number;
  name: string;
  namespace: string;
  workloadType: string;
  maxCount: number;
  maxAgeHours: number;
  maxTotalBytes: number;
  createdAt: string;
  updatedAt: string;
}

export async function fetchRetentionPolicies(): Promise<RetentionPolicy[]> {
  const { data } = await api.get<{ policies: RetentionPolicy[] }>('/retention-policies');
  return data.policies || [];
}

export async function createRetentionPolicy(policy: Partial<RetentionPolicy>): Promise<RetentionPolicy> {
  const { data } = await api.post<RetentionPolicy>('/retention-policies', policy);
  return data;
}

export async function deleteRetentionPolicy(id: number): Promise<void> {
  await api.delete(`/retention-policies/${id}`);
}

// --- Audit Log ---

export interface AuditEntry {
  id: number;
  timestamp: string;
  action: string;
  resource: string;
  resourceId: string;
  actor: string;
  message: string;
  status: string;
}

export async function fetchAuditLog(params?: { action?: string; resource?: string; limit?: number }): Promise<AuditEntry[]> {
  const { data } = await api.get<{ entries: AuditEntry[] }>('/audit', { params });
  return data.entries || [];
}

// --- Observability discovery ---
//
// /api/v1/observability returns the in-cluster Grafana/Jaeger/Prometheus
// services nvsnap-server detects. The sidebar renders a nav entry per
// target where available=true; clicks open /observability/<name>/
// which nvsnap-server reverse-proxies to the in-cluster Service. One
// external URL serves the whole observability stack (single pane).
export interface ObservabilityTarget {
  name: string;          // "grafana" | "jaeger" | "prometheus"
  displayName: string;
  description: string;
  available: boolean;
  url: string;           // "/observability/grafana/"
}

export async function fetchObservabilityTargets(): Promise<ObservabilityTarget[]> {
  const { data } = await api.get<{ targets: ObservabilityTarget[] }>('/observability');
  return data.targets || [];
}
