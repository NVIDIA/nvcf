CREATE KEYSPACE IF NOT EXISTS sis_api WITH replication = {'class': 'NetworkTopologyStrategy', 'ncp': '${REPLICA_COUNT}' }  AND durable_writes = true;
