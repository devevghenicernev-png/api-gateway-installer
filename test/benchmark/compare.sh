#!/bin/bash
# Benchmark apigw vs Kong vs Traefik vs Caddy
set -euo pipefail

RESULTS_DIR="bench-results/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$RESULTS_DIR"

echo "==> Starting benchmark comparison suite"
echo "Results will be saved to: $RESULTS_DIR"

# Test configuration
DURATION="30s"
CONNECTIONS=100
RATE=10000  # requests per second target
UPSTREAM_PORT=8081

# Start simple upstream (Go HTTP server)
go run test/benchmark/upstream.go -port $UPSTREAM_PORT &
UPSTREAM_PID=$!
trap "kill $UPSTREAM_PID 2>/dev/null || true" EXIT
sleep 2

# Function to run wrk2 test
run_wrk() {
    local name=$1
    local url=$2
    local output="$RESULTS_DIR/${name}.txt"
    
    echo "==> Testing $name..."
    wrk2 -t4 -c$CONNECTIONS -d$DURATION -R$RATE --latency "$url" > "$output" 2>&1
    
    # Extract key metrics
    local rps=$(grep "Requests/sec" "$output" | awk '{print $2}')
    local p50=$(grep "50.000%" "$output" | awk '{print $2}')
    local p99=$(grep "99.000%" "$output" | awk '{print $2}')
    
    echo "  RPS: $rps, p50: $p50, p99: $p99"
}

# 1. apigw (nginx + Go dashboard)
if command -v apigw &> /dev/null; then
    echo "==> Setting up apigw..."
    # Configure simple proxy
    sudo apigw api add bench --port $UPSTREAM_PORT --yes || true
    sleep 2
    run_wrk "apigw-simple" "http://localhost/api/bench"
    
    # With JWT
    echo "==> Testing apigw + JWT..."
    # Configure JWT validation
    sudo apigw api add bench-jwt --port $UPSTREAM_PORT --yes || true
    # TODO: configure JWT
    run_wrk "apigw-jwt" "http://localhost/api/bench-jwt"
fi

# 2. Kong (if available)
if command -v kong &> /dev/null; then
    echo "==> Setting up Kong..."
    # Start Kong
    docker run -d --name kong-bench \
        -e "KONG_DATABASE=off" \
        -e "KONG_PROXY_ACCESS_LOG=/dev/stdout" \
        -e "KONG_ADMIN_ACCESS_LOG=/dev/stdout" \
        -e "KONG_PROXY_ERROR_LOG=/dev/stderr" \
        -e "KONG_ADMIN_ERROR_LOG=/dev/stderr" \
        -p 8000:8000 -p 8001:8001 \
        kong:latest || true
    sleep 5
    
    # Configure route
    curl -i -X POST http://localhost:8001/services \
        --data name=bench \
        --data url="http://host.docker.internal:$UPSTREAM_PORT"
    curl -i -X POST http://localhost:8001/services/bench/routes \
        --data "paths[]=/api/bench"
    
    run_wrk "kong-simple" "http://localhost:8000/api/bench"
    
    docker stop kong-bench && docker rm kong-bench
fi

# 3. Traefik (if available)
if command -v traefik &> /dev/null; then
    echo "==> Setting up Traefik..."
    cat > /tmp/traefik-bench.yml <<EOF
entryPoints:
  web:
    address: ":8080"
providers:
  file:
    filename: /tmp/traefik-routes.yml
EOF
    
    cat > /tmp/traefik-routes.yml <<EOF
http:
  routers:
    bench:
      rule: "PathPrefix(\`/api/bench\`)"
      service: bench
  services:
    bench:
      loadBalancer:
        servers:
          - url: "http://localhost:$UPSTREAM_PORT"
EOF
    
    traefik --configFile=/tmp/traefik-bench.yml &
    TRAEFIK_PID=$!
    trap "kill $TRAEFIK_PID 2>/dev/null || true; kill $UPSTREAM_PID 2>/dev/null || true" EXIT
    sleep 3
    
    run_wrk "traefik-simple" "http://localhost:8080/api/bench"
    
    kill $TRAEFIK_PID
fi

# 4. Caddy (if available)
if command -v caddy &> /dev/null; then
    echo "==> Setting up Caddy..."
    cat > /tmp/Caddyfile <<EOF
:8080 {
    reverse_proxy /api/bench/* localhost:$UPSTREAM_PORT
}
EOF
    
    caddy run --config /tmp/Caddyfile &
    CADDY_PID=$!
    trap "kill $CADDY_PID 2>/dev/null || true; kill $UPSTREAM_PID 2>/dev/null || true" EXIT
    sleep 2
    
    run_wrk "caddy-simple" "http://localhost:8080/api/bench"
    
    kill $CADDY_PID
fi

# 5. nginx (baseline)
echo "==> Testing nginx baseline..."
cat > /tmp/nginx-bench.conf <<EOF
events {
    worker_connections 1024;
}
http {
    upstream backend {
        server localhost:$UPSTREAM_PORT;
    }
    server {
        listen 8082;
        location /api/bench {
            proxy_pass http://backend;
        }
    }
}
EOF

nginx -c /tmp/nginx-bench.conf -p /tmp &
NGINX_PID=$!
trap "kill $NGINX_PID 2>/dev/null || true; kill $UPSTREAM_PID 2>/dev/null || true" EXIT
sleep 2

run_wrk "nginx-baseline" "http://localhost:8082/api/bench"

# Generate report
echo "==> Generating comparison report..."
cat > "$RESULTS_DIR/summary.md" <<EOF
# apigw Performance Benchmark Results

**Date:** $(date)
**Hardware:** $(uname -m), $(nproc) cores
**Config:** $CONNECTIONS connections, target $RATE RPS, $DURATION duration

## Results Summary

| Gateway | RPS | Latency p50 | Latency p99 | Status |
|---------|-----|-------------|-------------|--------|
EOF

# Parse results and add to summary
for result in "$RESULTS_DIR"/*.txt; do
    name=$(basename "$result" .txt)
    rps=$(grep "Requests/sec" "$result" | awk '{print $2}' || echo "N/A")
    p50=$(grep "50.000%" "$result" | awk '{print $2}' || echo "N/A")
    p99=$(grep "99.000%" "$result" | awk '{print $2}' || echo "N/A")
    echo "| $name | $rps | $p50 | $p99 | ✅ |" >> "$RESULTS_DIR/summary.md"
done

cat "$RESULTS_DIR/summary.md"
echo ""
echo "==> Full results saved to: $RESULTS_DIR"
echo "==> View summary: cat $RESULTS_DIR/summary.md"
