package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MachineAgent is one coding agent a linked machine reports (agl
// `hello` / `agents.list`), trimmed to what the product shows.
type MachineAgent struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Available bool     `json:"available"`
	Features  []string `json:"features,omitempty"`
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
	// ListMachines lists userID's active machines, newest first.
	ListMachines(ctx context.Context, userID string) ([]MachineRecord, error)
	SetMachineToken(ctx context.Context, id, tokenHash string) error
	UpdateMachineHello(ctx context.Context, m *MachineRecord) error
	TouchMachine(ctx context.Context, id string, seen time.Time) error
	// RevokeMachine clears the token hash and stamps revoked_at.
	RevokeMachine(ctx context.Context, id string, now time.Time) error
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
		fmt.Sprintf(`INSERT INTO machine_pairings (code_hash, user_id, expires_at, used_at, machine_id, created_at) VALUES (%s, %s, %s, 0, '', %s)`,
			d.ph(1), d.ph(2), d.ph(3), d.ph(4)),
		p.CodeHash, p.UserID, toMillis(p.ExpiresAt), toMillis(p.CreatedAt))
	return err
}

func (d *DBStore) scanPairing(row *sql.Row) (*MachinePairingRecord, error) {
	var p MachinePairingRecord
	var exp, used, created int64
	if err := row.Scan(&p.CodeHash, &p.UserID, &exp, &used, &p.MachineID, &created); err != nil {
		return nil, scanErr(err)
	}
	p.ExpiresAt, p.UsedAt, p.CreatedAt = fromMillis(exp), fromMillis(used), fromMillis(created)
	return &p, nil
}

const pairingColumns = `code_hash, user_id, expires_at, used_at, machine_id, created_at`

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

const machineColumns = `id, user_id, agl_machine_id, name, hostname, os, arch, version, agents, token_hash, last_seen_at, created_at, revoked_at`

func scanMachine(scanner interface{ Scan(dest ...any) error }) (*MachineRecord, error) {
	var m MachineRecord
	var agents string
	var seen, created, revoked int64
	if err := scanner.Scan(&m.ID, &m.UserID, &m.AglMachineID, &m.Name, &m.Hostname, &m.OS, &m.Arch, &m.Version,
		&agents, &m.TokenHash, &seen, &created, &revoked); err != nil {
		return nil, scanErr(err)
	}
	if agents != "" {
		_ = json.Unmarshal([]byte(agents), &m.Agents)
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

func (d *DBStore) CreateMachine(ctx context.Context, m *MachineRecord) error {
	if m.ID == "" || m.UserID == "" {
		return errors.New("store: machine needs id and user")
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO machines (%s) VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)`, machineColumns,
			d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5), d.ph(6), d.ph(7), d.ph(8), d.ph(9), d.ph(10), d.ph(11), d.ph(12), d.ph(13)),
		m.ID, m.UserID, m.AglMachineID, m.Name, m.Hostname, m.OS, m.Arch, m.Version, marshalMachineAgents(m.Agents),
		m.TokenHash, toMillis(m.LastSeenAt), toMillis(m.CreatedAt), toMillis(m.RevokedAt))
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
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT %s FROM machines WHERE user_id = %s AND revoked_at = 0 ORDER BY created_at DESC, id DESC`, machineColumns, d.ph(1)), userID)
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

func (d *DBStore) SetMachineToken(ctx context.Context, id, tokenHash string) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE machines SET token_hash = %s WHERE id = %s AND revoked_at = 0`, d.ph(1), d.ph(2)), tokenHash, id)
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
