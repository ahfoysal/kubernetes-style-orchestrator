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
