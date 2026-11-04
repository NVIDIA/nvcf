import { useState, useEffect, useRef } from 'react';
import { motion } from 'framer-motion';
import clsx from 'clsx';
import {
  useDemoDeploy,
  useDemoInference,
  useDemoCheckpoint,
  useDemoRestore,
  useDemoCleanup,
  useDemoCleanTestPods,
  useNodes,
} from '../api/hooks';
import { useDemoStateWS, useDemoPodsWS, useDemoLogsWS } from '../api/ws-hooks';
import type { LogEntry } from '../api/ws-hooks';
import type { DemoPhase, InferenceResponse, CheckpointFileEntry } from '../api/client';
import axios from 'axios';
import { fetchCheckpointFiles, fetchCheckpointFileContent, demoScaleOut, fetchDemoManifest, fetchDemoWorkloads, demoInference } from '../api/client';
import type { DemoWorkload } from '../api/client';

// --- Step Progress Bar ---

const steps = [
  { id: 1, label: 'Deploy', phases: ['DEPLOYING', 'RUNNING'] as DemoPhase[] },
  { id: 2, label: 'Inference', phases: ['RUNNING'] as DemoPhase[] },
  { id: 3, label: 'Capture', phases: ['CHECKPOINTING', 'CHECKPOINTED'] as DemoPhase[] },
  { id: 4, label: 'Restore', phases: ['RESTORING', 'RESTORED'] as DemoPhase[] },
  { id: 5, label: 'Verify', phases: ['RESTORED'] as DemoPhase[] },
];

function stepStatus(stepId: number, phase: DemoPhase): 'done' | 'active' | 'future' {
  const phaseOrder: Record<DemoPhase, number> = {
    IDLE: 0,
    DEPLOYING: 1,
    RUNNING: 2,
    CHECKPOINTING: 3,
    CHECKPOINTED: 4,
    RESTORING: 5,
    RESTORED: 6,
  };
  const p = phaseOrder[phase] || 0;

  // Map step to its "completion" threshold
  const completionThresholds: Record<number, number> = {
    1: 2,  // Deploy done when RUNNING
    2: 3,  // Inference done when CHECKPOINTING starts
    3: 4,  // Checkpoint done when CHECKPOINTED
    4: 6,  // Restore done when RESTORED
    5: 6,  // Verify done when RESTORED (user verifies via inference panel)
  };

  const activeRanges: Record<number, [number, number]> = {
    1: [1, 1],  // Active during DEPLOYING
    2: [2, 2],  // Active during RUNNING
    3: [3, 3],  // Active during CHECKPOINTING
    4: [5, 5],  // Active during RESTORING
    5: [99, 99],  // Verify — never active (goes straight to done)
  };

  const threshold = completionThresholds[stepId];
  if (p >= threshold) return 'done';

  const [lo, hi] = activeRanges[stepId];
  if (p >= lo && p <= hi) return 'active';

  return 'future';
}

function StepProgressBar({ phase }: { phase: DemoPhase }) {
  if (phase === 'IDLE') return null;

  return (
    <div className="flex items-center justify-center gap-2 mb-8">
      {steps.map((step, i) => {
        const status = stepStatus(step.id, phase);
        return (
          <div key={step.id} className="flex items-center">
            <div className="flex flex-col items-center">
              <div
                className={clsx(
                  'w-8 h-8 rounded-full flex items-center justify-center text-sm font-medium transition-all duration-300',
                  status === 'done' && 'bg-gpu-500 text-white',
                  status === 'active' && 'bg-gpu-500/30 text-gpu-400 ring-2 ring-gpu-500 ring-offset-2 ring-offset-terminal-bg',
                  status === 'future' && 'bg-terminal-border text-terminal-muted'
                )}
              >
                {status === 'done' ? (
                  <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={3}>
                    <path strokeLinecap="round" strokeLinejoin="round" d="M5 13l4 4L19 7" />
                  </svg>
                ) : (
                  step.id
                )}
                {status === 'active' && (
                  <span className="absolute w-8 h-8 rounded-full animate-ping bg-gpu-500/20" />
                )}
              </div>
              <span
                className={clsx(
                  'text-xs mt-1.5 font-medium',
                  status === 'done' && 'text-gpu-400',
                  status === 'active' && 'text-gpu-400',
                  status === 'future' && 'text-terminal-muted'
                )}
              >
                {step.label}
              </span>
            </div>
            {i < steps.length - 1 && (
              <div
                className={clsx(
                  'w-12 h-0.5 mx-2 mb-5 transition-colors duration-300',
                  stepStatus(step.id, phase) === 'done' ? 'bg-gpu-500' : 'bg-terminal-border'
                )}
              />
            )}
          </div>
        );
      })}
    </div>
  );
}

// --- Elapsed Timer ---

