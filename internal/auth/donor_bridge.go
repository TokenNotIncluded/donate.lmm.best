package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"

	"github.com/go-webauthn/webauthn/webauthn"
)

// donorPasskeyUser is a snapshot of the authoritative administrator identity.
// It preserves the original WebAuthn user handle while granting no admin
// session. The unexported snapshot cannot be constructed by HTTP callers.
type donorPasskeyUser struct {
	adminUser
	manager    *Manager
	generation int64
}

// LookupDonorPasskey allows the administrator's existing device credential to
// authenticate as an ordinary donor. Both the credential ID and its original
// user handle must identify the current, active administrator generation.
// The returned snapshot must still pass normal WebAuthn assertion validation.
func (m *Manager) LookupDonorPasskey(ctx context.Context, rawID, userHandle []byte) (webauthn.User, error) {
	u, state, err := m.user(ctx)
	if err != nil {
		return nil, err
	}
	if state.passwordEnabled || len(userHandle) != len(u.id) || subtle.ConstantTimeCompare(userHandle, u.id) != 1 {
		return nil, errStale
	}
	for _, credential := range u.credentials {
		if len(rawID) == len(credential.ID) && subtle.ConstantTimeCompare(rawID, credential.ID) == 1 {
			return donorPasskeyUser{adminUser: u, manager: m, generation: state.generation}, nil
		}
	}
	return nil, errStale
}

// AcceptDonorPasskey updates the same credential and signature counter used by
// administrator login. It runs inside the donor session's transaction, checks
// the current generation again, and never creates or changes an admin session.
func (m *Manager) AcceptDonorPasskey(ctx context.Context, tx *sql.Tx, user webauthn.User, credential *webauthn.Credential) error {
	u, ok := user.(donorPasskeyUser)
	if !ok || u.manager != m || tx == nil {
		return errStale
	}
	return updateVerifiedCredential(ctx, tx, u.generation, u.adminUser, credential)
}

// QuarantineDonorPasskey applies the administrator's existing clone policy to
// the shared authority, including revoking administrator sessions. The proof
// was validated by WebAuthn; the verified snapshot is checked again before any
// state is changed. Ordinary donor sessions retain their independent lifetime.
func (m *Manager) QuarantineDonorPasskey(ctx context.Context, user webauthn.User, credential *webauthn.Credential) error {
	u, ok := user.(donorPasskeyUser)
	if !ok || u.manager != m {
		return errStale
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = quarantineVerifiedCredential(ctx, tx, u.generation, u.adminUser, credential); err != nil {
		return err
	}
	return tx.Commit()
}

// DonorPasskeyCount reads current usable credentials without copying them to
// donor storage. A transaction can be supplied for an atomic enrollment check.
func (m *Manager) DonorPasskeyCount(ctx context.Context, tx *sql.Tx) (int, error) {
	const query = `SELECT COUNT(*) FROM auth_credentials c JOIN auth_state s
ON s.id=1 AND c.generation=s.generation
WHERE s.password_enabled=0 AND c.active=1`
	var count int
	var err error
	if tx == nil {
		err = m.db.QueryRowContext(ctx, query).Scan(&count)
	} else {
		err = tx.QueryRowContext(ctx, query).Scan(&count)
	}
	return count, err
}

// DonorPasskeyIDInUse reserves all administrator credential IDs, including
// quarantined keys, against a second credential authority in donor storage.
func (m *Manager) DonorPasskeyIDInUse(ctx context.Context, tx *sql.Tx, rawID []byte) (bool, error) {
	if tx == nil {
		return false, errStale
	}
	var exists bool
	err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM auth_credentials WHERE id=?)", base64.RawURLEncoding.EncodeToString(rawID)).Scan(&exists)
	return exists, err
}

func donorCredentialIDInUse(ctx context.Context, tx *sql.Tx, rawID []byte) (bool, error) {
	// The CLI and standalone auth package can initialize before donor tables.
	var hasTable bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='donor_credentials')").Scan(&hasTable); err != nil {
		return false, err
	}
	if !hasTable {
		return false, nil
	}
	var exists bool
	err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM donor_credentials WHERE id=?)", base64.RawURLEncoding.EncodeToString(rawID)).Scan(&exists)
	return exists, err
}

// Both login paths use this compare-and-swap: a concurrent reset, quarantine,
// deletion or assertion invalidates the snapshot that was actually verified.
func updateVerifiedCredential(ctx context.Context, tx *sql.Tx, generation int64, u adminUser, credential *webauthn.Credential) error {
	if credential == nil || !credential.Flags.UserVerified || credential.Authenticator.CloneWarning {
		return errStale
	}
	var previous []byte
	for _, old := range u.credentials {
		if len(old.ID) == len(credential.ID) && subtle.ConstantTimeCompare(old.ID, credential.ID) == 1 {
			if (old.Authenticator.SignCount != 0 || credential.Authenticator.SignCount != 0) && credential.Authenticator.SignCount <= old.Authenticator.SignCount {
				return errStale
			}
			previous, _ = json.Marshal(old)
			break
		}
	}
	if previous == nil {
		return errStale
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE auth_credentials SET data=?
WHERE id=? AND generation=? AND active=1 AND data=?
AND EXISTS(SELECT 1 FROM auth_state WHERE id=1 AND generation=? AND password_enabled=0 AND user_id=?)`,
		raw, base64.RawURLEncoding.EncodeToString(credential.ID), generation, previous, generation, u.id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errStale
	}
	return nil
}

func quarantineVerifiedCredential(ctx context.Context, tx *sql.Tx, generation int64, u adminUser, credential *webauthn.Credential) error {
	if credential == nil || !credential.Flags.UserVerified || !credential.Authenticator.CloneWarning {
		return errStale
	}
	var previous []byte
	for _, old := range u.credentials {
		if len(old.ID) == len(credential.ID) && subtle.ConstantTimeCompare(old.ID, credential.ID) == 1 {
			previous, _ = json.Marshal(old)
			break
		}
	}
	if previous == nil {
		return errStale
	}
	result, err := tx.ExecContext(ctx, `UPDATE auth_credentials SET active=0
WHERE id=? AND generation=? AND active=1 AND data=?
AND EXISTS(SELECT 1 FROM auth_state WHERE id=1 AND generation=? AND password_enabled=0 AND user_id=?)`,
		base64.RawURLEncoding.EncodeToString(credential.ID), generation, previous, generation, u.id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errStale
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM auth_sessions WHERE generation=?", generation); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM auth_challenges WHERE generation=?", generation)
	return err
}
