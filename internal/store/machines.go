package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MachineAgent is one coding agent a linked machine reports (agl
// `hello` / `agents.list`), trimmed to what the product shows.
type MachineAgent struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Available bool     `json:"available"`
	Features  []string `json:"features,omitempty"`
	// As agl reports them (agents.list AgentInfo): the executable, its
	// --version line, ready | desktop-only | not-found, and for a missing
	// agent how to install it.
	Path           string `json:"path,omitempty"`
	Version        string `json:"version,omitempty"`
	Status         string `json:"status,omitempty"`
	InstallCommand string `json:"installCommand,omitempty"`
	Guide          string `json:"guide,omitempty"`
	// Capabilities beyond code agl detected (image_generation, web_search,
	// browser); agl ≥ 0.3.6 only.
	Capabilities []string `json:"capabilities,omitempty"`
}

// MachineAgentSetting is the owner's choice for one local agent on a
// machine: whether LLM agents may use it, and the model / reasoning
// effort its sessions start with ("" = the agent's own default).
type MachineAgentSetting struct {
	Disabled bool   `json:"disabled,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
}

// MachineRecord is a user's own computer linked through agl. UserID is
// the generic Fastclaw user that owns it (for an integrating product's
// end user, their app_user). AglMachineID is the id the agl daemon
// chose for itself; ID ("mch_…") is ours.
type MachineRecord struct {
	ID           string
	UserID       string
	AglMachineID string
	Name         string
	Hostname     string
	OS           string
	Arch         string
	Version      string
	Agents       []MachineAgent
	// TokenHash is sha256(machine token), hex. Empty once revoked.
	TokenHash  string
	LastSeenAt time.Time // zero until the first hello
	CreatedAt  time.Time
	RevokedAt  time.Time // zero while active

	// Settings for LLM agents that dispatch work to this machine
	// (run_local_agent): whether they may at all, the agl access level
	// their sessions get, and their working directory ("" = agl's
	// default). New machines: true, "ask", "".
	AllowAgents   bool
	DefaultAccess string
	DefaultCwd    string
	// AgentSettings is keyed by agent id; absent = enabled, defaults.
	AgentSettings map[string]MachineAgentSetting

	// HubScope is the hub address the machine was paired through: ""
	// for <public>/agl, else the product scope of <public>/agl/p/<scope>
	// (see agl.ProductScope). agl keeps one link per hub URL, so each
	// product gets its own and products sharing a Fastclaw don't
	// replace each other's link on the same computer.
	HubScope string
}

// AgentSetting returns the owner's setting for agent (zero value when unset).
func (m *MachineRecord) AgentSetting(agent string) MachineAgentSetting {
	return m.AgentSettings[agent]
}

// MachineSettings is a partial update of a machine's user-editable
// fields; nil fields are left unchanged.
type MachineSettings struct {
	Name          *string
	AllowAgents   *bool
	DefaultAccess *string
	DefaultCwd    *string
	// AgentSettings replaces the whole per-agent map when non-nil.
	AgentSettings map[string]MachineAgentSetting
}

// Revoked reports whether the machine was revoked.
func (m *MachineRecord) Revoked() bool { return !m.RevokedAt.IsZero() }

// MachinePairingRecord is a one-time pairing code (stored as its hash).
type MachinePairingRecord struct {
	CodeHash  string
	UserID    string
	ExpiresAt time.Time
	UsedAt    time.Time // zero while unused
	MachineID string    // set once the code paired a machine
	CreatedAt time.Time
	Scope     string // hub scope the machine pairs into (MachineRecord.HubScope)
}

// HubAppStore remembers the display name of each product hub scope
// (MachineRecord.HubScope). Optional: *DBStore implements it, fakes may not.
type HubAppStore interface {
	SetHubAppName(ctx context.Context, scope, name string) error
	// HubAppName is "" when the scope has none.
	HubAppName(ctx context.Context, scope string) (string, error)
}

// MachineStore persists linked machines and their pairing codes. It is
// separate from Store so the many Store fakes don't have to grow;
// *DBStore implements both.
type MachineStore interface {
	CreateMachinePairing(ctx context.Context, p *MachinePairingRecord) error
	GetMachinePairing(ctx context.Context, codeHash string) (*MachinePairingRecord, error)
	// ConsumeMachinePairing marks an unused, unexpired code used, once:
	// a second call (or an expired code) returns ErrNotFound.
	ConsumeMachinePairing(ctx context.Context, codeHash string, now time.Time) (*MachinePairingRecord, error)
	SetMachinePairingMachine(ctx context.Context, codeHash, machineID string) error

	CreateMachine(ctx context.Context, m *MachineRecord) error
	GetMachine(ctx context.Context, id string) (*MachineRecord, error)
	// GetMachineByTokenHash returns the active machine holding that
	// token; ErrNotFound when none (unknown or revoked).
	GetMachineByTokenHash(ctx context.Context, tokenHash string) (*MachineRecord, error)
	// FindActiveMachine returns userID's active machine with that agl
	// machine id, for re-pairing the same computer.
	FindActiveMachine(ctx context.Context, userID, aglMachineID string) (*MachineRecord, error)
	// ListActiveMachinesByAglID lists every user's active machines with
	// that agl machine id (one computer linked to several accounts).
	ListActiveMachinesByAglID(ctx context.Context, aglMachineID string) ([]MachineRecord, error)
	// ListMachines lists userID's active machines, newest first.
	ListMachines(ctx context.Context, userID string) ([]MachineRecord, error)
	// SetMachineToken replaces an active machine's token (re-pairing)
	// and records the hub scope it paired through.
	SetMachineToken(ctx context.Context, id, tokenHash, hubScope string) error
	UpdateMachineHello(ctx context.Context, m *MachineRecord) error
	TouchMachine(ctx context.Context, id string, seen time.Time) error
	// RevokeMachine clears the token hash and stamps revoked_at.
	RevokeMachine(ctx context.Context, id string, now time.Time) error
	// UpdateMachineSettings applies the non-nil fields of s to an
	// active machine; ErrNotFound when it is unknown or revoked.
	UpdateMachineSettings(ctx context.Context, id string, s MachineSettings) error
}

var _ MachineStore = (*DBStore)(nil)

func toMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func (d *DBStore) CreateMachinePairing(ctx context.Context, p *MachinePairingRecord) error {
	if p.CodeHash == "" || p.UserID == "" {
		return errors.New("store: machine pairing needs code hash and user")
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	// Drop long-expired codes on the way (best effort).
	_, _ = d.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM machine_pairings WHERE expires_at < %s`, d.ph(1)),
		time.Now().Add(-24*time.Hour).UnixMilli())
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO machine_pairings (code_hash, user_id, expires_at, used_at, machine_id, created_at, scope) VALUES (%s, %s, %s, 0, '', %s, %s)`,
			d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5)),
		p.CodeHash, p.UserID, toMillis(p.ExpiresAt), toMillis(p.CreatedAt), p.Scope)
	return err
}

func (d *DBStore) scanPairing(row *sql.Row) (*MachinePairingRecord, error) {
	var p MachinePairingRecord
	var exp, used, created int64
	if err := row.Scan(&p.CodeHash, &p.UserID, &exp, &used, &p.MachineID, &created, &p.Scope); err != nil {
		return nil, scanErr(err)
	}
	p.ExpiresAt, p.UsedAt, p.CreatedAt = fromMillis(exp), fromMillis(used), fromMillis(created)
	return &p, nil
}

const pairingColumns = `code_hash, user_id, expires_at, used_at, machine_id, created_at, scope`

func (d *DBStore) GetMachinePairing(ctx context.Context, codeHash string) (*MachinePairingRecord, error) {
	return d.scanPairing(d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT %s FROM machine_pairings WHERE code_hash = %s`, pairingColumns, d.ph(1)), codeHash))
}

