import { useState } from 'react';
import { useRetentionPolicies, useCreateRetentionPolicy, useDeleteRetentionPolicy } from '../api/hooks';
import { TrashIcon, PlusIcon } from '@heroicons/react/24/outline';

export function Policies() {
  const { data: policies, isLoading } = useRetentionPolicies();
  const createMutation = useCreateRetentionPolicy();
  const deleteMutation = useDeleteRetentionPolicy();
  const [showForm, setShowForm] = useState(false);
  const [form, setForm] = useState({ name: '', namespace: '*', workloadType: '*', maxCount: 10, maxAgeHours: 168, maxTotalBytes: 0 });

  const handleCreate = () => {
    createMutation.mutate(form, { onSuccess: () => { setShowForm(false); setForm({ name: '', namespace: '*', workloadType: '*', maxCount: 10, maxAgeHours: 168, maxTotalBytes: 0 }); } });
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-bold text-terminal-text">Retention Policies</h2>
          <p className="text-terminal-muted text-sm mt-1">Automatically clean up old checkpoints based on count, age, or size</p>
        </div>
        <button onClick={() => setShowForm(!showForm)} className="flex items-center gap-2 px-4 py-2 bg-gpu-600 hover:bg-gpu-500 text-white rounded-lg transition">
          <PlusIcon className="h-4 w-4" /> New Policy
        </button>
      </div>

      {showForm && (
        <div className="bg-terminal-surface border border-terminal-border rounded-xl p-6 space-y-4">
          <h3 className="text-lg font-semibold text-terminal-text">Create Retention Policy</h3>
          <div className="grid grid-cols-2 md:grid-cols-3 gap-4">
            <div>
              <label className="block text-xs text-terminal-muted mb-1">Name</label>
              <input value={form.name} onChange={e => setForm({...form, name: e.target.value})} className="w-full px-3 py-2 bg-terminal-bg border border-terminal-border rounded text-terminal-text text-sm" placeholder="e.g. weekly-cleanup" />
            </div>
            <div>
              <label className="block text-xs text-terminal-muted mb-1">Namespace (* = all)</label>
              <input value={form.namespace} onChange={e => setForm({...form, namespace: e.target.value})} className="w-full px-3 py-2 bg-terminal-bg border border-terminal-border rounded text-terminal-text text-sm" />
            </div>
            <div>
              <label className="block text-xs text-terminal-muted mb-1">Workload Type (* = all)</label>
              <input value={form.workloadType} onChange={e => setForm({...form, workloadType: e.target.value})} className="w-full px-3 py-2 bg-terminal-bg border border-terminal-border rounded text-terminal-text text-sm" />
            </div>
            <div>
              <label className="block text-xs text-terminal-muted mb-1">Max Count (0 = unlimited)</label>
              <input type="number" value={form.maxCount} onChange={e => setForm({...form, maxCount: parseInt(e.target.value) || 0})} className="w-full px-3 py-2 bg-terminal-bg border border-terminal-border rounded text-terminal-text text-sm" />
            </div>
            <div>
              <label className="block text-xs text-terminal-muted mb-1">Max Age (hours, 0 = unlimited)</label>
              <input type="number" value={form.maxAgeHours} onChange={e => setForm({...form, maxAgeHours: parseInt(e.target.value) || 0})} className="w-full px-3 py-2 bg-terminal-bg border border-terminal-border rounded text-terminal-text text-sm" />
            </div>
            <div>
              <label className="block text-xs text-terminal-muted mb-1">Max Total Size (bytes, 0 = unlimited)</label>
              <input type="number" value={form.maxTotalBytes} onChange={e => setForm({...form, maxTotalBytes: parseInt(e.target.value) || 0})} className="w-full px-3 py-2 bg-terminal-bg border border-terminal-border rounded text-terminal-text text-sm" />
            </div>
          </div>
          <div className="flex gap-3 pt-2">
            <button onClick={handleCreate} disabled={!form.name || createMutation.isPending} className="px-4 py-2 bg-gpu-600 hover:bg-gpu-500 disabled:opacity-50 text-white rounded-lg text-sm transition">
              {createMutation.isPending ? 'Creating...' : 'Create Policy'}
            </button>
            <button onClick={() => setShowForm(false)} className="px-4 py-2 text-terminal-muted hover:text-terminal-text text-sm transition">Cancel</button>
          </div>
        </div>
      )}

      {isLoading ? (
        <div className="text-terminal-muted">Loading policies...</div>
      ) : !policies?.length ? (
        <div className="text-center py-12 text-terminal-muted">
          <p>No retention policies configured</p>
          <p className="text-sm mt-1">Checkpoints will accumulate until manually deleted</p>
        </div>
      ) : (
        <div className="bg-terminal-surface border border-terminal-border rounded-xl overflow-hidden">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-terminal-border text-terminal-muted text-left">
                <th className="px-4 py-3">Name</th>
                <th className="px-4 py-3">Namespace</th>
                <th className="px-4 py-3">Workload</th>
                <th className="px-4 py-3">Max Count</th>
                <th className="px-4 py-3">Max Age</th>
                <th className="px-4 py-3">Max Size</th>
                <th className="px-4 py-3"></th>
              </tr>
            </thead>
            <tbody>
              {policies.map((p) => (
                <tr key={p.id} className="border-b border-terminal-border/50 hover:bg-terminal-bg/50">
                  <td className="px-4 py-3 text-terminal-text font-medium">{p.name}</td>
                  <td className="px-4 py-3 text-terminal-muted">{p.namespace}</td>
                  <td className="px-4 py-3 text-terminal-muted">{p.workloadType}</td>
                  <td className="px-4 py-3 text-terminal-text">{p.maxCount || 'unlimited'}</td>
                  <td className="px-4 py-3 text-terminal-text">{p.maxAgeHours ? `${p.maxAgeHours}h` : 'unlimited'}</td>
                  <td className="px-4 py-3 text-terminal-text">{p.maxTotalBytes ? `${(p.maxTotalBytes / 1e9).toFixed(1)} GB` : 'unlimited'}</td>
                  <td className="px-4 py-3">
                    <button onClick={() => deleteMutation.mutate(p.id)} className="text-red-400 hover:text-red-300 transition">
                      <TrashIcon className="h-4 w-4" />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
