# Shelly Exporter

The **Shelly Exporter** is a lightweight [Prometheus](https://prometheus.io/) exporter written in Go that collects metrics from one or more [Shelly devices](https://shelly.cloud/) via their HTTP RPC API and exposes them under a `/metrics` endpoint.  
It is primarily designed for energy monitoring devices such as **Shelly Pro 3EM**, but can work with any Shelly device supporting the HTTP RPC interface.

> This project was developed with the assistance of an AI agent.

---

## ✨ Features

- Supports **multiple Shelly devices** in one configuration
- Fetches data via Shelly's **HTTP RPC API** using **Digest Authentication**
- Exposes all numeric JSON fields as **Prometheus metrics**
- Allows selecting a specific device dynamically via the query parameter `?device=<name>`
- Optional exposure of Go runtime and process metrics
- Health check endpoint (`/healthz`) with built-in Docker `HEALTHCHECK` support
- Simple YAML configuration
- Multi-arch Docker image (`linux/amd64`, `linux/arm64`)
- Written in pure Go (no external dependencies other than Prometheus client libraries)

---

## 🧱 Installation

### From source

```bash
git clone https://github.com/TheChosenOne1984/shelly-exporter.git
cd shelly-exporter
go build -o shelly-exporter
```

### Docker

```bash
docker pull theChosenOne1984/shelly-exporter:latest
```

---

## ⚙️ Configuration

All configuration is done via a YAML file (e.g., `config.yaml`):

```yaml
devices:
  - name: "shelly-pro3em-house"
    address: "http://192.168.0.49"
    username: "user"
    password: "password"
    id: 0 # fallback in case no endpoints are listed
    endpoints:
      - "rpc/EM.GetStatus?id=0"
      # - "rpc/Sys.GetStatus"
      # - "rpc/Shelly.GetDeviceInfo"

  - name: "shelly-pro3em-garage"
    address: "http://192.168.0.50"
    username: "user"
    password: "password"
    id: 0
    endpoints:
      - "rpc/EM.GetStatus?id=0"

server:
  listen_address: ":9905"
  request_timeout_seconds: 5

metrics:
  enable_go_runtime: false
```

---

## 🚀 Usage

### Binary

```bash
./shelly-exporter -config ./config.yaml
```

### Docker

```bash
docker run -d \
  -p 9905:9905 \
  -v /path/to/config.yaml:/app/config/config.yaml:ro \
  theChosenOne1984/shelly-exporter:latest
```

### Docker Compose

```yaml
services:
  shelly-exporter:
    image: theChosenOne1984/shelly-exporter:latest
    ports:
      - "9905:9905"
    volumes:
      - ./config.yaml:/app/config/config.yaml:ro
    restart: unless-stopped
```

---

## 🌐 Endpoints

| Endpoint | Description |
|---|---|
| `/metrics` | Prometheus metrics for all configured devices |
| `/metrics?device=<name>` | Prometheus metrics for a single device |
| `/healthz` | Health check — returns `{"status":"ok"}` with HTTP 200 |
| `/` | Landing page with link to `/metrics` and list of configured devices |

---

## 🏥 Health Check

The binary has a built-in `-health-check` flag that calls `/healthz` on the running instance and exits with code `0` (healthy) or `1` (unhealthy). This is used by the Docker `HEALTHCHECK` instruction:

```bash
./shelly-exporter -health-check -config ./config.yaml
```

---

## 📊 Example Queries

Scrape all devices:
```bash
curl -s http://localhost:9905/metrics
```

Scrape a specific device:
```bash
curl -s "http://localhost:9905/metrics?device=shelly-pro3em-house"
```

Check health:
```bash
curl -s http://localhost:9905/healthz
```
