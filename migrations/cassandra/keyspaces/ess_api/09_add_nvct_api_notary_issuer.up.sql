-- Register the nvct-api NOTARY client so task-secret reads succeed.
--
-- Task-secret fetches (worker assertion tokens) authenticate on the ess-api
-- notary path, which reads only notary_authorizations. nvct-api was registered
-- for OAuth in 08_add_oauth_authorizations but never for notary in the OSS
-- migration history, so tasks created with secrets fail with
-- "client id=nvct-api is not registered" from ess-api. This mirrors the
-- nvcf-api notary entry in 06_fix_nvcf_api_notary_issuer.

UPDATE ess_api.namespaces
SET
  updated_at = toTimestamp(now()),
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
