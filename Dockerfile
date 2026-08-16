FROM --platform=$BUILDPLATFORM golang:1.26.2 AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-w -extldflags '-static'" \
    -o mcp-capi .

FROM gsoci.azurecr.io/giantswarm/alpine:3.20.3-giantswarm AS certs
FROM bitnami/kubectl:latest AS kubectl
FROM alpine/helm:latest AS helm
FROM scratch

COPY --from=certs /etc/passwd /etc/passwd
COPY --from=certs /etc/group /etc/group
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=certs --chmod=1777 /tmp /tmp
COPY --from=kubectl --chmod=0755 /opt/bitnami/kubectl/bin/kubectl /usr/local/bin/kubectl
COPY --from=helm --chmod=0755 /usr/bin/helm /usr/local/bin/helm

COPY --from=builder /app/mcp-capi /mcp-capi
USER giantswarm

# The image has no home directory for the giantswarm user, and helm always
# needs somewhere writable for its repository config/cache/plugins even for
# operations that don't touch a repo directly. Point it at /tmp, which is
# copied in above with sticky world-writable permissions.
ENV HELM_CACHE_HOME=/tmp/helm/cache \
    HELM_CONFIG_HOME=/tmp/helm/config \
    HELM_DATA_HOME=/tmp/helm/data

ENTRYPOINT ["/mcp-capi"]
