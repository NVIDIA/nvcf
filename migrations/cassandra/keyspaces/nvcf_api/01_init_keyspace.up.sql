CREATE KEYSPACE IF NOT EXISTS nvcf_api WITH replication = {'class': 'NetworkTopologyStrategy', 'ncp': '${REPLICA_COUNT}' }  AND durable_writes = true;
