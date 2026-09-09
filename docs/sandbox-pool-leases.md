# Cross-pod E2B sandbox pool lease registry — design proposal

> **Status**: proposal (design-first PR — no implementation in this branch)
> **Target**: FastClaw gateway replicas behind a shared Postgres
> **Last updated**: 2026-09-09
> **Decision owner**: TBD — to be assigned by maintainers
> **Reviewed by**: TBD — architecture review method below is reproducible

## Problem

The sandbox pool is per gateway process. With N gateway replicas behind a
round-robin service, one (agent, project, session) can be served by every pod
in turn, and each pod lazily creates its **own** E2B instance for the same
scope → up to N sandboxes per session (observed: 10 pods → 10 instances).

Consequences beyond cost: each instance has its own `/workspace`, so session
state diverges between pods, and there is no safe owner to destroy an
instance when a pod dies.

## Goal / non-goals

Goals:

- One sandbox per scope per session, shared across pods.
- Safe destruction: only the current owner may destroy, and only with a
  fencing version that is still current.
- Registry errors never destroy a shared sandbox (fail open).
- Dialect-neutral storage: identical schema on sqlite and Postgres, no
  `BLOB`/`BYTEA` branching.

Non-goals (v1):

- Heartbeat loops, degraded-state machine, metrics, liveness GC.
- Replaying hydration on adoption (creator already hydrated the scope).

## Proposed schema

Table `sandbox_leases`, created idempotently by `Migrate()`:

**Module**: `store` (adapter) · **Layer**: interface adapter · **Design
role**: durable lease persistence only — the adapter owns no policy, no
epoch logic, and no encryption key. **Modular definition**: it is the
single place that may name the table and issue SQL; every other module sees
only the `sandbox.SandboxLeaseStore` port.

```text
scope_key      TEXT PRIMARY KEY — "agent[:p:proj][:s:sess]"
owner          TEXT NOT NULL — pod identity ("hostname:pid")
sandbox_id     TEXT NOT NULL — E2B instance id
envd_token     TEXT NOT NULL — base64(AES-GCM) ciphertext (see Security)
template       TEXT NOT NULL DEFAULT ''
expires_at     BIGINT NOT NULL — unix seconds; expired ⇒ dead
epoch          BIGINT NOT NULL DEFAULT 0 — fencing version
updated_at     BIGINT NOT NULL
```

`envd_token` stays a universal `TEXT` column. Encrypted payloads are
base64-encoded, so no database dialect branch is needed.

Schema constraints that keep the module boundary honest:

- `epoch` is `BIGINT` on both dialects (no dialect branching).
- The token column is universal `TEXT`; ciphertext is base64.
- No foreign keys to agent/session tables: the scope key is opaque to the
  store, which keeps the adapter replaceable.

## Lease semantics (v1)

- A pod handling a scope first looks up a valid lease → adopts the existing
  sandbox and CAS-renews ownership to itself (epoch bumped).
- No valid lease → create + hydrate locally, then `AcquireSandboxLease`. A
  lost race closes the local copy and adopts the winner's sandbox.
- Before every use of a cached executor the pool reconciles against the
  shared lease and renews under this pod's ownership when the sandbox is
  unchanged (TTL default 15 min).
- Release/eviction deletes the row only when `owner` **and** the epoch this
  pod last received still match; otherwise the pod drops its local reference
  **without** destroying the sandbox.

### Failure semantics (fail open)

Guiding principle: **availability first, destruction right strictly gated** —
without proof of ownership (matching epoch) a pod must never close a sandbox,
even if that means leaking one until TTL/expiry.

| Condition | Pool behavior |
|---|---|
| No failure (baseline) | Acquire/renew/adopt succeed with fresh epoch; matching-epoch release deletes row and closes once |
| Lease lookup error before local create | Creates + registers locally; acquire error keeps it unregistered |
| Adopt renew error | Uses adopted executor, records no epoch → release can never destroy it |
| Reconcile lookup error (cached) | Keeps cached executor; no renew, no close |
| Reconcile reclaim acquire error | Keeps cached executor, unregistered |
| Double race (Acquire lost + adoption CAS miss) | Keeps local sandbox unregistered until next reconcile |
| Release / CloseAll error or declined delete | Drops local reference; sandbox stays alive |
| Token decrypt failure (rotated/mismatched key) | Read fails closed → treated as lookup error; unexpired row blocks registration until TTL |
| Token encrypt failure | Refuses to persist (never plaintext); error reaches pool acquire-error path → local sandbox kept unregistered |

