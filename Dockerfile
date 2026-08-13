# The Go binary is built by CircleCI (architect/go-build) and attached to the
# build context as <binary>-<os>-<arch>; this image only assembles the runtime.
# For a local build, produce the binary first:
#   CGO_ENABLED=0 go build -o mcp-capi-linux-amd64 .
FROM gsoci.azurecr.io/giantswarm/alpine:3.20.3-giantswarm AS certs
FROM bitnami/kubectl:latest AS kubectl
FROM scratch

COPY --from=certs /etc/passwd /etc/passwd
COPY --from=certs /etc/group /etc/group
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=certs --chmod=1777 /tmp /tmp
COPY --from=kubectl --chmod=0755 /opt/bitnami/kubectl/bin/kubectl /usr/local/bin/kubectl

ARG TARGETOS
ARG TARGETARCH
COPY mcp-capi-${TARGETOS}-${TARGETARCH} /mcp-capi
USER giantswarm

ENTRYPOINT ["/mcp-capi"]