function ElapsedTimer({ startedAt }: { startedAt?: string }) {
  const [elapsed, setElapsed] = useState(0);

  useEffect(() => {
    if (!startedAt) {
      setElapsed(0);
      return;
    }
    const start = new Date(startedAt).getTime();
    const interval = setInterval(() => {
      setElapsed(Math.floor((Date.now() - start) / 1000));
    }, 1000);
    return () => clearInterval(interval);
  }, [startedAt]);

  if (!startedAt) return null;

  const mins = Math.floor(elapsed / 60);
  const secs = elapsed % 60;
  return (
    <span className="font-mono text-2xl text-gpu-400">
      {mins}:{secs.toString().padStart(2, '0')}
    </span>
  );
}

// --- Format helpers ---

function formatDuration(seconds: number): string {
  if (seconds <= 0) return '0s';
  const m = Math.floor(seconds / 60);
  const s = Math.round(seconds % 60);
  if (m === 0) return `${s}s`;
  return `${m}m ${s.toString().padStart(2, '0')}s`;
}

function formatBytes(bytes: number): string {
  if (bytes <= 0) return '0 B';
  const gb = bytes / (1024 * 1024 * 1024);
  if (gb >= 1) return `${gb.toFixed(1)} GB`;
  const mb = bytes / (1024 * 1024);
  if (mb >= 1) return `${mb.toFixed(1)} MB`;
  const kb = bytes / 1024;
  if (kb >= 1) return `${kb.toFixed(1)} KB`;
  return `${bytes} B`;
}

// --- Idle Panel ---

function IdlePanel({ onDeploy }: { onDeploy: (wt: string) => void }) {
  // Workload tiles are populated dynamically from the server's catalog
  // at /api/v1/demo/workloads. The server reads /etc/nvsnap/workloads/*.yaml
  // at startup and parses nvsnap.io/* annotations on each Pod's metadata
  // to build the catalog entries — see internal/server/manifests.go.
  // Adding a new workload tile: drop a yaml pair in deploy/k8s/workloads/
  // and rebuild the nvsnap-server image.
  const [workloads, setWorkloads] = useState<DemoWorkload[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    fetchDemoWorkloads()
      .then((ws) => { if (!cancelled) setWorkloads(ws); })
      .catch((e) => { if (!cancelled) setError(e?.message ?? 'failed to load workloads'); });
    return () => { cancelled = true; };
  }, []);

  if (error) {
    return (
      <div className="text-center py-12">
        <h2 className="text-2xl font-bold text-terminal-text mb-2">GPU Snapshot / Restore Demo</h2>
        <p className="text-red-400 mb-4">Failed to load workload catalog</p>
        <p className="text-terminal-muted text-sm font-mono">{error}</p>
      </div>
    );
  }
  if (!workloads) {
    return (
      <div className="text-center py-12">
        <h2 className="text-2xl font-bold text-terminal-text mb-2">GPU Snapshot / Restore Demo</h2>
        <p className="text-terminal-muted">Loading workload catalog…</p>
      </div>
    );
  }
  if (workloads.length === 0) {
    return (
      <div className="text-center py-12">
        <h2 className="text-2xl font-bold text-terminal-text mb-2">GPU Snapshot / Restore Demo</h2>
        <p className="text-terminal-muted">
          No workloads in catalog. Mount yaml pairs into <code>/etc/nvsnap/workloads</code>
          {' '}on the nvsnap-server pod (see <code>deploy/k8s/workloads/</code> in the repo).
        </p>
      </div>
    );
  }

  return (
    <div className="text-center">
      <h2 className="text-2xl font-bold text-terminal-text mb-2">GPU Snapshot / Restore Demo</h2>
      <p className="text-terminal-muted mb-8">Select a workload to deploy on GPU</p>
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6 max-w-6xl mx-auto">
        {workloads.map((w) => (
          <div key={w.id}>
            <button
              onClick={() => onDeploy(w.id)}
              className="group p-6 rounded-xl border-2 border-terminal-border hover:border-gpu-500 bg-terminal-surface transition-all duration-200 text-left w-full"
            >
              <div className="flex items-center gap-3 mb-3">
                <div className="p-2 rounded-lg bg-gpu-500/20 group-hover:bg-gpu-500/30 transition-colors">
                  <svg className="w-6 h-6 text-gpu-400" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={1.5} d="M8.25 3v1.5M4.5 8.25H3m18 0h-1.5M4.5 12H3m18 0h-1.5m-15 3.75H3m18 0h-1.5M8.25 19.5V21M12 3v1.5m0 15V21m3.75-18v1.5m0 15V21m-9-1.5h10.5a2.25 2.25 0 002.25-2.25V6.75a2.25 2.25 0 00-2.25-2.25H6.75A2.25 2.25 0 004.5 6.75v10.5a2.25 2.25 0 002.25 2.25z" />
                  </svg>
                </div>
                <div>
                  <h3 className="text-lg font-bold text-terminal-text">{w.name}</h3>
                  <span className="text-sm text-gpu-400">{w.desc || w.model}</span>
                </div>
              </div>
              <div className="flex gap-3 text-xs flex-wrap">
                <span className="px-2 py-0.5 rounded bg-gpu-500/10 text-gpu-400">{w.gpus}× H100</span>
                {w.ckpt_size && (
                  <span className="px-2 py-0.5 rounded bg-terminal-bg text-terminal-muted">{w.ckpt_size}</span>
                )}
                <span
                  className={
                    'px-2 py-0.5 rounded font-mono ' +
                    (w.path === 'rootfs'
                      ? 'bg-fuchsia-500/15 text-fuchsia-300 border border-fuchsia-500/30'
                      : 'bg-blue-500/15 text-blue-300 border border-blue-500/30')
                  }
                  title={
                    w.path === 'rootfs'
                      ? 'rootfs path: agent watcher captures rootfs upperdir + caches; webhook injects bind mounts on restore. Multi-GPU.'
                      : 'CRIU path: process + GPU state via cuda-checkpoint. Single-GPU; in-memory state preserved across restore.'
                  }
                >
                  {w.path === 'rootfs' ? 'rootfs' : 'CRIU'}
                </span>
              </div>
            </button>
            <div className="mt-1 text-center"><YamlViewer workload={w.id} type="deploy" /></div>
          </div>
        ))}
      </div>
    </div>
  );
}

