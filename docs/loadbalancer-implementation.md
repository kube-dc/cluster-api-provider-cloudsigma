# CloudSigma CCM LoadBalancer Implementation

This document describes the LoadBalancer IP failover implementation for CloudSigma Cloud Controller Manager (CCM).

## Overview

The CloudSigma CCM implements LoadBalancer services using a "floating IP" approach. When a LoadBalancer service is created, the CCM:

1. Discovers available IPs from CloudSigma (static IPs with subscription, dynamic IPs without)
2. Allocates an IP to the service based on annotation (static by default, or dynamic)
3. Attaches the IP to the target node as a NIC via CloudSigma API (for external routing)
4. Creates a privileged pod on the node to configure the IP locally and set up iptables rules
5. Updates the service status with the external IP

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                     External Traffic                             │
│                          │                                       │
│                          ▼                                       │
│                   LoadBalancer IP                                │
│                   (31.171.254.211)                               │
│                          │                                       │
│            CloudSigma routes to NIC                              │
│            (attached via API)                                    │
│                          │                                       │
│                          ▼                                       │
│   ┌──────────────────────────────────────────────────────────┐  │
│   │                   Kubernetes Node                         │  │
│   │  ┌─────────────────────────────────────────────────────┐ │  │
│   │  │ Primary Interface (ens3) with LB IP as secondary    │ │  │
│   │  │                     │                                │ │  │
│   │  │                     ▼                                │ │  │
│   │  │              iptables DNAT                           │ │  │
│   │  │                     │                                │ │  │
│   │  │                     ▼                                │ │  │
│   │  │              Service Endpoint                        │ │  │
│   │  │              (Pod IP:Port)                           │ │  │
│   │  └─────────────────────────────────────────────────────┘ │  │
│   └──────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

### Components

1. **LoadBalancer Controller** - Watches for LoadBalancer services and manages IP allocation
2. **CloudSigma API** - Used for IP discovery AND NIC attachment/detachment
3. **LB IP Config Pod** - Privileged pod that configures IP locally and sets up iptables rules on the node

## How It Works

### 1. IP Discovery

On startup, the CCM queries CloudSigma API and categorizes IPs into two pools:

**Static IPs** (default):
- IPs with a subscription (owned IPs)
- Used by default for LoadBalancer services

**Dynamic IPs**:
- Unassigned IPs without subscription and not attached to any server
- Available for services with `cloudsigma.com/ip-pool: dynamic` annotation

### 2. Service Reconciliation

When a LoadBalancer service is created:
1. CCM checks service annotation `cloudsigma.com/ip-pool` to determine pool type
2. Allocates an available IP from the appropriate pool (static or dynamic)
3. Creates a privileged pod on a healthy node to configure the IP
4. Updates service status with the external IP

### 3. CloudSigma NIC Attachment

Before configuring the IP locally, the CCM attaches the IP to the server as a new NIC via CloudSigma API:

```bash
# The CCM performs this via API:
PUT /api/2.0/servers/{server-uuid}/
{
  "nics": [
    { existing NICs... },
    { "ip_v4_conf": { "conf": "static", "ip": { "uuid": "<lb-ip>" } } }
  ]
}
```

**Important considerations:**
- The full server object must be sent in the PUT request, preserving all fields including `vnc_password`
- Read-only fields (`resource_uri`, `runtime`, `status`, `uuid`, `owner`, `permissions`, `mounted_on`, `grantees`) must be removed before sending
- CloudSigma NIC attachment does NOT support hotplug - the new NIC won't appear in the OS without a VM restart

### 4. Node IP Configuration

The LB IP config pod (`lb-ip-<ip-address>`):
- Runs with `hostNetwork: true` and privileged security context
- Detects the primary network interface (first non-lo, non-cilium interface, typically `ens3`)
- Adds the LoadBalancer IP to the **primary interface** as a secondary IP with /32 netmask
- Configures iptables DNAT rule to forward traffic to the service endpoint (pod IP)
- Configures iptables MASQUERADE for return traffic
- Remains running to maintain the iptables rules

**Why both NIC attachment AND local IP config?**
- **NIC attachment via API**: CloudSigma's network uses this to route external traffic to the node
- **Local IP on interface**: The kernel needs the IP configured locally to accept packets (hotplug workaround)

Without NIC attachment, CloudSigma won't route external traffic to the node.
Without local IP config, the kernel will drop packets destined to the IP.

### 5. State Recovery

On CCM restart:
- Recovers service-to-IP mappings from existing Kubernetes services
- Ensures LB IP config pods exist for all assigned IPs

### 6. IP Tagging

When an IP is allocated to a service, the CCM creates tags in CloudSigma for tracking:
- `cluster:<cluster-name>` - Identifies which cluster is using the IP
- `service:<namespace>-<name>` - Identifies which service is using the IP
- `managed-by:cloudsigma-ccm` - Marks the IP as CCM-managed

When a service is deleted, the IP is removed from these tags.

This allows you to:
- Track which IPs are in use across multiple clusters
- Identify which service is using each IP in the CloudSigma console
- Filter IPs by cluster or service in the CloudSigma UI

## Configuration

### Service Annotations

| Annotation | Description | Values |
|------------|-------------|--------|
| `cloudsigma.com/ip-pool` | Specifies which IP pool to use | `static` (default), `dynamic` |

**Static Pool** (default): Uses owned IPs with CloudSigma subscription. These are dedicated IPs that you own.

**Dynamic Pool**: Uses unassigned IPs that are available in CloudSigma but not attached to any server. Use this for temporary or development workloads.

