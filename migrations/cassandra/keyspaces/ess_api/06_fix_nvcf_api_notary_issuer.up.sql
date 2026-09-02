-- Correct the original nvcf-api NOTARY issuer to match notary-service's
-- ASSERTION_ISSUER_URL and ess-api's strict issuer validation.

UPDATE ess_api.namespaces
SET
  updated_at = toTimestamp(now()),
  notary_authorizations = notary_authorizations + {
    'nvcf-api': {
      id: 'nvcf-api',
      name: 'nvcf notary client',
      jwks_url: '${NOTARY_BASE_URL}/.well-known/jwks.json',
      issuer: '${NOTARY_BASE_URL}',
      type: 'NOTARY'
    }
  }
WHERE namespace = 'nvcf';
