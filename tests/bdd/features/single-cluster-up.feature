@ncp-local @single-cluster @env-ncp-local-single-empty
Feature: Bring up a local single-cluster NVCF stack with the CLI
  As a self-managed NVCF operator,
  I want to install a single-cluster control plane and compute plane
  on a local k3d cluster, so that I can validate the install + register
  workflow before using real infrastructure.

  Rule: install --control-plane installs the CP; compute-plane register/install completes the worker layer

    Background:
      Given environment variable "NVCF_CLI" is set
      And environment variable "NGC_API_KEY" is set
      # SAMPLE_NGC_ORG / SAMPLE_NGC_TEAM are consumed by
      # `make build-and-deploy-cluster` (the credential provider
      # validation step) when the `a single-cluster ncp-local cluster is
      # running` step runs the build target. Without them, that target
      # fails at CREDENTIAL PROVIDER VALIDATION and skips the gateway
      # API setup.
      And environment variable "SAMPLE_NGC_ORG" is set
      And environment variable "SAMPLE_NGC_TEAM" is set
      # self-hosted install --env local pulls credentials from
      # deploy/stacks/self-managed/secrets/local-secrets.yaml, which is
      # gitignored and operator-authored (only secrets.yaml.template is
      # tracked). Author the file from the canonical template the same
      # way the Helmfile feature authors local-bdd-secrets.yaml: copy
      # the template into place, then substitute the placeholder with
      # the base64 NGC dockerconfig credential. Ledger snapshots
      # whatever local-secrets.yaml state existed before the first
      # write (its prior contents or absence) and restores or removes
      # it at suite teardown, so the working tree stays clean.
      And I copy the file "deploy/stacks/self-managed/secrets/secrets.yaml.template" to "deploy/stacks/self-managed/secrets/local-secrets.yaml"
      And I substitute "REPLACE_WITH_BASE64_DOCKER_CREDENTIAL" in file "deploy/stacks/self-managed/secrets/local-secrets.yaml" with base64 of "$oauthtoken:${NGC_API_KEY}"
      # Conflict precheck: ncp-local-cp's k3d serverlb claims
      # 0.0.0.0:8080/8443/4222, the same host ports single-cluster
      # ncp-local needs. Fail loudly here so the operator runs
      # `make -C tools/ncp-local-cluster destroy-multicluster`
      # before retrying this feature, rather than discovering the
      # port collision deep inside the build-and-deploy-cluster
      # make target. `k3d cluster get` exits 1 when the named
      # cluster is absent (k3d v5).
      And I run command "k3d cluster get ncp-local-cp"
      And the command exit code should be 1
      And a single-cluster ncp-local cluster is running
      # nvcf-cli self-hosted install renders helmfile manifests that
      # reference imagePullSecrets: [{name: nvcr-pull-secret}]. Create
      # the secret in each NVCF namespace before install so pods can
      # pull nvcr.io images. The step is idempotent (kubectl apply).
      # Real users were running this loop manually before reaching the
      # install command.
      And the "nvcr-pull-secret" image pull secret exists in namespaces:
        | cassandra-system |
        | nats-system      |
        | nvcf             |
        | api-keys         |
        | ess              |
        | sis              |
        | vault-system     |
        | nvca-operator    |
        | nvca-system      |
        | nvcf-backend     |
        | cert-manager     |

    Scenario: Operator installs the single-cluster ncp-local stack
      When I run command:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --stack deploy/stacks/self-managed --env local --plain install --control-plane --cluster-name ncp-local --region us-west-1 --nca-id nvcf-default
        """

      Then the command exit code should be 0
      And file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should exist

      # api-keys.localhost is reachable now that the control plane is
      # installed. Mint an admin JWT; the CLI writes it to the state
      # file under the harness-isolated HOME, and the compute-plane
      # commands below read it back from state.
      And command has succeeded:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml init
        """

      When I run command:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --stack deploy/stacks/self-managed --env local --plain compute-plane register --control-plane-profile deploy/stacks/self-managed/out/control-plane-profile.yaml --cluster-name ncp-local --kube-context k3d-ncp-local --region us-west-1 --output deploy/stacks/self-managed/out/ncp-local-register-values.yaml
        """
      Then the command exit code should be 0
      And file "deploy/stacks/self-managed/out/ncp-local-register-values.yaml" should exist

      When I run command:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --stack deploy/stacks/self-managed --env local --plain compute-plane install --values deploy/stacks/self-managed/out/ncp-local-register-values.yaml --kube-context k3d-ncp-local --cluster-name ncp-local
        """
      Then the command exit code should be 0

    @validate
    Scenario: Control-plane profile describes the installed single-cluster stack
      Given command has succeeded:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --stack deploy/stacks/self-managed --env local --plain install --control-plane --cluster-name ncp-local --region us-west-1 --nca-id nvcf-default
        """

      When I run command:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --stack deploy/stacks/self-managed --env local --plain control-plane profile validate --file deploy/stacks/self-managed/out/control-plane-profile.yaml --require in-cluster
        """
      Then the command exit code should be 0

      # Subset match (should contain, not should match) so additive
      # changes to the profile schema do not break this scenario.
      # Tighten to should match if extra-field drift becomes a bug source.
      And yaml file "deploy/stacks/self-managed/out/control-plane-profile.yaml" should contain:
        """
        apiVersion: nvcf.nvidia.com/v1alpha1
        kind: ControlPlaneProfile
        controlPlane:
          clusterName: ncp-local
          ncaID: nvcf-default
          region: us-west-1
          endpoints:
            inCluster:
              icmsURL: http://api.sis.svc.cluster.local:8080
              revalURL: http://reval.nvcf.svc.cluster.local:8080
              natsURL: nats://nats.nats-system.svc.cluster.local:4222
            computeReachable:
              icmsURL: http://sis.localhost:8080
              revalURL: http://reval.localhost:8080
              natsURL: nats://nats.localhost:4222
          gateway:
            httpURL: http://api.localhost:8080
            grpcURL: grpc.localhost:10081
          hosts:
            api: api.localhost
            apiKeys: api-keys.localhost
            sis: sis.localhost
            reval: reval.localhost
            nats: nats.localhost
            invocation: invocation.localhost
        """

    @nvca-registration
    Scenario: NVCA values describe the registered single-cluster compute plane
      Given command has succeeded:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --stack deploy/stacks/self-managed --env local --plain install --control-plane --cluster-name ncp-local --region us-west-1 --nca-id nvcf-default
        """
      And command has succeeded:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml init
        """
      And command has succeeded:
        """
        ${NVCF_CLI} --config tests/bdd/fixtures/nvcf-cli-local.yaml self-hosted --stack deploy/stacks/self-managed --env local --plain compute-plane register --control-plane-profile deploy/stacks/self-managed/out/control-plane-profile.yaml --cluster-name ncp-local --kube-context k3d-ncp-local --region us-west-1 --output deploy/stacks/self-managed/out/ncp-local-register-values.yaml
        """

      # Subset match (should contain, not should match) because the
      # values file carries non-deterministic IDs alongside the
      # deterministic block. The IDs are asserted by individual
      # should-not-be-empty steps below.
      #
      # The values file uses in-cluster URLs because compute-plane
      # register resolves the URL layer from the kube-context: when
      # the worker shares the same k3d cluster as the control plane
      # (single-cluster topology), the in-cluster service hostnames are
      # the directly reachable ones. This matches what self-hosted up
      # used to emit. The multi-cluster scenario, in contrast, gets
      # the compute-reachable .test hostnames because the worker lives
      # in a different k3d cluster.
      Then yaml file "deploy/stacks/self-managed/out/ncp-local-register-values.yaml" should contain:
        """
        clusterName: ncp-local
        ncaID: nvcf-default
        region: us-west-1
        selfManaged:
          icmsServiceURL: http://api.sis.svc.cluster.local:8080
          revalServiceURL: http://reval.nvcf.svc.cluster.local:8080
          natsURL: nats://nats.nats-system.svc.cluster.local:4222
        """
      And yaml file "deploy/stacks/self-managed/out/ncp-local-register-values.yaml" key "clusterID" should not be empty
      And yaml file "deploy/stacks/self-managed/out/ncp-local-register-values.yaml" key "clusterGroupID" should not be empty
      And yaml file "deploy/stacks/self-managed/out/ncp-local-register-values.yaml" key "selfManaged.identitySource" should not be empty
