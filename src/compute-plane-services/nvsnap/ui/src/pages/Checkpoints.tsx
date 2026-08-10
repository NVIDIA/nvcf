import { useState } from 'react';
import { Link } from 'react-router-dom';
import { motion } from 'framer-motion';
import {
  PlusIcon,
  MagnifyingGlassIcon,
  TrashIcon,
  EyeIcon,
  ArrowPathIcon,
} from '@heroicons/react/24/outline';
import clsx from 'clsx';
import toast from 'react-hot-toast';
import { useCheckpoints, useCreateCheckpoint, useDeleteCheckpoint, usePods } from '../api/hooks';
import type { PodInfo } from '../api/client';

function formatSize(bytes: number | undefined): string {
  if (!bytes) return '-';
  if (bytes >= 1e12) return `${(bytes / 1e12).toFixed(1)} TB`;
  if (bytes >= 1e9) return `${(bytes / 1e9).toFixed(1)} GB`;
  if (bytes >= 1e6) return `${(bytes / 1e6).toFixed(1)} MB`;
  return `${bytes} B`;
}

function timeAgo(dateStr: string | undefined): string {
  if (!dateStr) return '';
  const diff = Date.now() - new Date(dateStr).getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return 'just now';
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

export function Checkpoints() {
  const [searchQuery, setSearchQuery] = useState('');
  const [selectedStatus, setSelectedStatus] = useState<string | null>(null);
  const [showCreate, setShowCreate] = useState(false);

  const { data: checkpoints = [], isLoading } = useCheckpoints(undefined, 'agent');
  const createMutation = useCreateCheckpoint();
  const deleteMutation = useDeleteCheckpoint();

  const filteredCheckpoints = checkpoints.filter((ckpt) => {
    const matchesSearch =
      ckpt.id.toLowerCase().includes(searchQuery.toLowerCase()) ||
      (ckpt.podName || '').toLowerCase().includes(searchQuery.toLowerCase()) ||
      ckpt.namespace.toLowerCase().includes(searchQuery.toLowerCase());
    const matchesStatus = !selectedStatus || ckpt.phase === selectedStatus;
    return matchesSearch && matchesStatus;
  });

  const handleDelete = (id: string, namespace: string) => {
    deleteMutation.mutate({ id, namespace }, {
      onSuccess: () => toast.success('Checkpoint deleted'),
      onError: (err) => toast.error(`Delete failed: ${err.message}`),
    });
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div>
          <h2 className="text-2xl font-bold text-terminal-text">Captures</h2>
          <p className="text-sm text-terminal-muted mt-1">Manage captured GPU workload snapshots</p>
        </div>
        <motion.button
          whileHover={{ scale: 1.02 }}
          whileTap={{ scale: 0.98 }}
          onClick={() => setShowCreate(true)}
          className="inline-flex items-center gap-2 px-4 py-2 rounded-lg bg-gpu-500 text-white font-medium text-sm hover:bg-gpu-600 transition-colors"
        >
          <PlusIcon className="h-5 w-5" />
          New Capture
        </motion.button>
      </div>

      {/* Create Dialog */}
      {showCreate && (
        <CreateCheckpointDialog
          onClose={() => setShowCreate(false)}
          onCreate={(req) => {
            createMutation.mutate(req, {
              onSuccess: (data) => {
                toast.success(`Checkpoint ${data.id} started`);
                setShowCreate(false);
              },
              onError: (err) => toast.error(`Checkpoint failed: ${err.message}`),
            });
          }}
          isLoading={createMutation.isPending}
        />
      )}

      {/* Filters */}
      <div className="flex flex-col sm:flex-row gap-4">
        <div className="relative flex-1">
          <MagnifyingGlassIcon className="absolute left-3 top-1/2 -translate-y-1/2 h-5 w-5 text-terminal-muted" />
          <input
            type="text"
            placeholder="Search captures..."
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            className="w-full pl-10 pr-4 py-2 rounded-lg bg-terminal-surface border border-terminal-border text-terminal-text placeholder-terminal-muted focus:outline-none focus:ring-2 focus:ring-gpu-500/50 focus:border-gpu-500"
          />
        </div>
        <div className="flex gap-2">
          {['All', 'Completed', 'InProgress', 'Failed'].map((status) => (
            <button
              key={status}
              onClick={() => setSelectedStatus(status === 'All' ? null : status)}
              className={clsx(
                'px-3 py-2 rounded-lg text-sm font-medium transition-colors',
                (status === 'All' && !selectedStatus) || selectedStatus === status
                  ? 'bg-gpu-500/20 text-gpu-400 border border-gpu-500/30'
                  : 'bg-terminal-surface border border-terminal-border text-terminal-muted hover:text-terminal-text'
              )}
            >
              {status}
            </button>
          ))}
        </div>
      </div>

      {/* Table */}
      <div className="rounded-xl bg-terminal-surface border border-terminal-border overflow-hidden">
        {isLoading ? (
          <div className="p-8 text-center text-terminal-muted">Loading captures...</div>
        ) : filteredCheckpoints.length === 0 ? (
          <div className="p-8 text-center text-terminal-muted">
            {checkpoints.length === 0 ? 'No captures yet. Create one to get started.' : 'No captures match your filters.'}
          </div>
        ) : (
          <table className="w-full">
            <thead>
              <tr className="border-b border-terminal-border">
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">Name</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">Namespace</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">Status</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">Size</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">Node</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">Age</th>
                <th className="px-6 py-4 text-right text-xs font-semibold text-terminal-muted uppercase tracking-wider">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-terminal-border">
              {filteredCheckpoints.map((ckpt, idx) => (
                <motion.tr
                  key={ckpt.id}
                  initial={{ opacity: 0, y: 10 }}
                  animate={{ opacity: 1, y: 0 }}
                  transition={{ delay: idx * 0.05 }}
                  className="hover:bg-terminal-border/30 transition-colors"
                >
                  <td className="px-6 py-4">
                    <Link to={`/checkpoints/${ckpt.id}${ckpt.namespace ? `?namespace=${ckpt.namespace}` : ''}`} className="text-sm font-medium text-gpu-400 hover:text-gpu-300">
                      {ckpt.id}
                    </Link>
                    <p className="text-xs text-terminal-muted mt-0.5">{ckpt.podName}</p>
                  </td>
                  <td className="px-6 py-4 text-sm text-terminal-text">{ckpt.namespace}</td>
                  <td className="px-6 py-4">
                    <span className={clsx(
                      'inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium',
                      ckpt.phase === 'Completed' && 'bg-gpu-500/20 text-gpu-400',
                      ckpt.phase === 'InProgress' && 'bg-blue-500/20 text-blue-400',
                      ckpt.phase === 'Failed' && 'bg-red-500/20 text-red-400',
                      ckpt.phase === 'Pending' && 'bg-yellow-500/20 text-yellow-400',
                    )}>
                      {ckpt.phase === 'InProgress' && <span className="mr-1.5 h-1.5 w-1.5 rounded-full bg-blue-400 animate-pulse" />}
                      {ckpt.phase}
                    </span>
                  </td>
                  <td className="px-6 py-4 text-sm text-terminal-text">{formatSize(ckpt.checkpointSize)}</td>
                  <td className="px-6 py-4 text-sm text-terminal-text">{ckpt.nodeName || '-'}</td>
                  <td className="px-6 py-4 text-sm text-terminal-muted">{timeAgo(ckpt.createdAt)}</td>
                  <td className="px-6 py-4 text-right">
                    <div className="flex items-center justify-end gap-2">
                      <Link to={`/checkpoints/${ckpt.id}${ckpt.namespace ? `?namespace=${ckpt.namespace}` : ''}`}
                        className="p-1.5 rounded-lg text-terminal-muted hover:text-terminal-text hover:bg-terminal-border transition-colors">
                        <EyeIcon className="h-4 w-4" />
                      </Link>
                      {ckpt.phase === 'Completed' && (
                        <Link to={`/restores?checkpoint=${ckpt.id}`}
                          className="p-1.5 rounded-lg text-terminal-muted hover:text-blue-400 hover:bg-blue-500/10 transition-colors">
                          <ArrowPathIcon className="h-4 w-4" />
                        </Link>
                      )}
                      <button
                        onClick={() => handleDelete(ckpt.id, ckpt.namespace)}
                        className="p-1.5 rounded-lg text-terminal-muted hover:text-red-400 hover:bg-red-500/10 transition-colors"
                      >
                        <TrashIcon className="h-4 w-4" />
                      </button>
                    </div>
                  </td>
                </motion.tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

// --- Create Checkpoint Dialog ---

function CreateCheckpointDialog({ onClose, onCreate, isLoading }: {
  onClose: () => void;
  onCreate: (req: { podName: string; namespace: string; containerName?: string }) => void;
  isLoading: boolean;
}) {
  const { data: pods = [], isLoading: podsLoading } = usePods();
  const [selectedPod, setSelectedPod] = useState<PodInfo | null>(null);

  return (
    <div className="rounded-xl bg-terminal-surface border border-terminal-border p-6">
      <h3 className="text-lg font-semibold text-terminal-text mb-4">Capture Workload</h3>
      <p className="text-sm text-terminal-muted mb-4">Select a GPU pod to capture:</p>

      {podsLoading ? (
        <p className="text-sm text-terminal-muted">Loading GPU pods...</p>
      ) : pods.length === 0 ? (
        <p className="text-sm text-terminal-muted">No GPU pods found in the cluster.</p>
      ) : (
        <div className="space-y-2 mb-4 max-h-64 overflow-y-auto">
          {pods.filter(p => p.status === 'Running').map((pod) => (
            <button
              key={`${pod.namespace}/${pod.name}`}
              onClick={() => setSelectedPod(pod)}
              className={clsx(
                'w-full text-left p-3 rounded-lg border transition-colors',
                selectedPod?.name === pod.name && selectedPod?.namespace === pod.namespace
                  ? 'border-gpu-500 bg-gpu-500/10'
                  : 'border-terminal-border hover:border-terminal-muted'
              )}
            >
              <p className="text-sm font-medium text-terminal-text">{pod.name}</p>
              <p className="text-xs text-terminal-muted">{pod.namespace} / {pod.nodeName} / {pod.gpuCount} GPU(s)</p>
            </button>
          ))}
        </div>
      )}

      <div className="flex justify-end gap-3">
        <button onClick={onClose} className="px-4 py-2 rounded-lg border border-terminal-border text-terminal-muted hover:text-terminal-text text-sm">
          Cancel
        </button>
        <button
          onClick={() => selectedPod && onCreate({ podName: selectedPod.name, namespace: selectedPod.namespace })}
          disabled={!selectedPod || isLoading}
          className={clsx(
            'px-4 py-2 rounded-lg text-sm font-medium',
            selectedPod && !isLoading ? 'bg-gpu-500 text-white hover:bg-gpu-600' : 'bg-terminal-border text-terminal-muted cursor-not-allowed'
          )}
        >
          {isLoading ? 'Capturing...' : 'Capture'}
        </button>
      </div>
    </div>
  );
}
