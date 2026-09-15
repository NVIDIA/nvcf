CREATE KEYSPACE IF NOT EXISTS api_keys_api WITH replication = {'class': 'NetworkTopologyStrategy', 'ncp': '${REPLICA_COUNT}' }  AND durable_writes = true;
