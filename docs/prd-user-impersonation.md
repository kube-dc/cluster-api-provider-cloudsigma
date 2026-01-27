# PRD: CloudSigma User Impersonation for CAPCS

## Overview

Enable the Cluster API Provider CloudSigma (CAPCS) controller to create VM resources (worker nodes) in the user's CloudSigma account using service account impersonation, rather than using global shared credentials.

## Problem Statement

**Current State:**
- CAPCS uses a single set of global credentials (`CLOUDSIGMA_USERNAME`, `CLOUDSIGMA_PASSWORD`) configured at controller startup
- All VMs for all users are created under this single account
- Users cannot see their Kubernetes worker nodes in their own CloudSigma dashboard
- Billing and resource attribution is not per-user

**Desired State:**
- Controller uses a CloudSigma service account to impersonate individual users
- VMs are created in each user's CloudSigma account
- Users see their worker nodes in their CloudSigma dashboard
- Resource usage and billing is attributed to the correct user

## CloudSigma OAuth Impersonation Flow

Based on CloudSigma developer guidance, the impersonation flow is:

```
┌─────────────────┐    ┌──────────────────────────┐    ┌─────────────────────┐
│  CAPCS          │    │  CloudSigma OAuth        │    │  CloudSigma API     │
│  Controller     │    │  (Keycloak)              │    │                     │
└────────┬────────┘    └────────────┬─────────────┘    └──────────┬──────────┘
         │                          │                              │
         │  1. client_credentials   │                              │
         │  grant (service account) │                              │
         ├─────────────────────────►│                              │
         │                          │                              │
         │  access_token            │                              │
         │◄─────────────────────────┤                              │
         │                          │                              │
         │  2. UMA ticket grant     │                              │
         │  (RPT token)             │                              │
         ├─────────────────────────►│                              │
         │                          │                              │
         │  rpt_token               │                              │
         │◄─────────────────────────┤                              │
         │                          │                              │
         │  3. Impersonate user     │                              │
         │  (service_provider_api)  │                              │
         ├──────────────────────────┼─────────────────────────────►│
         │                          │                              │
         │  impersonated_token      │                              │
         │◄─────────────────────────┼──────────────────────────────┤
         │                          │                              │
         │  4. Create VM with       │                              │
         │  impersonated_token      │                              │
         ├──────────────────────────┼─────────────────────────────►│
         │                          │                              │
         │  VM created in           │                              │
         │  user's account          │                              │
         │◄─────────────────────────┼──────────────────────────────┤
         │                          │                              │
```

### Step 1: Get Service Account Access Token

```bash
POST https://oauth-stg.cloudsigma.com/realms/cloudsigma/protocol/openid-connect/token
Content-Type: application/x-www-form-urlencoded

grant_type=client_credentials
client_id=kubernetes_cluster_service
client_secret=<SERVICE_ACCOUNT_SECRET>
```

Response: `access_token` (service account JWT)

### Step 2: Exchange for RPT Token (UMA Ticket)

```bash
POST https://oauth-stg.cloudsigma.com/realms/cloudsigma/protocol/openid-connect/token
Content-Type: application/x-www-form-urlencoded
Authorization: Bearer <access_token_from_step_1>

grant_type=urn:ietf:params:oauth:grant-type:uma-ticket
audience=service_provider_api
```

Response: `rpt_token` (RPT with impersonation permissions)

### Step 3: Impersonate User

```bash
POST https://direct.{region}.cloudsigma.com/service_provider/api/v1/user/impersonate
Content-Type: application/json
Authorization: Bearer <rpt_token_from_step_2>

{
  "user_email": "user@example.com",
  "subject_token": "<access_token_from_step_1>"
}
```

Response: `impersonated_token` (JWT acting as user)

### Step 4: Call CloudSigma API as User

```bash
GET/POST https://direct.{region}.cloudsigma.com/api/2.0/servers/
Authorization: Bearer <impersonated_token_from_step_3>
```

## Architecture Changes

### 1. New Configuration Options

**Environment Variables / Flags:**
```
# Service Account Credentials (replaces username/password)
CLOUDSIGMA_OAUTH_URL=https://oauth.cloudsigma.com
CLOUDSIGMA_CLIENT_ID=kubernetes_cluster_service
CLOUDSIGMA_CLIENT_SECRET=<secret>

# Optional: API base URL per region
CLOUDSIGMA_API_URL=https://direct.{region}.cloudsigma.com

# Legacy mode (for backwards compatibility)
CLOUDSIGMA_IMPERSONATION_ENABLED=true
```

### 2. CRD Changes

