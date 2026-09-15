CREATE ROLE IF NOT EXISTS nvcf_autoscaler_app_access;
GRANT SELECT, MODIFY on keyspace nvcf_autoscaler to nvcf_autoscaler_app_access;
GRANT SELECT on keyspace system to nvcf_autoscaler_app_access;

CREATE ROLE IF NOT EXISTS nvcf_autoscaler_app_v0 with login = true and password = '${SERVICE_ROLE_PASSWORD}';

INSERT INTO system_auth.role_members (role, member) VALUES ('nvcf_autoscaler_app_access', 'nvcf_autoscaler_app_v0');
UPDATE system_auth.roles SET member_of = member_of + {'nvcf_autoscaler_app_access'} where role = 'nvcf_autoscaler_app_v0';