## Concurrency: CAS + epoch

- `Renew` only matches `scope_key + sandbox_id + expires_at > now` and
  increments `epoch`; a miss returns no row (never treated as ownership).
- `Release` is `DELETE … WHERE scope_key=? AND owner=? AND epoch=?`; a stale
  or delayed eviction can never win against a newer renewal/adoption.
- `Acquire` claims an expired/fresh row and stamps `epoch = 1`. The three
  SQL statements rely on Postgres `EvalPlanQual` / sqlite write serialization;
  see the "Verification" section for the cross-connection test.

Why epoch on top of owner: owner covers most takeovers but not same-owner
request reordering or a delete formulated before another pod's adoption
completed.

## Alternatives considered

- **No sharing (status quo)** — rejected: it is the problem (N instances,
  divergent workspaces, no destruction owner).
- **Sticky/affinity routing** — rejected as a substitute: load imbalance,
  pod loss strands instances, it needs its own shared mapping, and it cannot
  provide fencing (who may destroy). Acceptable only as a future traffic
  optimization on top of this registry.
- **Redis lease/lock** — rejected as the primary mechanism: Redis is an
  optional dependency (refresh locks, pub/sub), so requiring it would break
  single-node/sqlite deployments, and a Redis lock still needs hand-rolled
  fencing. The lease row belongs with the rest of per-agent state.
- **Workspace blob store** — rejected: it has no transactional
  compare-and-set.

## System design & module boundaries

The design maps onto the repository's existing package layout, following
Clean Architecture: policy stays inward, ports are defined by consumers,
adapters implement ports, and the composition root is the only place that
knows concretions and secrets.

### Module map (stack mapping)

| Module (dir) | Layer | Responsibility | Depends on | Must not depend on |
|---|---|---|---|---|
| `internal/sandbox` | Policy + Port | Lease lifecycle decisions (acquire/adopt/reconcile/release), epoch fencing, fail-open; defines `SandboxLeaseStore` + `SandboxLeaseRecord` | its own E2B executor abstractions | SQL, config keys, `store`, `gateway` |
| `internal/store` | Interface adapter | `DBStore` implements `SandboxLeaseStore` with dialect-neutral SQL; optional `EncryptedSandboxLeaseStore` decorator | `sandbox` port, `cryptoutil.Cipher` (if encryption is accepted) | MCP OAuth, policy details |
| `internal/cryptoutil` (new, optional) | Neutral contract | `Cipher` (Encrypt/Decrypt) shared by every at-rest credential store | none | — |
| `internal/gateway` | Composition root | Owner ID (`hostname:pid`), lease-store extraction, cryptor injection, pool construction | `sandbox`, `store`, config, concrete cryptor | — |
| E2B provider, Postgres/sqlite, AES-GCM | Framework / driver | External details | — | — |

### Port contracts

```go
// internal/sandbox/lease.go — owned by the consumer (DIP).
type SandboxLeaseStore interface {
    GetSandboxLease(ctx context.Context, scopeKey string) (*SandboxLeaseRecord, error)
    AcquireSandboxLease(ctx context.Context, scopeKey, owner, sandboxID, envdToken, template string, ttl time.Duration) (*SandboxLeaseRecord, bool, error)
    RenewSandboxLease(ctx context.Context, scopeKey, owner, sandboxID string, ttl time.Duration) (epoch int64, err error)
    ReleaseSandboxLease(ctx context.Context, scopeKey, owner string, epoch int64) (bool, error)
}

// internal/cryptoutil/cipher.go — only if encryption ships.
type Cipher interface {
    Encrypt(ctx context.Context, plaintext []byte) ([]byte, error)
    Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
}
```

The port has four operations because the lifecycle has four operations
(ISP); it is defined next to the pool that consumes it (SDP: stable
abstraction owned by the policy side), not inside `store`.

### Dependency rule

```text
sandbox (policy + port)
   ▲                ▲
   │                │ implements / decorates
store (DBStore ──▶ EncryptedSandboxLeaseStore ──▶ cryptoutil.Cipher)
   ▲
gateway (composition root: owner id + cryptor + pool)
```

Source dependencies always point inward. `store` never imports MCP OAuth;
policy never sees a key or SQL; the gateway is the only module that can name
both the concrete cryptor and the store.

### Data crossing boundaries

- Scope key, owner, `sandbox_id`, template, TTL, epoch — plain values, no
  framework types cross layers.
