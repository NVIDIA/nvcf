-- Unique per-acquisition ownership token for conditional lock refresh/release.
ALTER TABLE nvcf_autoscaler.locks ADD lock_token text;
