import { useAuditLog } from '../api/hooks';

export function Events() {
  const { data: entries, isLoading } = useAuditLog({ limit: 50 });

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-bold text-terminal-text">Events</h2>
        <p className="text-terminal-muted text-sm mt-1">System-wide audit trail and event history</p>
      </div>

      {isLoading ? (
        <div className="text-terminal-muted">Loading...</div>
      ) : !entries?.length ? (
        <div className="text-center py-12 text-terminal-muted">No events recorded yet</div>
      ) : (
        <div className="bg-terminal-surface border border-terminal-border rounded-xl overflow-hidden">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-terminal-border text-terminal-muted text-left">
                <th className="px-4 py-3">Time</th>
                <th className="px-4 py-3">User</th>
                <th className="px-4 py-3">Action</th>
                <th className="px-4 py-3">Resource</th>
                <th className="px-4 py-3">Message</th>
              </tr>
            </thead>
            <tbody>
              {entries.map((entry, i) => (
                <tr key={i} className="border-b border-terminal-border/50 hover:bg-terminal-bg/50">
                  <td className="px-4 py-3 text-terminal-muted text-xs whitespace-nowrap">{new Date(entry.timestamp).toLocaleString()}</td>
                  <td className="px-4 py-3 text-gpu-400 font-medium">{entry.actor}</td>
                  <td className="px-4 py-3">
                    <span className={`text-xs px-2 py-0.5 rounded ${
                      entry.action.includes('create') ? 'bg-green-500/20 text-green-400' :
                      entry.action.includes('delete') ? 'bg-red-500/20 text-red-400' :
                      'bg-blue-500/20 text-blue-400'
                    }`}>{entry.action}</span>
                  </td>
                  <td className="px-4 py-3 text-terminal-text">{entry.resourceId || entry.resource}</td>
                  <td className="px-4 py-3 text-terminal-muted text-xs truncate max-w-md">{entry.message}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
