# Build stage
FROM golang:1.23 AS builder

WORKDIR /workspace

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY api/ api/
COPY controllers/ controllers/
COPY pkg/ pkg/
COPY csi/ csi/

# Build controller binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o /workspace/csi-controller ./csi/cmd/controller/main.go

# Build node binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o /workspace/csi-node ./csi/cmd/node/main.go

# Controller image
FROM gcr.io/distroless/static:nonroot AS controller
WORKDIR /
COPY --from=builder /workspace/csi-controller /csi-controller
USER 65532:65532
ENTRYPOINT ["/csi-controller"]

# Node image - needs tools for mounting
FROM debian:bookworm-slim AS node

RUN apt-get update && apt-get install -y --no-install-recommends \
    e2fsprogs \
    xfsprogs \
    udev \
    util-linux \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /workspace/csi-node /csi-node
ENTRYPOINT ["/csi-node"]
