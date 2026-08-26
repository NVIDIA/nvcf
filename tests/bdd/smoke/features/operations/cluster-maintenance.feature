@smoke @cluster-maintenance @destructive
Feature: Preserve function availability through compute-cluster maintenance
  As an NVCF operator,
  I want maintenance intent to survive NVCA operator reconciliation,
  so that I can drain a compute cluster and explicitly restore its workloads.

  Rule: Cordon-and-drain remains active until the operator uncordons the cluster

    Scenario: Operator drains and restores an attached compute cluster
      Given these environment variables are set:
        | name                         |
        | BDD_NVCF_CLI_CONFIG           |
        | BDD_COMPUTE_KUBECONFIG        |
        | BDD_COMPUTE_CONTEXT           |
        | BDD_COMPUTE_CLUSTER           |
        | BDD_NVCA_BACKEND_NAMESPACE    |
        | BDD_NVCA_SYSTEM_NAMESPACE     |
        | BDD_COMPUTE_REGION            |
        | BDD_WORKLOAD_GPU              |
        | BDD_WORKLOAD_INSTANCE_TYPE    |
        | BDD_FUNCTION_IMAGE            |
        | BDD_RUN_ID                    |
        | BDD_ALLOW_CLUSTER_MAINTENANCE |
      And I use NVCF CLI config "${BDD_NVCF_CLI_CONFIG}"

      # Attach mode assumes authentication already exists for the selected
      # CLI config. Prove the target API is reachable before creating work.
      When I successfully run command:
        """
        ${NVCF_CLI} --config "${BDD_NVCF_CLI_CONFIG}" --json cluster list
        """

      # The product guard proves that the explicit kubeconfig and context
      # resolve to the cluster the operator approved before any mutation.
      When I successfully run command:
        """
        ${NVCF_CLI} --json cluster agent cordon-and-drain --kubeconfig "${BDD_COMPUTE_KUBECONFIG}" --compute-plane-context "${BDD_COMPUTE_CONTEXT}" --backend-namespace "${BDD_NVCA_BACKEND_NAMESPACE}" --expect-cluster-id "${BDD_ALLOW_CLUSTER_MAINTENANCE}" --dry-run
        """
      Then the JSON command output should contain:
        """
        {
          "dryRun": true,
          "configChanged": true
        }
        """

      When I successfully create function "bdd-maintenance-${BDD_RUN_ID}" from image "${BDD_FUNCTION_IMAGE}" with CLI options:
        | option           | value   |
        | --inference-url  | /echo   |
        | --inference-port | 8000    |
        | --health-uri     | /health |
        | --health-port    | 8000    |
        | --health-timeout | PT30S   |

      # Resolve both identifiers before registering cleanup. The compensation
      # never relies on mutable CLI selected-function state from another smoke.
      When I successfully run command:
        """
        /bin/sh -c "'${NVCF_CLI}' --config '${BDD_NVCF_CLI_CONFIG}' --json function list | jq -er '.functions[] | select(.name == \"bdd-maintenance-${BDD_RUN_ID}\") | .id'"
        """
      And I export command output to environment variable "BDD_FUNCTION_ID"

      When I successfully run command:
        """
        /bin/sh -c "'${NVCF_CLI}' --config '${BDD_NVCF_CLI_CONFIG}' --json function list | jq -er '.functions[] | select(.name == \"bdd-maintenance-${BDD_RUN_ID}\") | .versionId'"
        """
      And I export command output to environment variable "BDD_FUNCTION_VERSION_ID"

      And after this scenario I successfully run command within "5m":
        """
        ${NVCF_CLI} --config "${BDD_NVCF_CLI_CONFIG}" function delete --function-id "${BDD_FUNCTION_ID}" --version-id "${BDD_FUNCTION_VERSION_ID}"
        """

      And I successfully deploy the function selected by NVCF CLI with options:
        | option          | value                                |
        | --gpu           | ${BDD_WORKLOAD_GPU}                   |
        | --instance-type | ${BDD_WORKLOAD_INSTANCE_TYPE}         |
        | --backend       | ${BDD_COMPUTE_CLUSTER}                |
        | --regions       | ${BDD_COMPUTE_REGION}                 |
        | --min-instances | 1                                    |
        | --max-instances | 1                                    |
        | --timeout       | 900                                  |

      And I successfully generate a function API key with CLI options:
        | option        | value                                                                                 |
        | --description | bdd-maintenance-${BDD_RUN_ID}                                                         |
        | --scopes      | invoke_function,list_functions,queue_details,list_functions_details,delete_function |

      Then within "10m" this command should succeed, checking every "10s":
        """
        /bin/sh -c "'${NVCF_CLI}' --json cluster agent get-function '${BDD_FUNCTION_ID}' --kubeconfig '${BDD_COMPUTE_KUBECONFIG}' --compute-plane-context '${BDD_COMPUTE_CONTEXT}' | jq -e '.phase == \"ACTIVE\" and .instanceCount > 0'"
        """

      When I successfully invoke the function selected by NVCF CLI over HTTP with timeout "120" seconds and poll duration "5" seconds:
        """
        {"message":"bdd-before-maintenance","repeats":1}
        """
      Then the command output should contain "bdd-before-maintenance"

      # Register recovery before cordoning. Compensation commands run in
      # reverse order, so uncordon runs before function deletion.
      And after this scenario I successfully run command within "10m":
        """
        ${NVCF_CLI} --json cluster agent uncordon --kubeconfig "${BDD_COMPUTE_KUBECONFIG}" --compute-plane-context "${BDD_COMPUTE_CONTEXT}" --backend-namespace "${BDD_NVCA_BACKEND_NAMESPACE}" --expect-cluster-id "${BDD_ALLOW_CLUSTER_MAINTENANCE}" --yes
        """

      When I successfully run command:
        """
        ${NVCF_CLI} --json cluster agent cordon-and-drain --kubeconfig "${BDD_COMPUTE_KUBECONFIG}" --compute-plane-context "${BDD_COMPUTE_CONTEXT}" --backend-namespace "${BDD_NVCA_BACKEND_NAMESPACE}" --expect-cluster-id "${BDD_ALLOW_CLUSTER_MAINTENANCE}" --timeout 10m --yes
        """
      Then the JSON command output should contain:
        """
        {
          "mode": "CordonAndDrain",
          "configChanged": true,
          "rolloutComplete": true
        }
        """

      # Check both desired and effective state after the operator reports its
      # rollout complete. This is the regression seam from issue #1186.
      Then within "2m" this command should succeed, checking every "5s":
        """
        /bin/sh -c "kubectl --kubeconfig '${BDD_COMPUTE_KUBECONFIG}' --context '${BDD_COMPUTE_CONTEXT}' get nvcfbackend '${BDD_COMPUTE_CLUSTER}' -n '${BDD_NVCA_BACKEND_NAMESPACE}' -o jsonpath='{.spec.overrides.featureGate.values}' | grep -Fq CordonAndDrainMaintenance"
        """
      And within "2m" this command should succeed, checking every "5s":
        """
        /bin/sh -c "kubectl --kubeconfig '${BDD_COMPUTE_KUBECONFIG}' --context '${BDD_COMPUTE_CONTEXT}' get configmap agent-config -n '${BDD_NVCA_SYSTEM_NAMESPACE}' -o jsonpath='{.data.config\\.yaml}' | grep -Fq 'maintenanceMode: CordonAndDrain'"
        """
      And within "10m" this command should succeed, checking every "10s":
        """
        /bin/sh -c "'${NVCF_CLI}' --json cluster agent get-function '${BDD_FUNCTION_ID}' --kubeconfig '${BDD_COMPUTE_KUBECONFIG}' --compute-plane-context '${BDD_COMPUTE_CONTEXT}' | jq -e '.instanceCount == 0'"
        """

      When I successfully run command:
        """
        ${NVCF_CLI} --json cluster agent uncordon --kubeconfig "${BDD_COMPUTE_KUBECONFIG}" --compute-plane-context "${BDD_COMPUTE_CONTEXT}" --backend-namespace "${BDD_NVCA_BACKEND_NAMESPACE}" --expect-cluster-id "${BDD_ALLOW_CLUSTER_MAINTENANCE}" --timeout 10m --yes
        """
      Then the JSON command output should contain:
        """
        {
          "configChanged": true,
          "rolloutComplete": true
        }
        """

      Then within "2m" this command should succeed, checking every "5s":
        """
        /bin/sh -c "kubectl --kubeconfig '${BDD_COMPUTE_KUBECONFIG}' --context '${BDD_COMPUTE_CONTEXT}' get nvcfbackend '${BDD_COMPUTE_CLUSTER}' -n '${BDD_NVCA_BACKEND_NAMESPACE}' -o json | jq -e '[.spec.overrides.featureGate.values[]? | select(. == \"CordonAndDrainMaintenance\")] | length == 0'"
        """
      And within "2m" this command should succeed, checking every "5s":
        """
        /bin/sh -c "kubectl --kubeconfig '${BDD_COMPUTE_KUBECONFIG}' --context '${BDD_COMPUTE_CONTEXT}' get configmap agent-config -n '${BDD_NVCA_SYSTEM_NAMESPACE}' -o jsonpath='{.data.config\\.yaml}' | grep -Fq 'maintenanceMode: None'"
        """
      And within "10m" this command should succeed, checking every "10s":
        """
        /bin/sh -c "'${NVCF_CLI}' --json cluster agent get-function '${BDD_FUNCTION_ID}' --kubeconfig '${BDD_COMPUTE_KUBECONFIG}' --compute-plane-context '${BDD_COMPUTE_CONTEXT}' | jq -e '.phase == \"ACTIVE\" and .instanceCount > 0'"
        """
      And within "10m" this command should succeed, checking every "10s":
        """
        ${NVCF_CLI} --config "${BDD_NVCF_CLI_CONFIG}" function invoke --request-body '{"message":"bdd-after-maintenance","repeats":1}' --timeout 120 --poll-duration 5
        """
      And the command output should contain "bdd-after-maintenance"
