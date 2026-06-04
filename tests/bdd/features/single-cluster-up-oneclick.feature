@ncp-local @single-cluster @oneclick
Feature: Bring up a local single-cluster NVCF stack with the self-hosted up one-click
  As a self-managed NVCF operator,
  I want one command to install the control plane, register the cluster, and
  install the NVCA operator on a single local k3d cluster, so that I can
  validate the documented quickstart (nvcf-cli self-hosted up) end to end.

  # self-hosted up is the local k3d single-cluster one-click documented in
  # docs/user/quickstart.md. It defaults to --env local and requires a k3d-*
  # context, then runs the full pipeline in one command: preflight, resolve
  # stack, install the control plane, mint the admin token + discover the
  # issuer, register the cluster, install the compute plane, and print a
  # health summary. It rejects remote and split-cluster installs (those use
  # the Helmfile path), so this feature exercises the single local k3d layout.
  #
  # This is the CLI one-click path; the sibling single-cluster-up.feature
  # exercises the lower-level install --control-plane + compute-plane
  # register/install primitives against the same ncp-local topology.

  Rule: self-hosted up installs the control plane and compute plane in one command

    Background:
      Given environment variable "NVCF_CLI" is set
      And environment variable "NGC_API_KEY" is set
      # SAMPLE_NGC_ORG / SAMPLE_NGC_TEAM are consumed by the
      # build-and-deploy-cluster credential-provider validation the
      # `a single-cluster ncp-local cluster is running` step runs.
      And environment variable "SAMPLE_NGC_ORG" is set
      And environment variable "SAMPLE_NGC_TEAM" is set
      # self-hosted up --env local reads operator-authored local secrets
      # from deploy/stacks/self-managed/secrets/local-secrets.yaml (only
      # secrets.yaml.template is tracked). Author it from the template the
      # same way the other local features do; the Ledger restores or
      # removes it at suite teardown.
      And I copy the file "deploy/stacks/self-managed/secrets/secrets.yaml.template" to "deploy/stacks/self-managed/secrets/local-secrets.yaml"
      And I substitute "REPLACE_WITH_BASE64_DOCKER_CREDENTIAL" in file "deploy/stacks/self-managed/secrets/local-secrets.yaml" with base64 of "$oauthtoken:${NGC_API_KEY}"
      # Conflict precheck: ncp-local-cp's k3d serverlb claims
      # 0.0.0.0:8080/8443/4222, the same host ports single-cluster
      # ncp-local needs. Fail loudly so the operator runs
      # `make -C tools/ncp-local-cluster destroy-multicluster` before
      # retrying. `k3d cluster get` exits 1 when the cluster is absent.
      And I run command "k3d cluster get ncp-local-cp"
      And the command exit code should be 1
      And a single-cluster ncp-local cluster is running
      # self-hosted up renders manifests referencing
      # imagePullSecrets: [{name: nvcr-pull-secret}]. Create the secret in
      # the control-plane namespaces and nvca-operator before install so
      # pods can pull nvcr.io images; the operator propagates it onward.
      And the "nvcr-pull-secret" image pull secret exists in namespaces:
        | cassandra-system |
        | nats-system      |
        | nvcf             |
        | api-keys         |
        | ess              |
        | sis              |
        | vault-system     |
        | nvca-operator    |
        | cert-manager     |

    Scenario: Operator brings up the single-cluster ncp-local stack with self-hosted up
      # The BDD CLI config fixture mirrors the quickstart's
      # deploy/stacks/self-managed/nvcf-cli-local.yaml. Keeping it under
      # tests/bdd isolates feature tests from local operator edits.
      # One command runs every phase against the current k3d context. Run it
      # with a terminal: after installing the control plane, up's auth-gate
      # mints the admin token via init only when stdin is a TTY (otherwise it
      # refuses rather than blocking on a stdin read). A real operator runs
      # this from a terminal; the pty-backed step reproduces that.
      #
      # The quickstart omits --plain so users see the TUI. This BDD scenario
      # adds --plain because stdout and stderr are captured by a
      # noninteractive test runner.
      When I run command with a terminal:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --stack deploy/stacks/self-managed --env local --plain --icms-url http://sis.localhost:8080 up --cluster-name ncp-local --region us-west-1 --nca-id nvcf-default --refresh-token
        """
      Then the command exit code should be 0

      # Control plane releases are deployed on the single k3d cluster.
      When I run command "helm list --all-namespaces --kube-context k3d-ncp-local -o json"
      Then the json output should contain rows:
        | name           | namespace        | status   |
        | nats           | nats-system      | deployed |
        | cassandra      | cassandra-system | deployed |
        | openbao-server | vault-system     | deployed |
        | api-keys       | api-keys         | deployed |
        | sis            | sis              | deployed |
        | api            | nvcf             | deployed |

      # The compute plane (NVCA operator) is deployed on the same cluster.
      When I run command "helm list -n nvca-operator --kube-context k3d-ncp-local -o json"
      Then the json output should contain rows:
        | name          | namespace     | status   |
        | nvca-operator | nvca-operator | deployed |

      # The agent registered by up reports healthy.
      When I run command "kubectl wait nvcfbackend ncp-local -n nvca-operator --context k3d-ncp-local --for=jsonpath={.status.agentStatus}=healthy --timeout=10m"
      Then the command exit code should be 0