- `envd_token` crosses the adapter boundary as ciphertext (base64 in TEXT)
  and exists in plaintext only inside the pool process. DB rows never
  contain plaintext.
- SQL rows are converted at the adapter edge; no `sql.Row` or dialect
  placeholder leaks into policy.

### Boundary rationale (CCP / change axes)

- Lease lifecycle changes (fencing, TTL, fail-open policy) are co-located in
  `internal/sandbox` (CCP: same change reason/cadence).
- Storage engine changes (sqlite ↔ Postgres, later another DB) are confined
  to `store` behind the port.
- Encryption/rotation changes are confined to the decorator + composition
  root; adding or removing at-rest encryption does not touch policy or SQL
  adapter logic (OCP).
- E2B provider changes (E2B ↔ another remote sandbox) stay behind the
  existing executor/pool abstractions.

### Deliberately not a module

- No lock service/state machine module: the CAS row and the failure table
  are sufficient for v1 (Musk gate). If production later shows a need,
  stages 3–6 in the fork history (heartbeat, metrics, liveness GC) can be
  added without reshaping the seams above.

## Gateway wiring (proposal)

Extend the existing `buildSystemSandboxPool(cfg, ws)` composition point to
accept an optional lease store and per-pod owner:

```go
func buildSystemSandboxPool(
    cfg config.SandboxCfg,
    ws workspace.Store,
    leases sandbox.SandboxLeaseStore, // nil → per-pod behavior
    ownerID string,                   // "hostname:pid"
) sandbox.ExecutorPool
```

The relational `DBStore` implements the sandbox-defined
`SandboxLeaseStore` port. If the at-rest encryption key is unset, the
gateway **disables shared leases** (per-pod behavior) rather than writing
plaintext tokens.

The gateway must stay the only caller that:

- decides whether shared leases are enabled (key configured? lease store
  present?);
- mints the per-pod owner identity;
- selects the cryptor implementation.

## Key implementation contracts (module-attributed)

Every key code path below carries its module definition so implementers can
reproduce the boundaries without guessing.

### Store: acquire (CAS three-step)

**Module**: `store` · **Layer**: interface adapter · **Implements**:
`SandboxLeaseStore.AcquireSandboxLease` · **Design role**: claim a
fresh/expired row or lose to the winner; never a read-then-write.
**Constraints**: dialect-neutral placeholders; token arrives already
encrypted when the decorator is active; no advisory locks.

```sql
-- 1. claim an expired row (epoch reset to 1)
UPDATE sandbox_leases
   SET owner=?, sandbox_id=?, envd_token=?, template=?,
       expires_at=?, epoch=1, updated_at=?
 WHERE scope_key=? AND expires_at <= ?;
-- 2. insert when absent (concurrent winner's insert wins)
INSERT INTO sandbox_leases
       (scope_key, owner, sandbox_id, envd_token, template,
        expires_at, epoch, updated_at)
VALUES (?, ?, ?, ?, ?, ?, 1, ?)
ON CONFLICT (scope_key) DO NOTHING;
-- 3. read back the authoritative row; compare sandbox_id to decide winner
SELECT sandbox_id, envd_token, template, expires_at, epoch
  FROM sandbox_leases WHERE scope_key=? AND expires_at > ?;
```

### Store: renew and release (epoch fencing)

**Module**: `store` · **Layer**: interface adapter · **Design role**:
ownership transfer and destruction are compare-and-set; a miss is not an
error, it is "not yours anymore". **Constraints**: renew returns the new
epoch (0 = miss); release deletes only on `owner + epoch`.

```sql
UPDATE sandbox_leases
   SET owner=?, expires_at=?, epoch=epoch+1, updated_at=?
 WHERE scope_key=? AND sandbox_id=? AND expires_at > ?
RETURNING epoch;                     -- 0 rows => miss, caller owns nothing

DELETE FROM sandbox_leases
 WHERE scope_key=? AND owner=? AND epoch=?;   -- 1 row => destroy allowed
```

### Adapter: at-rest encryption decorator (if accepted)

**Module**: `store` (decorator) · **Layer**: interface adapter · **Design
role**: keep `DBStore` key-agnostic; confidentiality is a separate concern
applied at the boundary. **Implements**: same port, wraps inner store.
**Constraints**: never persist plaintext; never return ciphertext as if it
were a token; encrypt/decrypt failures are errors the pool treats per the
failure table.

