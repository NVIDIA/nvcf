import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { Toaster } from 'react-hot-toast';
import { Layout } from './components/Layout';
import { Dashboard } from './pages/Dashboard';
import { Checkpoints } from './pages/Checkpoints';
import { CheckpointDetail } from './pages/CheckpointDetail';
import { Restores } from './pages/Restores';
import { Nodes } from './pages/Nodes';
import { Policies } from './pages/Policies';
import { Settings } from './pages/Settings';
import { Clusters } from './pages/Clusters';
import { Events } from './pages/Events';
import { Demo } from './pages/Demo';
import { Blobstore } from './pages/Blobstore';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 5000,
      refetchInterval: 10000,
    },
  },
});

function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <div className="min-h-screen bg-terminal-bg text-terminal-text">
          <Layout>
            <Routes>
              <Route path="/" element={<Navigate to="/demo" replace />} />
              <Route path="/demo" element={<Demo />} />
              <Route path="/dashboard" element={<Dashboard />} />
              <Route path="/checkpoints" element={<Checkpoints />} />
              <Route path="/checkpoints/:id" element={<CheckpointDetail />} />
              <Route path="/restores" element={<Restores />} />
              <Route path="/nodes" element={<Nodes />} />
              <Route path="/blobstore" element={<Blobstore />} />
              <Route path="/clusters" element={<Clusters />} />
              <Route path="/events" element={<Events />} />
              <Route path="/policies" element={<Policies />} />
              <Route path="/settings" element={<Settings />} />
            </Routes>
          </Layout>
        </div>
      </BrowserRouter>
      <Toaster
        position="bottom-right"
        toastOptions={{
          className: 'bg-terminal-surface border border-terminal-border text-terminal-text',
          duration: 4000,
        }}
      />
    </QueryClientProvider>
  );
}

export default App;
