# CloudSigma Cloud Controller Manager (CCM)

The CloudSigma CCM provides Kubernetes cloud provider integration for CloudSigma infrastructure.

## Features

- **Node Initialization**: Removes cloud-provider taint and sets node addresses
- **Node Address Management**: Syncs CloudSigma VM IP addresses to Kubernetes nodes
- **OAuth Impersonation**: Manages nodes using impersonated user credentials (default)
- **Tenant Cluster Support**: Runs in management cluster, manages tenant cluster nodes

## Authentication

CCM supports two authentication modes. **Impersonation is the default and recommended mode**.

### Option A: OAuth Impersonation (Default - Recommended)

Uses service account impersonation to access CloudSigma API as the user who owns the VMs.

```bash
# Required environment variables
CLOUDSIGMA_OAUTH_URL=https://oauth.cloudsigma.com
CLOUDSIGMA_CLIENT_ID=your-service-account-client-id
CLOUDSIGMA_CLIENT_SECRET=your-service-account-secret
CLOUDSIGMA_USER_EMAIL=user@example.com  # User whose VMs to manage
CLOUDSIGMA_REGION=zrh
```

### Option B: Legacy Credentials (Must be explicitly enabled)

Uses a single CloudSigma account. **Disabled by default** - requires explicit opt-in.

```bash
# Enable legacy mode (required - disabled by default)
CLOUDSIGMA_ENABLE_LEGACY_CREDENTIALS=true
CLOUDSIGMA_USERNAME=your-email@example.com
CLOUDSIGMA_PASSWORD=your-api-password
CLOUDSIGMA_REGION=zrh
```

## Environment Variables Reference

| Variable | Description | Required |
|----------|-------------|----------|
| `CLOUDSIGMA_OAUTH_URL` | OAuth/Keycloak URL for impersonation | For impersonation |
| `CLOUDSIGMA_CLIENT_ID` | Service account client ID | For impersonation |
| `CLOUDSIGMA_CLIENT_SECRET` | Service account client secret | For impersonation |
| `CLOUDSIGMA_USER_EMAIL` | User email for impersonation | For impersonation |
| `CLOUDSIGMA_REGION` | CloudSigma region (zrh, sjc, hnl, per) | Yes |
| `CLOUDSIGMA_ENABLE_LEGACY_CREDENTIALS` | Set to `true` to enable legacy auth | For legacy mode |
| `CLOUDSIGMA_USERNAME` | CloudSigma username (legacy) | For legacy mode |
| `CLOUDSIGMA_PASSWORD` | CloudSigma password (legacy) | For legacy mode |

## Command Line Flags

```
--tenant-kubeconfig       Path to kubeconfig for tenant cluster (required)
--cluster-name            Name of the cluster being managed
--cloudsigma-region       CloudSigma region
--oauth-url               CloudSigma OAuth URL
--client-id               OAuth client ID
--client-secret           OAuth client secret
--user-email              User email for impersonation
--enable-legacy-credentials  Enable legacy username/password authentication
--cloudsigma-username     CloudSigma API username (legacy)
--cloudsigma-password     CloudSigma API password (legacy)
--metrics-bind-address    Metrics endpoint address (default :8080)
--health-probe-bind-address  Health probe address (default :8081)
```

## Deployment

CCM is typically deployed as a sidecar in tenant cluster control plane pods:

```yaml
containers:
  - name: cloudsigma-ccm
    image: shalb/cloudsigma-ccm:latest
    args:
      - --tenant-kubeconfig=/etc/kubernetes/kubeconfig
      - --cluster-name=$(CLUSTER_NAME)
      - --cloudsigma-region=zrh
    env:
      - name: CLOUDSIGMA_OAUTH_URL
        value: "https://oauth.cloudsigma.com"
      - name: CLOUDSIGMA_CLIENT_ID
        valueFrom:
          secretKeyRef:
            name: cloudsigma-impersonation
            key: client-id
      - name: CLOUDSIGMA_CLIENT_SECRET
        valueFrom:
          secretKeyRef:
            name: cloudsigma-impersonation
            key: client-secret
      - name: CLOUDSIGMA_USER_EMAIL
        value: "user@example.com"
      # Legacy credentials DISABLED by default
      # To enable (not recommended):
      # - name: CLOUDSIGMA_ENABLE_LEGACY_CREDENTIALS
      #   value: "true"
```

## Building

```bash
# Build binary
go build -o ccm-binary ./ccm/cmd/main.go

# Build Docker image
docker build -t shalb/cloudsigma-ccm:latest -f ccm/Dockerfile .

# Push image
docker push shalb/cloudsigma-ccm:latest
```

## License

Apache License 2.0 - See [LICENSE](../LICENSE)
