-- Keep 0.6.1 compatible with ESS images that still select the legacy
-- ssa_authorizations column while oauth_authorizations migration is in flight.
-- The column can be removed in a later release after all supported ESS images
-- stop selecting it.

ALTER TABLE ess_api.namespaces ADD IF NOT EXISTS (
    ssa_authorizations map<text, frozen<authorization>>
);

UPDATE ess_api.namespaces
SET
  ssa_authorizations = ssa_authorizations + {
    'nvcf-api': {
      id: 'nvcf-api',
      name: 'nvcf api service client',
      jwks_url: 'http://openbao-server.vault-system.svc.cluster.local:8200/v1/services/ess-api/jwt/jwks',
      issuer: 'http://ess-api.ess.svc.cluster.local',
      type: 'SSA'
    },
    'nvct-api': {
      id: 'nvct-api',
      name: 'nvct api service client',
      jwks_url: 'http://openbao-server.vault-system.svc.cluster.local:8200/v1/services/ess-api/jwt/jwks',
      issuer: 'http://ess-api.ess.svc.cluster.local',
      type: 'SSA'
    }
  }
WHERE namespace = 'nvcf';
