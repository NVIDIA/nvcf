// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { useState, useEffect, useRef } from 'react';
import { wsManager } from './websocket';
import type { DemoState, DemoPod } from './client';
import { fetchDemoState, fetchDemoPods } from './client';

export function useDemoStateWS(): DemoState | null {
  const [state, setState] = useState<DemoState | null>(null);

  // Initial fetch
  useEffect(() => {
    fetchDemoState().then(setState).catch(() => {});
  }, []);

  // WebSocket for real-time updates
  useEffect(() => {
    wsManager.connect();
    const unsub = wsManager.subscribe('demo:state', (msg) => {
      setState(msg as DemoState);
    });
    return unsub;
  }, []);

  // Polling fallback — ensures updates even when WebSocket fails
  useEffect(() => {
    const interval = setInterval(() => {
      fetchDemoState().then(setState).catch(() => {});
    }, 2000);
    return () => clearInterval(interval);
  }, []);

  return state;
}

export function useDemoPodsWS(): DemoPod[] {
  const [pods, setPods] = useState<DemoPod[]>([]);

  useEffect(() => {
    fetchDemoPods().then(setPods).catch(() => {});
  }, []);

  useEffect(() => {
    wsManager.connect();
    const unsub = wsManager.subscribe('demo:pods', (msg) => {
      const data = msg as { pods: DemoPod[] };
      setPods(data.pods || []);
    });
    return unsub;
  }, []);

  // Polling fallback
  useEffect(() => {
    const interval = setInterval(() => {
      fetchDemoPods().then(setPods).catch(() => {});
    }, 3000);
    return () => clearInterval(interval);
  }, []);

  return pods;
}

export interface LogEntry {
  timestamp: string;
  message: string;
}

export function useDemoLogsWS(): LogEntry[] {
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const logsRef = useRef<LogEntry[]>([]);

  useEffect(() => {
    wsManager.connect();
    const unsub = wsManager.subscribe('demo:logs', (msg) => {
      const entry = msg as LogEntry;
      logsRef.current = [...logsRef.current.slice(-199), entry]; // Keep last 200
      setLogs(logsRef.current);
    });
    return unsub;
  }, []);

  return logs;
}