**CloudSigmaCluster** - Add user email reference:
```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: CloudSigmaCluster
metadata:
  name: my-cluster
  namespace: user-project
spec:
  region: next
  # NEW: User email for impersonation
  userEmail: user@example.com
  # OR: Reference to a secret containing user info
  userRef:
    name: cluster-owner
    namespace: user-project
```

**CloudSigmaMachineTemplate** - Inherit from cluster or specify:
```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: CloudSigmaMachineTemplate
metadata:
  name: worker-template
spec:
  template:
    spec:
      cpu: 4000
      memory: 4096
      # Optional: Override user email per machine template
      userEmail: user@example.com
```

### 3. New Package: `pkg/auth/impersonation.go`

```go
package auth

import (
    "context"
    "time"
)

// ImpersonationClient handles CloudSigma OAuth impersonation
type ImpersonationClient struct {
    oauthURL     string
    clientID     string
    clientSecret string
    httpClient   *http.Client
    
    // Token cache with mutex
    tokenCache   map[string]*CachedToken
    cacheMutex   sync.RWMutex
}

// CachedToken holds an impersonated token with expiry
type CachedToken struct {
    Token     string
    ExpiresAt time.Time
    UserEmail string
}

// NewImpersonationClient creates a new impersonation client
func NewImpersonationClient(oauthURL, clientID, clientSecret string) *ImpersonationClient

// GetImpersonatedToken returns a valid impersonated token for the user
// Uses cache if available and not expired
func (c *ImpersonationClient) GetImpersonatedToken(ctx context.Context, userEmail, region string) (string, error)

// getServiceAccountToken gets the service account access token (Step 1)
func (c *ImpersonationClient) getServiceAccountToken(ctx context.Context) (string, error)

// getRPTToken exchanges access token for RPT token (Step 2)
func (c *ImpersonationClient) getRPTToken(ctx context.Context, accessToken string) (string, error)

// impersonateUser gets impersonated token for user (Step 3)
func (c *ImpersonationClient) impersonateUser(ctx context.Context, rptToken, accessToken, userEmail, region string) (string, error)
```

### 4. Modified Cloud Client

```go
package cloud

// Client wraps CloudSigma SDK with impersonation support
type Client struct {
    sdk              *cloudsigma.Client
    region           string
    impersonation    *auth.ImpersonationClient
    impersonatedUser string
    
    // For legacy mode
    username string
    password string
}

// NewClientWithImpersonation creates a client that uses impersonation
func NewClientWithImpersonation(impersonationClient *auth.ImpersonationClient, userEmail, region string) (*Client, error)

// NewClientWithCredentials creates a client with direct credentials (legacy)
func NewClientWithCredentials(username, password, region string) (*Client, error)
```

### 5. Controller Changes

**CloudSigmaMachineReconciler:**
```go
type CloudSigmaMachineReconciler struct {
    client.Client
    Scheme *runtime.Scheme

    // Impersonation mode (preferred)
    ImpersonationClient *auth.ImpersonationClient
    
    // Legacy mode (deprecated)
    CloudSigmaUsername string
    CloudSigmaPassword string
    CloudSigmaRegion   string
}

func (r *CloudSigmaMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // ... existing code ...
    
    // Get user email from CloudSigmaCluster
    userEmail := cloudSigmaCluster.Spec.UserEmail
    
    // Create cloud client with impersonation
    var cloudClient *cloud.Client
    if r.ImpersonationClient != nil && userEmail != "" {
        cloudClient, err = cloud.NewClientWithImpersonation(
            r.ImpersonationClient,
            userEmail,
            cloudSigmaCluster.Spec.Region,
        )
    } else {
        // Fallback to legacy mode
        cloudClient, err = cloud.NewClientWithCredentials(
            r.CloudSigmaUsername,
            r.CloudSigmaPassword,
            r.CloudSigmaRegion,
        )
    }
    
    // ... rest of reconciliation ...
}
```

## Integration with Kube-DC

### User Email Propagation Flow

```
┌──────────────────┐     ┌─────────────────────┐     ┌──────────────────┐
│ cs-marketplace   │     │ kube-dc-k8-manager  │     │ CAPCS            │
│ partner (BFF)    │     │ (KdcCluster)        │     │ Controller       │
└────────┬─────────┘     └──────────┬──────────┘     └────────┬─────────┘
         │                          │                          │
         │ 1. Create cluster        │                          │
         │ (user JWT contains email)│                          │
         ├─────────────────────────►│                          │
         │                          │                          │
         │                          │ 2. Extract email from    │
         │                          │ JWT or annotation        │
         │                          │                          │
         │                          │ 3. Create CloudSigma-    │
         │                          │ Cluster with userEmail   │
         │                          ├─────────────────────────►│
         │                          │                          │
         │                          │                          │ 4. Impersonate
         │                          │                          │    and create VM
         │                          │                          │
```

