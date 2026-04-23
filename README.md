# 09 — Kubernetes-style Orchestrator

**Stack:** Go 1.22 · gRPC + Protobuf · SQLite → embedded etcd · `containerd` as runtime · CNI plugins · k8s API machinery patterns (informer/reconciler) · Prometheus

## Full Vision
etcd-backed API server, declarative reconcilers, scheduler, CRDs + operators, CNI/CSI plugins, RBAC, admission webhooks, HPA, multi-tenant.

## MVP (1 weekend)
REST API server (SQLite-backed), single scheduler, node agent runs Docker containers. `kubectl apply` equivalent for Pods.

## Milestones
- **M1 (Week 1):** API server + SQLite store + Pod CRUD + YAML apply
- **M2 (Week 3):** Scheduler + node agent runs containers via containerd
- **M3 (Week 6):** Reconciliation loops + ReplicaSet + Deployment controllers
- **M4 (Week 9):** Services + kube-proxy-style networking + CoreDNS
- **M5 (Week 12):** CRDs + operator SDK + multi-node + RBAC

## Key References
- "Kubernetes: Up & Running"
- kubelet + kube-scheduler source
- Kine (etcd-over-SQL shim, k3s)
