FROM golang:1.26 AS builder
ENV GOWORK=off
WORKDIR /workspace
COPY slo/ slo/
COPY audit/ audit/
COPY delivery-controller/go.mod delivery-controller/go.sum delivery-controller/
RUN cd delivery-controller && go mod download
COPY delivery-controller/ delivery-controller/
RUN cd delivery-controller && CGO_ENABLED=0 GOOS=linux go build -o /workspace/kiln-delivery-controller ./cmd

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/kiln-delivery-controller .
USER 65532:65532
ENTRYPOINT ["/kiln-delivery-controller"]
