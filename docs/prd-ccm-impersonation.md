# PRD: CloudSigma CCM User Impersonation

## Overview

Enable the CloudSigma Cloud Controller Manager (CCM) to query VM resources in the user's CloudSigma account using OAuth service account impersonation. This is required when VMs are created via impersonation (user's account) rather than global credentials.

**Status: IMPLEMENTED**

## Problem Statement

**Current State:**
- CAPCS creates VMs in user's CloudSigma account via impersonation ✅
- CCM uses global `cloudsigma-credentials` secret (username/password)
- CCM cannot query VMs in user's account (403 Forbidden)
- Node addresses are not updated, nodes may remain tainted

**Desired State:**
- CCM uses same impersonation mechanism as CAPCS
- CCM can query VM details from user's CloudSigma account
- Node initialization completes successfully
- Full end-to-end impersonation flow

## Architecture

### Current CCM Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         MANAGEMENT CLUSTER                                   │
├─────────────────────────────────────────────────────────────────────────────┤
│  Namespace: cloudsigma-example-3415fbd3                                      │
│                                                                              │
│  ┌─────────────────────┐    ┌─────────────────────┐                         │
│  │ cloudsigma-         │    │ csccm-imper         │                         │
│  │ credentials         │───▶│ (CCM Deployment)    │                         │
│  │ Secret              │    │                     │                         │
│  │ • username          │    │ Queries CloudSigma  │──────┐                  │
│  │ • password          │    │ API with GLOBAL     │      │                  │
│  └─────────────────────┘    │ credentials         │      │                  │
│                             └─────────────────────┘      │                  │
│                                      │                   │                  │
│                                      │ kubeconfig        │ ❌ 403 Forbidden │
│                                      ▼                   │                  │
│                             ┌─────────────────────┐      │                  │
│                             │ Tenant Cluster      │      │                  │
│                             │ • Update node addr  │      │                  │
│                             │ • Remove taint      │      │                  │
│                             └─────────────────────┘      │                  │
└──────────────────────────────────────────────────────────┼──────────────────┘
                                                           │
                                                           ▼
                                              ┌─────────────────────────┐
                                              │ CloudSigma API          │
                                              │ (User's Account)        │
                                              │                         │
                                              │ VMs created here via    │
                                              │ CAPCS impersonation     │
                                              └─────────────────────────┘
```

### Proposed CCM Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         MANAGEMENT CLUSTER                                   │
├─────────────────────────────────────────────────────────────────────────────┤
│  Namespace: cloudsigma-example-3415fbd3                                      │
│                                                                              │
│  ┌─────────────────────┐    ┌─────────────────────┐                         │
│  │ cloudsigma-         │    │ csccm-imper         │                         │
│  │ impersonation       │───▶│ (CCM Deployment)    │                         │
│  │ Secret              │    │                     │                         │
│  │ • client-id         │    │ Uses IMPERSONATION  │──────┐                  │
│  │ • client-secret     │    │ with userEmail      │      │                  │
│  │ • oauth-url         │    │                     │      │                  │
│  └─────────────────────┘    └─────────────────────┘      │                  │
│                                      │                   │                  │
│  ┌─────────────────────┐             │ kubeconfig        │                  │
│  │ CloudSigmaCluster   │             │                   │                  │
│  │ • userEmail         │─────────────┤                   │ ✅ 200 OK        │
│  └─────────────────────┘             ▼                   │                  │
│                             ┌─────────────────────┐      │                  │
│                             │ Tenant Cluster      │      │                  │
│                             │ • Update node addr  │      │                  │
│                             │ • Remove taint      │      │                  │
│                             └─────────────────────┘      │                  │
└──────────────────────────────────────────────────────────┼──────────────────┘
                                                           │
                                                           ▼
                                              ┌─────────────────────────┐
                                              │ CloudSigma API          │
                                              │ (User's Account)        │
                                              │                         │
                                              │ CCM queries VMs using   │
                                              │ impersonated token      │
                                              └─────────────────────────┘
```

## Security Analysis

### Current Security (Already Good ✅)

| Component | Location | User Access |
|-----------|----------|-------------|
| CCM Deployment | Management cluster | ❌ No access |
| Credentials Secret | Management cluster | ❌ No access |
| OAuth Secret | Management cluster | ❌ No access |
| Tenant kubeconfig | Management cluster | ❌ No access |

Users **cannot** see any credentials because CCM runs in the management cluster, not in the tenant cluster.

### Impersonation Security Benefits

1. **No global credentials needed** - CCM uses OAuth service account
2. **Per-user tokens** - Each cluster gets its own impersonated token
3. **Token expiry** - Tokens expire and are refreshed automatically
4. **Audit trail** - All API calls logged under user's identity

## Implementation

### Files Modified

#### cluster-api-provider-cloudsigma

| File | Changes |
|------|--------|
| `ccm/cmd/main.go` | Added impersonation flags and client initialization |
| `ccm/controllers/node_controller.go` | Added impersonation support with fallback to legacy |

#### kube-dc-k8-manager

| File | Changes |
|------|--------|
| `internal/controller/kdccluster_ccm.go` | Updated CCM deployment env vars for impersonation |

### Key Changes

**CCM main.go** - Added flags:
- `--impersonation-enabled` / `CLOUDSIGMA_IMPERSONATION_ENABLED`
- `--oauth-url` / `CLOUDSIGMA_OAUTH_URL`
- `--client-id` / `CLOUDSIGMA_CLIENT_ID`
- `--client-secret` / `CLOUDSIGMA_CLIENT_SECRET`
- `--user-email` / `CLOUDSIGMA_USER_EMAIL`

**CCM node_controller.go** - Client initialization:
```go
if r.ImpersonationEnabled && r.ImpersonationClient != nil && r.UserEmail != "" {
    // Use impersonation (preferred)
    token, err := r.ImpersonationClient.GetImpersonatedToken(ctx, r.UserEmail, region)
    cred := cloudsigma.NewTokenCredentialsProvider(token)
    r.cloudsigmaClient = cloudsigma.NewClient(cred, cloudsigma.WithLocation(region))
} else if r.CloudSigmaUsername != "" && r.CloudSigmaPassword != "" {
    // Fallback to legacy credentials
    cred := cloudsigma.NewUsernamePasswordCredentialsProvider(...)
    r.cloudsigmaClient = cloudsigma.NewClient(cred, cloudsigma.WithLocation(region))
}
```

**kube-dc-k8-manager kdccluster_ccm.go** - Deployment env vars:
- Reads `cloudsigma.com/owner-email` annotation from KdcCluster
- Reads region from CloudSigma worker pool config
- Passes impersonation config via `cloudsigma-impersonation` secret

## Configuration

### Kubernetes Secret (Management Cluster)

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: cloudsigma-impersonation
  namespace: capcs-system  # Or per-namespace
type: Opaque
data:
  oauth-url: <base64-encoded>
  client-id: <base64-encoded>
  client-secret: <base64-encoded>
```

### CCM Deployment Environment

| Variable | Source | Description |
|----------|--------|-------------|
| `CLOUDSIGMA_IMPERSONATION_ENABLED` | Literal | Enable impersonation mode |
| `CLOUDSIGMA_OAUTH_URL` | Secret | OAuth/Keycloak URL |
| `CLOUDSIGMA_CLIENT_ID` | Secret | Service account client ID |
| `CLOUDSIGMA_CLIENT_SECRET` | Secret | Service account secret |
| `CLOUDSIGMA_USER_EMAIL` | CloudSigmaCluster | User to impersonate |
| `CLOUDSIGMA_REGION` | CloudSigmaCluster | CloudSigma region |

## Files to Modify

### cluster-api-provider-cloudsigma

| File | Changes |
|------|---------|
| `ccm/cmd/main.go` | Add impersonation flags and initialization |
| `ccm/controllers/node_controller.go` | Use impersonation client if configured |

### kube-dc-k8-manager

| File | Changes |
|------|---------|
| `internal/controller/kdccluster_ccm.go` | Pass impersonation env vars to CCM |

## Testing Plan

1. **Unit Tests**
   - Test CCM with impersonation client mock
   - Test fallback to legacy credentials

2. **Integration Tests**
   - Deploy CCM with impersonation enabled
   - Verify VM queries succeed in user's account
   - Verify node addresses are updated

3. **E2E Test**
   - Create cluster with `cloudsigma.com/owner-email` annotation
   - Verify CAPCS creates VM in user's account
   - Verify CCM queries VM and updates node
   - Verify node taint is removed

## Success Criteria

1. ✅ CCM can query VMs in user's CloudSigma account
2. ✅ Node addresses are populated correctly
3. ✅ Node initialization taint is removed
4. ✅ Fallback to legacy credentials works
5. ✅ Token caching works (reuses CAPCS implementation)

## Dependencies

- CAPCS impersonation implementation (✅ COMPLETED)
- `pkg/auth/impersonation.go` (✅ AVAILABLE)
- `pkg/cloud/client.go` NewClientWithImpersonation (✅ AVAILABLE)

## Build Status

- ✅ `cluster-api-provider-cloudsigma/ccm` builds successfully
- ✅ `kube-dc-k8-manager` builds successfully

## Open Questions

1. **Shared Secret**: Should impersonation secret be cluster-wide or per-namespace?
   - Recommendation: Cluster-wide in `capcs-system` namespace

2. **Fallback Behavior**: Should CCM fail or use legacy creds if impersonation fails?
   - Recommendation: Log error and continue with fallback

---

# CSI Token Provisioning

## Overview

Enable the CloudSigma CSI driver to authenticate with the CloudSigma API using CCM-provisioned OAuth tokens. The CCM obtains impersonated tokens and stores them in a tenant cluster secret for CSI consumption.

**Status: IMPLEMENTED (Blocked by CloudSigma API)**

## Problem Statement

**Current State:**
- CSI driver requires CloudSigma credentials to provision/attach volumes
- Previously required manual credential creation in tenant clusters
- CSI sidecar containers need Kubernetes API endpoint to communicate with K8s API

**Desired State:**
- CCM automatically provisions OAuth tokens for CSI driver
- Tokens stored in tenant cluster secret (`cloudsigma-token`)
- Tokens auto-refreshed by CCM before expiry
- CSI sidecars use Sveltos-templated K8s API endpoint (like Cilium)

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         MANAGEMENT CLUSTER                                   │
├─────────────────────────────────────────────────────────────────────────────┤
│  Namespace: cloudsigma-<cluster-namespace>                                   │
│                                                                              │
│  ┌─────────────────────┐    ┌─────────────────────┐                         │
│  │ cloudsigma-         │    │ csccm-<cluster>     │                         │
│  │ impersonation       │───▶│ (CCM Deployment)    │                         │
│  │ Secret              │    │                     │                         │
│  │ • client-id         │    │ CSI Token Controller│──────┐                  │
│  │ • client-secret     │    │ --csi-token-enabled │      │                  │
│  │ • oauth-url         │    └─────────────────────┘      │                  │
│  └─────────────────────┘             │                   │                  │
│                                      │ kubeconfig        │ Impersonation    │
│  ┌─────────────────────┐             ▼                   │                  │
│  │ Cluster CR          │    ┌─────────────────────┐      │                  │
│  │ spec:               │    │ Tenant Cluster      │      │                  │
│  │  controlPlaneEndpoint:   │ cloudsigma-csi ns   │      │                  │
│  │   host: <external>  │───▶│ ┌─────────────────┐ │      │                  │
│  │   port: 443         │    │ │cloudsigma-token │ │      │                  │
│  └─────────────────────┘    │ │ • access_token  │ │      │                  │
│                             │ │ • region        │ │      │                  │
│                             │ │ • user_email    │ │      │                  │
│                             │ └─────────────────┘ │      │                  │
│                             └─────────────────────┘      │                  │
└──────────────────────────────────────────────────────────┼──────────────────┘
                                                           │
                                                           ▼
                                              ┌─────────────────────────┐
                                              │ CloudSigma API          │
                                              │ service_provider/       │
                                              │ api/v1/user/impersonate │
                                              └─────────────────────────┘
```

## Implementation Details

### 1. CCM CSI Token Controller

**File:** `ccm/controllers/csi_token_controller.go`

**Status:** ✅ IMPLEMENTED

The CSI Token Controller runs alongside the Node Controller in CCM:
- Obtains impersonated OAuth token from CloudSigma
- Creates/updates `cloudsigma-token` secret in tenant cluster
- Refreshes token every 5 minutes (before 15min expiry)
- Stores: `access_token`, `region`, `user_email`

```go
type CSITokenController struct {
    TenantClient        kubernetes.Interface
    ImpersonationClient *auth.ImpersonationClient
    UserEmail           string
    Region              string
    Enabled             bool
}
```

### 2. CSI Driver Token Consumption

**File:** `csi/cmd/controller/main.go`

**Status:** ✅ IMPLEMENTED

CSI controller reads token from mounted secret:
- `--token-file=/etc/cloudsigma/access_token` flag
- Falls back to legacy credentials if token not available
- Token refreshed by CCM, CSI re-reads on each API call

### 3. Sveltos ConfigMap with Templating

**File:** `kube-dc-k8-manager/config/addons/sveltos/cloudsigma-csi-configmap.yaml`

**Status:** ✅ IMPLEMENTED

**Key Fix:** Added `projectsveltos.io/template: "ok"` annotation to enable Sveltos template variable substitution.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: cloudsigma-csi-template
  namespace: projectsveltos
  annotations:
    projectsveltos.io/template: "ok"  # ← Required for templating!
data:
  cloudsigma-csi.yaml: |
    # CSI sidecar containers use templated K8s API endpoint
    - name: csi-provisioner
      env:
      - name: KUBERNETES_SERVICE_HOST
        value: "{{ .Cluster.spec.controlPlaneEndpoint.host }}"
      - name: KUBERNETES_SERVICE_PORT
        value: "{{ .Cluster.spec.controlPlaneEndpoint.port }}"
```

### 4. ClusterProfile for CSI Deployment

**File:** `kube-dc-k8-manager/config/addons/sveltos/cloudsigma-csi-clusterprofile.yaml`

**Status:** ✅ IMPLEMENTED

Deploys CSI to clusters with `csi: cloudsigma` label.

## Files Modified

### cluster-api-provider-cloudsigma

| File | Changes | Status |
|------|---------|--------|
| `ccm/controllers/csi_token_controller.go` | New controller for token provisioning | ✅ |
| `ccm/cmd/main.go` | Added `--csi-token-enabled` flag | ✅ |
| `csi/cmd/controller/main.go` | Added `--token-file` flag support | ✅ |

### kube-dc-k8-manager

| File | Changes | Status |
|------|---------|--------|
| `config/addons/sveltos/cloudsigma-csi-configmap.yaml` | Added template annotation, K8s API env vars | ✅ |
| `config/addons/sveltos/cloudsigma-csi-clusterprofile.yaml` | ClusterProfile for CSI deployment | ✅ |
| `internal/controller/kdccluster_ccm.go` | Added `--csi-token-enabled` to CCM args | ✅ |

### kube-dc (installer)

| File | Changes | Status |
|------|---------|--------|
| `installer/kube-dc/templates/kube-dc/sveltos/cloudsigma-csi-configmap.yaml` | Copied from kube-dc-k8-manager | ✅ |
| `installer/kube-dc/templates/kube-dc/sveltos/cloudsigma-csi-clusterprofile.yaml` | Copied from kube-dc-k8-manager | ✅ |

## Docker Images

| Image | Version | Status |
|-------|---------|--------|
| `shalb/cloudsigma-ccm` | `v0.1.0-csi-token-v3`, `latest` | ✅ Pushed |
| `shalb/cloudsigma-csi-controller` | `latest` | ✅ Pushed |

## Verification Results

### Sveltos Templating

**Status:** ✅ WORKING

After adding `projectsveltos.io/template: "ok"` annotation, CSI sidecars correctly receive:
```
KUBERNETES_SERVICE_HOST: csi-test-cp-cloudsigma-tsap-183e269d.stage.kube-dc.com
KUBERNETES_SERVICE_PORT: 443
```

### CCM Token Provisioning

**Status:** ⏳ BLOCKED (CloudSigma API Issue)

The CCM CSI Token Controller is implemented and deployed, but CloudSigma's service provider API is returning **504 Gateway Timeout**:

```
POST https://direct.next.cloudsigma.com/service_provider/api/v1/user/impersonate
→ HTTP 504 (60s timeout)
```

**Impact:** CCM cannot create the `cloudsigma-token` secret in tenant clusters until CloudSigma resolves this API issue.

**Ticket:** Reported to CloudSigma (Feb 2, 2025)

## Testing Checklist

| Test | Status |
|------|--------|
| Sveltos deploys CSI to clusters with `csi: cloudsigma` label | ✅ |
| Sveltos templates K8s API endpoint correctly | ✅ |
| CCM starts CSI Token Controller | ✅ |
| CCM creates `cloudsigma-token` secret | ⏳ Blocked |
| CSI controller reads token from secret | ⏳ Blocked |
| PVC provisioning works | ⏳ Blocked |

## Next Steps

1. **Wait for CloudSigma** to fix the service_provider API timeout issue
2. **Verify token creation** once API is responsive
3. **Test PVC creation** end-to-end
4. **Document** final verification results

## Configuration

### Enable CSI for a Cluster

Add label to KdcCluster:
```yaml
metadata:
  labels:
    csi: cloudsigma
```

### CCM Environment Variables

| Variable | Description |
|----------|-------------|
| `CLOUDSIGMA_CSI_TOKEN_ENABLED` | Enable CSI token provisioning |
| `CLOUDSIGMA_USER_EMAIL` | User to impersonate for CSI |
| `CLOUDSIGMA_REGION` | CloudSigma region |

## References

- [CAPCS Impersonation PRD](./prd-user-impersonation.md)
- [CCM Deployment Guide](./ccm-deployment.md)
- [CloudSigma OAuth Flow](./prd-user-impersonation.md#cloudsigma-oauth-impersonation-flow)