func (d *DBStore) ConsumeMachinePairing(ctx context.Context, codeHash string, now time.Time) (*MachinePairingRecord, error) {
	// The conditional UPDATE is the single-use gate: only one caller
	// sees a row affected, even under races.
	res, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE machine_pairings SET used_at = %s WHERE code_hash = %s AND used_at = 0 AND expires_at > %s`,
			d.ph(1), d.ph(2), d.ph(3)),
		now.UnixMilli(), codeHash, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrNotFound
	}
	return d.GetMachinePairing(ctx, codeHash)
}

func (d *DBStore) SetMachinePairingMachine(ctx context.Context, codeHash, machineID string) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE machine_pairings SET machine_id = %s WHERE code_hash = %s`, d.ph(1), d.ph(2)),
		machineID, codeHash)
	return err
}

const machineColumns = `id, user_id, agl_machine_id, name, hostname, os, arch, version, agents, token_hash, last_seen_at, created_at, revoked_at, allow_agents, default_access, default_cwd, agent_settings, hub_scope`

func scanMachine(scanner interface{ Scan(dest ...any) error }) (*MachineRecord, error) {
	var m MachineRecord
	var agents, agentSettings string
	var seen, created, revoked, allow int64
	if err := scanner.Scan(&m.ID, &m.UserID, &m.AglMachineID, &m.Name, &m.Hostname, &m.OS, &m.Arch, &m.Version,
		&agents, &m.TokenHash, &seen, &created, &revoked, &allow, &m.DefaultAccess, &m.DefaultCwd, &agentSettings, &m.HubScope); err != nil {
		return nil, scanErr(err)
	}
	m.AllowAgents = allow != 0
	if m.DefaultAccess == "" {
		m.DefaultAccess = "ask"
	}
	if agents != "" {
		_ = json.Unmarshal([]byte(agents), &m.Agents)
	}
	if agentSettings != "" {
		_ = json.Unmarshal([]byte(agentSettings), &m.AgentSettings)
	}
	m.LastSeenAt, m.CreatedAt, m.RevokedAt = fromMillis(seen), fromMillis(created), fromMillis(revoked)
	return &m, nil
}

