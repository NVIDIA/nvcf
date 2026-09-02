-- Reconcile both current and compatibility authorization maps. This forward
-- migration repairs clusters that already applied the legacy, single-plane
-- seeds in migrations 04, 06, or 08.

ALTER TABLE ess_api.namespaces ADD IF NOT EXISTS (
    oauth_authorizations    map<text, frozen<authorization>>,
    ssa_authorizations      map<text, frozen<authorization>>,
    authorizations_version  timeuuid
);

UPDATE ess_api.namespaces
SET
  updated_at = toTimestamp(now()),
  authorizations_version = now(),
  ssa_authorizations = ssa_authorizations + {
    'nvcf-api': {id: 'nvcf-api', name: 'nvcf api service client', jwks_url: '${ESS_JWKS_URL}', issuer: '${ESS_ISSUER_URL}', type: 'SSA'},
    'nvct-api': {id: 'nvct-api', name: 'nvct api service client', jwks_url: '${ESS_JWKS_URL}', issuer: '${ESS_ISSUER_URL}', type: 'SSA'}
  },
  oauth_authorizations = oauth_authorizations + {
    'nvcf-api': {id: 'nvcf-api', name: 'nvcf api service client', jwks_url: '${ESS_JWKS_URL}', issuer: '${ESS_ISSUER_URL}', type: null},
    'nvct-api': {id: 'nvct-api', name: 'nvct api service client', jwks_url: '${ESS_JWKS_URL}', issuer: '${ESS_ISSUER_URL}', type: null}
  },
  notary_authorizations = notary_authorizations + {
    'nvcf-api': {id: 'nvcf-api', name: 'nvcf notary client', jwks_url: '${NOTARY_BASE_URL}/.well-known/jwks.json', issuer: '${NOTARY_BASE_URL}', type: 'NOTARY'},
    'nvct-api': {id: 'nvct-api', name: 'nvct api notary client', jwks_url: '${NOTARY_BASE_URL}/.well-known/jwks.json', issuer: '${NOTARY_BASE_URL}', type: 'NOTARY'}
  }
WHERE namespace = 'nvcf';
