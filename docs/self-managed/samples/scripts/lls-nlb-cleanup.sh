#!/usr/bin/env bash

set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "Usage: $0 <cluster-name> <aws-region>" >&2
  exit 2
fi

CLUSTER_NAME="$1"
REGION="$2"
NLB_NAME="rp-public-${CLUSTER_NAME}"
TG_NAME="${CLUSTER_NAME}-rp-tg"
SG_NAME="${CLUSTER_NAME}-rp-sg"
export AWS_PAGER=""

require_elbv2_tags() {
  local resource_arn="$1"
  local cluster_tag
  local managed_by_tag

  cluster_tag="$(aws elbv2 describe-tags \
    --resource-arns "$resource_arn" \
    --region "$REGION" \
    --query 'TagDescriptions[0].Tags[?Key==`lls:cluster`].Value | [0]' \
    --output text)"
  managed_by_tag="$(aws elbv2 describe-tags \
    --resource-arns "$resource_arn" \
    --region "$REGION" \
    --query 'TagDescriptions[0].Tags[?Key==`lls:managed-by`].Value | [0]' \
    --output text)"

  if [[ "$cluster_tag" != "$CLUSTER_NAME" || "$managed_by_tag" != "customer" ]]; then
    echo "Refusing to delete unverified resource: $resource_arn" >&2
    exit 1
  fi
}

require_security_group_tags() {
  local group_id="$1"
  local cluster_tag
  local managed_by_tag

  cluster_tag="$(aws ec2 describe-tags \
    --filters "Name=resource-id,Values=${group_id}" "Name=key,Values=lls:cluster" \
    --region "$REGION" --query 'Tags[0].Value' --output text)"
  managed_by_tag="$(aws ec2 describe-tags \
    --filters "Name=resource-id,Values=${group_id}" "Name=key,Values=lls:managed-by" \
    --region "$REGION" --query 'Tags[0].Value' --output text)"

  if [[ "$cluster_tag" != "$CLUSTER_NAME" || "$managed_by_tag" != "customer" ]]; then
    echo "Refusing to delete unverified security group: $group_id" >&2
    exit 1
  fi
}

aws sts get-caller-identity >/dev/null

NLB_ARN="$(aws elbv2 describe-load-balancers \
  --names "$NLB_NAME" --region "$REGION" \
  --query 'LoadBalancers[0].LoadBalancerArn' --output text 2>/dev/null || true)"
TG_ARN="$(aws elbv2 describe-target-groups \
  --names "$TG_NAME" --region "$REGION" \
  --query 'TargetGroups[0].TargetGroupArn' --output text 2>/dev/null || true)"
SG_ID="$(aws ec2 describe-security-groups \
  --filters "Name=group-name,Values=${SG_NAME}" \
  --region "$REGION" --query 'SecurityGroups[0].GroupId' --output text)"

[[ "$NLB_ARN" == "None" ]] && NLB_ARN=""
[[ "$TG_ARN" == "None" ]] && TG_ARN=""
[[ "$SG_ID" == "None" ]] && SG_ID=""

[[ -z "$NLB_ARN" ]] || require_elbv2_tags "$NLB_ARN"
[[ -z "$TG_ARN" ]] || require_elbv2_tags "$TG_ARN"
[[ -z "$SG_ID" ]] || require_security_group_tags "$SG_ID"

if [[ -n "$SG_ID" ]]; then
  NODE_SG_ID="$(aws ec2 describe-security-groups \
    --filters "Name=group-name,Values=${CLUSTER_NAME}-node-*" \
    --region "$REGION" --query 'SecurityGroups[0].GroupId' --output text)"
  if [[ -n "$NODE_SG_ID" && "$NODE_SG_ID" != "None" ]]; then
    aws ec2 revoke-security-group-ingress \
      --group-id "$NODE_SG_ID" \
      --protocol udp \
      --port 30504 \
      --source-group "$SG_ID" \
      --region "$REGION" 2>/dev/null || true
  fi
fi

if [[ -n "$NLB_ARN" ]]; then
  aws elbv2 delete-load-balancer \
    --load-balancer-arn "$NLB_ARN" --region "$REGION"
  aws elbv2 wait load-balancers-deleted \
    --load-balancer-arns "$NLB_ARN" --region "$REGION"
fi

if [[ -n "$TG_ARN" ]]; then
  aws elbv2 delete-target-group \
    --target-group-arn "$TG_ARN" --region "$REGION"
fi

if [[ -n "$SG_ID" ]]; then
  for attempt in {1..10}; do
    if aws ec2 delete-security-group \
      --group-id "$SG_ID" --region "$REGION" 2>/dev/null; then
      break
    fi
    if [[ "$attempt" -eq 10 ]]; then
      echo "Security group $SG_ID is still in use; retry cleanup later." >&2
      exit 1
    fi
    sleep 15
  done
fi

echo "Elastic IPs tagged for this cluster were not deleted:"
aws ec2 describe-addresses \
  --filters "Name=tag:lls:cluster,Values=${CLUSTER_NAME}" \
  --region "$REGION" \
  --query 'Addresses[*].[AllocationId,PublicIp]' \
  --output table
