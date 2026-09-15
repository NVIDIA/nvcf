import { motion } from 'framer-motion';
import { CircleStackIcon } from '@heroicons/react/24/outline';
import clsx from 'clsx';
import { useBlobstoreStats, useBlobstoreCaptures } from '../api/hooks';

function formatBytes(n: number): string {
  if (n === 0) return '0 B';
  if (!n || isNaN(n)) return '-';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}

function formatAge(iso: string): string {
  if (!iso) return '-';
  const t = new Date(iso).getTime();
  if (isNaN(t)) return '-';
  const secs = Math.max(0, (Date.now() - t) / 1000);
  if (secs < 60) return `${Math.floor(secs)}s ago`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
  return `${Math.floor(secs / 86400)}d ago`;
}

function shortHash(hash: string): string {
  return hash.length > 16 ? `${hash.slice(0, 12)}…${hash.slice(-4)}` : hash;
}

function Tile({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="rounded-xl bg-terminal-surface border border-terminal-border p-4">
      <p className="text-xs font-semibold text-terminal-muted uppercase tracking-wider">{label}</p>
      <p className="mt-2 text-2xl font-bold text-terminal-text">{value}</p>
      {sub && <p className="mt-1 text-xs text-terminal-muted">{sub}</p>}
    </div>
  );
}

export function Blobstore() {
  const { data: stats, isLoading: statsLoading, error: statsError } = useBlobstoreStats();
  const { data: captures = [], isLoading: capturesLoading } = useBlobstoreCaptures();

  const diskPct = stats?.disk && stats.disk.total_bytes > 0
    ? (stats.disk.used_bytes / stats.disk.total_bytes) * 100
    : 0;

  // Dedup gain: blobs are content-addressed, so total raw bytes across
  // manifests vs unique on-disk bytes shows how much repeat-content was
  // skipped. Only meaningful once at least one capture has a manifest.
  const rawBytes = captures.reduce((s, c) => s + (c.total_bytes || 0), 0);
  const dedupRatio = stats && stats.blob_bytes > 0 && rawBytes > 0
    ? rawBytes / stats.blob_bytes
    : null;

  if (statsError) {
    return (
      <div className="space-y-6">
        <div>
          <h2 className="text-2xl font-bold text-terminal-text">Blobstore</h2>
          <p className="text-sm text-terminal-muted mt-1">Content-addressed checkpoint storage (tier-3)</p>
        </div>
        <div className="rounded-xl bg-terminal-surface border border-red-500/30 p-6">
          <p className="text-red-400">nvsnap-blobstore is unreachable.</p>
          <p className="text-xs text-terminal-muted mt-2">
            nvsnap-server proxies <code className="text-terminal-text">/v1/stats</code> and{' '}
            <code className="text-terminal-text">/v1/captures</code> from the nvsnap-blobstore Service.
            Check that the Deployment is Running in nvsnap-system.
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-bold text-terminal-text">Blobstore</h2>
        <p className="text-sm text-terminal-muted mt-1">Content-addressed checkpoint storage (tier-3)</p>
      </div>

      {/* Stats tiles */}
      <div className="grid grid-cols-2 md:grid-cols-5 gap-4">
        <Tile
          label="Captures"
          value={statsLoading ? '—' : String(stats?.capture_count ?? 0)}
          sub={stats?.manifest_count !== undefined ? `${stats.manifest_count} with manifest` : undefined}
        />
        <Tile
          label="Blobs"
          value={statsLoading ? '—' : String(stats?.blob_count ?? 0)}
          sub="unique content-addressed"
        />
        <Tile
          label="Bytes Stored"
          value={statsLoading ? '—' : formatBytes(stats?.blob_bytes ?? 0)}
          sub="on disk (deduplicated)"
        />
        <Tile
          label="Dedup Gain"
          value={dedupRatio !== null ? `${dedupRatio.toFixed(2)}×` : '—'}
          sub={rawBytes > 0 ? `${formatBytes(rawBytes)} raw → ${formatBytes(stats?.blob_bytes ?? 0)}` : 'awaiting manifests'}
        />
        <Tile
          label="Disk"
          value={stats?.disk ? `${diskPct.toFixed(0)}%` : '—'}
          sub={stats?.disk ? `${formatBytes(stats.disk.free_bytes)} free of ${formatBytes(stats.disk.total_bytes)}` : undefined}
        />
      </div>

      {/* Captures table */}
      {capturesLoading ? (
        <p className="text-terminal-muted">Loading captures…</p>
      ) : captures.length === 0 ? (
        <div className="rounded-xl bg-terminal-surface border border-terminal-border p-8 text-center">
          <CircleStackIcon className="h-12 w-12 text-terminal-muted mx-auto mb-4" />
          <p className="text-terminal-muted">No captures in nvsnap-blobstore yet.</p>
          <p className="text-xs text-terminal-muted mt-2">
            Run a checkpoint from the Demo page; the agent's async uploader will populate this tier.
          </p>
        </div>
      ) : (
        <div className="rounded-xl bg-terminal-surface border border-terminal-border overflow-hidden">
          <table className="w-full">
            <thead>
              <tr className="border-b border-terminal-border">
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">Capture Hash</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">Files</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">Size</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">Manifest</th>
                <th className="px-6 py-4 text-left text-xs font-semibold text-terminal-muted uppercase tracking-wider">Age</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-terminal-border">
              {captures.map((c, idx) => (
                <motion.tr
                  key={c.hash}
                  initial={{ opacity: 0, y: 8 }}
                  animate={{ opacity: 1, y: 0 }}
                  transition={{ delay: Math.min(idx * 0.03, 0.3) }}
                  className="hover:bg-terminal-border/30 transition-colors"
                >
                  <td className="px-6 py-4">
                    <span className="font-mono text-xs text-terminal-text" title={c.hash}>{shortHash(c.hash)}</span>
                  </td>
                  <td className="px-6 py-4 text-sm text-terminal-text">{c.file_count || '-'}</td>
                  <td className="px-6 py-4 text-sm text-terminal-text">{formatBytes(c.total_bytes)}</td>
                  <td className="px-6 py-4">
                    <span className={clsx(
                      'inline-flex items-center gap-1.5 text-xs font-medium',
                      c.has_manifest ? 'text-gpu-400' : 'text-yellow-400'
                    )}>
                      <span className={clsx('h-1.5 w-1.5 rounded-full', c.has_manifest ? 'bg-gpu-500' : 'bg-yellow-500')} />
                      {c.has_manifest ? 'Present' : 'Missing'}
                    </span>
                  </td>
                  <td className="px-6 py-4 text-sm text-terminal-muted">{formatAge(c.modified_at)}</td>
                </motion.tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
