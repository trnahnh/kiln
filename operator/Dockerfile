FROM golang:1.26 AS builder
ENV GOWORK=off
WORKDIR /workspace
COPY audit/ audit/
COPY operator/go.mod operator/go.sum operator/
RUN cd operator && go mod download
COPY operator/ operator/
RUN cd operator && CGO_ENABLED=0 GOOS=linux go build -a -o /workspace/manager ./cmd

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532
ENTRYPOINT ["/manager"]
