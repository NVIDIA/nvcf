import { motion } from 'framer-motion';
import { ServerStackIcon } from '@heroicons/react/24/outline';
import clsx from 'clsx';
import { useNodes } from '../api/hooks';

export function Nodes() {
  const { data: nodes = [], isLoading } = useNodes();

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-bold text-terminal-text">GPU Nodes</h2>
        <p className="text-sm text-terminal-muted mt-1">Cluster GPU nodes and agent status</p>
      </div>

      {isLoading ? (
        <p className="text-terminal-muted">Loading nodes...</p>
      ) : nodes.length === 0 ? (
        <div className="rounded-xl bg-terminal-surface border border-terminal-border p-8 text-center">
          <ServerStackIcon className="h-12 w-12 text-terminal-muted mx-auto mb-4" />
          <p className="text-terminal-muted">No GPU nodes found in the cluster.</p>
          <p className="text-xs text-terminal-muted mt-2">Nodes must have the label nvidia.com/gpu.present=true</p>
        </div>
      ) : (
        <div className="rounded-xl bg-terminal-surface border border-terminal-border overflow-hidden">
          <table className="w-full">
            <thead>
              <tr className="border-b border-terminal-border">
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">Node</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">Status</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">GPUs</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">GPU Model</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">Agent</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">GPU Pods</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">IP</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-terminal-border">
              {nodes.map((node, idx) => (
                <motion.tr
                  key={node.name}
                  initial={{ opacity: 0, y: 10 }}
                  animate={{ opacity: 1, y: 0 }}
                  transition={{ delay: idx * 0.05 }}
                  className="hover:bg-terminal-border/30 transition-colors"
                >
                  <td className="px-6 py-4">
                    <p className="text-sm font-medium text-terminal-text">{node.name}</p>
                  </td>
                  <td className="px-6 py-4">
                    <span className={clsx(
                      'inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium',
                      node.status === 'Ready' ? 'bg-gpu-500/20 text-gpu-400' : 'bg-red-500/20 text-red-400'
                    )}>
                      {node.status}
                    </span>
                  </td>
                  <td className="px-6 py-4 text-sm text-terminal-text">{node.gpuCount}</td>
                  <td className="px-6 py-4 text-sm text-terminal-text">{node.gpuModel || '-'}</td>
                  <td className="px-6 py-4">
                    <span className={clsx(
                      'inline-flex items-center gap-1.5 text-xs font-medium',
                      node.agentReady ? 'text-gpu-400' : 'text-red-400'
                    )}>
                      <span className={clsx('h-1.5 w-1.5 rounded-full', node.agentReady ? 'bg-gpu-500' : 'bg-red-500')} />
                      {node.agentReady ? 'Ready' : 'Offline'}
                    </span>
                  </td>
                  <td className="px-6 py-4 text-sm text-terminal-text">{node.podCount}</td>
                  <td className="px-6 py-4 text-sm text-terminal-muted font-mono">{node.internalIP}</td>
                </motion.tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
