# CloudSigma Networking for CAPCS

## Important: CloudSigma Network Requirements

**CloudSigma VMs MUST have a VLAN to get network connectivity.** There is no way to create VMs with public IPs without using a VLAN.

## Network Configuration Options

### Option 1: VLAN with DHCP (Recommended for Production)

**Requirements:**
- A VLAN in CloudSigma
- DHCP server running on that VLAN

**Configuration:**
```yaml
nics:
  - vlan: "013ee207-5234-4f7b-8dd1-d06050b9fd38"
    ipv4_conf:
      conf: "dhcp"
```

**Setting up DHCP:**
1. Deploy a DHCP server VM on your VLAN
2. Configure it to serve IPs in your desired range
3. Ensure it has NAT/routing configured if you need internet access

### Option 2: VLAN with Static IPs

**Requirements:**
- A VLAN in CloudSigma
- Pre-allocated CloudSigma IP addresses

**Configuration:**
```yaml
nics:
  - vlan: "013ee207-5234-4f7b-8dd1-d06050b9fd38"
    ipv4_conf:
      conf: "static"
      ip:
        uuid: "<cloudsigma-ip-uuid>"
```

**Steps:**
1. Allocate IP addresses from CloudSigma
2. Note the IP UUIDs
3. Specify them in the machine template

### Option 3: No NICs (Not Recommended)

**Result:** VM will be created **without any network interfaces** - no connectivity at all.

```yaml
nics: []  # or omit nics field entirely
```

This is only useful for:
- VMs that don't need network (e.g., offline processing)
- Manual network configuration post-creation

## Current VLAN Status

Your current VLAN: `013ee207-5234-4f7b-8dd1-d06050b9fd38`

**Issue:** This VLAN does not appear to have DHCP configured.

**Solution:** 

**Option A:** Set up DHCP on this VLAN
1. Create a small Ubuntu VM on this VLAN
2. Install and configure dnsmasq or isc-dhcp-server:
   ```bash
   apt-get install dnsmasq
   cat > /etc/dnsmasq.conf <<EOF
   interface=eth0
   dhcp-range=192.168.1.100,192.168.1.200,24h
   dhcp-option=3,192.168.1.1  # Gateway
   dhcp-option=6,8.8.8.8      # DNS
   EOF
   systemctl restart dnsmasq
   ```
3. Configure NAT/routing for internet access

**Option B:** Use static IPs
1. Allocate public IPs from CloudSigma
2. Update machine templates with IP UUIDs
3. VMs will use these static IPs

## For Kubernetes Workers

**Recommendation:** Use VLAN with DHCP

Kubernetes worker nodes need:
- Outbound internet access (for pulling images)
- Access to control plane endpoint (`168.119.17.56:6443`)
- Konnectivity proxy handles inbound connections

A private VLAN with NAT/routing is perfect for this use case.

## Testing Network Configuration

After creating a VM, verify connectivity:

```bash
# SSH into the VM (if you have console access)
# Or use CloudSigma's VNC console

# Check IP address
ip addr show

# Check routing
ip route show

# Test connectivity
ping -c 3 8.8.8.8
ping -c 3 168.119.17.56

# Test DNS
nslookup google.com
```

## Summary

| Method | Network | Internet | Pros | Cons |
|--------|---------|----------|------|------|
| VLAN + DHCP | ✅ | ✅ (with NAT) | Easy, automatic | Requires DHCP server |
| VLAN + Static | ✅ | ✅ (with routing) | Predictable IPs | Manual IP management |
| No NICs | ❌ | ❌ | Simple config | No connectivity |

**For Kubernetes workers, use VLAN + DHCP with NAT.**
