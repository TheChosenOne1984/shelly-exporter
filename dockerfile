# syntax=docker/dockerfile:1

############################
# Stage 1: builder (Go 1.25.3)
############################
ARG GO_VERSION=1.25.3
FROM golang:${GO_VERSION}-alpine AS builder

WORKDIR /src
# Optional: git wird häufig für go mod benötigt (private deps etc.)
RUN apk add --no-cache git

# Modul-Cache
COPY go.mod go.sum ./
RUN go mod download

# Source
COPY . .

# Statisches Binary für Distroless bauen
ENV CGO_ENABLED=0
# (Optional) GOTOOLCHAIN lokal halten, damit genau diese Version verwendet wird
ENV GOTOOLCHAIN=local
RUN go build -trimpath -ldflags="-s -w" -o /out/shelly-exporter

############################
# Stage 2: runtime (Distroless, nonroot)
############################
FROM gcr.io/distroless/static:nonroot

# App-Port
EXPOSE 9905

# Arbeitsverzeichnis & Konfigurations-Volume
WORKDIR /app
VOLUME ["/app/config"]  # Konfig bleibt außerhalb des Images

# Binary kopieren
COPY --from=builder /out/shelly-exporter /app/shelly-exporter

# Ohne root laufen
USER nonroot:nonroot

# Standard-Start (Config wird zur Laufzeit gemountet)
ENTRYPOINT ["/app/shelly-exporter"]
CMD ["-config", "/app/config/config.yaml"]
