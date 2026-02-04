# Controller Enhancements for Impersonation Issues

## Root Cause Analysis

Based on the logs and code review, the issue has **two contributing factors**:

### Factor 1: Old InstanceID in Status (Controller Bug)

The `CloudSigmaMachine.Status.InstanceID` was populated with an **old VM UUID** (`feaf4d9d...`) before the new creation attempt. When reconcile runs:

```
Line 232: if cloudSigmaMachine.Status.InstanceID != "" {
Line 236:     server, err = cloudClient.GetServer(ctx, cloudSigmaMachine.Status.InstanceID)
              // ↑ This tries to access the OLD VM, which returns 403
```

The old UUID could have been set by:
1. A previous reconcile that created a VM but failed to update status atomically
2. Manual editing of the resource
3. A race condition with another reconcile

### Factor 2: No 403 Error Handling (Missing Feature)

The `GetServer` function treats 403 the same as other errors:

```go
// servers.go:182-195
func (c *Client) GetServer(ctx context.Context, uuid string) (*cloudsigma.Server, error) {
    server, resp, err := c.sdk.Servers.Get(ctx, uuid)
    if err != nil {
        if resp != nil && resp.StatusCode == 404 {
            return nil, nil // Only 404 is handled!
        }
        return nil, fmt.Errorf("failed to get server: %w", err)  // 403 returns error
    }
    return server, nil
}
```

**403 means**: "Server exists, but you don't have permission to access it"

This is different from 404 ("Server doesn't exist").

---

## Proposed Enhancements

### Enhancement 1: Handle 403 Errors in GetServer

Add special handling for 403 to indicate "permission denied":

```go
// servers.go - Enhanced GetServer
func (c *Client) GetServer(ctx context.Context, uuid string) (*cloudsigma.Server, error) {
    klog.V(4).Infof("Getting server: %s (user: %s)", uuid, c.impersonatedUser)

    server, resp, err := c.sdk.Servers.Get(ctx, uuid)
    if err != nil {
        if resp != nil {
            switch resp.StatusCode {
            case 404:
                klog.V(4).Infof("Server not found: %s", uuid)
                return nil, nil
            case 403:
                klog.Warningf("Permission denied for server %s (impersonated user: %s) - server may be owned by different user",
                    uuid, c.impersonatedUser)
                return nil, &PermissionDeniedError{
                    UUID:       uuid,
                    StatusCode: 403,
                    User:       c.impersonatedUser,
                    Err:        err,
                }
            }
        }
        return nil, fmt.Errorf("failed to get server: %w", err)
    }

    return server, nil
}

// New error type for permission denied
type PermissionDeniedError struct {
    UUID       string
    StatusCode int
    User       string
    Err        error
}

func (e *PermissionDeniedError) Error() string {
    return fmt.Sprintf("permission denied: user %s cannot access server %s (HTTP %d): %v",
        e.User, e.UUID, e.StatusCode, e.Err)
}

func IsPermissionDeniedError(err error) bool {
    var pde *PermissionDeniedError
    return errors.As(err, &pde)
}
```

### Enhancement 2: Handle Permission Denied in Controller

In `reconcileNormal`, handle 403 specially:

```go
// cloudsigmamachine_controller.go:232-250 - Enhanced
if cloudSigmaMachine.Status.InstanceID != "" {
    log.V(4).Info("Checking existing server", "instanceID", cloudSigmaMachine.Status.InstanceID)

    server, err = cloudClient.GetServer(ctx, cloudSigmaMachine.Status.InstanceID)
    if err != nil {
        // Check if this is a permission denied error
        if cloud.IsPermissionDeniedError(err) {
            log.Error(err, "Cannot access server - likely owned by different user or orphaned",
                "instanceID", cloudSigmaMachine.Status.InstanceID,
                "expectedUser", cloudClient.ImpersonatedUser())
            
            // Try to find a server by name/metadata that we CAN access
            existingServer, findErr := cloudClient.FindServerByNameOrMeta(ctx, cloudSigmaMachine.Name, string(cloudSigmaMachine.UID))
            if findErr == nil && existingServer != nil {
                log.Info("Found accessible server with matching name/metadata, updating status",
                    "oldInstanceID", cloudSigmaMachine.Status.InstanceID,
                    "newInstanceID", existingServer.UUID)
                cloudSigmaMachine.Status.InstanceID = existingServer.UUID
                cloudSigmaMachine.Status.InstanceState = existingServer.Status
                if updateErr := r.Status().Update(ctx, cloudSigmaMachine); updateErr != nil {
                    return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
                }
                server = existingServer
            } else {
                // No accessible server found - clear the orphaned instance ID
                log.Info("Clearing orphaned instance ID - will trigger recreation",
                    "orphanedInstanceID", cloudSigmaMachine.Status.InstanceID)
                cloudSigmaMachine.Status.InstanceID = ""
                cloudSigmaMachine.Status.InstanceState = ""
                if updateErr := r.Status().Update(ctx, cloudSigmaMachine); updateErr != nil {
                    log.V(4).Info("Failed to clear orphaned status", "error", updateErr)
                }
                // Requeue to trigger creation
                return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
            }
        } else {
            log.Error(err, "Failed to get server")
            return ctrl.Result{}, errors.Wrap(err, "failed to get server")
        }
    }
    // ... rest of the function
}
```

