# EKS Cluster Terraform (Optional)

<Warning>
This guide is completely optional. If you already have a Kubernetes cluster with GPU nodes, you can skip this page and proceed directly to [helmfile-installation](./helmfile-installation.md) to install the NVCF control plane.

</Warning>

This guide provides instructions for deploying the Amazon EKS infrastructure foundation for a fully self-hosted NVIDIA Cloud Functions (NVCF) deployment using Terraform. This includes:

- Amazon EKS cluster with dedicated node pools for various workloads
- GPU nodes with automatic taint configuration for inference workloads
- Core infrastructure components (VPC, subnets, IAM roles, security groups)
- NVIDIA GPU Operator deployment (required for GPU workloads)
- Infrastructure prerequisites for optional enhancements (LLS streaming, simulation caching)

<Note>
This guide covers infrastructure deployment only. Some Terraform options configure AWS resources (IAM policies, S3 buckets) required by optional enhancements deployed later. See [self-hosted-optional-enhancements](./optional-enhancements.md) for details on these components.

</Note>

## Prerequisites

### Required Tools

- **Terraform** >= 1.0.0
- **AWS CLI** configured with credentials
- **kubectl** >= 1.28
- **helm** >= 3.17
- **helmfile** >= 0.150
- **helm-diff** plugin >=3.11
- **helm-secrets** plugin >=4.7.4
- **skopeo** (required only if using `create_sm_ecr_repos = true` for automated ECR mirroring)

### Required Access

