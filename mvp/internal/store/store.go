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
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
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
	}
	for _, s1 := range stmts {
		if _, err := s.db.Exec(s1); err != nil {
			return err
		}
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
	_, err := s.db.Exec(`INSERT INTO pods(name, namespace, spec_json, metadata_json, phase, node_name, container_id, message, last_updated, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		p.Metadata.Name, nsOrDefault(p.Metadata.Namespace), string(specBytes), string(metaBytes),
		p.Status.Phase, p.Status.NodeName, p.Status.ContainerID, p.Status.Message, now, now)
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
	row := s.db.QueryRow(`SELECT spec_json, metadata_json, phase, node_name, container_id, message, last_updated FROM pods WHERE name=?`, name)
	var specJSON, metaJSON, phase, node, cid, msg string
	var updated time.Time
	if err := row.Scan(&specJSON, &metaJSON, &phase, &node, &cid, &msg, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p := &api.Pod{APIVersion: "v1", Kind: "Pod"}
	_ = json.Unmarshal([]byte(specJSON), &p.Spec)
	_ = json.Unmarshal([]byte(metaJSON), &p.Metadata)
	p.Status = api.PodStatus{Phase: phase, NodeName: node, ContainerID: cid, Message: msg, LastUpdated: updated}
	return p, nil
}

// ListPods returns all pods, optionally filtered by phase.
func (s *Store) ListPods(phaseFilter string) ([]*api.Pod, error) {
	var rows *sql.Rows
	var err error
	if phaseFilter != "" {
		rows, err = s.db.Query(`SELECT spec_json, metadata_json, phase, node_name, container_id, message, last_updated FROM pods WHERE phase=? ORDER BY created_at ASC`, phaseFilter)
	} else {
		rows, err = s.db.Query(`SELECT spec_json, metadata_json, phase, node_name, container_id, message, last_updated FROM pods ORDER BY created_at ASC`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.Pod
	for rows.Next() {
		var specJSON, metaJSON, phase, node, cid, msg string
		var updated time.Time
		if err := rows.Scan(&specJSON, &metaJSON, &phase, &node, &cid, &msg, &updated); err != nil {
			return nil, err
		}
		p := &api.Pod{APIVersion: "v1", Kind: "Pod"}
		_ = json.Unmarshal([]byte(specJSON), &p.Spec)
		_ = json.Unmarshal([]byte(metaJSON), &p.Metadata)
		p.Status = api.PodStatus{Phase: phase, NodeName: node, ContainerID: cid, Message: msg, LastUpdated: updated}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListPodsByNode returns pods scheduled to node.
func (s *Store) ListPodsByNode(node string) ([]*api.Pod, error) {
	rows, err := s.db.Query(`SELECT spec_json, metadata_json, phase, node_name, container_id, message, last_updated FROM pods WHERE node_name=? ORDER BY created_at ASC`, node)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.Pod
	for rows.Next() {
		var specJSON, metaJSON, phase, node, cid, msg string
		var updated time.Time
		if err := rows.Scan(&specJSON, &metaJSON, &phase, &node, &cid, &msg, &updated); err != nil {
			return nil, err
		}
		p := &api.Pod{APIVersion: "v1", Kind: "Pod"}
		_ = json.Unmarshal([]byte(specJSON), &p.Spec)
		_ = json.Unmarshal([]byte(metaJSON), &p.Metadata)
		p.Status = api.PodStatus{Phase: phase, NodeName: node, ContainerID: cid, Message: msg, LastUpdated: updated}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateStatus updates the status columns for a pod.
func (s *Store) UpdateStatus(name string, status api.PodStatus) error {
	status.LastUpdated = time.Now().UTC()
	res, err := s.db.Exec(`UPDATE pods SET phase=?, node_name=?, container_id=?, message=?, last_updated=? WHERE name=?`,
		status.Phase, status.NodeName, status.ContainerID, status.Message, status.LastUpdated, name)
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
