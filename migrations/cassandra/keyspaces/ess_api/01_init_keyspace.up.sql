{{ $replicaCount := envOrDefault "REPLICA_COUNT" "3" }}
CREATE KEYSPACE IF NOT EXISTS ess_api WITH replication = {'class': 'NetworkTopologyStrategy', 'ncp': '{{ $replicaCount }}' }  AND durable_writes = true;