func marshalMachineAgents(agents []MachineAgent) string {
	if agents == nil {
		agents = []MachineAgent{}
	}
	blob, _ := json.Marshal(agents)
	return string(blob)
}

func marshalAgentSettings(s map[string]MachineAgentSetting) string {
	if s == nil {
		s = map[string]MachineAgentSetting{}
	}
	blob, _ := json.Marshal(s)
	return string(blob)
}

func (d *DBStore) CreateMachine(ctx context.Context, m *MachineRecord) error {
	if m.ID == "" || m.UserID == "" {
		return errors.New("store: machine needs id and user")
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	if m.DefaultAccess == "" {
		m.DefaultAccess = "ask"
	}
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO machines (%s) VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)`, machineColumns,
			d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5), d.ph(6), d.ph(7), d.ph(8), d.ph(9), d.ph(10), d.ph(11), d.ph(12), d.ph(13),
			d.ph(14), d.ph(15), d.ph(16), d.ph(17), d.ph(18)),
		m.ID, m.UserID, m.AglMachineID, m.Name, m.Hostname, m.OS, m.Arch, m.Version, marshalMachineAgents(m.Agents),
		m.TokenHash, toMillis(m.LastSeenAt), toMillis(m.CreatedAt), toMillis(m.RevokedAt),
		boolInt(m.AllowAgents), m.DefaultAccess, m.DefaultCwd, marshalAgentSettings(m.AgentSettings), m.HubScope)
	return err
}

func (d *DBStore) GetMachine(ctx context.Context, id string) (*MachineRecord, error) {
	return scanMachine(d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT %s FROM machines WHERE id = %s`, machineColumns, d.ph(1)), id))
}

func (d *DBStore) GetMachineByTokenHash(ctx context.Context, tokenHash string) (*MachineRecord, error) {
	if tokenHash == "" {
		return nil, ErrNotFound
	}
	return scanMachine(d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT %s FROM machines WHERE token_hash = %s AND revoked_at = 0`, machineColumns, d.ph(1)), tokenHash))
}

func (d *DBStore) FindActiveMachine(ctx context.Context, userID, aglMachineID string) (*MachineRecord, error) {
	return scanMachine(d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT %s FROM machines WHERE user_id = %s AND agl_machine_id = %s AND revoked_at = 0 ORDER BY created_at DESC LIMIT 1`,
			machineColumns, d.ph(1), d.ph(2)), userID, aglMachineID))
}

