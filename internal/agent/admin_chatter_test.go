package agent

import (
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// Shared-identity channels resolve the chatter to the channel owner's
// web user id, so an owner-bound personal channel is admin in DMs with
// zero extra configuration. Groups on such channels must NOT inherit
// that: routing rewrites every group speaker to the owner id, so owner
// equality proves nothing there.
func TestAdminChatterSharedIdentity(t *testing.T) {
	a := &Agent{ownerUserID: "u_owner"}

	dm := bus.InboundMessage{
		Channel:        "telegram",
		UserID:         "u_owner",
		PeerKind:       "dm",
		SharedIdentity: true,
	}
	if !a.isAdminChatter(dm) {
		t.Fatal("shared-identity DM resolved to owner should be admin")
	}

	group := dm
	group.PeerKind = "group"
	if a.isAdminChatter(group) {
		t.Fatal("group speaker on shared-identity channel must not be admin")
	}
}

// Regular (non-shared) IM chatters are minted as app_users — never equal
// to the owner id — so they stay guests unless the admins allowlist
// names their platform id.
func TestAdminChatterRegularIMChannel(t *testing.T) {
	a := &Agent{
		ownerUserID: "u_owner",
		admins:      map[string][]string{"telegram": {"u_app_listed"}},
	}

	stranger := bus.InboundMessage{Channel: "telegram", UserID: "u_app_stranger", PeerKind: "dm"}
	if a.isAdminChatter(stranger) {
		t.Fatal("minted app_user chatter must not be admin")
	}

	listed := bus.InboundMessage{Channel: "telegram", UserID: "u_app_listed", PeerKind: "dm"}
	if !a.isAdminChatter(listed) {
		t.Fatal("allowlisted chatter should be admin")
	}
}

func TestWeComGroupChatterIsNeverAdmin(t *testing.T) {
	a := &Agent{
		ownerUserID: "u_owner",
		admins:      map[string][]string{"wecom": {"member-listed", "u_owner"}},
	}
	for _, uid := range []string{"member-listed", "u_owner"} {
		for _, source := range []string{bus.SourceUser, bus.SourceHeartbeat} {
			msg := bus.InboundMessage{Channel: "wecom", UserID: uid, PeerKind: "group", Source: source}
			if a.isAdminChatter(msg) {
				t.Fatalf("wecom group %q became admin", uid)
			}
			if a.isTrustedTurn(msg) {
				t.Fatalf("wecom group %q became trusted for source %q", uid, source)
			}
		}
	}
}

func TestWeComDMUsesExistingOwnerAndAllowlistPolicy(t *testing.T) {
	a := &Agent{
		ownerUserID: "u_owner",
		admins:      map[string][]string{"wecom": {"member-listed"}},
	}
	if a.isAdminChatter(bus.InboundMessage{Channel: "wecom", UserID: "member-stranger", PeerKind: "dm"}) {
		t.Fatal("ordinary WeCom DM chatter became admin")
	}
	if !a.isAdminChatter(bus.InboundMessage{Channel: "wecom", UserID: "member-listed", PeerKind: "dm"}) {
		t.Fatal("allowlisted WeCom DM chatter was denied")
	}
}
