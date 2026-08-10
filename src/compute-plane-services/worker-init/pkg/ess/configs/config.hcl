ess {

  # ESS address — override via ESS_FQDN env var at runtime
  address = "http://localhost:8200"

  namespace = "nvcf"
  ess_agent_token_file = "/config/ess-agent/jwt.token"

  # The default lease duration of each ess secret
  default_lease_duration = "15m"

  # The fraction of the lease duration of a secret
  lease_renewal_threshold = 0.80

}

template {
  source = "/config/ess-agent/secrets.tmpl"
  destination = "/var/secrets/secrets.json"
}

template {
  source = "/config/ess-agent/accounts-secrets.tmpl"
  destination = "/var/secrets/accounts-secrets.json"
}

telemetry {
  prometheus {
    tls_disable = true
    ip = "0.0.0.0"
    port = 10103
  }
}
