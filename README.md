# Kubernetes-style Orchestrator

**Stack:** Go 1.26 · REST/JSON (→ gRPC later) · SQLite (pure-Go `modernc.org/sqlite`, no CGo) → embedded etcd · `docker` → `containerd` runtime · k8s API machinery patterns (informer/reconciler) · Prometheus

## Full Vision
etcd-backed API server, declarative reconcilers, scheduler, CRDs + operators, CNI/CSI plugins, RBAC, admission webhooks, HPA, multi-tenant.

## MVP Status — v0.1 (working)

Implemented in [`mvp/`](./mvp):

- **`mk-apiserver`** — REST API server, SQLite-backed Pod store
  - `POST   /api/v1/pods` (create/upsert)
  - `GET    /api/v1/pods` (list; filter `?phase=` and/or `?nodeName=`)
  - `GET    /api/v1/pods/{name}`
  - `DELETE /api/v1/pods/{name}`
  - `PUT    /api/v1/pods/{name}/status` (status subresource — scheduler/kubelet writes)
- **`mk-scheduler`** — picks the single configured node, marks Pending pods `Scheduled`
- **`mk-kubelet`** — node agent: polls its assigned pods and runs them
  - Uses `docker run -d` when the Docker daemon is reachable
  - Falls back to an `echo`-based stub runtime when Docker is unavailable (`-docker=false`)
- **`mkctl`** — tiny `kubectl`-style CLI: `apply -f`, `get pods`, `describe pod`, `delete pod`

### Build

```bash
cd mvp
go build ./...                 # verifies everything compiles
mkdir -p bin
go build -o bin/mk-apiserver ./cmd/apiserver
go build -o bin/mk-scheduler ./cmd/scheduler
go build -o bin/mk-kubelet   ./cmd/kubelet
go build -o bin/mkctl        ./cmd/mkctl
```

### Run the demo (4 terminals from `mvp/`)

```bash
# 1) API server (sqlite file auto-created at data/mk.db)
mkdir -p data
./bin/mk-apiserver -addr :8080 -db data/mk.db

# 2) Scheduler — assigns Pending pods to node-1
./bin/mk-scheduler -api http://127.0.0.1:8080 -node node-1 -interval 1s

# 3) Kubelet for node-1 (drop -docker=false if Docker is running)
./bin/mk-kubelet -api http://127.0.0.1:8080 -node node-1 -interval 1s
# or, without Docker:
# ./bin/mk-kubelet -api http://127.0.0.1:8080 -node node-1 -interval 1s -docker=false

# 4) Apply a pod and query it
./bin/mkctl apply -f examples/pod-hello.yaml
./bin/mkctl get pods
./bin/mkctl describe pod hello
./bin/mkctl delete pod hello
```

Expected output from `mkctl get pods` once reconciliation completes:

```
NAME   PHASE    NODE    IMAGE        AGE  MESSAGE
hello  Running  node-1  alpine:3.20  2s   started
```

### Docker vs stub runtime

`mk-kubelet` auto-detects whether the Docker daemon is reachable (`docker version`).
If not, or if `-docker=false`, it falls back to `echo [stub-run] pod=<name> image=<img> ...`
so the full Pending → Scheduled → Running state machine still exercises without needing
Docker locally. The pod's `status.containerId` becomes `stub-<podName>` instead of a real
container ID.

### Example pods

- [`mvp/examples/pod-hello.yaml`](./mvp/examples/pod-hello.yaml) — alpine sleep 30
- [`mvp/examples/pod-nginx.yaml`](./mvp/examples/pod-nginx.yaml) — nginx web server

### Layout

```
mvp/
  cmd/
    apiserver/   # REST server + SQLite
    scheduler/   # picks node-1, sets phase=Scheduled
    kubelet/     # polls assigned pods, runs via docker or stub
    mkctl/       # CLI (apply -f, get, describe, delete) — uses yaml.v3
  internal/
    api/         # Pod/Container/PodSpec/PodStatus types + phase constants
    store/       # SQLite-backed Pod store (pure-Go driver)
  examples/      # sample Pod YAMLs
```

## Milestones
- **M1 (Week 1):** API server + SQLite store + Pod CRUD + YAML apply — DONE (MVP v0.1)
- **M2 (Week 3):** Scheduler + node agent runs containers via containerd — scheduler DONE; kubelet runs via docker (containerd migration pending)
- **M3 (Week 6):** Reconciliation loops + ReplicaSet + Deployment controllers
- **M4 (Week 9):** Services + kube-proxy-style networking + CoreDNS
- **M5 (Week 12):** CRDs + operator SDK + multi-node + RBAC

## Key References
- "Kubernetes: Up & Running"
- kubelet + kube-scheduler source
- Kine (etcd-over-SQL shim, k3s)
