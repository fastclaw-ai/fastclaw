package agent

import (
	"context"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

type MemoryStoreAdapter struct {
	st store.Store
}

func NewMemoryStoreAdapter(st store.Store) *MemoryStoreAdapter {
	return &MemoryStoreAdapter{st: st}
}

func (a *MemoryStoreAdapter) GetMemory(ctx context.Context, agentID string) (string, error) {
	return a.st.GetMemory(ctx, agentID)
}

func (a *MemoryStoreAdapter) SaveMemory(ctx context.Context, agentID, content string) error {
	return a.st.SaveMemory(ctx, agentID, content)
}

func (a *MemoryStoreAdapter) GetWorkspaceFile(ctx context.Context, agentID, filename string) ([]byte, error) {
	return a.st.GetWorkspaceFile(ctx, agentID, filename)
}

func (a *MemoryStoreAdapter) SaveWorkspaceFile(ctx context.Context, agentID, filename string, data []byte) error {
	return a.st.SaveWorkspaceFile(ctx, agentID, filename, data)
}
