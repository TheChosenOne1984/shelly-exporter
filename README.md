# Shelly Exporter

The **Shelly Exporter** is a lightweight [Prometheus](https://prometheus.io/) exporter written in Go that collects metrics from one or more [Shelly devices](https://shelly.cloud/) via their HTTP RPC API and exposes them under a `/metrics` endpoint.  
It is primarily designed for energy monitoring devices such as **Shelly Pro 3EM**, but can work with any Shelly device supporting the HTTP RPC interface.

---

## ✨ Features

- Supports **multiple Shelly devices** in one configuration  
- Fetches data via Shelly’s **HTTP RPC API** using **Digest Authentication**  
- Exposes all numeric JSON fields as **Prometheus metrics**  
- Allows selecting a specific device dynamically via the query parameter `?device=<name>`  
- Optional exposure of Go runtime and process metrics  
- Simple YAML configuration  
- Written in pure Go (no external dependencies other than Prometheus client libraries)

---

## 🧱 Installation

Clone the repository and build the exporter:

```bash
git clone git@github.com-private:privat/shelly-exporter.git
cd shelly-exporter
go build -o shelly-exporter
```

## ⚙️ Configuration

All configuration is done via a YAML file (e.g., config.yaml):

```yaml
devices:
  - name: "shelly-pro3em-house"
    address: "http://192.168.0.49"
    username: "user"
    password: "passwort"
    id: 0
    endpoints:
      - "rpc/EM.GetStatus?id=0"
      # - "rpc/Sys.GetStatus"
      # - "rpc/Shelly.GetDeviceInfo"

  - name: "shelly-pro3em-garage"
    address: "http://192.168.0.50"
    username: "user2"
    password: "pass2"
    id: 0
    endpoints:
      - "rpc/EM.GetStatus?id=0"

server:
  listen_address: ":9905"
  request_timeout_seconds: 5

metrics:
  enable_go_runtime: true
```

## 🚀 Usage

Start the exporter:
```bash
./shelly-exporter -config ./config.yaml
```
By default, the exporter serves metrics on http://localhost:9905/metrics

### Example Queries

Scrape all devices:
```bash
curl -s http://localhost:9905/metrics
```

Scrape a specific device:
```bash
curl -s "http://localhost:9905/metrics?device=shelly-pro3em-house"
```
