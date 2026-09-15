import { Fragment, useState } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { Dialog, Transition } from '@headlessui/react';
import { motion } from 'framer-motion';
import {
  Bars3Icon,
  HomeIcon,
  PlayIcon,
  ArchiveBoxIcon,
  ArrowPathIcon,
  ServerStackIcon,
  ShieldCheckIcon,
  Cog6ToothIcon,
  CpuChipIcon,
  CircleStackIcon,
  ChartBarIcon,
  ChartBarSquareIcon,
  ArrowsRightLeftIcon,
  ArrowTopRightOnSquareIcon,
} from '@heroicons/react/24/outline';
import clsx from 'clsx';
import { useQuery } from '@tanstack/react-query';
import { fetchObservabilityTargets, type ObservabilityTarget } from '../api/client';

const navigation = [
  { name: 'Demo', href: '/demo', icon: PlayIcon },
  { name: 'Dashboard', href: '/dashboard', icon: HomeIcon },
  { name: 'Captures', href: '/checkpoints', icon: ArchiveBoxIcon },
  { name: 'Restores', href: '/restores', icon: ArrowPathIcon },
  { name: 'Nodes', href: '/nodes', icon: ServerStackIcon },
  { name: 'Blobstore', href: '/blobstore', icon: CircleStackIcon },
  { name: 'Policies', href: '/policies', icon: ShieldCheckIcon },
  { name: 'Settings', href: '/settings', icon: Cog6ToothIcon },
];

// Icon per observability target. Names match the .name field from
// /api/v1/observability (grafana | jaeger | prometheus).
const observabilityIcons: Record<string, typeof ChartBarIcon> = {
  grafana: ChartBarSquareIcon,
  jaeger: ArrowsRightLeftIcon,
  prometheus: ChartBarIcon,
};

// useObservability fetches the discovered targets every 30s. Used by
// the sidebar to render the Observability section only for tools the
// cluster actually has installed.
function useObservability() {
  return useQuery({
    queryKey: ['observability'],
    queryFn: fetchObservabilityTargets,
    refetchInterval: 30_000,
    // Don't spam NvSnap logs when running against a vanilla install.
    retry: false,
  });
}

// ObservabilityLink renders one nav entry for an installed observability
// target. Same visual treatment as the main nav, plus the external-link
// glyph since clicks navigate out of the React SPA (the nvsnap-server
// reverse-proxy serves Grafana/Jaeger/Prometheus directly under
// /observability/<name>/).
function ObservabilityLink({ target }: { target: ObservabilityTarget }) {
  const Icon = observabilityIcons[target.name] || ChartBarIcon;
  return (
    <a
      href={target.url}
      title={target.description}
      className="group flex items-center gap-x-3 rounded-md p-2 text-sm leading-6 font-medium text-terminal-muted hover:text-terminal-text hover:bg-terminal-border/50 transition-all duration-200"
    >
      <Icon className="h-6 w-6 shrink-0 text-terminal-muted group-hover:text-terminal-text transition-colors" aria-hidden="true" />
      <span className="flex-1">{target.displayName}</span>
      <ArrowTopRightOnSquareIcon className="h-4 w-4 shrink-0 text-terminal-muted/60 group-hover:text-terminal-muted" />
    </a>
  );
}

interface LayoutProps {
  children: React.ReactNode;
}

function UserSelector() {
  const [user, setUser] = useState(() => localStorage.getItem('nvsnap-user') || '');
  const handleChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    setUser(e.target.value);
    localStorage.setItem('nvsnap-user', e.target.value);
  };
  return (
    <div className="flex items-center gap-2">
      <span className="text-xs text-terminal-muted">User:</span>
      <input
        type="text"
        value={user}
        onChange={handleChange}
        placeholder="your name"
        className="w-28 px-2 py-1 text-xs rounded bg-terminal-surface border border-terminal-border text-terminal-text placeholder-terminal-muted focus:outline-none focus:border-gpu-500"
      />
    </div>
  );
}

