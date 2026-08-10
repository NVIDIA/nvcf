ALTER TABLE ess_api.namespaces ADD IF NOT EXISTS (
    oauth_authorizations    map<text, frozen<authorization>>,
    authorizations_version  timeuuid
);
