CREATE ROLE IF NOT EXISTS ess_api_app_access;
GRANT SELECT, MODIFY on keyspace ess_api to ess_api_app_access;
GRANT SELECT on keyspace system to ess_api_app_access;

CREATE ROLE IF NOT EXISTS ess_api_app_v0 with login = true and password = '${SERVICE_ROLE_PASSWORD}';

INSERT INTO system_auth.role_members (role, member) VALUES ('ess_api_app_access', 'ess_api_app_v0');
UPDATE system_auth.roles SET member_of = member_of + {'ess_api_app_access'} where role = 'ess_api_app_v0';