export function Layout({ children }: LayoutProps) {
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const obsTargets = useObservability().data ?? [];
  const availableObsTargets = obsTargets.filter(t => t.available);
  const location = useLocation();

  return (
    <>
      {/* Mobile sidebar */}
      <Transition.Root show={sidebarOpen} as={Fragment}>
        <Dialog as="div" className="relative z-50 lg:hidden" onClose={setSidebarOpen}>
          <Transition.Child
            as={Fragment}
            enter="transition-opacity ease-linear duration-300"
            enterFrom="opacity-0"
            enterTo="opacity-100"
            leave="transition-opacity ease-linear duration-300"
            leaveFrom="opacity-100"
            leaveTo="opacity-0"
          >
            <div className="fixed inset-0 bg-black/80" />
          </Transition.Child>

          <div className="fixed inset-0 flex">
            <Transition.Child
              as={Fragment}
              enter="transition ease-in-out duration-300 transform"
              enterFrom="-translate-x-full"
              enterTo="translate-x-0"
              leave="transition ease-in-out duration-300 transform"
              leaveFrom="translate-x-0"
              leaveTo="-translate-x-full"
            >
              <Dialog.Panel className="relative mr-16 flex w-full max-w-xs flex-1">
                <div className="flex grow flex-col gap-y-5 overflow-y-auto bg-terminal-surface border-r border-terminal-border px-6 pb-4">
                  <div className="flex h-16 shrink-0 items-center">
                    <Logo />
                  </div>
                  <nav className="flex flex-1 flex-col">
                    <ul role="list" className="flex flex-1 flex-col gap-y-7">
                      <li>
                        <ul role="list" className="-mx-2 space-y-1">
                          {navigation.map((item) => (
                            <li key={item.name}>
                              <Link
                                to={item.href}
                                onClick={() => setSidebarOpen(false)}
                                className={clsx(
                                  location.pathname === item.href
                                    ? 'bg-terminal-border text-gpu-400'
                                    : 'text-terminal-muted hover:text-terminal-text hover:bg-terminal-border/50',
                                  'group flex gap-x-3 rounded-md p-2 text-sm leading-6 font-medium transition-colors'
                                )}
                              >
                                <item.icon
                                  className={clsx(
                                    location.pathname === item.href
                                      ? 'text-gpu-400'
                                      : 'text-terminal-muted group-hover:text-terminal-text',
                                    'h-6 w-6 shrink-0 transition-colors'
                                  )}
                                  aria-hidden="true"
                                />
                                {item.name}
                              </Link>
                            </li>
                          ))}
                        </ul>
                      </li>
                    </ul>
                  </nav>
                </div>
              </Dialog.Panel>
            </Transition.Child>
          </div>
        </Dialog>
      </Transition.Root>

      {/* Desktop sidebar */}
      <div className="hidden lg:fixed lg:inset-y-0 lg:z-50 lg:flex lg:w-72 lg:flex-col">
        <div className="flex grow flex-col gap-y-5 overflow-y-auto bg-terminal-surface border-r border-terminal-border px-6 pb-4">
          <div className="flex h-16 shrink-0 items-center">
            <Logo />
          </div>
          <nav className="flex flex-1 flex-col">
            <ul role="list" className="flex flex-1 flex-col gap-y-7">
              <li>
                <ul role="list" className="-mx-2 space-y-1">
                  {navigation.map((item) => (
                    <li key={item.name}>
                      <Link
                        to={item.href}
                        className={clsx(
                          location.pathname.startsWith(item.href)
                            ? 'bg-terminal-border text-gpu-400'
                            : 'text-terminal-muted hover:text-terminal-text hover:bg-terminal-border/50',
                          'group flex gap-x-3 rounded-md p-2 text-sm leading-6 font-medium transition-all duration-200'
                        )}
                      >
                        <item.icon
                          className={clsx(
                            location.pathname.startsWith(item.href)
                              ? 'text-gpu-400'
                              : 'text-terminal-muted group-hover:text-terminal-text',
                            'h-6 w-6 shrink-0 transition-colors'
                          )}
                          aria-hidden="true"
                        />
                        {item.name}
                        {location.pathname.startsWith(item.href) && (
                          <motion.div
                            layoutId="activeTab"
                            className="absolute left-0 w-1 h-8 bg-gpu-500 rounded-r-full"
                          />
                        )}
                      </Link>
                    </li>
                  ))}
                </ul>
              </li>

              {availableObsTargets.length > 0 && (
                <li>
                  <div className="text-xs font-semibold leading-6 text-terminal-muted px-2 uppercase tracking-wider">
                    Observability
                  </div>
                  <ul role="list" className="-mx-2 mt-2 space-y-1">
                    {availableObsTargets.map((t) => (
                      <li key={t.name}>
                        <ObservabilityLink target={t} />
                      </li>
                    ))}
                  </ul>
                </li>
              )}

              <li className="mt-auto">
                <div className="rounded-lg bg-gradient-to-br from-gpu-900/50 to-terminal-border p-4 border border-terminal-border">
                  <div className="flex items-center gap-3">
                    <div className="p-2 rounded-lg bg-gpu-500/20">
                      <CpuChipIcon className="h-6 w-6 text-gpu-400" />
                    </div>
                    <div>
                      <p className="text-sm font-medium text-terminal-text">NvSnap v0.1.0</p>
                      <p className="text-xs text-terminal-muted">Snapshot & Restore</p>
                    </div>
                  </div>
                </div>
              </li>
            </ul>
          </nav>
        </div>
      </div>

      {/* Main content */}
      <div className="lg:pl-72">
        {/* Mobile header */}
        <div className="sticky top-0 z-40 flex h-16 shrink-0 items-center gap-x-4 border-b border-terminal-border bg-terminal-surface/80 backdrop-blur-xl px-4 sm:gap-x-6 sm:px-6 lg:px-8">
          <button
            type="button"
            className="-m-2.5 p-2.5 text-terminal-muted lg:hidden"
            onClick={() => setSidebarOpen(true)}
          >
            <span className="sr-only">Open sidebar</span>
            <Bars3Icon className="h-6 w-6" aria-hidden="true" />
          </button>

          <div className="flex flex-1 gap-x-4 self-stretch lg:gap-x-6">
            <div className="flex flex-1 items-center">
              <h1 className="text-lg font-semibold text-terminal-text">
                {navigation.find((n) => location.pathname.startsWith(n.href))?.name || 'NvSnap'}
              </h1>
            </div>
            <div className="flex items-center gap-x-4 lg:gap-x-6">
              <UserSelector />
              <StatusIndicator />
            </div>
          </div>
        </div>

        {/* Page content */}
        <main className="py-8">
          <div className="px-4 sm:px-6 lg:px-8">
            <motion.div
              initial={{ opacity: 0, y: 20 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.3 }}
            >
              {children}
            </motion.div>
          </div>
        </main>
      </div>
    </>
  );
}

function Logo() {
  return (
    <div className="flex items-center gap-3">
      <div className="relative">
        <div className="absolute inset-0 bg-gpu-500 blur-lg opacity-50 animate-pulse-slow" />
        <div className="relative p-2 rounded-lg bg-gradient-to-br from-gpu-500 to-gpu-700">
          <CpuChipIcon className="h-8 w-8 text-white" />
        </div>
      </div>
      <div>
        <h1 className="text-xl font-bold text-terminal-text tracking-tight">NvSnap</h1>
        <p className="text-xs text-terminal-muted">Snapshot & Restore</p>
      </div>
    </div>
  );
}

function StatusIndicator() {
  return (
    <div className="flex items-center gap-2 px-3 py-1.5 rounded-full bg-gpu-500/10 border border-gpu-500/20">
      <span className="relative flex h-2 w-2">
        <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-gpu-400 opacity-75" />
        <span className="relative inline-flex rounded-full h-2 w-2 bg-gpu-500" />
      </span>
      <span className="text-xs font-medium text-gpu-400">System Healthy</span>
    </div>
  );
}
