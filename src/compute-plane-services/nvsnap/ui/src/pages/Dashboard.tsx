import { motion } from 'framer-motion';
import {
  ArchiveBoxIcon,
  ArrowPathIcon,
  ServerStackIcon,
  CpuChipIcon,
  CheckCircleIcon,
  ExclamationTriangleIcon,
  ClockIcon,
} from '@heroicons/react/24/outline';
import clsx from 'clsx';
import { useNodes, useCheckpoints, useRestores } from '../api/hooks';

const container = {
  hidden: { opacity: 0 },
  show: { opacity: 1, transition: { staggerChildren: 0.1 } },
};

const item = {
  hidden: { opacity: 0, y: 20 },
  show: { opacity: 1, y: 0 },
};

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

export function Dashboard() {
  const { data: nodes = [], isLoading: nodesLoading } = useNodes();
  const { data: checkpoints = [], isLoading: ckptLoading } = useCheckpoints(undefined, 'agent');
  const { data: restores = [] } = useRestores();

  const totalGPUs = nodes.reduce((sum, n) => sum + n.gpuCount, 0);
  const activeRestores = restores.filter(r => r.phase !== 'Completed' && r.phase !== 'Failed').length;
  const totalSize = checkpoints.reduce((sum, c) => sum + (c.checkpointSize || 0), 0);

  const stats = [
    { name: 'Total Captures', value: ckptLoading ? '...' : String(checkpoints.length), change: `${checkpoints.filter(c => c.phase === 'Completed').length} completed`, changeType: 'positive', icon: ArchiveBoxIcon },
    { name: 'Active Restores', value: String(activeRestores), change: `${restores.length} total`, changeType: 'neutral', icon: ArrowPathIcon },
    { name: 'GPU Nodes', value: nodesLoading ? '...' : String(nodes.length), change: `${nodes.filter(n => n.agentReady).length} agents ready`, changeType: 'positive', icon: ServerStackIcon },
    { name: 'Total GPUs', value: String(totalGPUs), change: formatSize(totalSize) + ' captured', changeType: 'positive', icon: CpuChipIcon },
  ];

  // Combined recent activity from checkpoints + restores, sorted by time
  const recentActivity = [
    ...checkpoints.map(c => ({
      id: c.id,
      type: 'checkpoint' as const,
      name: c.id,
      detail: c.podName,
      status: c.phase === 'Completed' ? 'completed' : c.phase === 'Failed' ? 'failed' : 'in_progress',
      time: c.createdAt,
      size: c.checkpointSize,
      message: c.message,
    })),
    ...restores.map(r => ({
      id: r.id,
      type: 'restore' as const,
      name: r.id,
      detail: r.checkpointName,
      status: r.phase === 'Completed' ? 'completed' : r.phase === 'Failed' ? 'failed' : 'in_progress',
      time: r.createdAt,
      size: undefined as number | undefined,
      message: r.message,
    })),
  ]
    .sort((a, b) => new Date(b.time).getTime() - new Date(a.time).getTime())
    .slice(0, 8);

  return (
    <motion.div variants={container} initial="hidden" animate="show" className="space-y-8">
      {/* Stats */}
      <motion.div variants={item} className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {stats.map((stat) => (
          <div
            key={stat.name}
            className="relative overflow-hidden rounded-xl bg-terminal-surface border border-terminal-border p-6 transition-all hover:border-gpu-500/30 hover:shadow-lg hover:shadow-gpu-500/5"
          >
            <div className="absolute top-0 right-0 -mt-4 -mr-4 h-24 w-24 rounded-full bg-gpu-500/5" />
            <div className="flex items-start justify-between">
              <div>
                <p className="text-sm font-medium text-terminal-muted">{stat.name}</p>
                <p className="mt-2 text-3xl font-bold tracking-tight text-terminal-text">{stat.value}</p>
                <p className={clsx('mt-1 text-xs', stat.changeType === 'positive' ? 'text-gpu-400' : 'text-terminal-muted')}>
                  {stat.change}
                </p>
              </div>
              <div className="rounded-lg bg-gpu-500/10 p-3">
                <stat.icon className="h-6 w-6 text-gpu-400" />
              </div>
            </div>
          </div>
        ))}
      </motion.div>

      {/* Recent Activity */}
      <motion.div variants={item} className="rounded-xl bg-terminal-surface border border-terminal-border p-6">
        <h3 className="text-lg font-semibold text-terminal-text mb-4">Recent Activity</h3>
        {recentActivity.length === 0 ? (
          <p className="text-sm text-terminal-muted">No activity yet. Capture a workload to get started.</p>
        ) : (
          <div className="space-y-3">
            {recentActivity.map((activity) => (
              <div key={activity.id} className="flex items-start gap-3 p-3 rounded-lg bg-terminal-bg/50 border border-terminal-border/50">
                <div className={clsx('mt-0.5 rounded-full p-1.5',
                  activity.status === 'completed' && 'bg-gpu-500/20',
                  activity.status === 'in_progress' && 'bg-blue-500/20',
                  activity.status === 'failed' && 'bg-red-500/20'
                )}>
                  {activity.status === 'completed' && <CheckCircleIcon className="h-4 w-4 text-gpu-400" />}
                  {activity.status === 'in_progress' && <ClockIcon className="h-4 w-4 text-blue-400" />}
                  {activity.status === 'failed' && <ExclamationTriangleIcon className="h-4 w-4 text-red-400" />}
                </div>
                <div className="flex-1 min-w-0">
                  <p className="text-sm font-medium text-terminal-text truncate">{activity.name}</p>
                  <p className="text-xs text-terminal-muted">
                    {activity.type === 'checkpoint' ? `Pod: ${activity.detail}` : `Checkpoint: ${activity.detail}`}
                  </p>
                  {activity.size && <p className="text-xs text-terminal-muted mt-0.5">{formatSize(activity.size)}</p>}
                  {activity.message && <p className="text-xs text-terminal-muted mt-0.5">{activity.message}</p>}
                </div>
                <span className="text-xs text-terminal-muted whitespace-nowrap">{timeAgo(activity.time)}</span>
              </div>
            ))}
          </div>
        )}
      </motion.div>

      {/* GPU Nodes */}
      <motion.div variants={item} className="rounded-xl bg-terminal-surface border border-terminal-border p-6">
        <h3 className="text-lg font-semibold text-terminal-text mb-4">GPU Nodes</h3>
        {nodes.length === 0 ? (
          <p className="text-sm text-terminal-muted">{nodesLoading ? 'Loading...' : 'No GPU nodes found.'}</p>
        ) : (
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
            {nodes.map((node) => (
              <div key={node.name} className="rounded-lg bg-terminal-bg/50 border border-terminal-border/50 p-4 transition-all hover:border-gpu-500/30">
                <div className="flex items-center justify-between mb-3">
                  <h4 className="text-sm font-medium text-terminal-text truncate">{node.name}</h4>
                  <span className="flex h-2 w-2">
                    {node.agentReady ? (
                      <>
                        <span className="animate-ping absolute inline-flex h-2 w-2 rounded-full bg-gpu-400 opacity-75" />
                        <span className="relative inline-flex rounded-full h-2 w-2 bg-gpu-500" />
                      </>
                    ) : (
                      <span className="relative inline-flex rounded-full h-2 w-2 bg-red-500" />
                    )}
                  </span>
                </div>
                <div className="space-y-2 text-xs text-terminal-muted">
                  <div className="flex justify-between">
                    <span>GPUs</span>
                    <span className="text-terminal-text">{node.gpuCount}x {node.gpuModel || 'GPU'}</span>
                  </div>
                  <div className="flex justify-between">
                    <span>Status</span>
                    <span className={node.status === 'Ready' ? 'text-gpu-400' : 'text-red-400'}>{node.status}</span>
                  </div>
                  <div className="flex justify-between">
                    <span>Pods</span>
                    <span className="text-terminal-text">{node.podCount}</span>
                  </div>
                  <div className="flex justify-between">
                    <span>Agent</span>
                    <span className={node.agentReady ? 'text-gpu-400' : 'text-red-400'}>
                      {node.agentReady ? 'Ready' : 'Offline'}
                    </span>
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}
      </motion.div>
    </motion.div>
  );
}
