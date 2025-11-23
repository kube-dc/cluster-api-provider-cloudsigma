# CloudSigma Cloud Controller Manager - Deployment Guide

## Overview

The CloudSigma CCM implements the Kubernetes cloud-provider interface for CloudSigma infrastructure, enabling:
- Automatic node registration with CloudSigma metadata
- LoadBalancer service type support
- Node lifecycle management

## Architecture

```
Tenant Cluster (Kamaji)
  ├── API Server (--cloud-provider=external)
  ├── Controller Manager (--cloud-provider=external)
  └── Worker Nodes
      ├── kubelet (--cloud-provider=external)
      └── CCM DaemonSet
          └── cloudsigma-cloud-controller-manager
              ├── Node Controller
              ├── Service Controller (LoadBalancer)
              └── Route Controller (optional)
```

## Prerequisites

- Tenant cluster created via KdcCluster
- CloudSigma API credentials
- Workers provisioned with CAPCS provider
- Kubelet configured with `--cloud-provider=external`

## Installation

### 1. Create Credentials Secret

```bash
kubectl create secret generic cloudsigma-credentials \
  --namespace=kube-system \
  --from-literal=username='your-email@example.com' \
  --from-literal=password='your-api-password' \
  --from-literal=region='zrh'
```

### 2. Create RBAC Resources

```yaml
# cloudsigma-ccm-rbac.yaml
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: cloud-controller-manager
  namespace: kube-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: system:cloud-controller-manager
rules:
  - apiGroups:
      - ""
    resources:
      - events
    verbs:
      - create
      - patch
      - update
  - apiGroups:
      - ""
    resources:
      - nodes
    verbs:
      - get
      - list
      - watch
      - update
      - patch
  - apiGroups:
      - ""
    resources:
      - nodes/status
    verbs:
      - patch
  - apiGroups:
      - ""
    resources:
      - services
    verbs:
      - get
      - list
      - watch
      - patch
      - update
  - apiGroups:
      - ""
    resources:
      - services/status
    verbs:
      - patch
      - update
  - apiGroups:
      - ""
    resources:
      - serviceaccounts
    verbs:
      - create
      - get
      - list
      - watch
      - update
  - apiGroups:
      - ""
    resources:
      - persistentvolumes
    verbs:
      - get
      - list
      - watch
      - update
  - apiGroups:
      - ""
    resources:
      - endpoints
    verbs:
      - create
      - get
      - list
      - watch
      - update
  - apiGroups:
      - coordination.k8s.io
    resources:
      - leases
    verbs:
      - get
      - create
      - update
  - apiGroups:
      - ""
    resources:
      - secrets
    verbs:
      - get
      - list
      - watch
---
kind: ClusterRoleBinding
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: system:cloud-controller-manager
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: system:cloud-controller-manager
subjects:
  - kind: ServiceAccount
    name: cloud-controller-manager
    namespace: kube-system
```

Apply RBAC:
```bash
kubectl apply -f cloudsigma-ccm-rbac.yaml
```

### 3. Deploy CCM DaemonSet

```yaml
# cloudsigma-ccm-daemonset.yaml
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: cloudsigma-cloud-controller-manager
  namespace: kube-system
  labels:
    app: cloudsigma-cloud-controller-manager
spec:
  selector:
    matchLabels:
      app: cloudsigma-cloud-controller-manager
  template:
    metadata:
      labels:
        app: cloudsigma-cloud-controller-manager
    spec:
      serviceAccountName: cloud-controller-manager
      hostNetwork: true
      nodeSelector:
        node-role.kubernetes.io/control-plane: ""
      tolerations:
        - key: node.cloudprovider.kubernetes.io/uninitialized
          value: "true"
          effect: NoSchedule
        - key: node-role.kubernetes.io/control-plane
          effect: NoSchedule
        - key: node-role.kubernetes.io/master
          effect: NoSchedule
      containers:
        - name: cloudsigma-cloud-controller-manager
          image: ghcr.io/shalb/cloudsigma-cloud-controller-manager:v1.0.0
          imagePullPolicy: IfNotPresent
          command:
            - /bin/cloudsigma-cloud-controller-manager
            - --cloud-provider=cloudsigma
            - --leader-elect=true
            - --use-service-account-credentials=true
            - --allocate-node-cidrs=false
            - --configure-cloud-routes=false
            - --bind-address=127.0.0.1
            - --v=2
          env:
            - name: CLOUDSIGMA_API_ENDPOINT
              value: "https://zrh.cloudsigma.com/api/2.0"
            - name: CLOUDSIGMA_USERNAME
              valueFrom:
                secretKeyRef:
                  name: cloudsigma-credentials
                  key: username
            - name: CLOUDSIGMA_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: cloudsigma-credentials
                  key: password
            - name: CLOUDSIGMA_REGION
              valueFrom:
                secretKeyRef:
                  name: cloudsigma-credentials
                  key: region
          resources:
            requests:
              cpu: 100m
              memory: 50Mi
            limits:
              cpu: 200m
              memory: 100Mi
          livenessProbe:
            httpGet:
              path: /healthz
              port: 10258
              scheme: HTTPS
            initialDelaySeconds: 20
            periodSeconds: 30
            timeoutSeconds: 5
```

