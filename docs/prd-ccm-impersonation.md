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
- Reads `kube-dc.com/owner-email` annotation from KdcCluster
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
   - Create cluster with `kube-dc.com/owner-email` annotation
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

## References

- [CAPCS Impersonation PRD](./prd-user-impersonation.md)
- [CCM Deployment Guide](./ccm-deployment.md)
- [CloudSigma OAuth Flow](./prd-user-impersonation.md#cloudsigma-oauth-impersonation-flow)
