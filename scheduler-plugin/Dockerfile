FROM golang:1.26 AS builder
ENV GOWORK=off
WORKDIR /workspace
COPY audit/ audit/
COPY scheduler-plugin/go.mod scheduler-plugin/go.sum scheduler-plugin/
RUN cd scheduler-plugin && go mod download
COPY scheduler-plugin/ scheduler-plugin/
RUN cd scheduler-plugin && CGO_ENABLED=0 GOOS=linux go build -o /workspace/kiln-scheduler ./cmd/kiln-scheduler

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/kiln-scheduler .
USER 65532:65532
ENTRYPOINT ["/kiln-scheduler"]
