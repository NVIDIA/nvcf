auto_auth = {
  method = {
    type = "jwt"
    mount_path = "%v"
    namespace = "%v"
    config = {
      path = "/var/run/secrets/kubernetes.io/serviceaccount-vault/token"
      role = "%v"
      skip_jwt_cleanup = true
    }
  }
  sinks = {
    sink = {
      type = "file"
      config = {
        path = "/home/vault/.agent-token%v"
      }
    }
  }
}
exit_after_auth = %v
pid_file = "/home/vault/.pid"
template_config {
  static_secret_render_interval = "2m"
  exit_on_retry_failure = true
}
template {
  source      = "/vault/configs/template.hcl"
  destination = "%v"
}
vault = {
  address = "%v"
}