### KdcCluster Changes

Add user email to KdcCluster spec or derive from namespace/annotations:

```yaml
apiVersion: k8s.kube-dc.com/v1alpha1
kind: KdcCluster
metadata:
  name: my-cluster
  namespace: user-project
  annotations:
    # Set by BFF when creating cluster
    kube-dc.com/owner-email: user@example.com
spec:
  workers:
    - name: worker-pool-1
      infrastructureProvider: cloudsigma
      # ...
```

**kube-dc-k8-manager reconcileCloudSigmaMachineTemplate:**
```go
// Get owner email from KdcCluster annotation
ownerEmail := cluster.Annotations["kube-dc.com/owner-email"]

// Pass to CloudSigmaCluster
cloudSigmaCluster.Spec.UserEmail = ownerEmail
```

## Security Considerations

1. **Service Account Secret Protection:**
   - Store `CLOUDSIGMA_CLIENT_SECRET` in Kubernetes Secret
   - Mount as environment variable or file
   - Rotate periodically

2. **Token Caching:**
   - Cache impersonated tokens with TTL (< token expiry)
   - Clear cache on reconciler restart
   - Consider using Kubernetes Secret for persistence across restarts

3. **Audit Logging:**
   - Log all impersonation requests
   - Include user email, action, resource created
   - Track failures and rate limit violations

4. **Rate Limiting:**
   - Implement exponential backoff on OAuth failures
   - Track per-user token requests
   - Avoid thundering herd on token refresh

## Implementation Status

> **Status: IMPLEMENTED** (January 2026)

### Phase 1: Core Impersonation Package ✅ COMPLETE
- [x] Created `pkg/auth/impersonation.go` with full OAuth flow
- [x] Implemented 3-step token acquisition (client_credentials → RPT → impersonate)
- [x] Added token caching with configurable TTL (5-minute buffer before expiry)
- [x] Unit tests in `pkg/auth/impersonation_test.go`

### Phase 2: CRD Updates ✅ COMPLETE
- [x] Added `userEmail` field to CloudSigmaCluster spec
- [x] Added `userRef` as alternative (Secret reference)
- [x] CRD validation generated via kubebuilder
- [x] Manifests regenerated (`make manifests`)

### Phase 3: Cloud Client Refactor ✅ COMPLETE
- [x] Created `NewClientWithImpersonation()` in `pkg/cloud/client.go`
- [x] Added `RefreshImpersonatedToken()` for long-running operations
- [x] Maintains backwards compatibility with `NewClient()` for credentials mode

### Phase 4: Controller Integration ✅ COMPLETE
- [x] Updated `CloudSigmaMachineReconciler` with `ImpersonationClient` field
- [x] Updated `CloudSigmaClusterReconciler` with `ImpersonationClient` field
- [x] Added `getCloudClient()` helper for dynamic auth mode selection
- [x] Added `getUserEmail()` helper for email extraction from spec or secret
- [x] Feature flag via `CLOUDSIGMA_IMPERSONATION_ENABLED` env var

### Phase 5: Kube-DC Integration ✅ COMPLETE
- [x] Updated `kube-dc-k8-manager`:
  - Added `reconcileCloudSigmaCluster()` function
  - Added `reconcileInfrastructureCluster()` for provider detection
  - CloudSigmaCluster created with `userEmail` from `kube-dc.com/owner-email` annotation
- [x] Updated `cs-marketplace-partner-kubedc` BFF:
  - Extracts user email from JWT token
  - Injects `kube-dc.com/owner-email` annotation on cluster creation
- [x] Updated `kube-dc` UI backend:
  - Accepts `annotations` in cluster creation request body

### Phase 6: Production Hardening 🔄 IN PROGRESS
- [x] Comprehensive logging (token acquisition, cache hits/misses)
- [ ] Metrics for token cache hits/misses (TODO)
- [ ] Alert on impersonation failures (TODO)
- [x] PRD documentation complete

## Files Implemented

### CAPCS (cluster-api-provider-cloudsigma)
| File | Description |
|------|-------------|
| `pkg/auth/impersonation.go` | OAuth impersonation client with 3-step flow |
| `pkg/auth/impersonation_test.go` | Unit tests for impersonation client |
| `api/v1beta1/cloudsigmacluster_types.go` | Added `userEmail`, `userRef` fields |
| `pkg/cloud/client.go` | Added `NewClientWithImpersonation()` |
| `controllers/cloudsigmamachine_controller.go` | Integrated impersonation |
| `controllers/cloudsigmacluster_controller.go` | Added ImpersonationClient field |
| `cmd/main.go` | Added impersonation config flags |

