-- Register nvct-api for legacy ESS authentication during the 0.6.x release.
--
-- The 0.6.x ESS API validates service assertions from ssa_authorizations and
-- validates task-secret reads from notary_authorizations.
UPDATE ess_api.namespaces
SET
  ssa_authorizations = ssa_authorizations + {
    'nvct-api': {
      id: 'nvct-api',
      name: 'nvct api service client',
      jwks_url: 'http://openbao-server.vault-system.svc.cluster.local:8200/v1/services/ess-api/jwt/jwks',
      issuer: 'http://ess-api.ess.svc.cluster.local',
      type: 'SSA'
    }
  },
  notary_authorizations = notary_authorizations + {
    'nvct-api': {
      id: 'nvct-api',
      name: 'nvct api notary client',
      jwks_url: 'http://notary.nvcf.svc.cluster.local:8080/.well-known/jwks.json',
      issuer: 'http://notary.nvcf.svc.cluster.local:8080',
      type: 'NOTARY'
    }
  }
WHERE namespace = 'nvcf';
