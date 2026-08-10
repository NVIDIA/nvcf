import { useHealth } from '../api/hooks';
import { useAuditLog } from '../api/hooks';

export function Settings() {
  const { data: health } = useHealth();
  const { data: audit } = useAuditLog({ limit: 20 });

  return (
    <div className="space-y-8">
      <div>
        <h2 className="text-2xl font-bold text-terminal-text">Settings & Info</h2>
        <p className="text-terminal-muted text-sm mt-1">System status, API documentation, and audit trail</p>
      </div>

      {/* System Status */}
      <div className="bg-terminal-surface border border-terminal-border rounded-xl p-6">
        <h3 className="text-lg font-semibold text-terminal-text mb-4">System Status</h3>
        <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
          <a href="/api/v1/health" target="_blank" className="bg-terminal-bg rounded-lg p-4 hover:bg-terminal-border transition cursor-pointer">
            <p className="text-xs text-terminal-muted">Server</p>
            <p className="text-lg font-bold text-green-400">{health?.status || 'checking...'} &#8599;</p>
          </a>
          <a href="/api/v1/checkpoints?source=agent" target="_blank" className="bg-terminal-bg rounded-lg p-4 hover:bg-terminal-border transition cursor-pointer">
            <p className="text-xs text-terminal-muted">API</p>
            <p className="text-lg font-bold text-terminal-text">/api/v1 &#8599;</p>
          </a>
          <a href="/metrics" target="_blank" className="bg-terminal-bg rounded-lg p-4 hover:bg-terminal-border transition cursor-pointer">
            <p className="text-xs text-terminal-muted">Metrics</p>
            <p className="text-lg font-bold text-terminal-text">/metrics &#8599;</p>
          </a>
          <div className="bg-terminal-bg rounded-lg p-4">
            <p className="text-xs text-terminal-muted">User</p>
            <p className="text-lg font-bold text-terminal-text">{localStorage.getItem('nvsnap-user') || 'anonymous'}</p>
          </div>
        </div>
      </div>

      {/* API Documentation */}
      <div className="bg-terminal-surface border border-terminal-border rounded-xl p-6">
        <h3 className="text-lg font-semibold text-terminal-text mb-4">API Documentation</h3>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4 text-sm">
          {[
            { method: 'GET', path: '/api/v1/nodes', desc: 'List GPU nodes' },
            { method: 'GET', path: '/api/v1/pods', desc: 'List pods' },
            { method: 'POST', path: '/api/v1/checkpoints', desc: 'Create checkpoint' },
            { method: 'GET', path: '/api/v1/checkpoints', desc: 'List checkpoints' },
            { method: 'DELETE', path: '/api/v1/checkpoints/:id', desc: 'Delete checkpoint' },
            { method: 'POST', path: '/api/v1/restores', desc: 'Restore from checkpoint' },
            { method: 'GET', path: '/api/v1/restores', desc: 'List restores' },
            { method: 'GET', path: '/api/v1/retention-policies', desc: 'List retention policies' },
            { method: 'POST', path: '/api/v1/retention-policies', desc: 'Create retention policy' },
            { method: 'GET', path: '/api/v1/audit', desc: 'Audit log' },
            { method: 'GET', path: '/api/v1/ws', desc: 'WebSocket streaming' },
            { method: 'GET', path: '/metrics', desc: 'Prometheus metrics' },
          ].map((ep, i) => (
            <a key={i} href={ep.method === 'GET' && !ep.path.includes(':') ? ep.path : undefined}
               target="_blank" rel="noopener noreferrer"
               className={`flex items-center gap-3 bg-terminal-bg rounded-lg px-4 py-2 transition ${ep.method === 'GET' && !ep.path.includes(':') ? 'hover:bg-terminal-border cursor-pointer' : 'cursor-default'}`}>
              <span className={`text-xs font-mono font-bold px-2 py-0.5 rounded ${ep.method === 'GET' ? 'bg-blue-500/20 text-blue-400' : ep.method === 'POST' ? 'bg-green-500/20 text-green-400' : 'bg-red-500/20 text-red-400'}`}>
                {ep.method}
              </span>
              <code className="text-terminal-text text-xs">{ep.path}</code>
              <span className="text-terminal-muted text-xs ml-auto">{ep.desc}</span>
              {ep.method === 'GET' && !ep.path.includes(':') && <span className="text-gpu-400 text-xs">&#8599;</span>}
            </a>
          ))}
        </div>
      </div>

      {/* Audit Trail */}
      <div className="bg-terminal-surface border border-terminal-border rounded-xl p-6">
        <h3 className="text-lg font-semibold text-terminal-text mb-4">Recent Activity</h3>
        {!audit?.length ? (
          <p className="text-terminal-muted text-sm">No recent activity</p>
        ) : (
          <div className="space-y-2 max-h-96 overflow-y-auto">
            {audit.map((entry, i) => (
              <div key={i} className="flex items-center gap-4 text-sm bg-terminal-bg rounded-lg px-4 py-2">
                <span className="text-terminal-muted text-xs w-36 flex-shrink-0">{new Date(entry.timestamp).toLocaleString()}</span>
                <span className="text-gpu-400 font-medium w-20 flex-shrink-0">{entry.actor}</span>
                <span className="text-terminal-text">{entry.action}</span>
                <span className="text-terminal-muted ml-auto text-xs truncate max-w-xs">{entry.message}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
