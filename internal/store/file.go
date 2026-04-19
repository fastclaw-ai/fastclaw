package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileStore implements Store using the local filesystem under ~/.fastclaw/.
type FileStore struct {
	rootDir string // ~/.fastclaw
}

func NewFileStore(rootDir string) *FileStore {
	return &FileStore{rootDir: rootDir}
}

func (f *FileStore) Close() error { return nil }

// agentDir returns ~/.fastclaw/agents/{agentID}/agent/
func (f *FileStore) agentDir(agentID string) string {
	return filepath.Join(f.rootDir, "agents", agentID, "agent")
}

// --- Config ---

func (f *FileStore) GetConfig(ctx context.Context) (*GlobalConfig, error) {
	path := filepath.Join(f.rootDir, "fastclaw.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)
	info, _ := os.Stat(path)
	return &GlobalConfig{Data: raw, UpdatedAt: info.ModTime()}, nil
}

func (f *FileStore) SaveConfig(ctx context.Context, cfg *GlobalConfig) error {
	path := filepath.Join(f.rootDir, "fastclaw.json")
	data, _ := json.MarshalIndent(cfg.Data, "", "  ")
	return os.WriteFile(path, data, 0o644)
}

// --- Agents ---

func (f *FileStore) ListAgents(ctx context.Context) ([]AgentRecord, error) {
	agentsDir := filepath.Join(f.rootDir, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return nil, nil
	}
	var agents []AgentRecord
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ag, err := f.GetAgent(ctx, e.Name())
		if err != nil {
			continue
		}
		agents = append(agents, *ag)
	}
	return agents, nil
}

func (f *FileStore) GetAgent(ctx context.Context, agentID string) (*AgentRecord, error) {
	wsDir := f.agentDir(agentID)
	if _, err := os.Stat(wsDir); err != nil {
		return nil, fmt.Errorf("agent not found: %s", agentID)
	}
	rec := &AgentRecord{ID: agentID, Name: agentID}
	if data, err := os.ReadFile(filepath.Join(wsDir, "agent.json")); err == nil {
		json.Unmarshal(data, &rec.Config)
		if m, ok := rec.Config["model"].(string); ok {
			rec.Model = m
		}
	}
	return rec, nil
}

func (f *FileStore) SaveAgent(ctx context.Context, agent *AgentRecord) error {
	wsDir := f.agentDir(agent.ID)
	os.MkdirAll(wsDir, 0o755)
	if agent.Config != nil {
		data, _ := json.MarshalIndent(agent.Config, "", "  ")
		os.WriteFile(filepath.Join(wsDir, "agent.json"), data, 0o644)
	}
	return nil
}

func (f *FileStore) DeleteAgent(ctx context.Context, agentID string) error {
	return os.RemoveAll(filepath.Join(f.rootDir, "agents", agentID))
}

// --- Sessions ---

func (f *FileStore) sessionPath(agentID, sessionKey string) string {
	safe := strings.ReplaceAll(sessionKey, ":", "_")
	return filepath.Join(f.agentDir(agentID), "sessions", safe+".jsonl")
}

func (f *FileStore) GetSession(ctx context.Context, agentID, sessionKey string) (*SessionRecord, error) {
	path := f.sessionPath(agentID, sessionKey)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var msgs []SessionMessage
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var msg SessionMessage
		if json.Unmarshal([]byte(line), &msg) == nil {
			msgs = append(msgs, msg)
		}
	}
	info, _ := os.Stat(path)
	return &SessionRecord{Messages: msgs, UpdatedAt: info.ModTime()}, nil
}

func (f *FileStore) SaveSession(ctx context.Context, agentID, sessionKey string, session *SessionRecord) error {
	path := f.sessionPath(agentID, sessionKey)
	os.MkdirAll(filepath.Dir(path), 0o755)
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	enc := json.NewEncoder(file)
	for _, msg := range session.Messages {
		enc.Encode(msg)
	}
	return nil
}

func (f *FileStore) ListSessions(ctx context.Context, agentID string) ([]SessionMeta, error) {
	sessDir := filepath.Join(f.agentDir(agentID), "sessions")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		return nil, nil
	}
	var metas []SessionMeta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, _ := e.Info()
		metas = append(metas, SessionMeta{
			Key:       strings.TrimSuffix(e.Name(), ".jsonl"),
			UpdatedAt: info.ModTime(),
		})
	}
	return metas, nil
}

