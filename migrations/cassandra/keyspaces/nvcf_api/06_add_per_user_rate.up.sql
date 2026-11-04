-- Add per_user_rate field to ratelimit_udt_v2 for per-user rate limiting.
-- Keeps the self-managed schema in sync with managed nvcf-api.
-- Additive and forward-compatible: existing rate limit configs are unaffected.

ALTER TYPE nvcf_api.ratelimit_udt_v2 ADD IF NOT EXISTS per_user_rate TEXT;
