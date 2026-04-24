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
    apiserver/           # REST server + SQLite (Pods, ReplicaSets, Deployments)
    scheduler/           # picks node-1, sets phase=Scheduled
    kubelet/             # polls assigned pods, runs via docker or stub
    controller-manager/  # runs RS + Deployment reconcilers (M2)
    mkctl/               # CLI (apply -f, get, describe, delete) — uses yaml.v3
  internal/
    api/                 # Pod/ReplicaSet/Deployment types + phase constants
    store/               # SQLite-backed store (pure-Go driver)
    client/              # HTTP client used by controllers + CLI
    controllers/         # replicaset + deployment reconcilers (M2)
  examples/              # sample Pod / Deployment YAMLs
```

## M2 Status — Reconciliation loops + ReplicaSet + Deployment (DONE)

New in M2:

- **`ReplicaSet` resource** (`apiVersion: apps/v1`, `kind: ReplicaSet`) with
  `spec.replicas`, `spec.selector`, `spec.template`. REST at
  `/apis/apps/v1/replicasets[/{name}[/status]]`.
- **`Deployment` resource** (`apiVersion: apps/v1`, `kind: Deployment`) with
  `spec.replicas` + `spec.template` and a stubbed `spec.strategy` field (real
  rolling update lands in M3). REST at
  `/apis/apps/v1/deployments[/{name}[/status]]`.
- **`mk-controller-manager`** — runs both reconcilers in-process, each on its
  own ticker (default 2s):
  - **ReplicaSet controller** — lists RS + pods, counts pods carrying label
    `mk.replicaset=<rsName>`, creates missing (`<rs>-<randhex5>`) or deletes
    extras, writes observed replica counts to RS status.
  - **Deployment controller** — creates/updates a single owned
    ReplicaSet named `<deploy>-rs` and mirrors its status up to the
    Deployment's status subresource.
- **`mkctl`** — `apply -f` now auto-dispatches on `kind` (Pod / ReplicaSet
  / Deployment); added `get replicasets`, `get deployments`, and
  `delete rs|deploy <name>`.
- **Example:** [`mvp/examples/deployment-hello.yaml`](./mvp/examples/deployment-hello.yaml)

### Run the M2 demo (from `mvp/`)

```bash
mkdir -p data bin
go build -o bin/mk-apiserver          ./cmd/apiserver
go build -o bin/mk-scheduler          ./cmd/scheduler
go build -o bin/mk-kubelet            ./cmd/kubelet
go build -o bin/mk-controller-manager ./cmd/controller-manager
go build -o bin/mkctl                 ./cmd/mkctl

# 4 long-running processes:
./bin/mk-apiserver          -addr :8080 -db data/mk.db
./bin/mk-scheduler          -api http://127.0.0.1:8080 -node node-1 -interval 1s
./bin/mk-kubelet            -api http://127.0.0.1:8080 -node node-1 -interval 1s -docker=false
./bin/mk-controller-manager -api http://127.0.0.1:8080 -interval 1s

./bin/mkctl apply -f examples/deployment-hello.yaml
./bin/mkctl get deployments
./bin/mkctl get rs
./bin/mkctl get pods
# kill one pod — controller recreates it
./bin/mkctl delete pod <one-of-the-pod-names>
./bin/mkctl get pods
```

### Demo output (captured)

```
$ mkctl apply -f examples/deployment-hello.yaml
deployment/hello applied (replicas=3)

$ mkctl get deployments
NAME   DESIRED  CURRENT  READY  IMAGE        AGE
hello  3        3        3      alpine:3.20  10s

$ mkctl get rs
NAME      DESIRED  CURRENT  READY  IMAGE        AGE
hello-rs  3        3        3      alpine:3.20  0s

$ mkctl get pods
NAME            PHASE    NODE    IMAGE        AGE  MESSAGE
hello-rs-ca396  Running  node-1  alpine:3.20  1s   started
hello-rs-07e85  Running  node-1  alpine:3.20  1s   started
hello-rs-855f9  Running  node-1  alpine:3.20  1s   started

$ mkctl delete pod hello-rs-ca396
pod/hello-rs-ca396 deleted

