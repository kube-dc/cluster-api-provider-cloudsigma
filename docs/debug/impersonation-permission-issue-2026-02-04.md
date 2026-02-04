# CloudSigma Impersonation Permission Issue Analysis

**Date**: 2026-02-04  
**Environment**: CloudSigma `next` region  
**Reporter**: Kube-DC Team  

---

## Executive Summary

VMs created via token impersonation are not accessible by the impersonated user. The CloudSigma API returns `403 Permission Denied` when the impersonated user tries to access VMs that were just created using their token.

---

## Timeline of Events

### Phase 1: Token Exchange & VM Creation (SUCCESS)

| Timestamp (UTC) | Event | Details |
|-----------------|-------|---------|
| `2026-02-04T12:03:28Z` | Impersonation mode activated | `userEmail: tsap@shalb.com`, `region: next` |
| `2026-02-04T12:03:28Z` | CreateServer called | `metal-worker-pool-1-hl9tl-qbrcf` (CPU: 4000 MHz, Memory: 4096 MB) |
| `2026-02-04T12:03:28Z` | Drive clone started | Source: `4afeb48e-3b2c-4f7e-ac8b-d9915ad69a79` |
| `2026-02-04T12:03:29Z` | Server creation API call | `POST https://next.cloudsigma.com/api/2.0/servers/` |
| `2026-02-04T12:03:29Z` | **Server created successfully** | UUID: `63dd227a-9502-4311-919e-cd49f3416753` |

### Phase 2: Status Update Race Condition

| Timestamp (UTC) | Event | Details |
|-----------------|-------|---------|
| `2026-02-04T12:03:29Z` | Status update conflict | `the object has been modified; please apply your changes to the latest version` |
| `2026-02-04T12:03:29Z` | Retry triggered | New reconcileID: `92599b9e-b365-49fa-88f2-71f9a979b5f1` |

### Phase 3: Permission Denied (FAILURE)

| Timestamp (UTC) | Event | Details |
|-----------------|-------|---------|
| `2026-02-04T12:03:29Z` | Impersonation mode re-activated | Same user: `tsap@shalb.com` |
| `2026-02-04T12:03:30Z` | **GET server FAILED** | `403 Permission Denied` |

---

## Error Details

### API Request
```
GET https://next.cloudsigma.com/api/2.0/servers/feaf4d9d-7cdc-4ecc-b2f7-bef11a0c7519/
```

### API Response
```json
{
  "error_type": "permission",
  "error_message": "User 4705fb9a-0591-4ead-9d42-b2e86493f9b4 does not have all of the permissions [LIST] in order to complete the operation on resources [feaf4d9d-7cdc-4ecc-b2f7-bef11a0c7519]"
}
```

### CloudSigma Request ID
- `c78c2720-fdc6-4b5c-8871-5f4109df24e7` (one of the failed requests)
- `53977f75-db6c-4002-8be8-2857afac316d` (another failed request)

---

## Key Observations

### 1. UUID Mismatch - CRITICAL BUG IDENTIFIED

**Two VMs exist in CloudSigma with the same name:**

| UUID | Origin | Ownership | Status |
|------|--------|-----------|--------|
| `feaf4d9d-7cdc-4ecc-b2f7-bef11a0c7519` | Previous failed attempt | ❓ Unknown owner | Running (visible in UI) |
| `63dd227a-9502-4311-919e-cd49f3416753` | Current creation | Should be `tsap@shalb.com` | Unknown |

**Root Cause Analysis:**
1. A previous VM creation succeeded in CloudSigma but the K8s status update failed
2. The status retained the OLD VM UUID (`feaf4d9d...`)
3. When the Machine was deleted and recreated, a NEW VM was created (`63dd227a...`)
4. But the status still points to the OLD VM which was created by a different token/user
5. The impersonated user cannot access the OLD VM → 403 error

**This is a combination of:**
- Controller bug: Not handling orphan VMs properly
- Possible impersonation issue: Old VM was created but owned by wrong user

### 2. User ID Mapping
- **Impersonated Email**: `tsap@shalb.com`
- **CloudSigma User UUID**: `4705fb9a-0591-4ead-9d42-b2e86493f9b4`

### 3. Working vs Failing VMs (Same User)

| VM Name | Instance UUID | Status | Can Access? |
|---------|---------------|--------|-------------|
| `metal-worker-pool-1-hl9tl-g2b7h` | `4192fc6e-0372-4961-99fa-6dc9d4038dbf` | Running | ✅ YES |
| `metal-worker-pool-1-hl9tl-qbrcf` | `feaf4d9d-7cdc-4ecc-b2f7-bef11a0c7519` | Stopped* | ❌ NO |

*CloudSigma UI shows "Running", but API returns 403 when accessed via impersonated token.

---

## Impersonation Flow