### CCM Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--disable-lb-ip-pool` | Disable LoadBalancer IP pool functionality | `false` |
| `--user-email` | CloudSigma user email for impersonation | Required |
| `--region` | CloudSigma region | Required |

### Environment Variables

| Variable | Description |
|----------|-------------|
| `CS_OAUTH_URL` | CloudSigma OAuth URL for impersonation |

## iptables Rules

The LB IP config pod creates the following rules:

```bash
# DNAT rule - forward incoming traffic to pod IP
iptables -t nat -I PREROUTING 1 -d <LB_IP> -p tcp --dport <PORT> -j DNAT --to-destination <POD_IP>:<PORT>

# MASQUERADE rule - for return traffic
iptables -t nat -A POSTROUTING -d <POD_IP> -p tcp --dport <PORT> -j MASQUERADE
```

## Direct Pod IP Routing

The implementation uses endpoint IPs (pod IPs) instead of ClusterIP for iptables rules. This:
- Provides direct routing from node to pod
- Bypasses potential ClusterIP routing issues with certain CNIs
- Falls back to ClusterIP if no endpoints are available

## Failover

When a node becomes unhealthy:
1. CCM detects node failure via node controller
2. Detaches IP from failed node via CloudSigma API
3. Attaches IP to a healthy node
4. Creates new LB IP config pod on the healthy node
5. Service continues to work with the same external IP

## Cilium CNI Integration

The LoadBalancer implementation works alongside Cilium CNI. Key considerations:

### Device Configuration
Cilium should be configured with `devices: 'ens3'` to prevent auto-detection issues:
```yaml
# In cilium-config ConfigMap
devices: 'ens3'
```

This ensures Cilium only uses the primary interface and doesn't interfere with LoadBalancer IPs.

### IP Aliases
LoadBalancer IPs are configured as `/32` aliases on `ens3`:
- Cilium sees `ens3` as the device, not individual IPs
- Aliases are NOT registered as Kubernetes node addresses
- Only the primary IP (e.g., `31.171.254.164`) appears in node status
- This separation allows CCM to manage LoadBalancer IPs independently

### Inter-node Traffic
- Cilium handles pod-to-pod traffic via VXLAN tunnel
- LoadBalancer traffic bypasses Cilium (handled by iptables DNAT)
- Both can coexist on the same interface without conflicts

## Known Issues

### CloudSigma External Routing

**Issue**: Some newly purchased static IPs may not be externally routable immediately, even when correctly attached via API.

**Symptoms**:
- NIC attachment succeeds (visible in CloudSigma API)
- IP is configured locally on `ens3` as `/32` alias
- iptables rules are correctly set
- `mtr` traceroute shows "no route to host" at CloudSigma's edge router
- Other static IPs on the same server work correctly

**Example** (observed 2026-02-05):
```
Working IP:     31.171.254.211 → reaches host-211-254-171-31.cloudsigma.net
Not working IP: 31.171.254.252 → stops at CloudSigma router (149.6.62.99)
```

**Verification**:
```bash
# Check NIC attachment (should show IP attached)
curl -H "Authorization: Bearer $TOKEN" \
  "https://next.cloudsigma.com/api/2.0/servers/{uuid}/" | jq '.nics'

# Check local config (should show IP on ens3)
ip addr show ens3 | grep "31.171.254.252"

# Check iptables (should show DNAT rule)
iptables -t nat -L PREROUTING -n | grep "31.171.254.252"

# Check packet count (0 means no external traffic reaching node)
iptables -t nat -L PREROUTING -n -v | grep "31.171.254.252"
```

**Root Cause**: CloudSigma's network infrastructure issue - external routing not propagated for the IP.

**Resolution**: Contact CloudSigma support to verify IP is properly provisioned for external routing.

### Dynamic IPs Not Externally Routable

**Issue**: Dynamic IPs (without subscription) may not be routable from external networks.

**Symptoms**:
- IP works for internal cluster traffic
- External `curl` times out or shows "no route to host"

**Workaround**: Use static IPs (with subscription) for production LoadBalancer services.

## Limitations

- Single IP per service (no multi-IP support)
- TCP traffic only for iptables DNAT rules
- First port in service spec is used for iptables rules
- Requires privileged pods in kube-system namespace
- External routing depends on CloudSigma infrastructure (NIC attachment alone may not be sufficient)

## Troubleshooting

### Check LB IP config pod status
```bash
kubectl get pods -n kube-system -l app=cloudsigma-lb-ip
kubectl logs -n kube-system lb-ip-<ip-with-dashes>
```

### Verify iptables rules
```bash
kubectl exec -n kube-system lb-ip-<ip-with-dashes> -- iptables -t nat -L PREROUTING -n
```

### Check IP assignment
```bash
kubectl logs deployment/csccm-metal | grep -E "Assigned|Recovered|Attached"
```

### Verify service status
```bash
kubectl get svc <service-name> -o jsonpath='{.status.loadBalancer.ingress[0].ip}'
```

## Examples

### Using Static IP Pool (default)

```yaml
apiVersion: v1
kind: Service
metadata:
  name: nginx-lb
spec:
  type: LoadBalancer
  selector:
    app: nginx
  ports:
  - port: 80
    targetPort: 80
```

### Using Dynamic IP Pool

```yaml
apiVersion: v1
kind: Service
metadata:
  name: nginx-lb-dynamic
  annotations:
    cloudsigma.com/ip-pool: "dynamic"
spec:
  type: LoadBalancer
  selector:
    app: nginx
  ports:
  - port: 80
    targetPort: 80
```

After CCM reconciliation:
- Service gets external IP from the specified CloudSigma pool
- IP is attached to a cluster node
- Traffic to external IP is forwarded to nginx pods
