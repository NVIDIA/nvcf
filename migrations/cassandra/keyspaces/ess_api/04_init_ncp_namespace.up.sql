INSERT INTO ess_api.namespaces (
  namespace,
  entity_types,
  created_at,
  updated_at,
  entity_hash_size,
  require_lwt_for_secret_version_writes,
  notary_authorizations
)
VALUES (
  'nvcf',
  {'functions': {name: 'functions', deleted_at: null}, 'accounts': {name: 'accounts', deleted_at: null}, 'tasks': {name: 'tasks', deleted_at: null}},
  toTimestamp(now()),
  toTimestamp(now()),
  10,
  False,
  {'nvcf-api': {id: 'nvcf-api', name: 'nvcf notary client', jwks_url: 'http://notary.nvcf.svc.cluster.local:8080/.well-known/jwks.json', issuer: 'http://notary.nvcf.svc.cluster.local:8080', type: 'NOTARY'}}
);
