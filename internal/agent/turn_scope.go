package agent

import (
	"context"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// Fork only mutable execution state. Session history and service dependencies
// remain shared; every turn selects its own session before using them.
func (a *Agent) forkTurn() *Agent {
	next := *a
	if a.ctxBuilder != nil {
		builder := *a.ctxBuilder
		next.ctxBuilder = &builder
	}
	if a.registry != nil {
		next.registry = a.registry.Fork()
		tools.RegisterDelegateTask(next.registry, &next)
		if next.projectRuntime != nil {
			next.registerProjectRuntimeTools()
		}
	}
	return &next
}

type turnModelKey struct{}

// WithTurnModel asks the next turn run with ctx to use model
// ("<provider>/<model>") instead of the agent's configured one. Callers
// check it first with ResolveTurnModel; an unusable value is ignored.
func WithTurnModel(ctx context.Context, model string) context.Context {
	if model = strings.TrimSpace(model); model == "" {
		return ctx
	}
	return context.WithValue(ctx, turnModelKey{}, model)
}

// ResolveTurnModel reports whether model can run on this agent for one
// turn, and the provider it would use. Usable means "<provider>/<model>"
// whose provider key has credentials among the agent's providers, or
// whose provider is the one the agent already runs on. Anything else —
// bare names like "gpt-4o" that OpenAI SDKs send by default, unknown
// provider keys — is not usable and leaves the agent's model in charge.
func (a *Agent) ResolveTurnModel(model string) (provider.Provider, bool) {
	model = strings.TrimSpace(model)
	if a == nil || model == "" {
		return nil, false
	}
	if model == a.model {
		return a.provider, a.provider != nil
	}
	prefix, id := provider.SplitProviderModel(model)
	if prefix == "" || id == "" {
		return nil, false
	}
	if pc, ok := a.providers[prefix]; ok && pc.APIKey != "" {
		if cur, _ := provider.SplitProviderModel(a.model); cur == prefix && a.provider != nil {
			return a.provider, true
		}
		return provider.NewProvider(pc.APIKey, pc.APIBase, pc.APIType), true
	}
	if cur, _ := provider.SplitProviderModel(a.model); cur == prefix && a.provider != nil {
		return a.provider, true
	}
	return nil, false
}

// applyTurnModel switches this turn's fork to the WithTurnModel model,
// when one is set and usable. Only call it on a forkTurn copy.
func (a *Agent) applyTurnModel(ctx context.Context) {
	model, _ := ctx.Value(turnModelKey{}).(string)
	if model == "" || model == a.model {
		return
	}
	if prov, ok := a.ResolveTurnModel(model); ok {
		a.provider = prov
		a.model = model
	}
}
