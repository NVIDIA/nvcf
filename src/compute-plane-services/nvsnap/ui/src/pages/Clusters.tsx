import { useNodes } from '../api/hooks';

export function Clusters() {
  const { data: nodes, isLoading } = useNodes();

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-bold text-terminal-text">Clusters</h2>
        <p className="text-terminal-muted text-sm mt-1">GPU cluster topology and node status</p>
      </div>

      {isLoading ? (
        <div className="text-terminal-muted">Loading...</div>
      ) : !nodes?.length ? (
        <div className="text-center py-12 text-terminal-muted">No nodes found</div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
          {nodes.map((node) => (
            <div key={node.name} className="bg-terminal-surface border border-terminal-border rounded-xl p-6">
              <div className="flex items-center justify-between mb-4">
                <h3 className="text-lg font-bold text-terminal-text">{node.name.split('-').pop()}</h3>
                <span className={`text-xs px-2 py-1 rounded-full ${node.status === 'Ready' ? 'bg-green-500/20 text-green-400' : 'bg-red-500/20 text-red-400'}`}>
                  {node.status}
                </span>
              </div>
              <div className="space-y-2 text-sm">
                <div className="flex justify-between">
                  <span className="text-terminal-muted">GPUs</span>
                  <span className="text-terminal-text font-medium">{node.gpuCount}x {node.gpuModel || 'H100'}</span>
                </div>
                <div className="flex justify-between">
                  <span className="text-terminal-muted">Agent</span>
                  <span className={node.agentReady ? 'text-green-400' : 'text-red-400'}>
                    {node.agentReady ? 'Ready' : 'Not Ready'}
                  </span>
                </div>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
