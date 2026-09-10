# Kubernetes

This example assumes you have built and published the repository image as `ghcr.io/your-org/progo-a2a:v0.1.0`. Replace that placeholder with your own immutable image reference.

## Configuration and secrets

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: progo-a2a-config
data:
  progo-a2a.yaml: |
    server:
      host: "0.0.0.0"
      port: 8080
      read_timeout_seconds: 30
      write_timeout_seconds: 120
      idle_timeout_seconds: 60
    storage:
      backend: "postgres"
      postgres:
        dsn: "${DATABASE_URL}"
        migrate_on_start: false
    security:
      enabled: true
      api_keys:
        - key: "${PROGO_API_KEY}"
          client_id: "cluster-client"
          allowed_agents: ["openai-chat"]
    agents:
      - id: "openai-chat"
        name: "OpenAI chat"
        type: "openai"
        endpoint: "https://api.openai.com/v1/chat/completions"
        capabilities: ["chat"]
        timeout_seconds: 45
        retries: 2
        auth:
          type: "bearer"
          token: "${OPENAI_API_KEY}"
        options:
          model: "gpt-4o"
---
apiVersion: v1
kind: Secret
metadata:
  name: progo-a2a-secrets
type: Opaque
stringData:
  proxy-api-key: replace-me
  openai-api-key: replace-me
  database-url: "postgres://progo:replace-me@postgres.example.internal:5432/progo?sslmode=verify-full"
```

Because interpolation happens before YAML parsing, quoting `${NAME}` references avoids surprises from secret characters. Use an external secret controller rather than committing real values.

## Deployment and service

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: progo-a2a
spec:
  replicas: 2
  selector:
    matchLabels:
      app: progo-a2a
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1
      maxUnavailable: 0
  template:
    metadata:
      labels:
        app: progo-a2a
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "8080"
        prometheus.io/path: /metrics
    spec:
      terminationGracePeriodSeconds: 20
      containers:
        - name: progo-a2a
          image: ghcr.io/your-org/progo-a2a:v0.1.0
          imagePullPolicy: IfNotPresent
          args: ["-config", "/app/config/progo-a2a.yaml", "-log-level", "info"]
          ports:
            - name: http
              containerPort: 8080
          env:
            - name: PROGO_API_KEY
              valueFrom:
                secretKeyRef:
                  name: progo-a2a-secrets
                  key: proxy-api-key
            - name: OPENAI_API_KEY
              valueFrom:
                secretKeyRef:
                  name: progo-a2a-secrets
                  key: openai-api-key
            - name: DATABASE_URL
              valueFrom:
                secretKeyRef:
                  name: progo-a2a-secrets
                  key: database-url
          volumeMounts:
            - name: config
              mountPath: /app/config
              readOnly: true
          livenessProbe:
            httpGet:
              path: /healthz
              port: http
            periodSeconds: 10
          readinessProbe:
            httpGet:
              path: /readyz
              port: http
            periodSeconds: 5
          resources:
            requests:
              cpu: 100m
              memory: 64Mi
            limits:
              cpu: 500m
              memory: 256Mi
      volumes:
        - name: config
          configMap:
            name: progo-a2a-config
---
apiVersion: v1
kind: Service
metadata:
  name: progo-a2a
  labels:
    app: progo-a2a
spec:
  selector:
    app: progo-a2a
  ports:
    - name: http
      port: 80
      targetPort: http
```

## Horizontal scaling and storage

Stateless request invocation scales horizontally without coordination. For task result retrieval:

- **In-Memory Backend (`memory`)**: Task results are process-local. A subsequent `GET /a2a/v1/tasks/{id}` routed to a different pod returns `404`. Use this backend only if task retrieval is not required by clients or when running a single pod.
- **PostgreSQL Backend (`postgres`)**: Recommended for multi-replica deployments. All pods connect to a shared PostgreSQL instance (configured via a Kubernetes Secret containing `DATABASE_URL`). Any pod can look up completed task results by canonical ID or alias, eliminating 404 routing anomalies without requiring session affinity.

See the [PostgreSQL storage guide](postgresql.md) for database provisioning, serialized migration options, and pool sizing.

Note that metrics remain process-local and should be scraped from each pod individually. When PostgreSQL storage is enabled, the `/readyz` probe checks database connectivity with a 2-second timeout (it does not probe upstream agents).

## Autoscaling

A CPU-based HPA is a reasonable starting point:

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: progo-a2a
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: progo-a2a
  minReplicas: 2
  maxReplicas: 10
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 70
```

For long-lived streaming traffic, evaluate concurrency and connection metrics rather than CPU alone.
