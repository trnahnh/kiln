FROM golang:1.26 AS builder
ENV GOWORK=off
WORKDIR /workspace
COPY slo/ slo/
COPY chaos/go.mod chaos/go.sum chaos/
RUN cd chaos && go mod download
COPY chaos/ chaos/
RUN cd chaos && CGO_ENABLED=0 GOOS=linux go build -o /workspace/kiln-chaos ./cmd

# The agent runs tc and iptables inside target pods' network namespaces and nsenter to
# get there; the controller uses none of it and drops every capability in its manifest.
FROM alpine:3.22
RUN apk add --no-cache iproute2 iptables util-linux
COPY --from=builder /workspace/kiln-chaos /kiln-chaos
ENTRYPOINT ["/kiln-chaos"]