func (d *DBStore) ListMachines(ctx context.Context, userID string) ([]MachineRecord, error) {
	return d.queryMachines(ctx,
		fmt.Sprintf(`SELECT %s FROM machines WHERE user_id = %s AND revoked_at = 0 ORDER BY created_at DESC, id DESC`, machineColumns, d.ph(1)), userID)
}

func (d *DBStore) ListActiveMachinesByAglID(ctx context.Context, aglMachineID string) ([]MachineRecord, error) {
	if aglMachineID == "" {
		return nil, nil
	}
	return d.queryMachines(ctx,
		fmt.Sprintf(`SELECT %s FROM machines WHERE agl_machine_id = %s AND revoked_at = 0 ORDER BY created_at DESC, id DESC`, machineColumns, d.ph(1)), aglMachineID)
}

func (d *DBStore) queryMachines(ctx context.Context, query string, args ...any) ([]MachineRecord, error) {
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MachineRecord
	for rows.Next() {
		m, err := scanMachine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (d *DBStore) SetMachineToken(ctx context.Context, id, tokenHash, hubScope string) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE machines SET token_hash = %s, hub_scope = %s WHERE id = %s AND revoked_at = 0`, d.ph(1), d.ph(2), d.ph(3)),
		tokenHash, hubScope, id)
	return err
}

func (d *DBStore) UpdateMachineHello(ctx context.Context, m *MachineRecord) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE machines SET name = %s, hostname = %s, os = %s, arch = %s, version = %s, agents = %s, last_seen_at = %s WHERE id = %s`,
			d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5), d.ph(6), d.ph(7), d.ph(8)),
		m.Name, m.Hostname, m.OS, m.Arch, m.Version, marshalMachineAgents(m.Agents), toMillis(m.LastSeenAt), m.ID)
	return err
}

func (d *DBStore) TouchMachine(ctx context.Context, id string, seen time.Time) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE machines SET last_seen_at = %s WHERE id = %s`, d.ph(1), d.ph(2)), seen.UnixMilli(), id)
	return err
}

func (d *DBStore) RevokeMachine(ctx context.Context, id string, now time.Time) error {
	res, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE machines SET token_hash = '', revoked_at = %s WHERE id = %s AND revoked_at = 0`, d.ph(1), d.ph(2)),
		now.UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (d *DBStore) UpdateMachineSettings(ctx context.Context, id string, s MachineSettings) error {
	var sets []string
	var args []any
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = %s", col, d.ph(len(args))))
	}
	if s.Name != nil {
		add("name", *s.Name)
	}
	if s.AllowAgents != nil {
		add("allow_agents", boolInt(*s.AllowAgents))
	}
	if s.DefaultAccess != nil {
		add("default_access", *s.DefaultAccess)
	}
	if s.DefaultCwd != nil {
		add("default_cwd", *s.DefaultCwd)
	}
	if s.AgentSettings != nil {
		add("agent_settings", marshalAgentSettings(s.AgentSettings))
	}
	if len(sets) == 0 {
		if _, err := d.GetMachine(ctx, id); err != nil {
			return err
		}
		return nil
	}
	args = append(args, id)
	res, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE machines SET %s WHERE id = %s AND revoked_at = 0`, strings.Join(sets, ", "), d.ph(len(args))), args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return nil
}

// SetHubAppName records scope's display name.
func (d *DBStore) SetHubAppName(ctx context.Context, scope, name string) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO agl_hub_apps (scope, app_name, updated_at) VALUES (%s, %s, %s)
			ON CONFLICT (scope) DO UPDATE SET app_name = excluded.app_name, updated_at = excluded.updated_at`, d.ph(1), d.ph(2), d.ph(3)),
		scope, name, toMillis(time.Now()))
	return err
}

// HubAppName returns scope's display name ("" when none).
func (d *DBStore) HubAppName(ctx context.Context, scope string) (string, error) {
	var name string
	err := d.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT app_name FROM agl_hub_apps WHERE scope = %s`, d.ph(1)), scope).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}
