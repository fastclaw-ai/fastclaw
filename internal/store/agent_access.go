package store

import "strings"

// End-user reach of an account's agents, kept in the agent's config blob
// (no schema change). A product that runs one Fastclaw account for many
// end users (its own users, X-Fastclaw-End-User) marks which of its
// agents each end user may reach from another agent (message_agent):
//
//   - endUserVisibility "public": every end user of the account.
//   - endUserOwner "<end-user id>": that end user (the X-Fastclaw-End-User
//     value the product sends for them), e.g. a draft only its creator
//     may use.
//
// Neither set: no end user reaches it through message_agent. The default
// is closed so an account's agents stay unreachable until the product
// says otherwise.
const (
	AgentCfgEndUserVisibility = "endUserVisibility"
	AgentCfgEndUserOwner      = "endUserOwner"
	// AgentCfgEndUserConnectors (bool): formerly opened an agent to every
	// end user's connected accounts. Every agent of the account is open
	// now (gateway endUserConnectorGate); kept so old writes still parse.
	AgentCfgEndUserConnectors = "endUserConnectors"

	EndUserVisibilityPublic  = "public"
	EndUserVisibilityPrivate = "private"
)

// AgentEndUserVisibility is rec's endUserVisibility ("" when unset).
func AgentEndUserVisibility(rec *AgentRecord) string {
	if rec == nil {
		return ""
	}
	v, _ := rec.Config[AgentCfgEndUserVisibility].(string)
	return v
}

// AgentEndUserOwner is rec's endUserOwner ("" when unset).
func AgentEndUserOwner(rec *AgentRecord) string {
	if rec == nil {
		return ""
	}
	v, _ := rec.Config[AgentCfgEndUserOwner].(string)
	return v
}

// AgentReachableByEndUser reports whether the end user externalID (their
// X-Fastclaw-End-User value) may reach rec, an agent of the account they
// belong to: it is public to the account's end users, or theirs.
func AgentReachableByEndUser(rec *AgentRecord, externalID string) bool {
	if rec == nil {
		return false
	}
	if AgentEndUserVisibility(rec) == EndUserVisibilityPublic {
		return true
	}
	owner := strings.TrimSpace(AgentEndUserOwner(rec))
	return owner != "" && owner == strings.TrimSpace(externalID)
}

// AgentEndUserConnectors reports whether rec is marked for every end
// user's connected accounts (AgentCfgEndUserConnectors).
func AgentEndUserConnectors(rec *AgentRecord) bool {
	if rec == nil {
		return false
	}
	v, _ := rec.Config[AgentCfgEndUserConnectors].(bool)
	return v
}