$ mkctl get pods   # controller recreates the missing replica
NAME            PHASE    NODE    IMAGE        AGE  MESSAGE
hello-rs-07e85  Running  node-1  alpine:3.20  12s  started
hello-rs-855f9  Running  node-1  alpine:3.20  12s  started
hello-rs-d717d  Running  node-1  alpine:3.20  3s   started
```

Relevant `mk-controller-manager` log lines:

```
deployment-controller: deployment=hello created rs=hello-rs replicas=3
replicaset-controller: rs=hello-rs created pod=hello-rs-ca396
replicaset-controller: rs=hello-rs created pod=hello-rs-07e85
replicaset-controller: rs=hello-rs created pod=hello-rs-855f9
replicaset-controller: rs=hello-rs created pod=hello-rs-d717d   # self-healed after delete
```

## M3 Status — Services + Endpoints + kube-proxy + DNS (DONE)

M3 introduces cluster-internal service discovery and L4 load balancing,
mirroring Kubernetes' `Service` / `Endpoints` + `kube-proxy` + `CoreDNS`
stack.

New in M3:

- **`Service` resource** (`apiVersion: v1`, `kind: Service`) with
  `spec.selector`, `spec.ports`, `spec.type` (`ClusterIP` only for M3),
  `spec.clusterIP` (auto-allocated). REST at
  `/api/v1/services[/{name}]`.
- **`Endpoints` resource** maintained 1:1 with Services, listing live
  pod IP:port tuples that match the selector. REST at
  `/api/v1/endpoints[/{name}]`.
- **`ServiceController`** (runs inside `mk-controller-manager`) — watches
  Services + Pods on every tick; for each Service it rebuilds the
  Endpoints object from Running pods whose labels match
  `spec.selector`.
- **`mk-kube-proxy`** — userspace L4 load balancer. Binds a TCP listener
  on `ClusterIP:port` per Service and round-robins new connections
  across the current endpoint set (atomic swap — no flapping when the
  endpoints list changes).
- **`mk-coredns-mini`** — embedded DNS server (`github.com/miekg/dns`)
  that resolves `<service>.<namespace>.svc.cluster.local` and
  `<service>.<namespace>.cluster.local` → ClusterIP. Listens on
  `127.0.0.1:15353` by default (port 5353 is taken by `mDNSResponder`
  on macOS).
- **Stub runtime now serves HTTP** — `mk-kubelet -docker=false` starts
  an in-process HTTP listener per pod on a random loopback port and
  writes `PodIP` + `HostPort` into the Pod's status, so the proxy can
  actually forward traffic without needing Docker. (A real container
  runtime would publish a hostPort instead.)
- **`mkctl` additions** — `apply -f` auto-dispatches `kind: Service`;
  new `get services` / `get svc` and `get endpoints` / `get ep`;
  `delete svc <name>`.
- **Example:** [`mvp/examples/service-hello.yaml`](./mvp/examples/service-hello.yaml).

### ClusterIP allocation (single-host MVP)

The apiserver allocates a ClusterIP per Service. On a real cluster this
would be a fresh `/32` from a reserved CIDR; here we run everything on
loopback, so the default is `127.0.0.1` and each Service must pick a
unique `spec.ports[].port`. Pass `-cluster-ip-loopback-cidr` to the
apiserver to allocate out of `127.20.0.0/24` instead (requires
`sudo ifconfig lo0 alias 127.20.0.X up` on macOS, or
`sudo ip addr add 127.20.0.X/8 dev lo` on linux).

### Run the M3 demo (from `mvp/`)

```bash
mkdir -p data bin
go build -o bin/mk-apiserver          ./cmd/apiserver
go build -o bin/mk-scheduler          ./cmd/scheduler
go build -o bin/mk-kubelet            ./cmd/kubelet
go build -o bin/mk-controller-manager ./cmd/controller-manager
go build -o bin/mk-kube-proxy         ./cmd/kube-proxy
go build -o bin/mk-coredns-mini       ./cmd/coredns-mini
go build -o bin/mkctl                 ./cmd/mkctl

# 6 long-running processes:
./bin/mk-apiserver          -addr :8080 -db data/mk.db
./bin/mk-scheduler          -api http://127.0.0.1:8080 -node node-1 -interval 500ms
./bin/mk-kubelet            -api http://127.0.0.1:8080 -node node-1 -interval 500ms -docker=false
./bin/mk-controller-manager -api http://127.0.0.1:8080 -interval 500ms
./bin/mk-kube-proxy         -api http://127.0.0.1:8080 -interval 500ms
./bin/mk-coredns-mini       -api http://127.0.0.1:8080 -listen 127.0.0.1:15353 -interval 500ms

./bin/mkctl apply -f examples/deployment-hello.yaml
./bin/mkctl apply -f examples/service-hello.yaml

./bin/mkctl get svc
./bin/mkctl get endpoints
./bin/mkctl get pods

