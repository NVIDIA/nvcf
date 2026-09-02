INSERT INTO ess_api.namespaces (
  namespace,
  entity_types,
  created_at,
  updated_at,
  entity_hash_size,
  require_lwt_for_secret_version_writes,
  ssa_authorizations,
  notary_authorizations
)
VALUES (
  'nvcf',
  {'functions': {name: 'functions', deleted_at: null}, 'accounts': {name: 'accounts', deleted_at: null}, 'tasks': {name: 'tasks', deleted_at: null}},
  toTimestamp(now()),
  toTimestamp(now()),
  10,
  False,
  {
    'nvcf-api': {id: 'nvcf-api', name: 'nvcf api service client', jwks_url: '${ESS_JWKS_URL}', issuer: '${ESS_ISSUER_URL}', type: 'SSA'},
    'nvct-api': {id: 'nvct-api', name: 'nvct api service client', jwks_url: '${ESS_JWKS_URL}', issuer: '${ESS_ISSUER_URL}', type: 'SSA'}
  },
  {
    'nvcf-api': {id: 'nvcf-api', name: 'nvcf notary client', jwks_url: '${NOTARY_BASE_URL}/.well-known/jwks.json', issuer: '${NOTARY_BASE_URL}', type: 'NOTARY'},
    'nvct-api': {id: 'nvct-api', name: 'nvct api notary client', jwks_url: '${NOTARY_BASE_URL}/.well-known/jwks.json', issuer: '${NOTARY_BASE_URL}', type: 'NOTARY'}
  }
);
