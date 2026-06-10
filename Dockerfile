# syntax=docker/dockerfile:1

############################
# Stage 1: builder
############################
ARG GO_VERSION=1.26.4
FROM golang:${GO_VERSION}-alpine AS builder

WORKDIR /src

RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ENV CGO_ENABLED=0
ENV GOTOOLCHAIN=local
RUN go build -trimpath -ldflags="-s -w" -o /out/shelly-exporter

############################
# Stage 2: runtime (distroless, non-root)
############################
FROM gcr.io/distroless/static:nonroot

EXPOSE 9905

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD ["/app/shelly-exporter", "-health-check", "-config", "/app/config/config.yaml"]

WORKDIR /app

COPY --from=builder /out/shelly-exporter /app/shelly-exporter

USER nonroot:nonroot

ENTRYPOINT ["/app/shelly-exporter"]
CMD ["-config", "/app/config/config.yaml"]