- **AWS Account** with permissions for EKS, VPC, EC2, IAM, S3
- **NGC API Key** from [ngc.nvidia.com](https://ngc.nvidia.com) authenticated with `nvcf-onprem` organization
  \- See [self-hosted-image-mirroring](./image-mirroring.md) for more details on required registry access.
- The `nvcf-base` repository must be downloaded to your local machine (see [download-nvcf-base](./image-mirroring.md)).

### Configure AWS Credentials

Terraform requires valid AWS credentials to create resources. Configure your AWS credentials using one of the following methods before running any `terraform` commands:

<Tabs>
<Tab title="AWS CLI (Recommended)">

Configure credentials using the AWS CLI:

```bash
# Interactive configuration
aws configure

# Or use SSO login
aws sso login --profile <profile-name>
```

</Tab>

<Tab title="Environment Variables">

Set credentials directly as environment variables:

```bash
export AWS_ACCESS_KEY_ID="<your-access-key>"
export AWS_SECRET_ACCESS_KEY="<your-secret-key>"
export AWS_SESSION_TOKEN="<your-session-token>"  # If using temporary credentials
export AWS_REGION="<your-region>"  # e.g., us-east-1
```

</Tab>

<Tab title="AWS Profile">

Use a named profile from `~/.aws/credentials`:

```bash
export AWS_PROFILE="<profile-name>"
```

</Tab>

</Tabs>

**Verify AWS credentials are configured correctly:**

```bash
aws sts get-caller-identity
```

You should see output showing your AWS account ID, user ARN, and user ID. If you receive an error, your credentials are not configured correctly.

**Set NGC API Key**

Before proceeding, set your NGC API key as an environment variable. This is required for automated ECR mirroring and GPU Operator deployment:

```bash
export NGC_API_KEY="nvapi-xxxxxxxxxxxxx"  # Replace with your NGC API key
```

### Network Planning

- VPC CIDR: `/16` recommended for production
- Service CIDR: `/16`, must not overlap with VPC CIDR
- Egress is required for third-party registry access to pull both service artifacts and function containers

## Node Pool Design

The Terraform configuration supports flexible node pool designs for different deployment scenarios:

- `self-managed`: 5 node pools (compute, GPU, control-plane, database, and secrets management), the extra compute node pool is primarily for supporting optional simulation components and can be disabled for inference-only self-hosted NVCF
- `byoc`: 2 node pools (compute and GPU) - if deploying with this configuration, `nodeSelectors` must be disabled in the self-hosted stack environment configuration file.

Please refer to the codebase `nvcf-base/terraform/tfvars-examples` for the full list of node configurations and deployment options. Though the `byoc` configuration may support a self-hosted stack deployment, it is primarily meant for BYOC cluster deployments with NVIDIA-managed NVCF control plane services (see [cluster-setup-management](./cluster-management/index.md)).

<Note>
You can customize node pools (instance types, capacities, and configurations) by copying one of the example tfvars files from `terraform/tfvars-examples/` to your environment directory and modifying it to match your requirements.

</Note>

**Automatic GPU Taint Configuration**

GPU nodes are automatically tainted with `nvidia.com/gpu=present:NoSchedule` based on instance family detection (`g*` or `p*` patterns). No manual configuration required.

## Cluster Creation

### Step 1: Create Environment

The `nvcf-base` repository includes a base Terraform environment under `terraform/envs/byoc/` containing the required Terraform configuration files. Create your own environment by copying this folder:

```bash
cd nvcf-base
cp -r terraform/envs/byoc terraform/envs/<your-environment>
cp terraform/tfvars-examples/self-managed-full.tfvars terraform/envs/<your-environment>/terraform.tfvars
```

Replace `<your-environment>` with your environment name (e.g., `nvcf-prod`, `staging`). This copies all required Terraform files (`main.tf`, `variables.tf`, `providers.tf`, `outputs.tf`) along with the tfvars configuration template.

### Step 2: Configure Environment

Edit `terraform/envs/<your-environment>/terraform.tfvars` to match your requirements. The key sections are described below. Feel free to use this example terraform.tfvars directly to bring up an EKS cluster ready for NVCF self-hosted control plane deployment. LLS (Low Latency Streaming) is disabled by default; enable it only if you plan to deploy simulation or streaming VM workloads (see [self-hosted-lls-installation](./lls-installation.md)).

<Accordion title="Example terraform.tfvars Configuration">
</Accordion>
```hcl title="terraform.tfvars"
# =============================================================================
# NVCF Fully Self-Managed Configuration (Co-located)
# =============================================================================
# This configuration deploys a cluster with BOTH:
#   - NVCF control plane (self-hosted)
#   - BYOC workloads
#
# Co-located architecture - both components in the same EKS cluster.
# =============================================================================

# -----------------------------------------------------------------------------
# REQUIRED: Cluster Identification
# -----------------------------------------------------------------------------
cluster_name = "my-self-hosted-cluster" # Must be under 20 characters if enabling LLS (EA limitation)
cluster_version = "1.32"
region       = "us-west-2"
environment  = "production"

# -----------------------------------------------------------------------------
# VPC and Networking (larger for control plane + workloads)
# -----------------------------------------------------------------------------
# Default: null lets AWS auto-assign a non-colliding CIDR.
# Override with a specific CIDR if you need deterministic addressing:
#   vpc_cidr = "10.110.0.0/16"
vpc_cidr = null

availability_zones = ["us-west-2a", "us-west-2b", "us-west-2c"]

# When vpc_cidr is null, leave these as null for automatic subnet calculation.
# When using a specific vpc_cidr, override with matching subnets, e.g.:
#   private_subnet_cidrs = ["10.110.0.0/19", "10.110.32.0/19", "10.110.64.0/19"]
#   public_subnet_cidrs  = ["10.110.101.0/24", "10.110.102.0/24", "10.110.103.0/24"]
private_subnet_cidrs = null
public_subnet_cidrs  = null

service_ipv4_cidr = "172.20.0.0/16"

create_nat_gateways = true

# -----------------------------------------------------------------------------
# Node Pool Configuration (Control Plane + BYOC)
# -----------------------------------------------------------------------------
node_pools = {
  # NVCF Control Plane Nodes
  "nvcf-control-plane" = {
    instance_type    = "m5.4xlarge"  # Control plane services need CPU/memory
    desired_capacity = 3
    max_capacity     = 5
    min_capacity     = 3
    labels = {
      "node-type" = "control-plane"
      "workload"  = "nvcf-control-plane"
      "nvcf.nvidia.com/workload" = "control-plane"
    }
  },
  
  # Compute nodes for BYOC workloads
  "compute" = {
    instance_type    = "m5.2xlarge"
    desired_capacity = 3
    max_capacity     = 10
    min_capacity     = 2
    labels = {
      "node-type" = "compute"
      "workload"  = "byoc"
    }
  },
  
  # GPU nodes for BYOC workloads
  # Change to appropriate GPU instance type for your workload. For single-GPU simulation workloads, this should be g6e.4xlarge.
  # For very basic workloads to test the stack, we recommend g5.4xlarge (A10G) or for inference workloads, A100, H100 or better.
  # min_capacity is 1 because the NVCF cluster agent (NVCA) will not be able to start if there are no GPU nodes.
  "gpu" = {
    instance_type    = "g6e.4xlarge"
    desired_capacity = 2
    max_capacity     = 8
    min_capacity     = 1
    labels = {
      "node-type"      = "gpu"
      "nvidia.com/gpu" = "true"
      "workload"       = "byoc-gpu"
    }
  },
  
  # Cassandra nodes for control plane storage
  "cassandra" = {
    instance_type    = "r5.2xlarge"  # Memory-optimized for database
    desired_capacity = 3
    max_capacity     = 5
    min_capacity     = 3
    labels = {
      "node-type" = "storage"
      "workload"  = "cassandra"
      "nvcf.nvidia.com/workload" = "cassandra"
    }
  },
  
  # OpenBao nodes for secrets management
  "openbao" = {
    instance_type    = "m5.xlarge"
    desired_capacity = 3
    max_capacity     = 3
    min_capacity     = 3
    labels = {
      "node-type" = "security"
      "workload"  = "openbao"
      "nvcf.nvidia.com/workload" = "vault"
    }
  }
}

# Storage configuration (larger for control plane data)
node_root_volume_size     = 100  # GB for control plane nodes
gpu_node_root_volume_size = 250  # GB for GPU nodes

# AMI Configuration
# Default (null) automatically discovers the latest Ubuntu 22.04 EKS-optimized AMI for your region
# This is RECOMMENDED for most deployments (always uses latest security patches)
# This determines the base OS image for the EKS nodes.
node_ami_id = null

# Advanced: Pin a specific AMI for compliance/reproducibility
# NOTE: AMI IDs are region-specific. Examples:
#   us-west-2: ami-0bce1583264e581a6
#   us-east-1: ami-0e70225fadb23da91
#   us-east-2: ami-0a12b3c4d5e6f7890
# Uncomment and update for your region:
# node_ami_id = "ami-0bce1583264e581a6"

# SSH access (recommended for control plane troubleshooting)
# ssh_public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample..."

# -----------------------------------------------------------------------------
# Feature Flags
# -----------------------------------------------------------------------------

# Set to true to create ECR repositories and copy NVCF images from NGC
# IMPORTANT: Requires NGC_API_KEY to be set in your environment
create_sm_ecr_repos = true

# =============================================================================
# Additional Configuration (Optional)
# =============================================================================

# Observability - OPTIONAL, DEPRECATED
# WARNING: The CloudWatch Observability addon is disabled to avoid conflicts
# with the stack bring-up.
enable_cloudwatch_observability = false

# S3 Buckets - OPTIONAL, REQUIRED for DDCS and UCC (Simulation components)
create_s3_buckets = true
s3_bucket_name    = "my-self-hosted-data" # REPLACE: Must be globally unique

# Autoscaling (important for handling varying workload)
enable_autoscaling = true

# -----------------------------------------------------------------------------
# Advanced Autoscaling Configuration (optional)
# -----------------------------------------------------------------------------
# Uncomment and customize for fine-grained control

# autoscaling_cooldown_period = 300
# autoscaling_polling_interval = 30
# autoscaling_scale_up_threshold = 70
# autoscaling_scale_down_threshold = 30
# gpu_autoscaling_enabled = true
# gpu_autoscaling_min_nodes = 0
# gpu_autoscaling_max_nodes = 10
# compute_autoscaling_enabled = true
# compute_autoscaling_min_nodes = 2
# compute_autoscaling_max_nodes = 15
# autoscaling_metrics = ["CPUUtilization", "MemoryUtilization"]
# enable_spot_instances = false
# spot_instance_percentage = 0
# enable_predictive_scaling = false

# -----------------------------------------------------------------------------
# Tags
# -----------------------------------------------------------------------------
tags = {
  Environment  = "production"
  Project      = "nvcf-self-hosted"
  ManagedBy    = "terraform"
  Deployment   = "co-located"
  CostCenter   = "engineering"
  Owner        = "platform-team"
  Architecture = "self-hosted-full"
}
```


<Note>
If you plan on using NVCF streaming functions, cluster_name must be less than 20 characters. Please double-check before proceeding, or you'll need to unwind and restart.

</Note>

<Warning>
**AMI IDs are region-specific.** The sample configuration uses `node_ami_id = null` which automatically discovers the correct EKS-optimized AMI for your region. This is the **recommended** setting.

If you need to pin a specific AMI (for compliance or reproducibility), you **must** use an AMI ID that exists in your target region. Using an AMI from a different region will cause `terraform apply` to fail with "image id does not exist" errors. See [AMI Does Not Exist Error] in Troubleshooting.

</Warning>

**ECR Registry Image Mirroring**

For ECR users, this Terraform module can automatically mirror all required NVCF artifacts from NGC:

```hcl
create_sm_ecr_repos = true  # Enable automated mirroring
```

<Info>
Requires `NGC_API_KEY` environment variable set before running `terraform apply`. Generate this key from the `nvcf-onprem` organization at [https://org.ngc.nvidia.com/setup/api-keys](https://org.ngc.nvidia.com/setup/api-keys).

</Info>

See [ecr-automated-mirroring](./image-mirroring.md) for details on what's included (control plane, LLS, worker components) and what's not (simulation cache, custom streaming apps).

If you're not using ECR or prefer manual mirroring, set `create_sm_ecr_repos = false` and follow [self-hosted-image-mirroring](./image-mirroring.md).

**GPU Node Configuration**

For GPU workloads, you must set the appropriate GPU instance type in the `terraform.tfvars` configuration. NVCF supports all GPU types supported by the NVIDIA GPU Operator.
Ensure the instance type is available in your chosen region and availability zones (specified in `availability_zones`).

<Note>
For single-GPU simulation workloads, this should be `g6e.4xlarge` or better.

</Note>

```hcl
"gpu" = {
   instance_type    = "g6e.4xlarge"  # Change to appropriate GPU instance type for your workload.
   desired_capacity = 2
   max_capacity     = 8
   min_capacity     = 1
   labels = {
      "node-type"      = "gpu"
      "nvidia.com/gpu" = "true"
      "workload"       = "byoc-gpu"
   }
}
```

**Deploying to Different AWS Regions**

If deploying to a region other than `us-west-2`, you **must** update these three variables:

| Variable | Required Change | Example |
| --- | --- | --- |
| `region` | Target AWS region | `"us-east-1"` |
| `availability_zones` | Valid AZs for that region | `["us-east-1a", "us-east-1b", "us-east-1c"]` |
| `node_ami_id` | Set to `null` for auto-detection | `null` |

**Why these are required:**

- Availability zones are region-specific - `us-west-2a` doesn't exist in `us-east-1`
- AMI IDs are region-specific - setting to `null` auto-detects the latest EKS-optimized AMI for your region
- Region determines resource location - all AWS resources will be created in this region

**Example for US-East-1:**

```hcl
region = "us-east-1"
availability_zones = ["us-east-1a", "us-east-1b", "us-east-1c"]
node_ami_id = null  # Auto-detects correct AMI for us-east-1
```

**To find availability zones for your region:**

```bash
aws ec2 describe-availability-zones --region <your-region> \
  --query 'AvailabilityZones[].ZoneName' --output text
```

For detailed guidance in any region, see `nvcf-base/terraform/tfvars-examples/README.md`.

### Step 3: Initialize and Validate

Initialize Terraform and validate the configuration.

```bash
cd terraform/envs/<your-environment>
terraform init
terraform validate
```

<Warning>
- **VPC Subnets** (`availability_zones`, `private_subnet_cidrs`, `public_subnet_cidrs`) - **MUST span at least 2 AZs** (AWS EKS requirement for high availability)
- **Node Placement** (`node_availability_zones`) - **MUST use only 1 AZ** (LLS limitation)

**Configuration:**

```hcl
# VPC subnets - keep multiple AZs (AWS EKS requirement)
availability_zones = [
  "us-west-2a",
  "us-west-2b",
  "us-west-2c"
]
```

**Do NOT** change `availability_zones` to a single zone - this will cause Terraform to fail with "subnets not in at least two different availability zones" error.

</Warning>

### Step 4: Deploy Cluster

**Expected Duration:** 30-45 minutes

**What gets deployed:**

- VPC with public/private subnets across 3 AZs
- EKS control plane
- Node pool configuration
- IAM roles and policies
- Security groups
- S3 buckets (if enabled)

1. Ensure you are in the environment directory and have run `terraform init` (see Step 3). Review the deployment plan.

```bash
terraform plan
```

<Note>
Review the plan output to verify expected resources will be created based on your configuration. Key items to check:

- **Node pools**: Verify correct number and instance types (e.g., 5 node pools for self-managed deployment with optional caching components)
- **VPC/Networking**: Confirm subnets match your CIDR configuration
- **S3 buckets**: If `create_s3_buckets = true`, verify bucket name is correct

</Note>

2. Apply the configuration.

```bash
terraform apply
```

### Verify Deployment

1. After deployment completes, configure kubectl:

```bash
# Replace <region> and <cluster-name> with your values from terraform.tfvars
aws eks update-kubeconfig \
  --region <region> \
  --name <cluster-name>
```

<Note>
Use the same `region` and `cluster_name` values from your `terraform.tfvars` configuration.

</Note>

2. Verify cluster health:

```bash
# Check all nodes are Ready
kubectl get nodes

# Verify GPU taints are applied automatically
kubectl get nodes -o=custom-columns="NAME:.metadata.name, INSTANCE:.metadata.labels.node\.kubernetes\.io/instance-type, TAINTS:.spec.taints"
```

**Example output for GPU nodes (should match your GPU instance type):**

```text
NAME                          INSTANCE        TAINTS
ip-10-120-x-x.compute.internal  g6e.4xlarge   [map[effect:NoSchedule key:nvidia.com/gpu value:present]]
```

### Step 5: Deploy GPU Operator

The NVIDIA GPU Operator is **required** for GPU workloads. It installs GPU drivers, device plugins, and monitoring components on GPU nodes.

1. Set NGC credentials:

```bash
export NGC_API_KEY="nvapi-xxxxxxxxxxxxx"  # Your NGC API key
```

2. Deploy the GPU Operator:

```bash
# Navigate to core-apps under the nvcf-base top-level directory
cd /path/to/nvcf-base/core-apps

helmfile apply --selector component=gpu
```

3. Verify deployment is proceeding.

**Expected Duration:** 5-10 minutes

```bash
# Check GPU operator pods are running (if some pods are in init state, wait a few minutes)
kubectl get pods -n gpu-operator

# Verify GPU resources are advertised on nodes
kubectl get nodes -o custom-columns="NAME:.metadata.name,GPU:.status.allocatable.nvidia\.com/gpu"
```

**Expected output:** All pods should be `Running`, and GPU nodes should show available GPU count (e.g., `1` for g6e.4xlarge).

The GPU Operator installs:

- GPU driver (NVIDIA 570.x)
- NVIDIA device plugin
- GPU feature discovery
- DCGM exporter (metrics)

### Next Steps

With the infrastructure and GPU Operator deployed:

1. Begin control plane deployment by following [helmfile-installation](./helmfile-installation.md).
2. Deploy optional application components (including simulation components such as DDCS, UCC, Storage API, LLS) under `nvcf-base/core-apps`. See [self-hosted-optional-enhancements](./optional-enhancements.md).

## Operations

### Scaling Node Pools

Update `terraform.tfvars` and reapply:

```bash
# Edit desired_capacity or max_capacity for node pools
vim terraform/envs/<your-environment>/terraform.tfvars

cd terraform/envs/<your-environment>
terraform plan
terraform apply
```

### Upgrading GPU Operator

Re-run the deployment command to upgrade to the latest version:

```bash
helmfile apply --selector component=gpu
```

<Note>
To upgrade optional enhancements (container caching, simulation caching, LLS), re-run the corresponding `make deploy-*` commands from [self-hosted-optional-enhancements](./optional-enhancements.md).

</Note>

### Adding GPU Capacity

GPU taints are applied automatically when new GPU nodes join:

1. Increase `desired_capacity` for gpu node pool in tfvars
2. Run `terraform apply`
3. New nodes will automatically receive GPU taints
4. Verify: `kubectl get nodes -o=custom-columns="NAME:.metadata.name,TAINTS:.spec.taints"`

## Decommissioning

```bash
cd terraform/envs/<your-environment>
terraform destroy
```

<Warning>
This destroys all cluster resources.

</Warning>

## Troubleshooting

### GPU Taints Not Applied

**Symptoms:** GPU nodes do not have `nvidia.com/gpu` taint

**Diagnosis:**

```bash
# SSH to GPU node
ssh ubuntu@<gpu-node-ip>

# Check cloud-init logs
sudo cat /var/log/cloud-init-output.log | grep -E "IMDSv2|GPU|TAINT"
```

**Expected output:**

```text
DEBUG: Obtained IMDSv2 token
DEBUG: Instance Type: g5.12xlarge
DEBUG: Instance Family: g5
DEBUG: Matched GPU family (g* or p*) - adding GPU taint
DEBUG: Added GPU taint for GPU instance family: nvidia.com/gpu=present:NoSchedule
```

**Resolution:**

- Verify instance type starts with `g` or `p`
- Check launch template user-data rendered correctly
- Terminate node and let ASG create replacement

### AMI Does Not Exist Error

**Symptom:** During `terraform apply`, Auto Scaling Group creation fails with:

```text
Error: creating Auto Scaling Group (my-cluster-gpu): ValidationError: You must use a
valid fully-formed launch template. The image id '[ami-0bce1583264e581a6]' does not exist
```

**Cause:** The `node_ami_id` is set to an AMI that doesn't exist in your target region. AMI IDs are region-specific — an AMI available in `us-west-2` will not exist in `ap-south-1` or other regions.

**Resolution:**

1. **Recommended: Use automatic AMI discovery**

   Set `node_ami_id = null` in your `terraform.tfvars`. This automatically discovers the latest EKS-optimized Ubuntu AMI for your region:

   ```hcl
   node_ami_id = null  # Recommended - auto-discovers correct AMI for your region
   ```

2. **If you must pin a specific AMI**, find the correct AMI for your region:

   ```bash
   # Replace <your-region> and <k8s-version> with your values
   aws ec2 describe-images --region <your-region> \
     --owners amazon \
     --filters "Name=name,Values=ubuntu-eks/k8s_<k8s-version>/images/*x86_64*" \
     --query 'Images | sort_by(@, &CreationDate) | [-1].[ImageId,Name]' \
     --output text

   # Example for us-west-2 with Kubernetes 1.32:
   aws ec2 describe-images --region us-west-2 \
     --owners amazon \
     --filters "Name=name,Values=ubuntu-eks/k8s_1.32/images/*x86_64*" \
     --query 'Images | sort_by(@, &CreationDate) | [-1].[ImageId,Name]' \
     --output text
   ```

3. **After fixing**, run `terraform apply` again.

<Tip>
Using `node_ami_id = null` is strongly recommended as it ensures you always get the latest security patches and eliminates region compatibility issues.

</Tip>

### Pods Not Scheduling on GPU Nodes

**Symptoms:** Pods with GPU requests stay in Pending state

**Cause:** Missing toleration for GPU taint

**Resolution:** Add toleration to pod spec:

```yaml
tolerations:
- key: nvidia.com/gpu
  operator: Exists
  effect: NoSchedule
```

### ImagePullBackOff Errors

**Symptoms:** Pods fail to pull images from nvcr.io

**Resolution:**

```bash
# Verify NGC_API_KEY is set
echo $NGC_API_KEY

# Recreate image pull secret
kubectl create secret docker-registry nvcr-creds \
  --docker-server=nvcr.io \
  --docker-username='$oauthtoken' \
  --docker-password="$NGC_API_KEY" \
  --namespace=<namespace> \
  --dry-run=client -o yaml | kubectl apply -f -

# Restart affected pods
kubectl rollout restart deployment <deployment-name> -n <namespace>
```

### Resuming Installation After AWS Credential Expiration

**Symptoms:** Terraform fails during long-running operations (e.g., EKS cluster creation) with authentication errors, or you see "Resource already exists" errors when trying to resume

**Cause:** AWS credentials expired during Terraform provisioning. AWS credentials typically expire after a certain period (often 1 hour for temporary credentials), which can occur during long-running deployments like EKS cluster creation.

**Resolution:**

When AWS credentials expire mid-deployment, Terraform may lose track of resources it created. You can recover by removing the resource from Terraform state and re-importing it.

**Example: EKS Cluster Creation**

```bash
# Remove the EKS cluster from Terraform state
terraform state rm module.eks.aws_eks_cluster.main

# Re-import the existing cluster
terraform import module.eks.aws_eks_cluster.main <cluster_name>

# Continue with your terraform operation
terraform apply
```

**General Process:**

1. Identify the resource that was partially created (check AWS Console)
2. Remove it from Terraform state: `terraform state rm <resource_address>`
3. Import the existing resource: `terraform import <resource_address> <resource_id>`
4. Continue with `terraform apply`

<Tip>
To find the correct resource address, use `terraform state list` to see all resources in your state file.

</Tip>

<Note>
This approach works for any Terraform resource that exists in AWS but Terraform has lost track of due to credential expiration. Investigate this process whenever you encounter "Resource already exists" errors after a failed deployment.

</Note>

### CSI Driver or CloudWatch Add-on Installation Timeouts

**Symptoms:** Terraform times out when deploying CSI drivers (SMB CSI Driver) or CloudWatch Observability add-on, or add-on installation appears to hang

**Common Causes:**

- AWS credential expiration during long-running deployments
- Network connectivity issues between EKS and AWS services
- IAM permissions issues preventing add-on installation
- Resource limits or quota exhaustions in your AWS account

**Resolution Steps:**

1. **Retry the deployment**

   AWS credential expiration is the most common cause. See [Resuming Installation After AWS Credential Expiration] for recovery steps.

2. **If the issue persists, consult AWS documentation**

   AWS provides detailed troubleshooting guides for add-on installations:

   - **CloudWatch Observability Add-on**: [Troubleshooting CloudWatch Observability EKS Add-on](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/install-CloudWatch-Observability-EKS-addon.html#Container-Insights-setup-EKS-addon-troubleshoot)
   - **CSI Driver Issues**: [Amazon EKS CSI Driver Troubleshooting](https://docs.aws.amazon.com/eks/latest/userguide/troubleshooting.html)

3. **Verify add-on status via AWS CLI**

   ```bash
   # Check CloudWatch add-on status
   aws eks describe-addon \
     --cluster-name <cluster-name> \
     --addon-name amazon-cloudwatch-observability \
     --region <region>

   # Check all add-ons
   aws eks list-addons \
     --cluster-name <cluster-name> \
     --region <region>
   ```

4. **Last resort: Destroy and recreate**

   If troubleshooting steps fail to resolve the issue, you may need to start fresh. See the [Decommissioning] section to destroy the cluster, then return to [Step 4: Deploy Cluster] to recreate it.

<Tip>
**Prevention:** Use long-lived AWS credentials or AWS IAM roles when possible to avoid credential expiration during deployments. If using temporary credentials, ensure they have sufficient validity period (at least 2-3 hours) before starting a cluster deployment.

</Tip>
