package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ahfoysal/kubernetes-style-orchestrator/mvp/internal/api"
)

// ErrNotFound is returned when a pod is missing.
var ErrNotFound = errors.New("pod not found")

// Store is a SQLite-backed Pod store.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) a SQLite DB at path and runs migrations.
func Open(path string) (*Store, error) {
	// WAL + busy_timeout greatly reduces SQLITE_BUSY under multiple
	// controllers hammering the server concurrently.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single writer avoids interleaved writes on the same connection.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close closes the DB.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS pods (
	name TEXT PRIMARY KEY,
	namespace TEXT NOT NULL DEFAULT 'default',
	spec_json TEXT NOT NULL,
	metadata_json TEXT NOT NULL,
	phase TEXT NOT NULL DEFAULT 'Pending',
	node_name TEXT NOT NULL DEFAULT '',
	container_id TEXT NOT NULL DEFAULT '',
	message TEXT NOT NULL DEFAULT '',
	pod_ip TEXT NOT NULL DEFAULT '',
	host_port INTEGER NOT NULL DEFAULT 0,
	last_updated DATETIME NOT NULL,
	created_at DATETIME NOT NULL
);`,
		`CREATE TABLE IF NOT EXISTS replicasets (
	name TEXT PRIMARY KEY,
	namespace TEXT NOT NULL DEFAULT 'default',
	spec_json TEXT NOT NULL,
	metadata_json TEXT NOT NULL,
	status_json TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	last_updated DATETIME NOT NULL
);`,
		`CREATE TABLE IF NOT EXISTS deployments (
	name TEXT PRIMARY KEY,
	namespace TEXT NOT NULL DEFAULT 'default',
	spec_json TEXT NOT NULL,
	metadata_json TEXT NOT NULL,
	status_json TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	last_updated DATETIME NOT NULL
);`,
		`CREATE TABLE IF NOT EXISTS services (
	name TEXT PRIMARY KEY,
	namespace TEXT NOT NULL DEFAULT 'default',
	spec_json TEXT NOT NULL,
	metadata_json TEXT NOT NULL,
	status_json TEXT NOT NULL,
	cluster_ip TEXT NOT NULL DEFAULT '',
	created_at DATETIME NOT NULL,
	last_updated DATETIME NOT NULL
);`,
		`CREATE TABLE IF NOT EXISTS endpoints (
	name TEXT PRIMARY KEY,
	namespace TEXT NOT NULL DEFAULT 'default',
	metadata_json TEXT NOT NULL,
	subsets_json TEXT NOT NULL,
	last_updated DATETIME NOT NULL
);`,
		`CREATE TABLE IF NOT EXISTS nodes (
	name TEXT PRIMARY KEY,
	metadata_json TEXT NOT NULL,
	status_json TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	last_heartbeat DATETIME NOT NULL
);`,
		`CREATE TABLE IF NOT EXISTS crds (
	name TEXT PRIMARY KEY,
	group_name TEXT NOT NULL,
	version TEXT NOT NULL,
	plural TEXT NOT NULL,
	kind TEXT NOT NULL,
	singular TEXT NOT NULL,
	scope TEXT NOT NULL,
	spec_json TEXT NOT NULL,
	metadata_json TEXT NOT NULL,
	created_at DATETIME NOT NULL
);`,
		`CREATE TABLE IF NOT EXISTS custom_resources (
	crd_name TEXT NOT NULL,
	name TEXT NOT NULL,
	namespace TEXT NOT NULL DEFAULT 'default',
	body_json TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	last_updated DATETIME NOT NULL,
	PRIMARY KEY (crd_name, name)
);`,
		`CREATE TABLE IF NOT EXISTS users (
	name TEXT PRIMARY KEY,
	token TEXT NOT NULL UNIQUE,
	groups_json TEXT NOT NULL,
	created_at DATETIME NOT NULL
);`,
		`CREATE TABLE IF NOT EXISTS roles (
	name TEXT NOT NULL,
	scope TEXT NOT NULL,
	namespace TEXT NOT NULL DEFAULT '',
	rules_json TEXT NOT NULL,
	metadata_json TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	PRIMARY KEY (scope, name)
);`,
		`CREATE TABLE IF NOT EXISTS role_bindings (
	name TEXT NOT NULL,
	scope TEXT NOT NULL,
	namespace TEXT NOT NULL DEFAULT '',
	subjects_json TEXT NOT NULL,
	roleref_json TEXT NOT NULL,
	metadata_json TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	PRIMARY KEY (scope, name)
);`,
		// M5: leader election lease. One row per lease name; holder wins
		// while renew_time is within the TTL.
		`CREATE TABLE IF NOT EXISTS leases (
	name TEXT PRIMARY KEY,
	holder TEXT NOT NULL,
	renew_time DATETIME NOT NULL,
	ttl_seconds INTEGER NOT NULL
);`,
		// M5: admission webhook registry.
		`CREATE TABLE IF NOT EXISTS admission_webhooks (
	name TEXT PRIMARY KEY,
	type TEXT NOT NULL,
	url TEXT NOT NULL,
	resources_json TEXT NOT NULL,
	failure_policy TEXT NOT NULL DEFAULT 'Fail',
	created_at DATETIME NOT NULL
);`,
		// M5: HPA registry.
		`CREATE TABLE IF NOT EXISTS hpas (
	name TEXT PRIMARY KEY,
	namespace TEXT NOT NULL DEFAULT 'default',
	spec_json TEXT NOT NULL,
	metadata_json TEXT NOT NULL,
	status_json TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	last_updated DATETIME NOT NULL
);`,
	}
	for _, s1 := range stmts {
		if _, err := s.db.Exec(s1); err != nil {
			return err
		}
	}
	// Additive migrations. SQLite lacks IF NOT EXISTS for ALTER ADD COLUMN;
	// attempt-and-ignore so repeated startups are idempotent.
	addCols := []string{
		`ALTER TABLE pods ADD COLUMN pod_ip TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pods ADD COLUMN host_port INTEGER NOT NULL DEFAULT 0`,
	}
	for _, a := range addCols {
		_, _ = s.db.Exec(a)
	}
	return nil
}

// CreatePod inserts a pod in Pending phase.
func (s *Store) CreatePod(p *api.Pod) error {
	specBytes, _ := json.Marshal(p.Spec)
	metaBytes, _ := json.Marshal(p.Metadata)
	now := time.Now().UTC()
	if p.Status.Phase == "" {
		p.Status.Phase = api.PhasePending
	}
	p.Status.LastUpdated = now
	_, err := s.db.Exec(`INSERT INTO pods(name, namespace, spec_json, metadata_json, phase, node_name, container_id, message, pod_ip, host_port, last_updated, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.Metadata.Name, nsOrDefault(p.Metadata.Namespace), string(specBytes), string(metaBytes),
		p.Status.Phase, p.Status.NodeName, p.Status.ContainerID, p.Status.Message,
		p.Status.PodIP, p.Status.HostPort, now, now)
	return err
}

