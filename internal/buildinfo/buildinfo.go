// Package buildinfo holds the binary's build identity (version, commit,
// date) populated at link time via -ldflags -X. Lives in its own package
// (rather than cmd/fastclaw/main) so internal/* code — notably the agent
// system-prompt builder — can read it without dragging the cmd package
// into a dependency loop.
package buildinfo

import (
	"os"
	"strings"
	"sync/atomic"
)

// Version is the FastClaw release tag (e.g. "v0.4.2") set by the
// Makefile via `git describe --tags`. Defaults to "dev" for ad-hoc
// `go build`s where no ldflag is passed; consumers should treat that
// as "no published version" rather than a real release.
var Version = "dev"

// Commit is the short git SHA of the build's source tree.
var Commit = "unknown"

// Date is the UTC timestamp the binary was built.
var Date = "unknown"

// IsHostedDeploy reports whether this fastclaw process is running in a
// hosted/multi-tenant deployment (cloud) versus a self-hosted single-
// operator install. Driven by the FASTCLAW_DEPLOY env var:
//
//	FASTCLAW_DEPLOY=hosted        → IsHostedDeploy() == true
//	FASTCLAW_DEPLOY=self-hosted   → false
//	(unset or anything else)      → false (default = self-hosted)
//
// Operators set this in their cloud deployment manifests (k8s
// values.yaml, docker-compose env, …). Default-self-hosted matches
// the most common case (developer running fastclaw on their own
// laptop) and avoids surprising upgrade prompts when an operator
// just forgets the env var on their cloud deploy — better to
// default to "tell the user how to upgrade" and only suppress when
// explicitly opted in.
//
// Read each call (not cached) so a config-edit + sighup flow can
// flip it without a process restart, though in practice it's set
// once at boot.
func IsHostedDeploy() bool {
	return osDeployVar() == "hosted"
}

// osDeployVar reads FASTCLAW_DEPLOY and normalizes it. Lowercased so
// casing variations don't silently bypass the hosted flag.
func osDeployVar() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv("FASTCLAW_DEPLOY")))
}

// IsHostExecAllowed reports whether the agent runtime should register
// the `host_exec` escape-hatch tool. Returns true only when the
// operator has explicitly opted in via FASTCLAW_ALLOW_HOST_EXEC=1
// (or "true"/"yes") AND this isn't a hosted multi-tenant deploy.
//
// Default OFF. Earlier versions registered host_exec for every
// self-hosted install — a too-permissive default that exposed the
// operator's host shell to ANY external IM user (WeChat, Discord,
// Feishu, …) the agent was reachable from. With the new gate, an
// operator who actually needs host_exec (single-user laptop dev
// flow, `fastclaw upgrade`, `~/Downloads` triage) sets the env var
// at deploy time; everyone else gets the safer sandbox-or-nothing
// behavior.
func IsHostExecAllowed() bool {
	if IsHostedDeploy() {
		return false
	}
	v := strings.ToLower(strings.TrimSpace(os.Getenv("FASTCLAW_ALLOW_HOST_EXEC")))
	return v == "1" || v == "true" || v == "yes"
}

// IsSandboxEnforced reports whether a configured sandbox acts as a hard
// execution BOUNDARY (every exec / file tool locked inside it) versus an
// optional TOOL the agent opts into per call (exec with sandbox:true).
//
// Hosted multi-tenant deploys are always enforced: the host is shared
// infrastructure and chatters are strangers, so nothing may touch it.
// Self-hosted installs default to NOT enforced — the operator's own
// machine is the natural execution environment ("install ffmpeg for me"
// must work on the host), and the sandbox exists to provide an isolated,
// pre-provisioned toolbox (python, node, camoufox-cli, …), not to wall
// the operator out of their own laptop.
//
// A self-hosted operator who exposes agents to untrusted IM chatters
// (WeChat, Discord, Feishu, …) and wants the old sandbox-or-nothing
// boundary back opts in with FASTCLAW_SANDBOX_ENFORCE=1 (or true/yes),
// or flips "force sandbox" in the admin runtime settings (sandbox.enforce,
// published here via SetSandboxEnforceSetting whenever the sandbox pool
// is (re)built, so it takes effect without a restart).
// Read per call, same as IsHostedDeploy.
func IsSandboxEnforced() bool {
	if IsHostedDeploy() || sandboxEnforceSetting.Load() {
		return true
	}
	v := strings.ToLower(strings.TrimSpace(os.Getenv("FASTCLAW_SANDBOX_ENFORCE")))
	return v == "1" || v == "true" || v == "yes"
}

var sandboxEnforceSetting atomic.Bool

// SetSandboxEnforceSetting publishes the system sandbox config's
// "force sandbox" toggle. Callers pass enabled && enforce: forcing a
// sandbox that isn't configured would only make every exec refuse.
func SetSandboxEnforceSetting(v bool) { sandboxEnforceSetting.Store(v) }
