## Commands

### Deployment
* `deploy.sh`:

* `cleanup.sh`:

### Admin Setup
* `admin/setup.sh`:

* `admin/setup.sh --cleanup`:

### Tool
`tool/` directory contains useful wrappers to interact with OpenBao via kubectl

* `tool/raft-list-peers.sh`

### Tests
`test/` directory contains various test scripts written in Bash

### Useful Debug Commands

```
# MetaLLB loadbalancer related commands
kubectl logs -n metallb-system -l app=metallb,component=speaker
kubectl logs -n metallb-system -l app=metallb,component=controller
kubectl get ipaddresspool -n metallb-system
kubectl get l2advertisement -n metallb-system
kubectl get svc -n vault-server
kubectl get endpoints openbao-server -n vault-server -o yaml

# kubernetes secret read
kubectl get secret openbao-server-unseal -n vault-server -o jsonpath='{.data.unseal_key}' | xargs | base64 -d
kubectl get secret openbao-server-root-token -n vault-server -o jsonpath='{.data.root_token}' | xargs | base64 -d

# force delete pod
kubectl delete pod openbao-server-<> -n vault-server --force --grace-period=0
```