// UpsertPod inserts or replaces spec/metadata (keeps status if existing? simpler: replace).
func (s *Store) UpsertPod(p *api.Pod) error {
	existing, err := s.GetPod(p.Metadata.Name)
	if err == nil {
		// preserve existing status
		p.Status = existing.Status
		specBytes, _ := json.Marshal(p.Spec)
		metaBytes, _ := json.Marshal(p.Metadata)
		_, err := s.db.Exec(`UPDATE pods SET namespace=?, spec_json=?, metadata_json=?, last_updated=? WHERE name=?`,
			nsOrDefault(p.Metadata.Namespace), string(specBytes), string(metaBytes), time.Now().UTC(), p.Metadata.Name)
		return err
	}
	if errors.Is(err, ErrNotFound) {
		return s.CreatePod(p)
	}
	return err
}

// GetPod returns a pod by name.
func (s *Store) GetPod(name string) (*api.Pod, error) {
	row := s.db.QueryRow(`SELECT spec_json, metadata_json, phase, node_name, container_id, message, pod_ip, host_port, last_updated FROM pods WHERE name=?`, name)
	var specJSON, metaJSON, phase, node, cid, msg, podIP string
	var hostPort int
	var updated time.Time
	if err := row.Scan(&specJSON, &metaJSON, &phase, &node, &cid, &msg, &podIP, &hostPort, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p := &api.Pod{APIVersion: "v1", Kind: "Pod"}
	_ = json.Unmarshal([]byte(specJSON), &p.Spec)
	_ = json.Unmarshal([]byte(metaJSON), &p.Metadata)
	p.Status = api.PodStatus{Phase: phase, NodeName: node, ContainerID: cid, Message: msg, PodIP: podIP, HostPort: hostPort, LastUpdated: updated}
	return p, nil
}

func scanPodRows(rows *sql.Rows) ([]*api.Pod, error) {
	defer rows.Close()
	var out []*api.Pod
	for rows.Next() {
		var specJSON, metaJSON, phase, node, cid, msg, podIP string
		var hostPort int
		var updated time.Time
		if err := rows.Scan(&specJSON, &metaJSON, &phase, &node, &cid, &msg, &podIP, &hostPort, &updated); err != nil {
			return nil, err
		}
		p := &api.Pod{APIVersion: "v1", Kind: "Pod"}
		_ = json.Unmarshal([]byte(specJSON), &p.Spec)
		_ = json.Unmarshal([]byte(metaJSON), &p.Metadata)
		p.Status = api.PodStatus{Phase: phase, NodeName: node, ContainerID: cid, Message: msg, PodIP: podIP, HostPort: hostPort, LastUpdated: updated}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListPods returns all pods, optionally filtered by phase.
func (s *Store) ListPods(phaseFilter string) ([]*api.Pod, error) {
	var rows *sql.Rows
	var err error
	if phaseFilter != "" {
		rows, err = s.db.Query(`SELECT spec_json, metadata_json, phase, node_name, container_id, message, pod_ip, host_port, last_updated FROM pods WHERE phase=? ORDER BY created_at ASC`, phaseFilter)
	} else {
		rows, err = s.db.Query(`SELECT spec_json, metadata_json, phase, node_name, container_id, message, pod_ip, host_port, last_updated FROM pods ORDER BY created_at ASC`)
	}
	if err != nil {
		return nil, err
	}
	return scanPodRows(rows)
}

// ListPodsByNode returns pods scheduled to node.
func (s *Store) ListPodsByNode(node string) ([]*api.Pod, error) {
	rows, err := s.db.Query(`SELECT spec_json, metadata_json, phase, node_name, container_id, message, pod_ip, host_port, last_updated FROM pods WHERE node_name=? ORDER BY created_at ASC`, node)
	if err != nil {
		return nil, err
	}
	return scanPodRows(rows)
}

// UpdateStatus updates the status columns for a pod.
func (s *Store) UpdateStatus(name string, status api.PodStatus) error {
	status.LastUpdated = time.Now().UTC()
	res, err := s.db.Exec(`UPDATE pods SET phase=?, node_name=?, container_id=?, message=?, pod_ip=?, host_port=?, last_updated=? WHERE name=?`,
		status.Phase, status.NodeName, status.ContainerID, status.Message, status.PodIP, status.HostPort, status.LastUpdated, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePod removes a pod.
func (s *Store) DeletePod(name string) error {
	res, err := s.db.Exec(`DELETE FROM pods WHERE name=?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func nsOrDefault(ns string) string {
	if ns == "" {
		return "default"
	}
	return ns
}

// ---------------- ReplicaSet ----------------

// UpsertReplicaSet inserts or updates an RS, preserving status on update.
func (s *Store) UpsertReplicaSet(rs *api.ReplicaSet) error {
	now := time.Now().UTC()
	existing, err := s.GetReplicaSet(rs.Metadata.Name)
	if err == nil {
		rs.Status = existing.Status
		specB, _ := json.Marshal(rs.Spec)
		metaB, _ := json.Marshal(rs.Metadata)
		statusB, _ := json.Marshal(rs.Status)
		_, err := s.db.Exec(`UPDATE replicasets SET namespace=?, spec_json=?, metadata_json=?, status_json=?, last_updated=? WHERE name=?`,
			nsOrDefault(rs.Metadata.Namespace), string(specB), string(metaB), string(statusB), now, rs.Metadata.Name)
		return err
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	rs.Status.LastUpdated = now
	specB, _ := json.Marshal(rs.Spec)
	metaB, _ := json.Marshal(rs.Metadata)
	statusB, _ := json.Marshal(rs.Status)
	_, err = s.db.Exec(`INSERT INTO replicasets(name, namespace, spec_json, metadata_json, status_json, created_at, last_updated) VALUES(?,?,?,?,?,?,?)`,
		rs.Metadata.Name, nsOrDefault(rs.Metadata.Namespace), string(specB), string(metaB), string(statusB), now, now)
	return err
}

func (s *Store) GetReplicaSet(name string) (*api.ReplicaSet, error) {
	row := s.db.QueryRow(`SELECT spec_json, metadata_json, status_json FROM replicasets WHERE name=?`, name)
	var specJ, metaJ, statusJ string
	if err := row.Scan(&specJ, &metaJ, &statusJ); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rs := &api.ReplicaSet{APIVersion: "v1", Kind: "ReplicaSet"}
	_ = json.Unmarshal([]byte(specJ), &rs.Spec)
	_ = json.Unmarshal([]byte(metaJ), &rs.Metadata)
	_ = json.Unmarshal([]byte(statusJ), &rs.Status)
	return rs, nil
}

func (s *Store) ListReplicaSets() ([]*api.ReplicaSet, error) {
	rows, err := s.db.Query(`SELECT spec_json, metadata_json, status_json FROM replicasets ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.ReplicaSet
	for rows.Next() {
		var specJ, metaJ, statusJ string
		if err := rows.Scan(&specJ, &metaJ, &statusJ); err != nil {
			return nil, err
		}
		rs := &api.ReplicaSet{APIVersion: "v1", Kind: "ReplicaSet"}
		_ = json.Unmarshal([]byte(specJ), &rs.Spec)
		_ = json.Unmarshal([]byte(metaJ), &rs.Metadata)
		_ = json.Unmarshal([]byte(statusJ), &rs.Status)
		out = append(out, rs)
	}
	return out, rows.Err()
}

func (s *Store) UpdateReplicaSetStatus(name string, status api.ReplicaSetStatus) error {
	status.LastUpdated = time.Now().UTC()
	b, _ := json.Marshal(status)
	res, err := s.db.Exec(`UPDATE replicasets SET status_json=?, last_updated=? WHERE name=?`, string(b), status.LastUpdated, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteReplicaSet(name string) error {
	res, err := s.db.Exec(`DELETE FROM replicasets WHERE name=?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------- Deployment ----------------

func (s *Store) UpsertDeployment(d *api.Deployment) error {
	now := time.Now().UTC()
	existing, err := s.GetDeployment(d.Metadata.Name)
	if err == nil {
		d.Status = existing.Status
		specB, _ := json.Marshal(d.Spec)
		metaB, _ := json.Marshal(d.Metadata)
		statusB, _ := json.Marshal(d.Status)
		_, err := s.db.Exec(`UPDATE deployments SET namespace=?, spec_json=?, metadata_json=?, status_json=?, last_updated=? WHERE name=?`,
			nsOrDefault(d.Metadata.Namespace), string(specB), string(metaB), string(statusB), now, d.Metadata.Name)
		return err
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	d.Status.LastUpdated = now
	specB, _ := json.Marshal(d.Spec)
	metaB, _ := json.Marshal(d.Metadata)
	statusB, _ := json.Marshal(d.Status)
	_, err = s.db.Exec(`INSERT INTO deployments(name, namespace, spec_json, metadata_json, status_json, created_at, last_updated) VALUES(?,?,?,?,?,?,?)`,
		d.Metadata.Name, nsOrDefault(d.Metadata.Namespace), string(specB), string(metaB), string(statusB), now, now)
	return err
}

func (s *Store) GetDeployment(name string) (*api.Deployment, error) {
	row := s.db.QueryRow(`SELECT spec_json, metadata_json, status_json FROM deployments WHERE name=?`, name)
	var specJ, metaJ, statusJ string
	if err := row.Scan(&specJ, &metaJ, &statusJ); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	d := &api.Deployment{APIVersion: "apps/v1", Kind: "Deployment"}
	_ = json.Unmarshal([]byte(specJ), &d.Spec)
	_ = json.Unmarshal([]byte(metaJ), &d.Metadata)
	_ = json.Unmarshal([]byte(statusJ), &d.Status)
	return d, nil
}

func (s *Store) ListDeployments() ([]*api.Deployment, error) {
	rows, err := s.db.Query(`SELECT spec_json, metadata_json, status_json FROM deployments ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.Deployment
	for rows.Next() {
		var specJ, metaJ, statusJ string
		if err := rows.Scan(&specJ, &metaJ, &statusJ); err != nil {
			return nil, err
		}
		d := &api.Deployment{APIVersion: "apps/v1", Kind: "Deployment"}
		_ = json.Unmarshal([]byte(specJ), &d.Spec)
		_ = json.Unmarshal([]byte(metaJ), &d.Metadata)
		_ = json.Unmarshal([]byte(statusJ), &d.Status)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) UpdateDeploymentStatus(name string, status api.DeploymentStatus) error {
	status.LastUpdated = time.Now().UTC()
	b, _ := json.Marshal(status)
	res, err := s.db.Exec(`UPDATE deployments SET status_json=?, last_updated=? WHERE name=?`, string(b), status.LastUpdated, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteDeployment(name string) error {
	res, err := s.db.Exec(`DELETE FROM deployments WHERE name=?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------- Service ----------------

// UpsertService inserts or updates a Service. On insert, if ClusterIP is
// empty, the caller should assign one before calling.
func (s *Store) UpsertService(svc *api.Service) error {
	now := time.Now().UTC()
	existing, err := s.GetService(svc.Metadata.Name)
	if err == nil {
		// Preserve cluster IP + status on update.
		svc.Spec.ClusterIP = existing.Spec.ClusterIP
		svc.Status = existing.Status
		specB, _ := json.Marshal(svc.Spec)
		metaB, _ := json.Marshal(svc.Metadata)
		statusB, _ := json.Marshal(svc.Status)
		_, err := s.db.Exec(`UPDATE services SET namespace=?, spec_json=?, metadata_json=?, status_json=?, cluster_ip=?, last_updated=? WHERE name=?`,
			nsOrDefault(svc.Metadata.Namespace), string(specB), string(metaB), string(statusB), svc.Spec.ClusterIP, now, svc.Metadata.Name)
		return err
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	svc.Status.LastUpdated = now
	specB, _ := json.Marshal(svc.Spec)
	metaB, _ := json.Marshal(svc.Metadata)
	statusB, _ := json.Marshal(svc.Status)
	_, err = s.db.Exec(`INSERT INTO services(name, namespace, spec_json, metadata_json, status_json, cluster_ip, created_at, last_updated) VALUES(?,?,?,?,?,?,?,?)`,
		svc.Metadata.Name, nsOrDefault(svc.Metadata.Namespace), string(specB), string(metaB), string(statusB), svc.Spec.ClusterIP, now, now)
	return err
}

func (s *Store) GetService(name string) (*api.Service, error) {
	row := s.db.QueryRow(`SELECT spec_json, metadata_json, status_json FROM services WHERE name=?`, name)
	var specJ, metaJ, statusJ string
	if err := row.Scan(&specJ, &metaJ, &statusJ); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	svc := &api.Service{APIVersion: "v1", Kind: "Service"}
	_ = json.Unmarshal([]byte(specJ), &svc.Spec)
	_ = json.Unmarshal([]byte(metaJ), &svc.Metadata)
	_ = json.Unmarshal([]byte(statusJ), &svc.Status)
	return svc, nil
}

func (s *Store) ListServices() ([]*api.Service, error) {
	rows, err := s.db.Query(`SELECT spec_json, metadata_json, status_json FROM services ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.Service
	for rows.Next() {
		var specJ, metaJ, statusJ string
		if err := rows.Scan(&specJ, &metaJ, &statusJ); err != nil {
			return nil, err
		}
		svc := &api.Service{APIVersion: "v1", Kind: "Service"}
		_ = json.Unmarshal([]byte(specJ), &svc.Spec)
		_ = json.Unmarshal([]byte(metaJ), &svc.Metadata)
		_ = json.Unmarshal([]byte(statusJ), &svc.Status)
		out = append(out, svc)
	}
	return out, rows.Err()
}

// ListClusterIPs returns the set of already-allocated cluster IPs.
func (s *Store) ListClusterIPs() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT cluster_ip FROM services WHERE cluster_ip != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, err
		}
		out[ip] = true
	}
	return out, rows.Err()
}

func (s *Store) DeleteService(name string) error {
	res, err := s.db.Exec(`DELETE FROM services WHERE name=?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	// Endpoints live under same name; clean up.
	_, _ = s.db.Exec(`DELETE FROM endpoints WHERE name=?`, name)
	return nil
}

// ---------------- Endpoints ----------------

func (s *Store) UpsertEndpoints(ep *api.Endpoints) error {
	now := time.Now().UTC()
	ep.LastUpdated = now
	metaB, _ := json.Marshal(ep.Metadata)
	subB, _ := json.Marshal(ep.Subsets)
	_, err := s.db.Exec(`INSERT INTO endpoints(name, namespace, metadata_json, subsets_json, last_updated) VALUES(?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET namespace=excluded.namespace, metadata_json=excluded.metadata_json, subsets_json=excluded.subsets_json, last_updated=excluded.last_updated`,
		ep.Metadata.Name, nsOrDefault(ep.Metadata.Namespace), string(metaB), string(subB), now)
	return err
}

func (s *Store) GetEndpoints(name string) (*api.Endpoints, error) {
	row := s.db.QueryRow(`SELECT metadata_json, subsets_json, last_updated FROM endpoints WHERE name=?`, name)
	var metaJ, subJ string
	var updated time.Time
	if err := row.Scan(&metaJ, &subJ, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	ep := &api.Endpoints{APIVersion: "v1", Kind: "Endpoints"}
	_ = json.Unmarshal([]byte(metaJ), &ep.Metadata)
	_ = json.Unmarshal([]byte(subJ), &ep.Subsets)
	ep.LastUpdated = updated
	return ep, nil
}

// ---------------- Nodes (M4) ----------------

// UpsertNode inserts/updates a registered node and stamps last_heartbeat=now.
func (s *Store) UpsertNode(n *api.Node) error {
	now := time.Now().UTC()
	n.Status.LastHeartbeat = now
	metaB, _ := json.Marshal(n.Metadata)
	statusB, _ := json.Marshal(n.Status)
	_, err := s.db.Exec(`INSERT INTO nodes(name, metadata_json, status_json, created_at, last_heartbeat) VALUES(?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET metadata_json=excluded.metadata_json, status_json=excluded.status_json, last_heartbeat=excluded.last_heartbeat`,
		n.Metadata.Name, string(metaB), string(statusB), now, now)
	return err
}

func (s *Store) GetNode(name string) (*api.Node, error) {
	row := s.db.QueryRow(`SELECT metadata_json, status_json FROM nodes WHERE name=?`, name)
	var metaJ, statusJ string
	if err := row.Scan(&metaJ, &statusJ); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	n := &api.Node{APIVersion: "v1", Kind: "Node"}
	_ = json.Unmarshal([]byte(metaJ), &n.Metadata)
	_ = json.Unmarshal([]byte(statusJ), &n.Status)
	return n, nil
}

func (s *Store) ListNodes() ([]*api.Node, error) {
	rows, err := s.db.Query(`SELECT metadata_json, status_json FROM nodes ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.Node
	for rows.Next() {
		var metaJ, statusJ string
		if err := rows.Scan(&metaJ, &statusJ); err != nil {
			return nil, err
		}
		n := &api.Node{APIVersion: "v1", Kind: "Node"}
		_ = json.Unmarshal([]byte(metaJ), &n.Metadata)
		_ = json.Unmarshal([]byte(statusJ), &n.Status)
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) DeleteNode(name string) error {
	res, err := s.db.Exec(`DELETE FROM nodes WHERE name=?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------- CRDs (M4) ----------------

func (s *Store) UpsertCRD(c *api.CRD) error {
	now := time.Now().UTC()
	specB, _ := json.Marshal(c.Spec)
	metaB, _ := json.Marshal(c.Metadata)
	_, err := s.db.Exec(`INSERT INTO crds(name, group_name, version, plural, kind, singular, scope, spec_json, metadata_json, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET group_name=excluded.group_name, version=excluded.version, plural=excluded.plural, kind=excluded.kind, singular=excluded.singular, scope=excluded.scope, spec_json=excluded.spec_json, metadata_json=excluded.metadata_json`,
		c.Metadata.Name, c.Spec.Group, c.Spec.Version, c.Spec.Names.Plural, c.Spec.Names.Kind, c.Spec.Names.Singular, c.Spec.Scope, string(specB), string(metaB), now)
	return err
}

func (s *Store) GetCRD(name string) (*api.CRD, error) {
	row := s.db.QueryRow(`SELECT spec_json, metadata_json FROM crds WHERE name=?`, name)
	var specJ, metaJ string
	if err := row.Scan(&specJ, &metaJ); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c := &api.CRD{APIVersion: "mk.io/v1", Kind: "CustomResourceDefinition"}
	_ = json.Unmarshal([]byte(specJ), &c.Spec)
	_ = json.Unmarshal([]byte(metaJ), &c.Metadata)
	return c, nil
}

// GetCRDByPlural returns a CRD indexed by its plural REST name (e.g. "databases").
func (s *Store) GetCRDByPlural(plural string) (*api.CRD, error) {
	row := s.db.QueryRow(`SELECT spec_json, metadata_json, name FROM crds WHERE plural=?`, plural)
	var specJ, metaJ, name string
	if err := row.Scan(&specJ, &metaJ, &name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c := &api.CRD{APIVersion: "mk.io/v1", Kind: "CustomResourceDefinition"}
	_ = json.Unmarshal([]byte(specJ), &c.Spec)
	_ = json.Unmarshal([]byte(metaJ), &c.Metadata)
	c.Metadata.Name = name
	return c, nil
}

func (s *Store) ListCRDs() ([]*api.CRD, error) {
	rows, err := s.db.Query(`SELECT spec_json, metadata_json, name FROM crds ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.CRD
	for rows.Next() {
		var specJ, metaJ, name string
		if err := rows.Scan(&specJ, &metaJ, &name); err != nil {
			return nil, err
		}
		c := &api.CRD{APIVersion: "mk.io/v1", Kind: "CustomResourceDefinition"}
		_ = json.Unmarshal([]byte(specJ), &c.Spec)
		_ = json.Unmarshal([]byte(metaJ), &c.Metadata)
		c.Metadata.Name = name
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCRD(name string) error {
	res, err := s.db.Exec(`DELETE FROM crds WHERE name=?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	_, _ = s.db.Exec(`DELETE FROM custom_resources WHERE crd_name=?`, name)
	return nil
}

// ---------------- Custom Resources (M4) ----------------

func (s *Store) UpsertCustomResource(crdName string, cr *api.CustomResource) error {
	now := time.Now().UTC()
	bodyB, _ := json.Marshal(cr)
	_, err := s.db.Exec(`INSERT INTO custom_resources(crd_name, name, namespace, body_json, created_at, last_updated)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(crd_name, name) DO UPDATE SET namespace=excluded.namespace, body_json=excluded.body_json, last_updated=excluded.last_updated`,
		crdName, cr.Metadata.Name, nsOrDefault(cr.Metadata.Namespace), string(bodyB), now, now)
	return err
}

func (s *Store) GetCustomResource(crdName, name string) (*api.CustomResource, error) {
	row := s.db.QueryRow(`SELECT body_json FROM custom_resources WHERE crd_name=? AND name=?`, crdName, name)
	var bodyJ string
	if err := row.Scan(&bodyJ); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	cr := &api.CustomResource{}
	_ = json.Unmarshal([]byte(bodyJ), cr)
	return cr, nil
}

func (s *Store) ListCustomResources(crdName string) ([]*api.CustomResource, error) {
	rows, err := s.db.Query(`SELECT body_json FROM custom_resources WHERE crd_name=? ORDER BY created_at ASC`, crdName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.CustomResource
	for rows.Next() {
		var bodyJ string
		if err := rows.Scan(&bodyJ); err != nil {
			return nil, err
		}
		cr := &api.CustomResource{}
		_ = json.Unmarshal([]byte(bodyJ), cr)
		out = append(out, cr)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCustomResource(crdName, name string) error {
	res, err := s.db.Exec(`DELETE FROM custom_resources WHERE crd_name=? AND name=?`, crdName, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------- Users (M4 RBAC) ----------------

func (s *Store) UpsertUser(u *api.User) error {
	now := time.Now().UTC()
	groupsB, _ := json.Marshal(u.Groups)
	_, err := s.db.Exec(`INSERT INTO users(name, token, groups_json, created_at) VALUES(?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET token=excluded.token, groups_json=excluded.groups_json`,
		u.Metadata.Name, u.Token, string(groupsB), now)
	return err
}

func (s *Store) GetUserByToken(token string) (*api.User, error) {
	row := s.db.QueryRow(`SELECT name, token, groups_json FROM users WHERE token=?`, token)
	var name, tok, gJ string
	if err := row.Scan(&name, &tok, &gJ); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u := &api.User{APIVersion: "mk.io/v1", Kind: "User"}
	u.Metadata.Name = name
	u.Token = tok
	_ = json.Unmarshal([]byte(gJ), &u.Groups)
	return u, nil
}

func (s *Store) ListUsers() ([]*api.User, error) {
	rows, err := s.db.Query(`SELECT name, token, groups_json FROM users ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.User
	for rows.Next() {
		var name, tok, gJ string
		if err := rows.Scan(&name, &tok, &gJ); err != nil {
			return nil, err
		}
		u := &api.User{APIVersion: "mk.io/v1", Kind: "User"}
		u.Metadata.Name = name
		u.Token = tok
		_ = json.Unmarshal([]byte(gJ), &u.Groups)
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) DeleteUser(name string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE name=?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------- Roles (M4 RBAC) ----------------
// scope is "Role" or "ClusterRole".

func (s *Store) UpsertRole(scope string, r *api.Role) error {
	now := time.Now().UTC()
	rulesB, _ := json.Marshal(r.Rules)
	metaB, _ := json.Marshal(r.Metadata)
	_, err := s.db.Exec(`INSERT INTO roles(name, scope, namespace, rules_json, metadata_json, created_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(scope, name) DO UPDATE SET namespace=excluded.namespace, rules_json=excluded.rules_json, metadata_json=excluded.metadata_json`,
		r.Metadata.Name, scope, nsOrDefault(r.Metadata.Namespace), string(rulesB), string(metaB), now)
	return err
}

func (s *Store) GetRole(scope, name string) (*api.Role, error) {
	row := s.db.QueryRow(`SELECT rules_json, metadata_json FROM roles WHERE scope=? AND name=?`, scope, name)
	var rulesJ, metaJ string
	if err := row.Scan(&rulesJ, &metaJ); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r := &api.Role{APIVersion: "rbac.mk.io/v1", Kind: scope}
	_ = json.Unmarshal([]byte(rulesJ), &r.Rules)
	_ = json.Unmarshal([]byte(metaJ), &r.Metadata)
	return r, nil
}

func (s *Store) ListRoles(scope string) ([]*api.Role, error) {
	rows, err := s.db.Query(`SELECT rules_json, metadata_json FROM roles WHERE scope=? ORDER BY created_at ASC`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.Role
	for rows.Next() {
		var rulesJ, metaJ string
		if err := rows.Scan(&rulesJ, &metaJ); err != nil {
			return nil, err
		}
		r := &api.Role{APIVersion: "rbac.mk.io/v1", Kind: scope}
		_ = json.Unmarshal([]byte(rulesJ), &r.Rules)
		_ = json.Unmarshal([]byte(metaJ), &r.Metadata)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteRole(scope, name string) error {
	res, err := s.db.Exec(`DELETE FROM roles WHERE scope=? AND name=?`, scope, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------- RoleBindings ----------------
// scope is "RoleBinding" or "ClusterRoleBinding".

func (s *Store) UpsertRoleBinding(scope string, b *api.RoleBinding) error {
	now := time.Now().UTC()
	subB, _ := json.Marshal(b.Subjects)
	refB, _ := json.Marshal(b.RoleRef)
	metaB, _ := json.Marshal(b.Metadata)
	_, err := s.db.Exec(`INSERT INTO role_bindings(name, scope, namespace, subjects_json, roleref_json, metadata_json, created_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(scope, name) DO UPDATE SET namespace=excluded.namespace, subjects_json=excluded.subjects_json, roleref_json=excluded.roleref_json, metadata_json=excluded.metadata_json`,
		b.Metadata.Name, scope, nsOrDefault(b.Metadata.Namespace), string(subB), string(refB), string(metaB), now)
	return err
}

func (s *Store) ListRoleBindings(scope string) ([]*api.RoleBinding, error) {
	rows, err := s.db.Query(`SELECT subjects_json, roleref_json, metadata_json FROM role_bindings WHERE scope=? ORDER BY created_at ASC`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.RoleBinding
	for rows.Next() {
		var subJ, refJ, metaJ string
		if err := rows.Scan(&subJ, &refJ, &metaJ); err != nil {
			return nil, err
		}
		b := &api.RoleBinding{APIVersion: "rbac.mk.io/v1", Kind: scope}
		_ = json.Unmarshal([]byte(subJ), &b.Subjects)
		_ = json.Unmarshal([]byte(refJ), &b.RoleRef)
		_ = json.Unmarshal([]byte(metaJ), &b.Metadata)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) DeleteRoleBinding(scope, name string) error {
	res, err := s.db.Exec(`DELETE FROM role_bindings WHERE scope=? AND name=?`, scope, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------- M5: Leases (leader election) ----------------

// TryAcquireLease attempts to become the holder of the given lease. It
// succeeds if the lease does not exist, is already held by this identity,
// or has expired (renew_time older than ttl). Returns (true, currentHolder)
// on success, (false, currentHolder) otherwise.
func (s *Store) TryAcquireLease(name, identity string, ttl time.Duration) (bool, string, error) {
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return false, "", err
	}
	defer tx.Rollback()
	var holder string
	var renew time.Time
	var ttlSec int
	row := tx.QueryRow(`SELECT holder, renew_time, ttl_seconds FROM leases WHERE name=?`, name)
	err = row.Scan(&holder, &renew, &ttlSec)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.Exec(`INSERT INTO leases(name, holder, renew_time, ttl_seconds) VALUES(?,?,?,?)`,
			name, identity, now, int(ttl.Seconds())); err != nil {
			return false, "", err
		}
		if err := tx.Commit(); err != nil {
			return false, "", err
		}
		return true, identity, nil
	}
	if err != nil {
		return false, "", err
	}
	expired := now.Sub(renew) > time.Duration(ttlSec)*time.Second
	if holder == identity || expired {
		if _, err := tx.Exec(`UPDATE leases SET holder=?, renew_time=?, ttl_seconds=? WHERE name=?`,
			identity, now, int(ttl.Seconds()), name); err != nil {
			return false, "", err
		}
		if err := tx.Commit(); err != nil {
			return false, "", err
		}
		return true, identity, nil
	}
	return false, holder, nil
}

// GetLease returns the current holder + renew time, for observability.
func (s *Store) GetLease(name string) (string, time.Time, int, error) {
	row := s.db.QueryRow(`SELECT holder, renew_time, ttl_seconds FROM leases WHERE name=?`, name)
	var holder string
	var renew time.Time
	var ttl int
	if err := row.Scan(&holder, &renew, &ttl); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", time.Time{}, 0, ErrNotFound
		}
		return "", time.Time{}, 0, err
	}
	return holder, renew, ttl, nil
}

// ---------------- M5: Admission Webhooks ----------------

func (s *Store) UpsertWebhook(w *api.AdmissionWebhook) error {
	if w.FailurePolicy == "" {
		w.FailurePolicy = "Fail"
	}
	resB, _ := json.Marshal(w.Resources)
	_, err := s.db.Exec(`INSERT INTO admission_webhooks(name, type, url, resources_json, failure_policy, created_at)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET type=excluded.type, url=excluded.url, resources_json=excluded.resources_json, failure_policy=excluded.failure_policy`,
		w.Metadata.Name, w.Type, w.URL, string(resB), w.FailurePolicy, time.Now().UTC())
	return err
}

func (s *Store) ListWebhooks() ([]*api.AdmissionWebhook, error) {
	rows, err := s.db.Query(`SELECT name, type, url, resources_json, failure_policy FROM admission_webhooks ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.AdmissionWebhook
	for rows.Next() {
		var name, typ, url, resJ, fp string
		if err := rows.Scan(&name, &typ, &url, &resJ, &fp); err != nil {
			return nil, err
		}
		w := &api.AdmissionWebhook{APIVersion: "admission.mk.io/v1", Kind: "AdmissionWebhook"}
		w.Metadata.Name = name
		w.Type = typ
		w.URL = url
		w.FailurePolicy = fp
		_ = json.Unmarshal([]byte(resJ), &w.Resources)
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) DeleteWebhook(name string) error {
	res, err := s.db.Exec(`DELETE FROM admission_webhooks WHERE name=?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------- M5: HPA ----------------

func (s *Store) UpsertHPA(h *api.HPA) error {
	now := time.Now().UTC()
	existing, err := s.GetHPA(h.Metadata.Name)
	if err == nil {
		h.Status = existing.Status
	}
	specB, _ := json.Marshal(h.Spec)
	metaB, _ := json.Marshal(h.Metadata)
	statusB, _ := json.Marshal(h.Status)
	_, err = s.db.Exec(`INSERT INTO hpas(name, namespace, spec_json, metadata_json, status_json, created_at, last_updated)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET namespace=excluded.namespace, spec_json=excluded.spec_json, metadata_json=excluded.metadata_json, status_json=excluded.status_json, last_updated=excluded.last_updated`,
		h.Metadata.Name, nsOrDefault(h.Metadata.Namespace), string(specB), string(metaB), string(statusB), now, now)
	return err
}

func (s *Store) GetHPA(name string) (*api.HPA, error) {
	row := s.db.QueryRow(`SELECT spec_json, metadata_json, status_json FROM hpas WHERE name=?`, name)
	var specJ, metaJ, statusJ string
	if err := row.Scan(&specJ, &metaJ, &statusJ); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	h := &api.HPA{APIVersion: "autoscaling.mk.io/v1", Kind: "HorizontalPodAutoscaler"}
	_ = json.Unmarshal([]byte(specJ), &h.Spec)
	_ = json.Unmarshal([]byte(metaJ), &h.Metadata)
	_ = json.Unmarshal([]byte(statusJ), &h.Status)
	return h, nil
}

func (s *Store) ListHPAs() ([]*api.HPA, error) {
	rows, err := s.db.Query(`SELECT spec_json, metadata_json, status_json FROM hpas ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.HPA
	for rows.Next() {
		var specJ, metaJ, statusJ string
		if err := rows.Scan(&specJ, &metaJ, &statusJ); err != nil {
			return nil, err
		}
		h := &api.HPA{APIVersion: "autoscaling.mk.io/v1", Kind: "HorizontalPodAutoscaler"}
		_ = json.Unmarshal([]byte(specJ), &h.Spec)
		_ = json.Unmarshal([]byte(metaJ), &h.Metadata)
		_ = json.Unmarshal([]byte(statusJ), &h.Status)
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) UpdateHPAStatus(name string, st api.HPAStatus) error {
	st.LastUpdated = time.Now().UTC()
	b, _ := json.Marshal(st)
	res, err := s.db.Exec(`UPDATE hpas SET status_json=?, last_updated=? WHERE name=?`, string(b), st.LastUpdated, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteHPA(name string) error {
	res, err := s.db.Exec(`DELETE FROM hpas WHERE name=?`, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListEndpoints() ([]*api.Endpoints, error) {
	rows, err := s.db.Query(`SELECT metadata_json, subsets_json, last_updated FROM endpoints ORDER BY last_updated ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.Endpoints
	for rows.Next() {
		var metaJ, subJ string
		var updated time.Time
		if err := rows.Scan(&metaJ, &subJ, &updated); err != nil {
			return nil, err
		}
		ep := &api.Endpoints{APIVersion: "v1", Kind: "Endpoints"}
		_ = json.Unmarshal([]byte(metaJ), &ep.Metadata)
		_ = json.Unmarshal([]byte(subJ), &ep.Subsets)
		ep.LastUpdated = updated
		out = append(out, ep)
	}
	return out, rows.Err()
}