// --- Progress Panel (Deploying / Checkpointing / Restoring) ---

function ProgressPanel({ message, startedAt }: { message: string; startedAt?: string }) {
  return (
    <div className="text-center py-12">
      <div className="mb-6">
        <div className="inline-block relative">
          <div className="w-16 h-16 rounded-full border-4 border-terminal-border border-t-gpu-500 animate-spin" />
        </div>
      </div>
      <ElapsedTimer startedAt={startedAt} />
      <p className="text-terminal-muted mt-4 text-lg">{message}</p>
    </div>
  );
}

// --- Inference Panel ---

function InferencePanel({ isRestored }: { isRestored: boolean }) {
  const inference = useDemoInference();
  const [prompt, setPrompt] = useState('The meaning of life is');
  const [result, setResult] = useState<InferenceResponse | null>(null);

  const handleSubmit = () => {
    setResult(null);
    inference.mutate(
      { prompt, maxTokens: 100 },
      { onSuccess: (data) => setResult(data) }
    );
  };

  return (
    <div className="bg-terminal-surface rounded-xl border border-terminal-border p-6">
      <div className="flex items-center gap-2 mb-4">
        <h3 className="text-lg font-bold text-terminal-text">
          {isRestored ? 'Verify Inference' : 'Test Inference'}
        </h3>
        {isRestored && (
          <span className="px-2 py-0.5 text-xs font-medium rounded-full bg-gpu-500/20 text-gpu-400">
            Post-Restore
          </span>
        )}
      </div>
      <div className="space-y-4">
        <div>
          <label className="block text-sm text-terminal-muted mb-1">Prompt</label>
          <textarea
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            rows={2}
            className="w-full bg-terminal-bg border border-terminal-border rounded-lg px-3 py-2 text-terminal-text text-sm focus:outline-none focus:ring-2 focus:ring-gpu-500 focus:border-transparent resize-none"
          />
        </div>
        <button
          onClick={handleSubmit}
          disabled={inference.isPending || !prompt}
          className="px-4 py-2 rounded-lg bg-gpu-500 text-white font-medium text-sm hover:bg-gpu-600 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
        >
          {inference.isPending ? 'Generating...' : 'Send'}
        </button>
        {inference.isError && (
          <div className="p-3 rounded-lg bg-red-500/10 border border-red-500/30 text-red-400 text-sm">
            {(inference.error as Error).message}
          </div>
        )}
        {result && (
          <div className="p-4 rounded-lg bg-terminal-bg border border-terminal-border">
            <p className="text-terminal-text text-sm whitespace-pre-wrap">{result.text}</p>
            <div className="flex gap-4 mt-3 text-xs text-terminal-muted">
              <span>{result.tokens} tokens</span>
              <span>{result.latency.toFixed(2)}s latency</span>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

// --- Running Panel ---

function RunningPanel({ onCheckpoint }: { onCheckpoint: () => void }) {
  const checkpoint = useDemoCheckpoint();

  return (
    <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
      <InferencePanel isRestored={false} />
      <div className="flex flex-col items-center justify-center p-6 bg-terminal-surface rounded-xl border border-terminal-border">
        <svg className="w-16 h-16 text-terminal-muted mb-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={1.5} d="M20.25 7.5l-.625 10.632a2.25 2.25 0 01-2.247 2.118H6.622a2.25 2.25 0 01-2.247-2.118L3.75 7.5m8.25 3v6.75m0 0l-3-3m3 3l3-3M3.375 7.5h17.25c.621 0 1.125-.504 1.125-1.125v-1.5c0-.621-.504-1.125-1.125-1.125H3.375c-.621 0-1.125.504-1.125 1.125v1.5c0 .621.504 1.125 1.125 1.125z" />
        </svg>
        <h3 className="text-lg font-bold text-terminal-text mb-2">Capture GPU State</h3>
        <p className="text-sm text-terminal-muted mb-4 text-center">
          Save the entire model state including GPU memory to disk
        </p>
        <button
          onClick={() => {
            checkpoint.mutate();
            onCheckpoint();
          }}
          disabled={checkpoint.isPending}
          className="px-6 py-3 rounded-lg bg-gpu-500 text-white font-medium hover:bg-gpu-600 disabled:opacity-50 transition-colors"
        >
          Capture GPU State
        </button>
      </div>
    </div>
  );
}

// --- Checkpoint Browser ---

function CheckpointBrowser() {
  const [currentPath, setCurrentPath] = useState('');
  const [files, setFiles] = useState<CheckpointFileEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [fileContent, setFileContent] = useState<string | null>(null);
  const [viewingFile, setViewingFile] = useState('');

  useEffect(() => {
    setLoading(true);
    fetchCheckpointFiles(currentPath || undefined)
      .then((data) => setFiles(data.files || []))
      .catch(() => setFiles([]))
      .finally(() => setLoading(false));
  }, [currentPath]);

  const breadcrumbs = currentPath ? currentPath.split('/').filter(Boolean) : [];

  const handleFileClick = (file: CheckpointFileEntry) => {
    if (file.isDir) {
      setFileContent(null);
      setViewingFile('');
      setCurrentPath(currentPath ? `${currentPath}/${file.name}` : file.name);
      return;
    }
    const ext = file.name.split('.').pop()?.toLowerCase() || '';
    if (['json', 'log', 'txt', 'cfg', 'conf'].includes(ext) && file.size <= 1024 * 1024) {
      const filePath = currentPath ? `${currentPath}/${file.name}` : file.name;
      setViewingFile(file.name);
      fetchCheckpointFileContent(filePath)
        .then((content) => setFileContent(content))
        .catch((err) => setFileContent(`Error: ${err.message}`));
    }
  };

  const navigateTo = (index: number) => {
    setFileContent(null);
    setViewingFile('');
    if (index < 0) {
      setCurrentPath('');
    } else {
      setCurrentPath(breadcrumbs.slice(0, index + 1).join('/'));
    }
  };

  return (
    <div className="mt-4 bg-terminal-bg rounded-lg border border-terminal-border overflow-hidden">
      <div className="px-4 py-2 border-b border-terminal-border flex items-center gap-2 text-sm">
        <button onClick={() => navigateTo(-1)} className="text-gpu-400 hover:text-gpu-300 font-mono">
          checkpoint/
        </button>
        {breadcrumbs.map((part, i) => (
          <span key={i} className="flex items-center gap-1">
            <span className="text-terminal-muted">/</span>
            <button
              onClick={() => navigateTo(i)}
              className={clsx(
                'font-mono',
                i === breadcrumbs.length - 1 ? 'text-terminal-text' : 'text-gpu-400 hover:text-gpu-300'
              )}
            >
              {part}
            </button>
          </span>
        ))}
      </div>

      {loading ? (
        <div className="p-4 text-center text-terminal-muted text-sm">Loading...</div>
      ) : (
        <div className="max-h-64 overflow-y-auto">
          <table className="w-full text-sm">
            <tbody>
              {files.map((file) => {
                const ext = file.name.split('.').pop()?.toLowerCase() || '';
                const isViewable = ['json', 'log', 'txt', 'cfg', 'conf'].includes(ext) && file.size <= 1024 * 1024;
                const isClickable = file.isDir || isViewable;
                return (
                  <tr
                    key={file.name}
                    onClick={() => isClickable && handleFileClick(file)}
                    className={clsx(
                      'border-b border-terminal-border/50 last:border-0',
                      isClickable && 'cursor-pointer hover:bg-terminal-surface'
                    )}
                  >
                    <td className="px-4 py-1.5 font-mono">
                      <span className={clsx(
                        file.isDir ? 'text-gpu-400' : isViewable ? 'text-terminal-text' : 'text-terminal-muted'
                      )}>
                        {file.isDir ? `${file.name}/` : file.name}
                      </span>
                    </td>
                    <td className="px-4 py-1.5 text-right text-terminal-muted whitespace-nowrap">
                      {file.isDir ? '' : formatBytes(file.size)}
                    </td>
                  </tr>
                );
              })}
              {files.length === 0 && (
                <tr>
                  <td colSpan={2} className="px-4 py-3 text-center text-terminal-muted">Empty directory</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}

      {fileContent !== null && (
        <div className="border-t border-terminal-border">
          <div className="px-4 py-2 flex items-center justify-between bg-terminal-surface">
            <span className="text-sm font-mono text-terminal-text">{viewingFile}</span>
            <button
              onClick={() => { setFileContent(null); setViewingFile(''); }}
              className="text-terminal-muted hover:text-terminal-text"
            >
              <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12" />
              </svg>
            </button>
          </div>
          <pre className="px-4 py-3 max-h-80 overflow-auto text-xs text-terminal-text font-mono whitespace-pre-wrap">
            {viewingFile.endsWith('.json')
              ? (() => { try { return JSON.stringify(JSON.parse(fileContent), null, 2); } catch { return fileContent; } })()
              : fileContent}
          </pre>
        </div>
      )}
    </div>
  );
}

// --- Checkpointed Panel ---

function YamlViewer({ workload, type }: { workload?: string; type?: string }) {
  const [yaml, setYaml] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const toggle = async () => {
    if (yaml) { setYaml(null); return; }
    setLoading(true);
    try {
      const res = await fetchDemoManifest(workload, type);
      setYaml(res.yaml);
    } catch { setYaml('Failed to load manifest'); }
    setLoading(false);
  };

  return (
    <>
      <button onClick={toggle} className="text-xs text-gpu-400 hover:text-gpu-300 transition">
        {loading ? 'Loading...' : 'View Pod YAML'}
      </button>
      {yaml && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60" onClick={() => setYaml(null)}>
          <div className="bg-terminal-surface border border-terminal-border rounded-xl w-[800px] max-h-[80vh] flex flex-col shadow-2xl text-left" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between px-6 py-3 border-b border-terminal-border">
              <h3 className="text-sm font-bold text-terminal-text">Pod Manifest — {workload} ({type})</h3>
              <button onClick={() => setYaml(null)} className="text-terminal-muted hover:text-terminal-text text-lg">&times;</button>
            </div>
            <pre className="p-6 text-[11px] leading-5 text-terminal-text overflow-auto flex-1 font-mono text-left" style={{whiteSpace: 'pre', tabSize: 2}}>{yaml}</pre>
          </div>
        </div>
      )}
    </>
  );
}

function ReplicaInference({ podName }: { podName: string }) {
  const [status, setStatus] = useState<'waiting' | 'ready' | 'testing' | 'pass' | 'fail'>('waiting');
  const [response, setResponse] = useState('');

  useEffect(() => {
    // Poll for pod readiness
    const interval = setInterval(async () => {
      try {
        const { data } = await axios.get('/api/v1/demo/pods');
        const pod = (data.pods || []).find((p: {name: string; ready: string}) => p.name === podName);
        if (pod && pod.ready === '1/1') {
          setStatus('ready');
          clearInterval(interval);
        }
      } catch { /* keep polling */ }
    }, 3000);
    return () => clearInterval(interval);
  }, [podName]);

  const testInference = async () => {
    setStatus('testing');
    try {
      const res = await demoInference('Hello', 5);
      setResponse(res.text || 'No response');
      setStatus('pass');
    } catch {
      setResponse('Failed');
      setStatus('fail');
    }
  };

  return (
    <div className="bg-terminal-bg rounded-lg p-4 text-left">
      <div className="flex items-center justify-between mb-2">
        <span className="text-xs font-mono text-terminal-text">{podName}</span>
        <span className={`text-xs px-2 py-0.5 rounded ${
          status === 'ready' || status === 'pass' ? 'bg-green-500/20 text-green-400' :
          status === 'fail' ? 'bg-red-500/20 text-red-400' :
          status === 'testing' ? 'bg-blue-500/20 text-blue-400' :
          'bg-yellow-500/20 text-yellow-400'
        }`}>{status === 'waiting' ? 'Starting...' : status === 'ready' ? 'Ready' : status === 'testing' ? 'Testing...' : status === 'pass' ? 'Inference OK' : 'Failed'}</span>
      </div>
      {status === 'ready' && (
        <button onClick={testInference} className="text-xs px-3 py-1 bg-gpu-600 hover:bg-gpu-500 text-white rounded transition">
          Verify Inference
        </button>
      )}
      {response && <p className="text-xs text-terminal-muted mt-2 font-mono">{response}</p>}
    </div>
  );
}

function ScaleOutButton() {
  const [replicas, setReplicas] = useState(2);
  const [loading, setLoading] = useState(false);
  const [result, setResult] = useState<string[] | null>(null);

  const handleScale = async () => {
    setLoading(true);
    try {
      const res = await demoScaleOut(replicas);
      setResult(res.replicas || []);
    } catch {
      setResult([]);
    }
    setLoading(false);
  };

  if (result && result.length > 0) {
    return (
      <div className="space-y-3 max-w-lg mx-auto">
        <p className="text-sm font-bold text-gpu-400 text-center">Scaled to {result.length} replicas</p>
        {result.map((pod) => (
          <ReplicaInference key={pod} podName={pod} />
        ))}
      </div>
    );
  }

  return (
    <div className="flex items-center gap-2">
      <select value={replicas} onChange={e => setReplicas(parseInt(e.target.value))}
        className="px-3 py-4 rounded-xl bg-terminal-surface border border-terminal-border text-terminal-text text-lg">
        {[2,3,4].map(n => <option key={n} value={n}>{n}</option>)}
      </select>
      <button onClick={handleScale} disabled={loading}
        className="px-8 py-4 rounded-xl bg-blue-600 text-white font-bold text-lg hover:bg-blue-500 disabled:opacity-50 transition-colors shadow-lg shadow-blue-500/20">
        {loading ? 'Scaling...' : 'Scale Out'}
      </button>
    </div>
  );
}

function CheckpointedPanel({ checkpoints, sourceNode, onRestore }: { checkpoints: { id: string; size: number; duration: number }[]; sourceNode: string; onRestore: () => void }) {
  const restore = useDemoRestore();
  const { data: nodes = [] } = useNodes();
  const latest = checkpoints[checkpoints.length - 1];

  // Default to "auto" (let the webhook pin to the capture-source node —
  // zero-byte same-node restore). The dropdown lets the user pick a
  // different GPU node to demonstrate cross-node restore (cascade fetch
  // from peer or blobstore copies the bytes to the target node first).
  const [targetNode, setTargetNode] = useState<string>('');
  const otherNodes = nodes.filter((n) => n.gpuCount > 0 && n.name !== sourceNode);
  const shortName = (n: string) => (n.includes('-') ? n.split('-').slice(-1)[0] : n);

  return (
    <div className="py-8">
      {latest && (
        <div className="max-w-2xl mx-auto mb-6">
          <div className="p-6 rounded-xl bg-terminal-surface border border-terminal-border text-left">
            <h3 className="text-lg font-bold text-terminal-text mb-3">Captured State Saved</h3>
            <div className="grid grid-cols-2 gap-x-8 gap-y-2 text-sm">
              <span className="text-terminal-muted">ID</span>
              <span className="text-terminal-text font-mono text-xs">{latest.id}</span>
              <span className="text-terminal-muted">Size</span>
              <span className="text-terminal-text">{formatBytes(latest.size)}</span>
              <span className="text-terminal-muted">Duration</span>
              <span className="text-terminal-text">{formatDuration(latest.duration)}</span>
              <span className="text-terminal-muted">Source Node</span>
              <span className="text-terminal-text font-mono text-xs">{shortName(sourceNode) || '-'}</span>
            </div>
            <CheckpointBrowser />
          </div>
        </div>
      )}
      <div className="text-center space-y-4">
        {otherNodes.length > 0 && (
          <div className="flex gap-2 justify-center items-center text-sm">
            <label className="text-terminal-muted">Restore on:</label>
            <select
              value={targetNode}
              onChange={(e) => setTargetNode(e.target.value)}
              className="px-3 py-1.5 rounded bg-terminal-surface border border-terminal-border text-terminal-text"
            >
              <option value="">Source node ({shortName(sourceNode)}) — same-node, zero copy</option>
              {otherNodes.map((n) => (
                <option key={n.name} value={n.name}>
                  Cross-node → {shortName(n.name)} (cascade fetch from {shortName(sourceNode)})
                </option>
              ))}
            </select>
          </div>
        )}
        <div className="flex gap-4 justify-center">
          <button
            onClick={() => {
              restore.mutate({ checkpointId: latest?.id, targetNode: targetNode || undefined });
              onRestore();
            }}
            disabled={restore.isPending}
            className="px-8 py-4 rounded-xl bg-gpu-500 text-white font-bold text-lg hover:bg-gpu-600 disabled:opacity-50 transition-colors shadow-lg shadow-gpu-500/20"
          >
            Restore (1 replica)
          </button>
          <ScaleOutButton />
        </div>
        <p className="text-sm text-terminal-muted">
          Restore to a single pod, or scale out to multiple replicas from the same captured state
        </p>
      </div>
    </div>
  );
}

// --- Restored Panel ---

function RestoredPanel() {
  return (
    <div>
      <InferencePanel isRestored={true} />
    </div>
  );
}

// --- Timing Comparison Banner ---

function TimingBanner({ deployDuration, restoreDuration }: { deployDuration: number; restoreDuration: number }) {
  if (deployDuration <= 0 || restoreDuration <= 0) return null;

  const maxDuration = Math.max(deployDuration, restoreDuration);
  const coldPct = (deployDuration / maxDuration) * 100;
  const restorePct = (restoreDuration / maxDuration) * 100;
  const speedup = Math.round(((deployDuration - restoreDuration) / deployDuration) * 100);

  return (
    <motion.div
      initial={{ opacity: 0, y: 20 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.5, delay: 0.3 }}
      className="mt-8 p-6 rounded-xl bg-gradient-to-br from-gpu-900/50 to-terminal-surface border border-gpu-500/30"
    >
      <h3 className="text-lg font-bold text-terminal-text mb-5 text-center">Startup Time Comparison</h3>
      <div className="space-y-4 max-w-xl mx-auto">
        <div>
          <div className="flex justify-between text-sm mb-1.5">
            <span className="text-terminal-muted">Cold Start</span>
            <span className="text-terminal-text font-mono">{formatDuration(deployDuration)}</span>
          </div>
          <div className="h-6 bg-terminal-border rounded-full overflow-hidden">
            <motion.div
              initial={{ width: 0 }}
              animate={{ width: `${coldPct}%` }}
              transition={{ duration: 0.8, delay: 0.5 }}
              className="h-full bg-terminal-muted rounded-full"
            />
          </div>
        </div>
        <div>
          <div className="flex justify-between text-sm mb-1.5">
            <span className="text-gpu-400 font-medium">NvSnap Restore</span>
            <span className="text-gpu-400 font-mono font-medium">{formatDuration(restoreDuration)}</span>
          </div>
          <div className="h-6 bg-terminal-border rounded-full overflow-hidden">
            <motion.div
              initial={{ width: 0 }}
              animate={{ width: `${restorePct}%` }}
              transition={{ duration: 0.8, delay: 0.7 }}
              className="h-full bg-gradient-to-r from-gpu-500 to-gpu-400 rounded-full"
            />
          </div>
        </div>
      </div>
      {speedup > 0 && (
        <p className="text-center mt-5 text-xl font-bold text-gpu-400">
          {speedup}% faster startup with NvSnap
        </p>
      )}
    </motion.div>
  );
}

// --- Pod Watcher ---

function PodWatcher() {
  const pods = useDemoPodsWS();
  const cleanTestPods = useDemoCleanTestPods();
  const [open, setOpen] = useState(true);

  // Count test pods (user workloads, not nvsnap infrastructure).
  // The `nvsnap-` prefix is reserved for everything the Helm chart
  // ships — agent, server, blobstore, plus the optional showcase
  // subcharts (nvsnap-jaeger-*, nvsnap-grafana-*, nvsnap-prometheus-*).
  // User test pods land under other names (vllm-small-restored,
  // fastapi-echo, etc).
  const testPodCount = pods.filter(p => !p.name.startsWith('nvsnap-')).length;

  const statusColor = (status: string) => {
    if (status === 'Running') return 'text-gpu-400';
    if (status === 'Terminating') return 'text-red-400';
    if (status.startsWith('Init') || status === 'ContainerCreating' || status === 'PodInitializing') return 'text-yellow-400';
    if (status === 'Pending') return 'text-terminal-muted';
    if (status === 'Completed' || status === 'Succeeded') return 'text-blue-400';
    return 'text-terminal-text';
  };

  return (
    <div className="mt-6 rounded-xl border border-terminal-border bg-terminal-surface overflow-hidden">
      <div className="px-4 py-2.5 flex items-center justify-between text-sm">
        <button
          onClick={() => setOpen(!open)}
          className="flex items-center gap-2 hover:opacity-80 transition-opacity"
        >
          <span className="font-mono text-terminal-muted">$</span>
          <span className="font-mono text-terminal-text">kubectl get pods -n nvsnap-system</span>
          <span className="text-terminal-muted">({pods.length})</span>
          <svg
            className={clsx('w-4 h-4 text-terminal-muted transition-transform', open && 'rotate-180')}
            fill="none" viewBox="0 0 24 24" stroke="currentColor"
          >
            <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M19 9l-7 7-7-7" />
          </svg>
        </button>
        {testPodCount > 0 && (
          <button
            onClick={() => cleanTestPods.mutate()}
            disabled={cleanTestPods.isPending}
            className="px-3 py-1 rounded text-xs border border-red-500/30 text-red-400 hover:bg-red-500/10 disabled:opacity-50 transition-colors"
          >
            {cleanTestPods.isPending ? 'Cleaning...' : `Clean ${testPodCount} test pod(s)`}
          </button>
        )}
      </div>
      {open && (
        <div className="border-t border-terminal-border bg-terminal-bg px-4 py-2 font-mono text-xs overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="text-terminal-muted">
                <th className="text-left pr-6 py-1 font-medium">NAME</th>
                <th className="text-left pr-6 py-1 font-medium">READY</th>
                <th className="text-left pr-6 py-1 font-medium">STATUS</th>
                <th className="text-left pr-6 py-1 font-medium">RESTARTS</th>
                <th className="text-left py-1 font-medium">AGE</th>
              </tr>
            </thead>
            <tbody>
              {pods.map((pod) => (
                <tr key={pod.name}>
                  <td className="pr-6 py-0.5 text-terminal-text">{pod.name}</td>
                  <td className="pr-6 py-0.5 text-terminal-text">{pod.ready}</td>
                  <td className={clsx('pr-6 py-0.5', statusColor(pod.status))}>{pod.status}</td>
                  <td className="pr-6 py-0.5 text-terminal-text">{pod.restarts}</td>
                  <td className="py-0.5 text-terminal-text">{pod.age}</td>
                </tr>
              ))}
              {pods.length === 0 && (
                <tr>
                  <td colSpan={5} className="py-2 text-terminal-muted">No resources found in nvsnap-system namespace.</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

// --- Log Stream ---

function LogStream({ logs }: { logs: LogEntry[] }) {
  const endRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [logs.length]);

  if (logs.length === 0) return null;

  return (
    <div className="mt-4 rounded-xl border border-terminal-border bg-terminal-surface overflow-hidden">
      <div className="px-4 py-2 border-b border-terminal-border flex items-center gap-2">
        <div className="w-2 h-2 rounded-full bg-gpu-500 animate-pulse" />
        <span className="text-sm font-medium text-terminal-text">Live Operation Log</span>
      </div>
      <div className="max-h-48 overflow-y-auto bg-terminal-bg px-4 py-2 font-mono text-xs">
        {logs.map((entry, i) => {
          const ts = new Date(entry.timestamp);
          const timeStr = ts.toLocaleTimeString('en-US', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' });
          return (
            <div key={i} className="py-0.5">
              <span className="text-terminal-muted">{timeStr}</span>
              <span className="text-terminal-muted mx-2">|</span>
              <span className="text-terminal-text">{entry.message}</span>
            </div>
          );
        })}
        <div ref={endRef} />
      </div>
    </div>
  );
}

// --- Error Banner ---

function ErrorBanner({ error, onDismiss }: { error: string; onDismiss: () => void }) {
  return (
    <div className="mb-6 p-4 rounded-xl bg-red-500/10 border border-red-500/30 flex items-start gap-3">
      <svg className="w-5 h-5 text-red-400 mt-0.5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor">
        <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M12 9v3.75m9-.75a9 9 0 11-18 0 9 9 0 0118 0zm-9 3.75h.008v.008H12v-.008z" />
      </svg>
      <div className="flex-1">
        <p className="text-red-400 text-sm">{error}</p>
      </div>
      <button onClick={onDismiss} className="text-red-400 hover:text-red-300">
        <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12" />
        </svg>
      </button>
    </div>
  );
}

// --- Main Demo Page ---

export function Demo() {
  const state = useDemoStateWS();
  const deploy = useDemoDeploy();
  const cleanup = useDemoCleanup();
  const logs = useDemoLogsWS();
  const [dismissedError, setDismissedError] = useState('');

  if (!state) {
    return (
      <div className="flex items-center justify-center h-64">
        <div className="w-8 h-8 rounded-full border-2 border-terminal-border border-t-gpu-500 animate-spin" />
      </div>
    );
  }

  const phase = state.phase;
  const hasError = state.error && state.error !== dismissedError;

  return (
    <div className="max-w-4xl mx-auto">
      <StepProgressBar phase={phase} />

      {hasError && (
        <ErrorBanner
          error={state.error!}
          onDismiss={() => setDismissedError(state.error!)}
        />
      )}

      {/* Status bar when a workload is active */}
      {phase !== 'IDLE' && (
        <div className="flex items-center justify-between mb-6 px-4 py-3 rounded-lg bg-terminal-surface border border-terminal-border">
          <div className="flex items-center gap-3">
            <span className="text-sm text-terminal-muted">Workload:</span>
            <span className="text-sm font-medium text-terminal-text">{state.workloadType}</span>
            {state.podName && (
              <>
                <span className="text-terminal-border">|</span>
                <span className="text-sm text-terminal-muted">Pod:</span>
                <span className="text-sm font-mono text-terminal-text">{state.podName}</span>
              </>
            )}
            {state.podStatus && (
              <>
                <span className="text-terminal-border">|</span>
                <span className={clsx(
                  'text-sm font-medium',
                  state.podStatus === 'Ready' ? 'text-gpu-400' : 'text-yellow-400'
                )}>
                  {state.podStatus}
                </span>
              </>
            )}
          </div>
          {state.nodeName && (
            <span className="text-xs text-terminal-muted font-mono">{state.nodeName.split('-').slice(-1)[0]}</span>
          )}
        </div>
      )}

      {/* Main action panel */}
      {phase === 'IDLE' && (
        <IdlePanel onDeploy={(wt) => deploy.mutate(wt)} />
      )}

      {phase === 'DEPLOYING' && (
        <ProgressPanel message={state.message} startedAt={state.startedAt} />
      )}

      {phase === 'RUNNING' && (
        <RunningPanel onCheckpoint={() => {}} />
      )}

      {phase === 'CHECKPOINTING' && (
        <ProgressPanel message={state.message} startedAt={state.startedAt} />
      )}

      {phase === 'CHECKPOINTED' && (
        <CheckpointedPanel
          checkpoints={state.checkpoints}
          sourceNode={state.nodeName}
          onRestore={() => {}}
        />
      )}

      {phase === 'RESTORING' && (
        <ProgressPanel message={state.message} startedAt={state.startedAt} />
      )}

      {phase === 'RESTORED' && (
        <RestoredPanel />
      )}

      {/* Timing comparison — show after restore completes */}
      {phase === 'RESTORED' && state.deployDuration > 0 && state.restoreDuration > 0 && (
        <TimingBanner
          deployDuration={state.deployDuration}
          restoreDuration={state.restoreDuration}
        />
      )}

      {/* Reset button */}
      {phase !== 'IDLE' && (
        <div className="text-center mt-8">
          <button
            onClick={() => cleanup.mutate()}
            disabled={cleanup.isPending}
            className="px-4 py-2 rounded-lg border border-terminal-border text-terminal-muted hover:text-terminal-text hover:border-red-500/50 text-sm transition-colors"
          >
            {cleanup.isPending ? 'Resetting...' : 'Reset Demo'}
          </button>
        </div>
      )}

      {/* Live logs — visible during active operations */}
      {(phase === 'DEPLOYING' || phase === 'CHECKPOINTING' || phase === 'RESTORING') && (
        <LogStream logs={logs} />
      )}

      {/* Pod watcher — always visible */}
      <PodWatcher />
    </div>
  );
}