Apply DaemonSet:
```bash
kubectl apply -f cloudsigma-ccm-daemonset.yaml
```

### 4. Verify Installation

```bash
# Check CCM pods
kubectl get pods -n kube-system -l app=cloudsigma-cloud-controller-manager

# Check CCM logs
kubectl logs -n kube-system -l app=cloudsigma-cloud-controller-manager --tail=50

# Verify node providerID is set
kubectl get nodes -o custom-columns=NAME:.metadata.name,PROVIDER-ID:.spec.providerID

# Expected output:
# NAME           PROVIDER-ID
# worker-0       cloudsigma://server-uuid-1
# worker-1       cloudsigma://server-uuid-2
```

## Features

### 1. Node Controller

**Responsibilities:**
- Initialize nodes with providerID
- Add CloudSigma-specific labels
- Update node addresses

**Node Labels Added:**
```yaml
topology.kubernetes.io/region: zrh
topology.kubernetes.io/zone: zrh
node.kubernetes.io/instance-type: cpu-2000-mem-4096
beta.kubernetes.io/instance-type: cpu-2000-mem-4096
failure-domain.beta.kubernetes.io/region: zrh
```

**Example:**
```bash
kubectl get node worker-0 -o yaml
```

```yaml
apiVersion: v1
kind: Node
metadata:
  labels:
    topology.kubernetes.io/region: zrh
    node.kubernetes.io/instance-type: cpu-2000-mem-4096
spec:
  providerID: cloudsigma://12345678-abcd-efgh-ijkl-1234567890ab
status:
  addresses:
    - address: 10.220.0.5
      type: InternalIP
    - address: 185.12.34.56
      type: ExternalIP
```

### 2. Service Controller (LoadBalancer)

**Enable LoadBalancer services:**

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-app
  namespace: default
spec:
  type: LoadBalancer
  selector:
    app: my-app
  ports:
    - port: 80
      targetPort: 8080
      protocol: TCP
```

**How it works:**
1. CCM detects Service with `type: LoadBalancer`
2. Creates CloudSigma load balancer via API
3. Configures backend servers (worker nodes)
4. Updates Service status with external IP

```bash
kubectl get svc my-app

# Output:
# NAME     TYPE           CLUSTER-IP      EXTERNAL-IP     PORT(S)        AGE
# my-app   LoadBalancer   10.96.123.45    185.12.34.100   80:30123/TCP   2m
```

### 3. Route Controller (Optional)

Manages pod network routes. Typically not needed with CNI plugins.

Can be disabled with: `--configure-cloud-routes=false`

## Configuration

### Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `CLOUDSIGMA_API_ENDPOINT` | Yes | - | CloudSigma API URL |
| `CLOUDSIGMA_USERNAME` | Yes | - | API username (email) |
| `CLOUDSIGMA_PASSWORD` | Yes | - | API password |
| `CLOUDSIGMA_REGION` | Yes | - | Region (zrh, lvs, sjc, tyo) |

### Command-line Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--cloud-provider` | - | Must be "cloudsigma" |
| `--leader-elect` | true | Enable leader election |
| `--use-service-account-credentials` | true | Use SA tokens |
| `--allocate-node-cidrs` | false | Allocate CIDRs to nodes |
| `--configure-cloud-routes` | false | Configure routes |
| `--bind-address` | 0.0.0.0 | Metrics bind address |
| `--v` | 2 | Log verbosity (0-5) |

## Troubleshooting

### Nodes not getting providerID

