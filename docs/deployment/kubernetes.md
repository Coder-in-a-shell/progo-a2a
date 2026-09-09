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

## Horizontal scaling caveats

Invocation is suitable for horizontal replicas, but cached task results and metrics are process-local. A later `GET /a2a/v1/tasks/{id}` can reach another pod and return `404`. Use session affinity only as a temporary workaround; durable shared storage is the correct design if result retrieval matters.

Readiness does not test upstream agents, so a ready pod may still return upstream errors.

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
