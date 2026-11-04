-- Correct the original nvcf-api NOTARY issuer to match notary-service's
-- ASSERTION_ISSUER_URL and ess-api's strict issuer validation.

UPDATE ess_api.namespaces
SET
  updated_at = toTimestamp(now()),
  notary_authorizations = notary_authorizations + {
    'nvcf-api': {
      id: 'nvcf-api',
      name: 'nvcf notary client',
      jwks_url: 'http://notary.nvcf.svc.cluster.local:8080/.well-known/jwks.json',
      issuer: 'http://notary.nvcf.svc.cluster.local:8080',
      type: 'NOTARY'
    }
  }
WHERE namespace = 'nvcf';
