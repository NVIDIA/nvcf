CREATE KEYSPACE IF NOT EXISTS event_ledger WITH replication = {'class': 'NetworkTopologyStrategy', 'ncp': '${REPLICA_COUNT}' }  AND durable_writes = true;
