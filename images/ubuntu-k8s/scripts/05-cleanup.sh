#!/bin/bash
set -euxo pipefail

echo "==> Cleaning up"

# Clean apt cache
sudo apt-get clean
sudo apt-get autoremove -y

# Remove machine-id to allow new ID on clone
sudo truncate -s 0 /etc/machine-id
sudo rm -f /var/lib/dbus/machine-id

# Remove hostname (will be set by cloud-init)
sudo truncate -s 0 /etc/hostname
sudo sed -i '/^127.0.1.1/d' /etc/hosts

# Remove SSH host keys (will be regenerated on first boot)
sudo rm -f /etc/ssh/ssh_host_*

# Remove netplan machine-id
sudo rm -f /etc/netplan/*.yaml.bak
sudo find /etc/netplan -name "*~" -delete

# Clean shell history
cat /dev/null > ~/.bash_history || true
sudo sh -c 'cat /dev/null > /root/.bash_history' || true
history -c

# Ensure cloudsigma user password is set and unlocked
echo "==> Configuring cloudsigma user for final image"
echo "cloudsigma:Cloud2025" | sudo chpasswd
sudo passwd -u cloudsigma 2>/dev/null || true
# Remove password expiry to allow login without forced password change
sudo chage -I -1 -m 0 -M 99999 -E -1 cloudsigma 2>/dev/null || true

# Verify user is configured correctly
echo "==> Verifying cloudsigma user configuration"
sudo passwd -S cloudsigma || echo "Warning: Could not check password status"
sudo chage -l cloudsigma || echo "Warning: Could not check password aging"

# Re-enable SSH password authentication (might be disabled during build)
sudo sed -i 's/^PasswordAuthentication no/PasswordAuthentication yes/' /etc/ssh/sshd_config || true
sudo sed -i 's/^#PasswordAuthentication yes/PasswordAuthentication yes/' /etc/ssh/sshd_config || true

# Create systemd service to fetch and apply CloudSigma metadata
# This works around cloud-init datasource issues with CAPI metadata injection
echo "==> Creating CloudSigma metadata bootstrap service"
sudo tee /usr/local/bin/cloudsigma-bootstrap.sh > /dev/null << 'BOOTSTRAP'
#!/bin/bash
# CloudSigma Bootstrap Service
# Reads metadata from CloudSigma via Cepko and executes cloud-init bootstrap

set -e

LOG_FILE="/var/log/cloudsigma-bootstrap.log"
exec > "${LOG_FILE}" 2>&1

echo "[$(date)] CloudSigma Bootstrap starting..."

# Step 1: Disable swap (required for kubelet)
echo "[$(date)] Disabling swap..."
swapoff -a 2>/dev/null || true
sed -i '/ swap / s/^\(.*\)$/#\1/g' /etc/fstab 2>/dev/null || true
echo "[$(date)] Swap disabled"

# Step 2: Read metadata from CloudSigma using Cepko
echo "[$(date)] Reading CloudSigma metadata via Cepko..."
python3 << 'PYEOF'
import sys, base64, yaml, subprocess, os, re
sys.path.insert(0, '/usr/lib/python3/dist-packages')

from cloudinit.sources.helpers.cloudsigma import Cepko

try:
    # Read server context from CloudSigma
    cepko = Cepko()
    result = cepko.all()
    server_context = result.result
    
    # Get metadata
    meta = server_context.get('meta', {})
    userdata_b64 = meta.get('cloudinit-user-data', '')
    
    if not userdata_b64:
        print(f"[{subprocess.check_output(['date']).decode().strip()}] No cloudinit-user-data found")
        sys.exit(0)
    
    # Decode base64 user-data
    userdata = base64.b64decode(userdata_b64).decode('utf-8')
    print(f"[{subprocess.check_output(['date']).decode().strip()}] User-data decoded successfully")
    
    # Get instance metadata for template rendering
    instance_id = server_context.get('uuid', '')
    hostname = server_context.get('name', '')
    
    # Render Jinja templates in user-data
    print(f"[{subprocess.check_output(['date']).decode().strip()}] Rendering templates (instance_id={instance_id}, hostname={hostname})")
    userdata = userdata.replace('{{ ds.meta_data.instance_id }}', instance_id)
    userdata = userdata.replace('{{ ds.meta_data.local_hostname }}', hostname)
    
    # Parse YAML
    config = yaml.safe_load(userdata)
    
    # Write files
    if 'write_files' in config:
        print(f"[{subprocess.check_output(['date']).decode().strip()}] Writing files...")
        for file_entry in config['write_files']:
            path = file_entry['path']
            content = file_entry['content']
            owner = file_entry.get('owner', 'root:root')
            perms = file_entry.get('permissions', '0644')
            
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, 'w') as f:
                f.write(content)
            os.chmod(path, int(perms, 8))
            print(f"  Created {path}")
    
    # Execute runcmd
    if 'runcmd' in config:
        print(f"[{subprocess.check_output(['date']).decode().strip()}] Executing runcmd...")
        for cmd in config['runcmd']:
            print(f"  {cmd}")
            result = subprocess.run(cmd, shell=True, capture_output=True, text=True)
            if result.returncode == 0:
                print(f"  Success")
                if result.stdout:
                    for line in result.stdout.strip().split('\n')[:5]:
                        print(f"    {line}")
            else:
                print(f"  Failed with exit code {result.returncode}")
                if result.stderr:
                    for line in result.stderr.strip().split('\n')[:10]:
                        print(f"    {line}")
                sys.exit(1)
    
    print(f"[{subprocess.check_output(['date']).decode().strip()}] Bootstrap completed successfully!")
    
except Exception as e:
    print(f"[{subprocess.check_output(['date']).decode().strip()}] ERROR: {e}")
    import traceback
    traceback.print_exc()
    sys.exit(1)
PYEOF

EXIT_CODE=$?
echo "[$(date)] Bootstrap service finished with exit code ${EXIT_CODE}"
exit ${EXIT_CODE}
BOOTSTRAP

sudo chmod +x /usr/local/bin/cloudsigma-bootstrap.sh

# Create systemd service
sudo tee /etc/systemd/system/cloudsigma-bootstrap.service > /dev/null << 'SERVICE'
[Unit]
Description=CloudSigma Metadata Bootstrap
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/cloudsigma-bootstrap.sh
RemainAfterExit=yes
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
SERVICE

sudo systemctl enable cloudsigma-bootstrap.service

# Keep cloud-init configuration but it won't block boot
echo "datasource_list: [ NoCloud, CloudSigma ]" | sudo tee /etc/cloud/cloud.cfg.d/99_datasource.cfg

# Clean cloud-init (but preserve users)
sudo cloud-init clean --logs --seed

# Clean logs
sudo find /var/log -type f -exec truncate -s 0 {} \;

# Remove temporary files
sudo rm -rf /tmp/*
sudo rm -rf /var/tmp/*

# Zero out free space to improve compression (optional, can be slow)
# Uncomment if you want better compression but longer build time
# echo "Zeroing free space (this may take a while)..."
# sudo dd if=/dev/zero of=/EMPTY bs=1M || true
# sudo rm -f /EMPTY

sync

echo "==> Cleanup complete"
