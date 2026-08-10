UPDATE ess_api.namespaces
SET
  updated_at = toTimestamp(now()),
  oauth_authorizations = oauth_authorizations + {
    'nvcf-api': {
      id: 'nvcf-api', 
      name: 'nvcf api service client', 
      jwks_url: 'http://openbao-server.vault-system.svc.cluster.local:8200/v1/services/ess-api/jwt/jwks',
      issuer: 'http://ess-api.ess.svc.cluster.local',
      type: null
    },
    'nvct-api': {
      id: 'nvct-api',
      name: 'nvct api service client',
      jwks_url: 'http://openbao-server.vault-system.svc.cluster.local:8200/v1/services/ess-api/jwt/jwks',
      issuer: 'http://ess-api.ess.svc.cluster.local',
      type: null
    }
  }
WHERE namespace = 'nvcf';