### kube-dc-k8-manager
| File | Description |
|------|-------------|
| `internal/controller/kdccluster_capi.go` | Added `reconcileCloudSigmaCluster()`, `reconcileInfrastructureCluster()` |
| `internal/controller/kdccluster_controller.go` | Call `reconcileInfrastructureCluster()` |

### cs-marketplace-partner-kubedc
| File | Description |
|------|-------------|
| `backend/routes/clusters.js` | Extract email from JWT, inject annotation |

### kube-dc
| File | Description |
|------|-------------|
| `ui/backend/controllers/kubernetesClusterModule.js` | Accept annotations in request |

## Data Flow (Implemented)

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         END-TO-END IMPERSONATION FLOW                        │
└─────────────────────────────────────────────────────────────────────────────┘

User (email: user@example.com) creates cluster via UI
                    │
                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ cs-marketplace-partner BFF                                                   │
│  1. Extract email from JWT: tokenPayload.email                              │
│  2. Inject annotation: kube-dc.com/owner-email=user@example.com             │
└─────────────────────────────────────────────────────────────────────────────┘
                    │
                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ kube-dc UI Backend                                                           │
│  3. Create KdcCluster with annotations from request                         │
└─────────────────────────────────────────────────────────────────────────────┘
                    │
                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ kube-dc-k8-manager                                                           │
│  4. reconcileInfrastructureCluster() detects cloudsigma provider            │
│  5. reconcileCloudSigmaCluster() extracts annotation                        │
│  6. Creates CloudSigmaCluster with spec.userEmail=user@example.com          │
└─────────────────────────────────────────────────────────────────────────────┘
                    │
                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ CAPCS Controller                                                             │
│  7. getCloudClient() sees ImpersonationClient + userEmail                   │
│  8. NewClientWithImpersonation() triggers OAuth flow:                       │
│     a. client_credentials → access_token                                    │
│     b. UMA ticket grant → RPT token                                         │
│     c. /user/impersonate → impersonated_token                               │
│  9. VM created with impersonated_token                                      │
└─────────────────────────────────────────────────────────────────────────────┘
                    │
                    ▼
         VM appears in user's CloudSigma dashboard ✓
```

## Configuration Example

**Kubernetes Secret:**
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: cloudsigma-impersonation
  namespace: capcs-system
type: Opaque
data:
  client-id: a3ViZXJuZXRlc19jbHVzdGVyX3NlcnZpY2U=
  client-secret: <base64-encoded-secret>
```

**Controller Deployment:**
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: capcs-controller-manager
spec:
  template:
    spec:
      containers:
      - name: manager
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
        - name: CLOUDSIGMA_IMPERSONATION_ENABLED
          value: "true"
```

## API Endpoints Reference

| Step | Endpoint | Method | Auth |
|------|----------|--------|------|
| 1 | `{oauth_url}/realms/cloudsigma/protocol/openid-connect/token` | POST | None (client_credentials) |
| 2 | `{oauth_url}/realms/cloudsigma/protocol/openid-connect/token` | POST | Bearer (access_token) |
| 3 | `https://direct.{region}.cloudsigma.com/service_provider/api/v1/user/impersonate` | POST | Bearer (RPT token) |
| 4+ | `https://direct.{region}.cloudsigma.com/api/2.0/*` | * | Bearer (impersonated_token) |

## Success Metrics

1. **Functional:**
   - VMs created appear in user's CloudSigma dashboard
   - User can see billing attributed to their account
   - Cluster operations succeed with impersonation

2. **Performance:**
   - Token cache hit rate > 90%
   - Impersonation adds < 500ms latency to first operation
   - No impact on cached operations

3. **Reliability:**
   - Graceful degradation to global credentials if impersonation fails
   - Automatic token refresh on expiry
   - No data loss on OAuth outages

## Open Questions

1. **Multi-region Support:** Does each region require separate impersonation, or is the token valid across regions?

2. **Token Lifetime:** What is the TTL of impersonated tokens? (Need to verify with CloudSigma)

3. **Resource Cleanup:** When a cluster is deleted, should we verify the user still exists before cleanup?

4. **Shared Resources:** How to handle cluster-level resources (VLANs, IPs) that might be shared across users?

## References

- CloudSigma Public API: https://docs.cloudsigma.com/en/latest/
- Keycloak UMA: https://www.keycloak.org/docs/latest/authorization_services/
- OAuth 2.0 Token Exchange: RFC 8693