func (f *FileStore) DeleteSession(ctx context.Context, agentID, sessionKey string) error {
	return os.Remove(f.sessionPath(agentID, sessionKey))
}

// --- Memory ---

func (f *FileStore) GetMemory(ctx context.Context, agentID string) (string, error) {
	data, err := os.ReadFile(filepath.Join(f.agentDir(agentID), "MEMORY.md"))
	if err != nil {
		return "", nil
	}
	return string(data), nil
}

func (f *FileStore) SaveMemory(ctx context.Context, agentID, content string) error {
	return os.WriteFile(filepath.Join(f.agentDir(agentID), "MEMORY.md"), []byte(content), 0o644)
}

// --- Workspace Files ---

func (f *FileStore) GetWorkspaceFile(ctx context.Context, agentID, filename string) ([]byte, error) {
	return os.ReadFile(filepath.Join(f.agentDir(agentID), filename))
}

func (f *FileStore) SaveWorkspaceFile(ctx context.Context, agentID, filename string, data []byte) error {
	path := filepath.Join(f.agentDir(agentID), filename)
	os.MkdirAll(filepath.Dir(path), 0o755)
	return os.WriteFile(path, data, 0o644)
}

func (f *FileStore) ListWorkspaceFiles(ctx context.Context, agentID string) ([]string, error) {
	entries, err := os.ReadDir(f.agentDir(agentID))
	if err != nil {
		return nil, nil
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() {
			files = append(files, e.Name())
		}
	}
	return files, nil
}

// --- Cron Jobs ---

func (f *FileStore) cronJobsPath() string {
	return filepath.Join(f.rootDir, "cron_jobs.json")
}

func (f *FileStore) loadCronJobs() ([]CronJobRecord, error) {
	data, err := os.ReadFile(f.cronJobsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var jobs []CronJobRecord
	json.Unmarshal(data, &jobs)
	return jobs, nil
}

func (f *FileStore) saveCronJobs(jobs []CronJobRecord) error {
	data, _ := json.MarshalIndent(jobs, "", "  ")
	return os.WriteFile(f.cronJobsPath(), data, 0o644)
}

func (f *FileStore) ListCronJobs(ctx context.Context) ([]CronJobRecord, error) {
	return f.loadCronJobs()
}

func (f *FileStore) GetCronJob(ctx context.Context, jobID string) (*CronJobRecord, error) {
	jobs, err := f.loadCronJobs()
	if err != nil {
		return nil, err
	}
	for i := range jobs {
		if jobs[i].ID == jobID {
			return &jobs[i], nil
		}
	}
	return nil, fmt.Errorf("cron job not found: %s", jobID)
}

func (f *FileStore) SaveCronJob(ctx context.Context, job *CronJobRecord) error {
	jobs, _ := f.loadCronJobs()
	found := false
	for i := range jobs {
		if jobs[i].ID == job.ID {
			jobs[i] = *job
			found = true
			break
		}
	}
	if !found {
		jobs = append(jobs, *job)
	}
	return f.saveCronJobs(jobs)
}

func (f *FileStore) DeleteCronJob(ctx context.Context, jobID string) error {
	jobs, _ := f.loadCronJobs()
	for i := range jobs {
		if jobs[i].ID == jobID {
			jobs = append(jobs[:i], jobs[i+1:]...)
			return f.saveCronJobs(jobs)
		}
	}
	return fmt.Errorf("cron job not found: %s", jobID)
}

func (f *FileStore) GetDueCronJobs(ctx context.Context, now time.Time) ([]CronJobRecord, error) {
	jobs, _ := f.loadCronJobs()
	var due []CronJobRecord
	for _, j := range jobs {
		if j.Enabled && j.NextRun != nil && !j.NextRun.After(now) {
			due = append(due, j)
		}
	}
	return due, nil
}

func (f *FileStore) LockCronJob(ctx context.Context, jobID, instanceID string) (bool, error) {
	return true, nil
}

func (f *FileStore) UpdateCronJobRun(ctx context.Context, jobID string, lastRun, nextRun time.Time) error {
	jobs, _ := f.loadCronJobs()
	for i := range jobs {
		if jobs[i].ID == jobID {
			jobs[i].LastRun = &lastRun
			jobs[i].NextRun = &nextRun
			return f.saveCronJobs(jobs)
		}
	}
	return fmt.Errorf("cron job not found: %s", jobID)
}

var _ Store = (*FileStore)(nil)
