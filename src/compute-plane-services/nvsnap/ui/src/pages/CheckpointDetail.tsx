import { useState } from 'react';
import { useParams, useSearchParams, Link } from 'react-router-dom';
import { motion } from 'framer-motion';
import { ArrowLeftIcon, ArrowPathIcon, FolderIcon } from '@heroicons/react/24/outline';
import { useCheckpoint } from '../api/hooks';
import axios from 'axios';

function formatSize(bytes: number | undefined): string {
  if (!bytes) return '-';
  if (bytes >= 1e12) return `${(bytes / 1e12).toFixed(1)} TB`;
  if (bytes >= 1e9) return `${(bytes / 1e9).toFixed(1)} GB`;
  if (bytes >= 1e6) return `${(bytes / 1e6).toFixed(1)} MB`;
  if (bytes >= 1e3) return `${(bytes / 1e3).toFixed(1)} KB`;
  return `${bytes} B`;
}

// Matches GET /api/v1/checkpoints/{id}/files: a flat list of capture
// sources (the rootfs tree + any per-volume emptyDirs), each with its
// type and aggregate size/file count. Not a navigable file tree.
interface FileEntry {
  path: string;
  type: string;
  sizeBytes: number;
  fileCount: number;
}

export function CheckpointDetail() {
  const { id } = useParams<{ id: string }>();
  const [searchParams] = useSearchParams();
  const namespace = searchParams.get('namespace') || undefined;
  const { data: checkpoint, isLoading, error } = useCheckpoint(id!, namespace);
  const [files, setFiles] = useState<FileEntry[] | null>(null);
  const [filesLoading, setFilesLoading] = useState(false);

  const loadFiles = async () => {
    setFilesLoading(true);
    try {
      const { data } = await axios.get(`/api/v1/checkpoints/${id}/files`);
      setFiles(data.files || []);
    } catch {
      setFiles([]);
    }
    setFilesLoading(false);
  };

  if (isLoading) {
    return <div className="text-terminal-muted">Loading capture details...</div>;
  }

  if (error || !checkpoint) {
    return (
      <div className="space-y-4">
        <Link to="/checkpoints" className="inline-flex items-center gap-2 text-sm text-terminal-muted hover:text-terminal-text">
          <ArrowLeftIcon className="h-4 w-4" /> Back to captures
        </Link>
        <p className="text-red-400">Checkpoint not found.</p>
      </div>
    );
  }

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const ckpt = checkpoint as any;
  // durationSecs is the agent/catalog field; durationSeconds was the old
  // (never-populated) name — accept either so the value renders.
  const durationSecs = ckpt.durationSecs ?? ckpt.durationSeconds;
  const gpu = ckpt.gpuType
    ? `${ckpt.gpuType}${ckpt.gpuCount ? ` ×${ckpt.gpuCount}` : ''}`
    : (ckpt.hasGpu ? 'yes' : '-');
  const fields = [
    { label: 'ID', value: ckpt.id },
    { label: 'Namespace', value: ckpt.namespace || ckpt.podNamespace },
    { label: 'Pod', value: ckpt.podName },
    { label: 'Container', value: ckpt.containerName },
    { label: 'Image', value: ckpt.containerImage },
    { label: 'Model', value: ckpt.modelName },
    { label: 'Workload', value: ckpt.workloadType },
    { label: 'Capture Method', value: ckpt.captureMethod },
    { label: 'GPU', value: gpu },
    { label: 'Driver', value: ckpt.driverVersion },
    { label: 'Node', value: ckpt.nodeName || '-' },
    { label: 'Size', value: formatSize(ckpt.checkpointSize) },
    { label: 'Duration', value: durationSecs ? `${durationSecs.toFixed(1)}s` : '-' },
    { label: 'Created', value: ckpt.createdAt ? new Date(ckpt.createdAt).toLocaleString() : '-' },
  ].filter(f => f.value && f.value !== '-');

  return (
    <motion.div initial={{ opacity: 0, y: 20 }} animate={{ opacity: 1, y: 0 }} className="space-y-6">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-4">
          <Link to="/checkpoints" className="p-2 rounded-lg text-terminal-muted hover:text-terminal-text hover:bg-terminal-surface transition-colors">
            <ArrowLeftIcon className="h-5 w-5" />
          </Link>
          <div>
            <h2 className="text-2xl font-bold text-terminal-text">{checkpoint.id}</h2>
            <p className="text-sm text-terminal-muted mt-1">Capture details</p>
          </div>
        </div>
        <Link
          to={`/restores?checkpoint=${checkpoint.id}&namespace=${ckpt.namespace || ckpt.podNamespace}`}
          className="inline-flex items-center gap-2 px-4 py-2 rounded-lg bg-blue-500 text-white font-medium text-sm hover:bg-blue-600 transition-colors"
        >
          <ArrowPathIcon className="h-5 w-5" />
          Restore
        </Link>
      </div>

      {/* Metadata */}
      <div className="rounded-xl bg-terminal-surface border border-terminal-border overflow-hidden">
        <div className="divide-y divide-terminal-border">
          {fields.map(({ label, value }) => (
            <div key={label} className="flex px-6 py-3">
              <dt className="w-32 text-sm font-medium text-terminal-muted">{label}</dt>
              <dd className="text-sm text-terminal-text flex-1">
                {label === 'Image' ? <code className="text-xs bg-terminal-bg px-2 py-0.5 rounded">{value}</code> : value}
              </dd>
            </div>
          ))}
        </div>
      </div>

      {/* File Browser */}
      <div className="rounded-xl bg-terminal-surface border border-terminal-border overflow-hidden">
        <div className="flex items-center justify-between px-6 py-4 border-b border-terminal-border">
          <h3 className="text-lg font-semibold text-terminal-text">Captured Sources</h3>
          {files === null && (
            <button onClick={() => loadFiles()} className="px-3 py-1.5 text-xs bg-gpu-600 hover:bg-gpu-500 text-white rounded-lg transition">
              Browse
            </button>
          )}
        </div>

        {filesLoading && <div className="px-6 py-4 text-terminal-muted text-sm">Loading...</div>}

        {files !== null && !filesLoading && (
          <div className="divide-y divide-terminal-border/50">
            {files.length === 0 ? (
              <div className="px-6 py-4 text-terminal-muted text-sm">No sources</div>
            ) : files.map((file) => (
              <div key={file.path} className="flex items-center px-6 py-2 hover:bg-terminal-bg/50 transition">
                <span className="flex items-center gap-2 text-sm text-terminal-text">
                  <FolderIcon className="h-4 w-4 text-gpu-400" />
                  <code className="text-xs">{file.path}</code>
                  <span className="text-xs text-terminal-muted">({file.type})</span>
                </span>
                <span className="ml-auto text-xs text-terminal-muted">
                  {file.fileCount} files · {formatSize(file.sizeBytes)}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    </motion.div>
  );
}
