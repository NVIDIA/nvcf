import { useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { motion } from 'framer-motion';
import { PlusIcon, ArrowPathIcon } from '@heroicons/react/24/outline';
import clsx from 'clsx';
import toast from 'react-hot-toast';
import { useRestores, useCreateRestore, useCheckpoints } from '../api/hooks';
import type { CheckpointInfo } from '../api/client';

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

export function Restores() {
  const [searchParams] = useSearchParams();
  const preselectedCheckpoint = searchParams.get('checkpoint') || '';
  const [showCreate, setShowCreate] = useState(!!preselectedCheckpoint);

  const { data: restores = [], isLoading } = useRestores();
  const createMutation = useCreateRestore();

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div>
          <h2 className="text-2xl font-bold text-terminal-text">Restores</h2>
          <p className="text-sm text-terminal-muted mt-1">Restore operations from captured snapshots</p>
        </div>
        <motion.button
          whileHover={{ scale: 1.02 }}
          whileTap={{ scale: 0.98 }}
          onClick={() => setShowCreate(true)}
          className="inline-flex items-center gap-2 px-4 py-2 rounded-lg bg-blue-500 text-white font-medium text-sm hover:bg-blue-600 transition-colors"
        >
          <PlusIcon className="h-5 w-5" />
          New Restore
        </motion.button>
      </div>

      {/* Create Dialog */}
      {showCreate && (
        <CreateRestoreDialog
          preselectedCheckpoint={preselectedCheckpoint}
          onClose={() => setShowCreate(false)}
          onCreate={(req) => {
            createMutation.mutate(req, {
              onSuccess: (data) => {
                toast.success(`Restore ${data.id} started`);
                setShowCreate(false);
              },
              onError: (err) => toast.error(`Restore failed: ${err.message}`),
            });
          }}
          isLoading={createMutation.isPending}
        />
      )}

      {/* Table */}
      <div className="rounded-xl bg-terminal-surface border border-terminal-border overflow-hidden">
        {isLoading ? (
          <div className="p-8 text-center text-terminal-muted">Loading restores...</div>
        ) : restores.length === 0 ? (
          <div className="p-8 text-center text-terminal-muted">
            <ArrowPathIcon className="h-12 w-12 text-terminal-muted mx-auto mb-4" />
            No restore operations yet. Restore from a captured snapshot to get started.
          </div>
        ) : (
          <table className="w-full">
            <thead>
              <tr className="border-b border-terminal-border">
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">ID</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">Checkpoint</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">Phase</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">New Pod</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">Node</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">Message</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">Age</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-terminal-border">
              {restores.map((restore, idx) => (
                <motion.tr
                  key={restore.id}
                  initial={{ opacity: 0, y: 10 }}
                  animate={{ opacity: 1, y: 0 }}
                  transition={{ delay: idx * 0.05 }}
                  className="hover:bg-terminal-border/30 transition-colors"
                >
                  <td className="px-6 py-4 text-sm font-medium text-terminal-text">{restore.id}</td>
                  <td className="px-6 py-4 text-sm text-gpu-400">{restore.checkpointName}</td>
                  <td className="px-6 py-4">
                    <span className={clsx(
                      'inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium',
                      restore.phase === 'Completed' && 'bg-gpu-500/20 text-gpu-400',
                      (restore.phase === 'Restoring' || restore.phase === 'CreatingPod') && 'bg-blue-500/20 text-blue-400',
                      restore.phase === 'Failed' && 'bg-red-500/20 text-red-400',
                      restore.phase === 'Pending' && 'bg-yellow-500/20 text-yellow-400',
                    )}>
                      {(restore.phase === 'Restoring' || restore.phase === 'CreatingPod') && (
                        <span className="mr-1.5 h-1.5 w-1.5 rounded-full bg-blue-400 animate-pulse" />
                      )}
                      {restore.phase}
                    </span>
                  </td>
                  <td className="px-6 py-4 text-sm text-terminal-text">{restore.newPodName || '-'}</td>
                  <td className="px-6 py-4 text-sm text-terminal-text">{restore.nodeName || '-'}</td>
                  <td className="px-6 py-4 text-sm text-terminal-muted truncate max-w-xs">{restore.message || '-'}</td>
                  <td className="px-6 py-4 text-sm text-terminal-muted">{timeAgo(restore.createdAt)}</td>
                </motion.tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

// --- Create Restore Dialog ---

function CreateRestoreDialog({ preselectedCheckpoint, onClose, onCreate, isLoading }: {
  preselectedCheckpoint: string;
  onClose: () => void;
  onCreate: (req: { checkpointName: string; namespace: string; newPodName?: string }) => void;
  isLoading: boolean;
}) {
  const { data: checkpoints = [], isLoading: ckptLoading } = useCheckpoints();
  const completedCheckpoints = checkpoints.filter(c => c.phase === 'Completed');
  const [selected, setSelected] = useState<CheckpointInfo | null>(
    completedCheckpoints.find(c => c.id === preselectedCheckpoint) || null
  );

  return (
    <div className="rounded-xl bg-terminal-surface border border-terminal-border p-6">
      <h3 className="text-lg font-semibold text-terminal-text mb-4">Restore from Captured Snapshot</h3>
      <p className="text-sm text-terminal-muted mb-4">Select a completed capture to restore:</p>

      {ckptLoading ? (
        <p className="text-sm text-terminal-muted">Loading checkpoints...</p>
      ) : completedCheckpoints.length === 0 ? (
        <p className="text-sm text-terminal-muted">No completed captures available for restore.</p>
      ) : (
        <div className="space-y-2 mb-4 max-h-64 overflow-y-auto">
          {completedCheckpoints.map((ckpt) => (
            <button
              key={ckpt.id}
              onClick={() => setSelected(ckpt)}
              className={clsx(
                'w-full text-left p-3 rounded-lg border transition-colors',
                selected?.id === ckpt.id
                  ? 'border-blue-500 bg-blue-500/10'
                  : 'border-terminal-border hover:border-terminal-muted'
              )}
            >
              <p className="text-sm font-medium text-terminal-text">{ckpt.id}</p>
              <p className="text-xs text-terminal-muted">{ckpt.podName} / {ckpt.namespace} / {ckpt.nodeName}</p>
            </button>
          ))}
        </div>
      )}

      <div className="flex justify-end gap-3">
        <button onClick={onClose} className="px-4 py-2 rounded-lg border border-terminal-border text-terminal-muted hover:text-terminal-text text-sm">
          Cancel
        </button>
        <button
          onClick={() => selected && onCreate({ checkpointName: selected.id, namespace: selected.namespace })}
          disabled={!selected || isLoading}
          className={clsx(
            'px-4 py-2 rounded-lg text-sm font-medium',
            selected && !isLoading ? 'bg-blue-500 text-white hover:bg-blue-600' : 'bg-terminal-border text-terminal-muted cursor-not-allowed'
          )}
        >
          {isLoading ? 'Restoring...' : 'Start Restore'}
        </button>
      </div>
    </div>
  );
}