```
┌─────────────────────────────────────────────────────────────────┐
│                    Kube-DC CAPI Controller                       │
├─────────────────────────────────────────────────────────────────┤
│ 1. Get CloudSigmaCluster.spec.userEmail (tsap@shalb.com)        │
│ 2. Request impersonation token from CloudSigma Keycloak         │
│ 3. Use impersonated token for CloudSigma API calls              │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                CloudSigma Keycloak (Token Exchange)              │
├─────────────────────────────────────────────────────────────────┤
│ Client: kube-dc-partner                                          │
│ Grant Type: urn:ietf:params:oauth:grant-type:token-exchange     │
│ Requested Subject: tsap@shalb.com                                │
│ Audience: cloudsigma-api                                         │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                    CloudSigma API (next region)                  │
├─────────────────────────────────────────────────────────────────┤
│ Expected: Token represents user tsap@shalb.com                   │
│ Expected: VMs created with this token owned by tsap@shalb.com   │
│                                                                  │
│ ACTUAL ISSUE:                                                    │
│ - VM creation succeeds                                           │
│ - But subsequent GET fails with 403 Permission Denied            │
│ - User UUID 4705fb9a-0591-4ead-9d42-b2e86493f9b4 cannot LIST    │
│   the VM they just created                                       │
└─────────────────────────────────────────────────────────────────┘
```

---

## Questions for CloudSigma Keycloak Team

### Critical Questions

1. **VM Ownership for Both UUIDs**: Please verify the owner of these two VMs:
   - `feaf4d9d-7cdc-4ecc-b2f7-bef11a0c7519` (old VM, created ~Feb 3)
   - `63dd227a-9502-4311-919e-cd49f3416753` (new VM, created Feb 4 12:03:29 UTC)
   
   **Expected**: Both should be owned by `tsap@shalb.com` (UUID: `4705fb9a-0591-4ead-9d42-b2e86493f9b4`)

2. **Token Exchange Ownership**: When a VM is created using an impersonated token, is the VM ownership correctly assigned to the impersonated user (`tsap@shalb.com`) or to the service account performing the impersonation?

3. **Request Tracing**: Can you trace these CloudSigma request IDs to see what user identity was actually used for the API calls?
   - `c78c2720-fdc6-4b5c-8871-5f4109df24e7` (GET failed with 403)
   - `53977f75-db6c-4002-8be8-2857afac316d` (GET failed with 403)
   - `21a57032-7527-4e65-b67e-d0167cf13dc4` (GET failed with 403)

### Additional Questions

4. **User Verification**: Can you confirm that user UUID `4705fb9a-0591-4ead-9d42-b2e86493f9b4` corresponds to `tsap@shalb.com`?

5. **ACL Propagation Delay**: Is there any delay between VM creation and ACL assignment that could cause a race condition?

6. **Token Claims**: What claims are present in the impersonated token? Is the `sub` claim correctly set to the impersonated user?

---

## Current Workaround

Manually delete the affected CloudSigmaMachine and let the controller recreate it:

```bash
kubectl delete cloudsigmamachine metal-worker-pool-1-hl9tl-qbrcf -n cloudsigma-tsap-183e269d
```

---

## Controller Logs (Raw)

```
2026-02-04T12:03:28Z    INFO    Using impersonation mode    {"userEmail": "tsap@shalb.com", "region": "next"}
I0204 12:03:28.434779       1 servers.go:34] ==> CreateServer called for: metal-worker-pool-1-hl9tl-qbrcf
I0204 12:03:28.555630       1 servers.go:52] ==> Starting drive clone: source=4afeb48e-3b2c-4f7e-ac8b-d9915ad69a79
I0204 12:03:29.412708       1 servers_custom.go:74] Creating server via direct API call: metal-worker-pool-1-hl9tl-qbrcf
I0204 12:03:29.893412       1 servers_custom.go:141] Server created successfully: metal-worker-pool-1-hl9tl-qbrcf (UUID: 63dd227a-9502-4311-919e-cd49f3416753)
2026-02-04T12:03:29Z    INFO    Server created successfully    {"instanceID": "63dd227a-9502-4311-919e-cd49f3416753"}
2026-02-04T12:03:29Z    ERROR   Failed to update status with instance ID    {"error": "the object has been modified"}
2026-02-04T12:03:29Z    INFO    Using impersonation mode    {"userEmail": "tsap@shalb.com", "region": "next"}
2026-02-04T12:03:30Z    ERROR   Failed to get server    {"error": "403: User 4705fb9a-0591-4ead-9d42-b2e86493f9b4 does not have permissions [LIST] on resource feaf4d9d-7cdc-4ecc-b2f7-bef11a0c7519"}
```

---

## Environment Details

- **Kubernetes Version**: v1.34.0
- **CAPI Provider**: cluster-api-provider-cloudsigma
- **CloudSigma Region**: `next`
- **Keycloak Client**: `kube-dc-partner`
- **Token Exchange Grant**: RFC 8693 Token Exchange

---

## Contact

For further debugging, please contact the Kube-DC team with the CloudSigma request IDs listed above.