# Hit the Service — kube-proxy round-robins across the 3 pods.
for i in 1 2 3 4 5 6; do curl -s http://127.0.0.1:9090/; done

# Resolve the service DNS name.
dig @127.0.0.1 -p 15353 +short hello.default.svc.cluster.local
```

### Demo output (captured)

```
$ mkctl apply -f examples/deployment-hello.yaml
deployment/hello applied (replicas=3)

$ mkctl apply -f examples/service-hello.yaml
service/hello applied (clusterIP=127.0.0.1)

$ mkctl get svc
NAME   TYPE       CLUSTER-IP  PORT(S)         SELECTOR   AGE
hello  ClusterIP  127.0.0.1   9090->8080/TCP  app=hello  5s

$ mkctl get endpoints
NAME   ENDPOINTS                                        AGE
hello  127.0.0.1:62192,127.0.0.1:62193,127.0.0.1:62195  0s

$ for i in 1 2 3 4 5 6; do curl -s http://127.0.0.1:9090/; done
hello from pod hello-rs-09cc4 (image=alpine:3.20 port=62192)
hello from pod hello-rs-ffb0c (image=alpine:3.20 port=62193)
hello from pod hello-rs-3443f (image=alpine:3.20 port=62195)
hello from pod hello-rs-09cc4 (image=alpine:3.20 port=62192)
hello from pod hello-rs-ffb0c (image=alpine:3.20 port=62193)
hello from pod hello-rs-3443f (image=alpine:3.20 port=62195)

$ dig @127.0.0.1 -p 15353 +short hello.default.svc.cluster.local
127.0.0.1
```

Delete a pod and the ReplicaSet + ServiceController recreate the
backend and update Endpoints; subsequent connections balance across
the new set:

```
$ mkctl delete pod hello-rs-09cc4
pod/hello-rs-09cc4 deleted

$ mkctl get endpoints
NAME   ENDPOINTS                                        AGE
hello  127.0.0.1:54645,127.0.0.1:62193,127.0.0.1:62195  0s

$ for i in 1 2 3 4; do curl -s http://127.0.0.1:9090/; done
hello from pod hello-rs-8cccf (image=alpine:3.20 port=54645)
hello from pod hello-rs-ffb0c (image=alpine:3.20 port=62193)
hello from pod hello-rs-3443f (image=alpine:3.20 port=62195)
hello from pod hello-rs-8cccf (image=alpine:3.20 port=54645)
```

## M6 Status — NetworkPolicy + Service Mesh Sidecars (mTLS) + Multi-cluster Federation (DONE)

M6 layers three big Kubernetes concepts on top of the M5 control plane:

1. **NetworkPolicy** — label-selector-based L4 ingress filtering enforced
   in `mk-kube-proxy`. Matches upstream semantics: an unselected pod is
   allow-all; once any policy selects a pod, only explicitly allow-listed
   peers + ports may reach it (default-deny).
2. **Service-mesh sidecar (`mk-sidecar`)** — a tiny Go proxy that
   terminates and originates mutual TLS between pods, plus an admission
   webhook (`mk-sidecar-injector`) that auto-injects the sidecar into
   any pod carrying `mk.io/inject-sidecar=true`.
3. **Multi-cluster federation** — `Cluster` + `FederatedDeployment`
   resources with an in-process controller that probes every member
   cluster's `/healthz` and fans out derived `Deployment` objects to
   the healthy ones. Status reports `readyClusters / totalClusters`.

### New resources

| apiVersion                | kind                 | REST path                                                     |
| ------------------------- | -------------------- | ------------------------------------------------------------- |
| `networking.mk.io/v1`     | `NetworkPolicy`      | `/apis/networking.mk.io/v1/networkpolicies[/{name}]`          |
| `federation.mk.io/v1`     | `Cluster`            | `/apis/federation.mk.io/v1/clusters[/{name}[/status]]`        |
| `federation.mk.io/v1`     | `FederatedDeployment`| `/apis/federation.mk.io/v1/federateddeployments[/{name}[/status]]` |

### New binaries / packages

- **`cmd/mk-sidecar/`** — dual-mode (`-mode=server|client`) TLS proxy.
  Terminates / originates mTLS between pods; auto-issues a shared CA
  plus per-pod leaf certs if no paths are provided.
- **`cmd/mk-sidecar-injector/`** — mutating admission webhook that
  appends an `mk-sidecar` container to any pod labelled
  `mk.io/inject-sidecar=true`. Idempotent, opt-in.
- **`internal/netpol/`** — pure NetworkPolicy evaluator (label match +
  ingress rule + port check). Called on every inbound connection by
  `mk-kube-proxy`.
- **`internal/federation/`** — `Controller` that reconciles
  `FederatedDeployment` → per-cluster `Deployment` + probes cluster
  `/healthz` for heartbeat.

### Run the M6 demo (from `mvp/`)

```bash
mkdir -p data bin
go build -o bin/mk-apiserver         ./cmd/apiserver
go build -o bin/mk-scheduler         ./cmd/scheduler
go build -o bin/mk-kubelet           ./cmd/kubelet
go build -o bin/mk-controller-manager./cmd/controller-manager
go build -o bin/mk-kube-proxy        ./cmd/kube-proxy
go build -o bin/mk-sidecar           ./cmd/mk-sidecar
go build -o bin/mk-sidecar-injector  ./cmd/mk-sidecar-injector
go build -o bin/mkctl                ./cmd/mkctl

