#!/bin/bash
set -e
REPO="/tmp/repos/noncode_03_tech_translate"
rm -rf "$REPO"
mkdir -p "$REPO"
cd "$REPO"

cat > SPEC.md << 'EOF'
# Designing a Rate-Limited API Gateway with Circuit Breaker Pattern

## 1. Overview

This specification describes the architecture and design of an API Gateway that implements rate limiting and the circuit breaker pattern to protect backend services from overload and cascading failures. The gateway acts as a single entry point for all client requests, enforcing traffic policies and providing resilience against service failures.

## 2. Architecture

The API Gateway is composed of four core components arranged in a pipeline:

```
Client Request → Rate Limiter → Request Router → Circuit Breaker → Backend Service
                                      ↓
                               Metrics Collector
```

Each component is designed to be independently configurable and composable, allowing operators to tune behavior per route or per backend service.

## 3. Components

### 3.1 Rate Limiter

The Rate Limiter enforces per-client or per-endpoint request quotas using a token bucket algorithm. Configuration parameters:

| Parameter | Description | Default |
|-----------|-------------|---------|
| `tokens_per_second` | Token replenishment rate | 100 |
| `burst_size` | Maximum token bucket capacity | 200 |
| `per_client` | Whether limits apply per client IP | true |
| `per_endpoint` | Whether limits apply per API endpoint | false |

When the rate limit is exceeded, the gateway returns HTTP 429 (Too Many Requests) with a `Retry-After` header indicating when the client can retry.

### 3.2 Request Router

The Request Router maps incoming request paths to backend service instances. It supports:
- Path-based routing with pattern matching
- Load balancing (round-robin, least-connections, random)
- Health checking via periodic heartbeat probes
- Request/response transformation (header injection, body modification)

### 3.3 Circuit Breaker

The Circuit Breaker monitors backend service health and prevents requests from being sent to failing services. It implements a three-state model:

- **Closed**: Normal operation. Requests pass through. Failure count is tracked.
- **Open**: After `failure_threshold` consecutive failures, the circuit opens. All requests are immediately rejected with HTTP 503 (Service Unavailable).
- **Half-Open**: After `cooldown_seconds`, a limited number of probe requests are allowed. If they succeed, the circuit closes. If they fail, it re-opens.

Configuration parameters:

| Parameter | Description | Default |
|-----------|-------------|---------|
| `failure_threshold` | Consecutive failures to open circuit | 5 |
| `cooldown_seconds` | Time before attempting half-open | 30 |
| `half_open_max_requests` | Probe requests in half-open state | 3 |
| `request_timeout_ms` | Per-request timeout | 5000 |

### 3.4 Metrics Collector

The Metrics Collector aggregates operational metrics for monitoring and alerting:
- Request count, latency percentiles (p50, p95, p99)
- Rate limit rejection count
- Circuit breaker state transitions
- Backend service error rates

Metrics are exposed via a Prometheus-compatible `/metrics` endpoint.

## 4. Data Flow

1. Client sends an HTTP request to the gateway
2. Rate Limiter checks the client's token bucket:
   - If tokens are available, decrement and proceed
   - If no tokens, return HTTP 429
3. Request Router maps the request path to a backend service endpoint
4. Circuit Breaker evaluates the backend's current state:
   - If **closed** → forward request, track success/failure
   - If **open** → immediately return HTTP 503
   - If **half-open** → allow probe request, evaluate result
5. On successful response, the gateway relays the response to the client
6. Metrics Collector asynchronously records all events and decisions

## 5. Failure Scenarios

| Scenario | Behavior |
|----------|----------|
| Backend timeout | Circuit breaker increments failure count. Request returns 504 Gateway Timeout. |
| Rate limit exhaustion | Rate limiter returns 429. Client must wait for token replenishment. |
| All backends unhealthy | Router returns 503 with health status details. |
| Circuit open | Immediate 503 response without proxying. Saves backend resources. |
| Configuration change | Hot-reload via file watcher, no restart required. |

## 6. Monitoring & Alerting

Operators should configure alerts on:
- Circuit breaker state changes (especially Closed → Open)
- Rate limit hit rate exceeding 10% of requests
- p99 latency exceeding 2x baseline
- Backend error rate exceeding 5%

Recommended dashboard panels: request rate, latency heatmap, circuit breaker status timeline, rate limit utilization gauge.

## 7. Implementation Considerations

- Use connection pooling for backend HTTP clients
- Implement graceful shutdown with in-flight request drainage
- Support distributed rate limiting with Redis for multi-instance deployments
- Log all circuit breaker transitions with timestamps for post-mortem analysis
- Cache routing table in memory with periodic refresh from service registry
EOF

echo "noncode_03_tech_translate setup done"