```go
func (e *EncryptedSandboxLeaseStore) AcquireSandboxLease(...) (*SandboxLeaseRecord, bool, error) {
    enc := base64(AESGCM.Encrypt(envdToken))   // key injected at composition
    rec, acquired, err := e.Inner.AcquireSandboxLease(..., enc, ...)
    rec.EnvdToken = string(AESGCM.Decrypt(base64.Decode(rec.EnvdToken)))
    return rec, acquired, err
}
```

### Policy: pool integration points (existing upstream code)

**Module**: `internal/sandbox` (pool) · **Layer**: policy · **Design role**:
all fail-open and epoch decisions live here; the pool never imports store or
SQL. **Integration points** to add:

1. `Get` with no cached executor → lease lookup → adopt (renew) or create +
   `Acquire` (loser adopts winner).
2. `Get` with a cached executor → reconcile (same sandbox: renew; stale:
   adopt after closing local; expired: reclaim or adopt winner).
3. `Release` / `CloseAll` → delete only when `owner + epoch` matches; on
   error or decline, drop the local reference and keep the sandbox alive.

## Fallback ladder (兜底方案)

Degradation is deliberate and ordered — never a hard failure of the sandbox
path:

1. **Full shared leases** — lease store + encryption key configured, all
   replicas converge; one sandbox per scope.
2. **Registry degraded (runtime errors)** — every read/write error fails
   open to the local path: pool creates/keeps its own sandbox, records no
   epoch when it cannot prove ownership, and never destroys anything it may
   not own. Duplicates are transient and cleaned by TTL/timeout.
3. **Encryption key missing** — shared leases are disabled at startup;
   behavior is identical to today's per-pod pool. Plaintext tokens are never
   written.
4. **Sandbox feature disabled** (`cfg.Sandbox.Enabled=false`) — no pool at
   all; file tools fall back to path-only mode, unchanged behavior.

No fallback writes plaintext tokens, and no fallback destroys a sandbox the
pod does not provably own.

## Fault tolerance (容错方案)

- **Lease store down** (Postgres unreachable, sqlite error): lookup/renew/
  release failures follow the failure table above — local sandbox stays
  usable; no shared destruction.
- **Pod crash**: its lease expires within TTL (default 15 min); another pod
  reclaims the scope. The orphaned E2B instance lives until the provider
  timeout (~30 min) and is never destroyed by the registry.
- **E2B provider failure during create/adopt**: create errors propagate to
  the caller (no fake sandbox cached); adopt renew errors keep the adopted
  executor usable but unregistered; hydration/verify failures tear the new
  sandbox down so callers retry loudly.
- **Race conditions**: concurrent acquire yields exactly one winner; every
  loser adopts the winner; a double race (Acquire loss + CAS adoption miss)
  keeps the local unregistered sandbox until the next reconcile.
- **Registry state corruption** (undecryptable row, wrong-key row): read
  fails closed → local create; the stale row is reclaimed at expiry; it can
  never cause a destroy.

## Compatibility & upgrade considerations

- **Schema**: new table only, `CREATE TABLE IF NOT EXISTS`, identical on
  sqlite/Postgres; no existing table is altered, so single-node and
  multi-pod installs share one code path.
- **Behavior default**: without the encryption key (or without a lease
  store) the feature is off — existing deployments observe no change.
- **API compatibility**: the pool port (`SandboxLeaseStore`) is additive;
  `buildSystemSandboxPool` gains optional parameters — all existing call
  sites keep working with nil leases.
- **Rolling upgrade**: during a rolling deploy, replicas without the secret
  run per-pod while replicas with it share leases; both are safe (no
  plaintext, no cross-destroy), but a mixed window may create duplicate
  sandboxes for the same scope. Completing the rollout converges them.
- **Key rotation**: rows written under the old key become unreadable
  (fail-open local create) and are reclaimed at TTL; rotation never destroys
  sandboxes but does orphan instances until provider timeout.
- **Logging**: new log fields are additive; tokens are never logged.

## Security design (key security design)

Threat model and controls:

- **Threat: DB dump / backup leak.** `envd_token` is AES-256-GCM encrypted at
  rest; ciphertext is base64 in a universal `TEXT` column (no dialect
  branch). The key is never in the DB, ConfigMap, or logs.
- **Threat: cross-pod impersonation.** `sandbox_id` + `envd_token` only
  grant access to one short-lived E2B instance, not to the account-level API
  key (which never touches the database).
- **Threat: stale/delayed destroy.** Destruction requires
  `owner + epoch`; any stale release fails closed and leaves the sandbox
  alive.
