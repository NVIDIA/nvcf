-- Canonical schema for event_ledger keyspace.

-- Table for K8s events coming from the v3 endpoint
-- Stores one row per unique (namespace, context, event_name) combination
-- Multiple event_names per context are retained
CREATE TABLE IF NOT EXISTS event_ledger.events_v3 (
    namespace TEXT,
    context TEXT,
    event_name TEXT,
    source TEXT,
    details BLOB,  -- Free-form JSON
    timestamp TIMESTAMP,
    created_at TIMESTAMP,
    updated_at TIMESTAMP,
    PRIMARY KEY ((namespace, context), event_name)
);

-- Stats table for latest event per namespace+context
-- Only stores the MOST RECENT event name (replaces on any new event regardless of event_name)
-- For full details, query events_v3
CREATE TABLE IF NOT EXISTS event_ledger.stats_v3 (
    namespace TEXT,
    context TEXT,
    event_name TEXT,       -- The latest event name
    timestamp TIMESTAMP,
    created_at TIMESTAMP,  -- When this namespace+context first got an event
    updated_at TIMESTAMP,  -- When last updated
    PRIMARY KEY ((namespace), context)
);