### Enhancement 3: Better Logging Throughout

Add detailed logging for debugging:

```go
// In getCloudClient - log more details
if r.ImpersonationClient != nil && userEmail != "" {
    log.Info("Using impersonation mode", 
        "userEmail", userEmail, 
        "region", region,
        "machineName", cloudSigmaMachine.Name,
        "currentInstanceID", cloudSigmaMachine.Status.InstanceID)
    return cloud.NewClientWithImpersonation(ctx, r.ImpersonationClient, userEmail, region)
}

// In CreateServer - log the impersonated user
klog.Infof("==> CreateServer called for: %s (CPU: %d MHz, Memory: %d MB, Disks: %d, ImpersonatedUser: %s)",
    spec.Name, spec.CPU, spec.Memory, len(spec.Disks), c.impersonatedUser)

// After server creation - log ownership info
klog.Infof("Server created successfully: %s (UUID: %s, Owner: %s)", 
    createdServer.Name, createdServer.UUID, c.impersonatedUser)
```

### Enhancement 4: Atomic Status Update with Retry

Improve the status update logic to handle conflicts better:

```go
// After server creation (lines 324-333)
// Use retry with fresh fetch to prevent conflicts
err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
    // Re-fetch the latest version
    latest := &infrav1.CloudSigmaMachine{}
    if err := r.Get(ctx, client.ObjectKeyFromObject(cloudSigmaMachine), latest); err != nil {
        return err
    }
    
    // Check if someone else already set the instance ID
    if latest.Status.InstanceID != "" && latest.Status.InstanceID != server.UUID {
        log.Info("Instance ID already set by another reconcile, verifying",
            "existingInstanceID", latest.Status.InstanceID,
            "ourInstanceID", server.UUID)
        // This is a potential duplicate - log for investigation
        log.Error(nil, "POTENTIAL DUPLICATE VM CREATED",
            "existingInstanceID", latest.Status.InstanceID,
            "newlyCreatedInstanceID", server.UUID,
            "machineName", cloudSigmaMachine.Name)
    }
    
    latest.Status.InstanceID = server.UUID
    latest.Status.InstanceState = server.Status
    return r.Status().Update(ctx, latest)
})
if err != nil {
    log.Error(err, "Failed to update status after retries", "instanceID", server.UUID)
    return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}
```

### Enhancement 5: Add Validation Before Creation

Before creating a new server, verify no orphaned servers exist:

```go
// Before creating server (around line 283)
log.Info("No existing server found in status, checking for orphans", 
    "name", cloudSigmaMachine.Name, 
    "machineUID", machineUID)

// Check if server already exists by name or metadata
existingServer, err := cloudClient.FindServerByNameOrMeta(ctx, cloudSigmaMachine.Name, machineUID)
if err != nil {
    // If we can't list servers (e.g., 403 on list), log and proceed carefully
    log.Error(err, "Failed to check for existing server - proceeding with caution")
    // Consider whether to proceed or fail
}

if existingServer != nil {
    log.Info("Found existing server matching name/metadata",
        "instanceID", existingServer.UUID,
        "name", existingServer.Name,
        "status", existingServer.Status)
    // Update status and don't create duplicate
    cloudSigmaMachine.Status.InstanceID = existingServer.UUID
    // ...
}
```

---

## Summary of Changes

| File | Change | Purpose |
|------|--------|---------|
| `pkg/cloud/servers.go` | Add `PermissionDeniedError` type | Distinguish 403 from other errors |
| `pkg/cloud/servers.go` | Handle 403 in `GetServer` | Return specific error type |
| `pkg/cloud/servers.go` | Add user logging in all methods | Traceability |
| `controllers/cloudsigmamachine_controller.go` | Handle 403 with self-healing | Auto-recover from orphaned VMs |
| `controllers/cloudsigmamachine_controller.go` | Add retry logic for status update | Prevent race conditions |
| `controllers/cloudsigmamachine_controller.go` | Enhanced logging | Better debugging |

---

## Implementation Priority

1. **High**: Handle 403 errors (Enhancement 1 & 2) - Prevents infinite retry loops
2. **High**: Atomic status update (Enhancement 4) - Prevents duplicate VMs
3. **Medium**: Better logging (Enhancement 3) - Easier debugging
4. **Low**: Pre-creation validation (Enhancement 5) - Extra safety

---

## Test Scenarios

After implementing these changes, test:

1. Create a VM, manually change its owner in CloudSigma → Controller should detect 403 and self-heal
2. Create a VM, delete the K8s resource during creation → No orphaned VMs
3. Concurrent reconciles → No duplicate VMs
4. Network failures during status update → Proper retry and recovery
