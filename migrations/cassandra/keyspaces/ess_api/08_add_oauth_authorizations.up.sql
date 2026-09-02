UPDATE ess_api.namespaces
SET
  updated_at = toTimestamp(now()),
  oauth_authorizations = oauth_authorizations + {
    'nvcf-api': {
      id: 'nvcf-api', 
      name: 'nvcf api service client', 
      jwks_url: '${ESS_JWKS_URL}',
      issuer: '${ESS_ISSUER_URL}',
      type: null
    },
    'nvct-api': {
      id: 'nvct-api',
      name: 'nvct api service client',
      jwks_url: '${ESS_JWKS_URL}',
      issuer: '${ESS_ISSUER_URL}',
      type: null
    }
  }
WHERE namespace = 'nvcf';
