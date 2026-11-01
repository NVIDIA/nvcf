CREATE ROLE IF NOT EXISTS api_keys_api_app_access;
GRANT SELECT, MODIFY on keyspace api_keys_api to api_keys_api_app_access;
GRANT SELECT on keyspace system to api_keys_api_app_access;

CREATE ROLE IF NOT EXISTS api_keys_api_app_v0 with login = true and password = '${SERVICE_ROLE_PASSWORD}';

INSERT INTO system_auth.role_members (role, member) VALUES ('api_keys_api_app_access', 'api_keys_api_app_v0');
UPDATE system_auth.roles SET member_of = member_of + {'api_keys_api_app_access'} where role = 'api_keys_api_app_v0';