**Symptoms:**
```bash
kubectl get nodes -o custom-columns=NAME:.metadata.name,PROVIDER-ID:.spec.providerID

# Output shows empty providerID:
# NAME       PROVIDER-ID
# worker-0   <none>
```

**Solution:**
1. Check CCM logs:
```bash
kubectl logs -n kube-system -l app=cloudsigma-cloud-controller-manager
```

2. Verify credentials:
```bash
kubectl get secret cloudsigma-credentials -n kube-system -o yaml
```

3. Check kubelet is using external cloud provider:
```bash
# On worker node
systemctl status kubelet
# Should see: --cloud-provider=external
```

### LoadBalancer stuck in Pending

**Symptoms:**
```bash
kubectl get svc

# Output:
# NAME     TYPE           CLUSTER-IP      EXTERNAL-IP   PORT(S)        AGE
# my-app   LoadBalancer   10.96.123.45    <pending>     80:30123/TCP   5m
```

**Solution:**
1. Check CCM logs for LB creation errors
2. Verify CloudSigma API credentials have LB permissions
3. Check CloudSigma quota limits

### Nodes showing NotReady

**Symptoms:**
```bash
kubectl get nodes

# Output:
# NAME       STATUS     ROLES    AGE   VERSION
# worker-0   NotReady   <none>   5m    v1.34.0
```

**Solution:**
1. CCM might be blocking node initialization
2. Check node conditions:
```bash
kubectl describe node worker-0
```

3. Look for taint:
```yaml
taints:
  - effect: NoSchedule
    key: node.cloudprovider.kubernetes.io/uninitialized
    value: "true"
```

4. CCM should remove this taint once initialized. Check CCM logs.

## Monitoring

### Metrics

CCM exposes Prometheus metrics on port 10258:

```bash
# Port-forward to access metrics
kubectl port-forward -n kube-system \
  $(kubectl get pod -n kube-system -l app=cloudsigma-cloud-controller-manager -o name | head -1) \
  10258:10258

# Access metrics
curl http://localhost:10258/metrics
```

**Key Metrics:**
- `cloudprovider_cloudsigma_api_requests_total` - API request count
- `cloudprovider_cloudsigma_api_request_duration_seconds` - API latency
- `cloudprovider_cloudsigma_api_request_errors_total` - API errors

### Health Checks

```bash
# Liveness probe
curl -k https://localhost:10258/healthz

# Expected: ok
```

## Upgrade

### Rolling Upgrade

```bash
# Update image version
kubectl set image daemonset/cloudsigma-cloud-controller-manager \
  -n kube-system \
  cloudsigma-cloud-controller-manager=ghcr.io/shalb/cloudsigma-cloud-controller-manager:v1.1.0

# Monitor rollout
kubectl rollout status daemonset/cloudsigma-cloud-controller-manager -n kube-system
```

### Rollback

```bash
# Rollback to previous version
kubectl rollout undo daemonset/cloudsigma-cloud-controller-manager -n kube-system

# Check rollout history
kubectl rollout history daemonset/cloudsigma-cloud-controller-manager -n kube-system
```

## Uninstall

```bash
# Delete DaemonSet
kubectl delete daemonset cloudsigma-cloud-controller-manager -n kube-system

# Delete RBAC
kubectl delete clusterrolebinding system:cloud-controller-manager
kubectl delete clusterrole system:cloud-controller-manager
kubectl delete serviceaccount cloud-controller-manager -n kube-system

# Delete credentials (optional)
kubectl delete secret cloudsigma-credentials -n kube-system
```

## Development

### Building from Source

```bash
git clone https://github.com/shalb/cloudsigma-cloud-controller-manager
cd cloudsigma-cloud-controller-manager

# Build binary
make build

# Build Docker image
make docker-build IMG=myrepo/cloudsigma-ccm:dev

# Push image
make docker-push IMG=myrepo/cloudsigma-ccm:dev
```

### Testing Locally

```bash
# Run CCM locally (connects to current kubeconfig context)
./bin/cloudsigma-cloud-controller-manager \
  --cloud-provider=cloudsigma \
  --leader-elect=false \
  --kubeconfig=$HOME/.kube/config \
  --v=4
```

## References

- [Kubernetes Cloud Controller Manager](https://kubernetes.io/docs/concepts/architecture/cloud-controller/)
- [CloudSigma API Documentation](https://docs.cloudsigma.com/en/latest/)
- [CAPI Cloud Provider Integration](https://cluster-api.sigs.k8s.io/developer/providers/cloud-provider-integration)