- **Threat: wrong-key reads after rotation.** Decryption failure is treated
  as a registry read error (fail-open local create) — ciphertext is never
  returned as if it were a token.
- **Assumptions**: DB transport encryption and filesystem permissions are
  platform responsibilities; this design protects the token at rest and on
  export. Token lifetime is bounded by the E2B instance; rows are deleted on
  release and replaced on expiry.
- **Key management**: one shared key for all replicas (proposed
  `FASTCLAW_OAUTH_SECRET`, injected as a Secret, never in plain files);
  naming deliberately mirrors fastclaw's OAuth at-rest secret so later
  upstream merges do not churn environment names; rotation runbook to
  follow once the key name is settled upstream.

Open question for maintainers: ship encryption in the same PR as the lease
feature, or land the lease feature first and encryption as a second PR?

## Migration & rollout

- No manual migration: `CREATE TABLE IF NOT EXISTS sandbox_leases` runs at
  boot on both dialects. The feature is new, so there is no legacy table to
  upgrade.
- Prerequisite: the encryption key must be configured (otherwise shared
  leases are disabled and behavior is unchanged from today).
- Verify: gateway log `sharedLeases=true`; one session produces one
  `sandbox created` even across pods; later hits log
  `sandbox adopted from shared lease`. If `sharedLeases=false`, re-check the
  key configuration before debugging further.

## Verification plan (implementation contract)

The design is only "done" when these exist and pass:

1. Policy-level unit tests with a fake lease store covering every row of the
   failure table (no database, no network).
2. Adapter tests against real sqlite (CAS miss, stale release, monotonic
   epoch) and against real Postgres behind `FASTAGENT_TEST_PG_DSN`
   (concurrent acquire → exactly one winner, stale-release fencing,
   idempotent migrate).
3. Encryption decorator tests: raw row contains no plaintext, wrong key
   fails closed, rotation reclaims after TTL (sqlite UT + Postgres variant).
4. Live E2B e2e (credentials-gated, CI fails loud without them): pod A
   creates + writes a `/workspace` marker; pod B adopts the same
   `sandbox_id` and reads the marker back; pod A's stale-epoch release must
   not destroy the sandbox.
5. CI workflow runs the sandbox/store/gateway suites against a Postgres
   service; the E2B job only runs when credentials exist.
6. Fallback/compatibility tests: gateway wiring with no secret → shared
   leases disabled (no plaintext); nil lease store → per-pod pool;
   `Migrate()` twice on both dialects is a no-op.

## Architecture review (clean-architecture skill)

Reviewed with the
[clean-architecture skill](https://github.com/tokenaissance/clean-architecture/blob/main/SKILL.md)
(theory: [clean-architecture.md](https://github.com/tokenaissance/clean-architecture/blob/main/references/clean-architecture.md);
anti-over-engineering gate: [musk-algorithm.md](https://github.com/tokenaissance/clean-architecture/blob/main/references/musk-algorithm.md)).
Method recorded so any reviewer can reproduce it without the skill
installed.

### Four-layer map

| Layer | Contents |
|---|---|
| Policy (innermost) | Lease lifecycle: acquire/adopt/reconcile/release, epoch fencing, fail-open |
| Port | `sandbox.SandboxLeaseStore` (defined by the consumer) |
| Interface adapter | `store.DBStore` (SQL, key-agnostic) + encryption decorator (if accepted) |
| Composition root | Gateway: owner ID, lease store, secret-keyed cryptor |
| Framework detail | E2B provider, Postgres/sqlite, AES-256-GCM |

### Dependency rule

```text
sandbox (policy + port)  ←── store (DBStore + optional encryption decorator)
        ▲                              ▲
        └── gateway (composition root, imports concretions) ──┘
```

Policy and SQL never see a key; the gateway is the only layer that knows the
secret.

### Anti-over-engineering verdict (Musk five-step)

Question → duplicate-sandbox problem is real and observed. Delete → no extra
lock system, no state machine, no shared crypto package beyond one small
interface. Simplify → three SQL statements provide the CAS; base64-in-TEXT
keeps schema dialect-neutral. Accelerate/Automate → left to CI. Verdict:
necessary and not over-engineered.

## Open questions for upstream

1. Encryption scope: same PR as the lease feature or a follow-up?
2. Confirm key environment name (`FASTCLAW_OAUTH_SECRET`, aligned with
   fastclaw so upstream merges stay low-noise).
3. Default TTL (15 min proposed) and lease-column naming.
4. Acceptable log fields for adoption verification.