# Core control plane + data plane.
./bin/mk-apiserver         -addr :8080 -db data/mk.db &
./bin/mk-scheduler         -api http://127.0.0.1:8080 -node node-1 -interval 500ms &
./bin/mk-kubelet           -api http://127.0.0.1:8080 -node node-1 -interval 500ms -docker=false &
./bin/mk-kube-proxy        -api http://127.0.0.1:8080 -interval 500ms -netpol=true &

# Sidecar injection webhook (registers with apiserver via a webhook CRD).
./bin/mk-sidecar-injector  -addr :9444 &
./bin/mkctl apply -f examples/webhook-sidecar-inject.yaml

# Apply workload, then apply NetworkPolicy — connections from
# non-allowed peers are dropped by mk-kube-proxy (look for
# "NETPOL DENY" in its log).
./bin/mkctl apply -f examples/deployment-hello.yaml
./bin/mkctl apply -f examples/service-hello.yaml
./bin/mkctl apply -f examples/networkpolicy-allow-frontend.yaml
./bin/mkctl get networkpolicies

# mTLS between two sidecars on the same host. Sidecar 1 terminates,
# forwards to a local echo service on :8080; sidecar 2 originates.
nc -l -p 8080 &                                      # fake workload
./bin/mk-sidecar -mode=server -listen=:9443 -upstream=127.0.0.1:8080 \
                 -server-name=pod-a &
./bin/mk-sidecar -mode=client -listen=127.0.0.1:8081 -upstream=127.0.0.1:9443 \
                 -server-name=pod-b -peer-name=pod-a &
echo hello | nc 127.0.0.1 8081                       # plaintext in, mTLS on wire

# Multi-cluster federation: register a second apiserver as a member
# cluster, apply a FederatedDeployment, verify it appears there.
./bin/mk-apiserver -addr :8090 -db data/mk-edge.db &
./bin/mkctl apply -f examples/cluster-edge.yaml      # points at :8081
./bin/mkctl apply -f examples/federateddeployment-hello.yaml
./bin/mkctl get clusters
./bin/mkctl get federateddeployments
```

Expected:

```
$ mkctl get networkpolicies
NAME                     SELECTOR      INGRESS-RULES
backend-allow-frontend   app=backend   1

$ mkctl get clusters
NAME    SERVER                   REGION    READY   LAST-HEARTBEAT
edge-1  http://127.0.0.1:8081    us-east   true    2s ago

$ mkctl get federateddeployments
NAME    REPLICAS  CLUSTERS   READY-CLUSTERS
hello   2         <all>      1/1
```

`mk-kube-proxy` will print `NETPOL DENY` on connections that hit a
selected destination pod from a non-matching source label set, and
`NETPOL` is bypassed silently when no policy selects the destination.

## Milestones
- **M1 (Week 1):** API server + SQLite store + Pod CRUD + YAML apply — DONE (MVP v0.1)
- **M2 (Week 3):** Scheduler + node agent runs containers via containerd — scheduler DONE; kubelet runs via docker (containerd migration pending)
- **M2 (controllers):** Reconciliation loops + ReplicaSet + Deployment controllers — DONE
- **M3 (Week 6):** Services + Endpoints + kube-proxy-style L4 LB + embedded DNS — DONE
- **M4:** Rolling update strategy, revision history, horizontal pod autoscaler stub
- **M5 (Week 12):** CRDs + operator SDK + multi-node + RBAC — DONE
- **M6:** NetworkPolicy + service-mesh sidecars (mTLS) + multi-cluster federation — DONE

## Key References
- "Kubernetes: Up & Running"
- kubelet + kube-scheduler source
- Kine (etcd-over-SQL shim, k3s)
