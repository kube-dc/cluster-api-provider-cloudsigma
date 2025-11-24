# CloudSigma Provider Deployment Configuration

This directory contains Kubernetes manifests to deploy the CloudSigma Cluster API Provider to a management cluster.

## Quick Start

### 1. Install CRDs

```bash
kubectl apply -f crd/bases/
```

### 2. Create Credentials Secret

```bash
kubectl create secret generic cloudsigma-credentials \
  --namespace=capcs-system \
  --from-literal=username='your-email@example.com' \
  --from-literal=password='your-api-password' \
  --from-literal=region='zrh'
```

Or use the template:
```bash
cp credentials-template.yaml credentials.yaml
# Edit credentials.yaml with your values
kubectl apply -f credentials.yaml
rm credentials.yaml  # Don't commit this!
```

### 3. Install Provider

```bash
kubectl apply -f install.yaml
```

### 4. Verify Installation

```bash
# Check pods
kubectl get pods -n capcs-system

# Check logs
kubectl logs -n capcs-system -l control-plane=controller-manager -f

# Verify CRDs
kubectl get crd | grep cloudsigma
```

Expected output:
```
NAME                              READY   STATUS    RESTARTS   AGE
cloudsigma-controller-manager-*   1/1     Running   0          30s
```

## Directory Structure

```
config/
├── README.md                    # This file
├── install.yaml                 # Complete installation manifest
├── credentials-template.yaml    # Secret template for credentials
├── crd/
│   └── bases/                   # Custom Resource Definitions
├── manager/
│   ├── namespace.yaml           # Namespace (capcs-system)
│   ├── deployment.yaml          # Controller deployment
│   └── service.yaml             # Metrics service
└── rbac/
    ├── service_account.yaml     # ServiceAccount
    ├── role.yaml                # ClusterRole with permissions
    └── role_binding.yaml        # ClusterRoleBinding
```

## Configuration

### Environment Variables

The controller reads CloudSigma credentials from a Kubernetes Secret:

- `CLOUDSIGMA_USERNAME` - Your CloudSigma email/username
- `CLOUDSIGMA_PASSWORD` - Your CloudSigma API password
- `CLOUDSIGMA_REGION` - CloudSigma region (zrh, lvs, sjc, tyo)

### Controller Arguments

Default arguments in `manager/deployment.yaml`:

- `--leader-elect` - Enable leader election (for HA)
- `--metrics-bind-address=:8080` - Metrics endpoint
- `--health-probe-bind-address=:8081` - Health check endpoint

### Resource Limits

Default resource configuration:

```yaml
resources:
  requests:
    cpu: 100m
    memory: 128Mi
  limits:
    cpu: 500m
    memory: 512Mi
```

Adjust in `manager/deployment.yaml` based on your needs.

## Updating the Provider

### Update Image Version

Edit `manager/deployment.yaml`:

```yaml
image: ghcr.io/shalb/cluster-api-provider-cloudsigma:v0.2.0  # Update version
```

Apply changes:
```bash
kubectl apply -f manager/deployment.yaml
```

### Rolling Restart

```bash
kubectl rollout restart deployment/cloudsigma-controller-manager -n capcs-system
```

## Troubleshooting

### Check Controller Status

```bash
kubectl get deployment -n capcs-system
kubectl describe deployment cloudsigma-controller-manager -n capcs-system
```

### View Logs

```bash
# Follow logs
kubectl logs -n capcs-system -l control-plane=controller-manager -f

# Last 100 lines
kubectl logs -n capcs-system -l control-plane=controller-manager --tail=100

# Specific pod
kubectl logs -n capcs-system cloudsigma-controller-manager-xxx -f
```

### Common Issues

**Pods in CrashLoopBackOff:**
- Check credentials secret exists: `kubectl get secret cloudsigma-credentials -n capcs-system`
- Verify credentials are correct
- Check logs for authentication errors

**Controller not reconciling:**
- Verify CRDs are installed: `kubectl get crd | grep cloudsigma`
- Check RBAC permissions: `kubectl describe clusterrole cloudsigma-controller-manager-role`
- Check CloudSigma API connectivity from the pod

**ImagePullBackOff:**
- The image needs to be built and pushed to a registry
- Update `manager/deployment.yaml` with your image location
- Or use `imagePullPolicy: Never` for local development

## Development

### Local Testing

Run controller locally (outside cluster):

```bash
# Export credentials
export CLOUDSIGMA_USERNAME="your-email@example.com"
export CLOUDSIGMA_PASSWORD="your-api-password"
export CLOUDSIGMA_REGION="zrh"

# Run controller
go run cmd/main.go
```

### Build and Deploy Custom Image

```bash
# Build
make docker-build IMG=your-registry/cluster-api-provider-cloudsigma:dev

# Push
make docker-push IMG=your-registry/cluster-api-provider-cloudsigma:dev

# Update deployment
kubectl set image deployment/cloudsigma-controller-manager \
  manager=your-registry/cluster-api-provider-cloudsigma:dev \
  -n capcs-system
```

## Uninstall

```bash
# Delete provider
kubectl delete -f install.yaml

# Delete CRDs (this will delete all CloudSigma resources!)
kubectl delete -f crd/bases/

# Delete credentials
kubectl delete secret cloudsigma-credentials -n capcs-system
```

## Security Notes

- **Never commit `credentials.yaml`** - It contains sensitive information
- The `.gitignore` excludes `credentials.yaml` and `*.env.local`
- Credentials are stored in Kubernetes Secrets
- Controller runs as non-root user (UID 65532)
- Security context enforces least privilege

## References

- [Cluster API Documentation](https://cluster-api.sigs.k8s.io/)
- [CloudSigma API Documentation](https://docs.cloudsigma.com/)
- [Provider Examples](../examples/)
