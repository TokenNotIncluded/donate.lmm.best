package donors

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// AdministratorPasskeys is a one-way bridge to the administrator's existing
// credential authority. Implementations must use the same database as Manager
// and accept verified credentials inside the supplied donor transaction.
// No credential data is duplicated in donor storage, and no method grants an
// administrator session to a donor.
type AdministratorPasskeys interface {
	LookupDonorPasskey(context.Context, []byte, []byte) (webauthn.User, error)
	AcceptDonorPasskey(context.Context, *sql.Tx, webauthn.User, *webauthn.Credential) error
	QuarantineDonorPasskey(context.Context, webauthn.User, *webauthn.Credential) error
	DonorPasskeyCount(context.Context, *sql.Tx) (int, error)
	DonorPasskeyIDInUse(context.Context, *sql.Tx, []byte) (bool, error)
}

type Option func(*Manager)

// WithAdministratorPasskeys permits the existing administrator passkeys to
// create ordinary donor sessions. Administrator authorization remains separate.
func WithAdministratorPasskeys(source AdministratorPasskeys) Option {
	return func(m *Manager) { m.administrator = source }
}

const administratorSource = "administrator"

func (m *Manager) migrateIdentityLinks() error {
	_, err := m.db.Exec(`CREATE TABLE IF NOT EXISTS donor_identity_links (
 source TEXT PRIMARY KEY CHECK (source='administrator'),
 user_id TEXT NOT NULL UNIQUE REFERENCES donor_users(id)
);`)
	return err
}

func (m *Manager) passkeyCount(ctx context.Context, userID string, tx *sql.Tx) (int, error) {
	var count int
	var linked bool
	const query = `SELECT (SELECT COUNT(*) FROM donor_credentials WHERE user_id=?),
EXISTS(SELECT 1 FROM donor_identity_links WHERE source=? AND user_id=?)`
	var err error
	if tx == nil {
		err = m.db.QueryRowContext(ctx, query, userID, administratorSource, userID).Scan(&count, &linked)
	} else {
		err = tx.QueryRowContext(ctx, query, userID, administratorSource, userID).Scan(&count, &linked)
	}
	if err != nil || !linked || m.administrator == nil {
		return count, err
	}
	adminCount, err := m.administrator.DonorPasskeyCount(ctx, tx)
	return count + adminCount, err
}

func (m *Manager) acceptAdministratorLogin(r *http.Request, previous session, principal webauthn.User, credential *webauthn.Credential) (session, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.administrator == nil {
		return session{}, "", ErrUnauthorized
	}
	tx, err := m.db.BeginTx(r.Context(), nil)
	if err != nil {
		return session{}, "", err
	}
	defer tx.Rollback()
	// The authority verifies generation, active state, user handle and its
	// previously verified credential snapshot while acquiring the writer lock.
	if err = m.administrator.AcceptDonorPasskey(r.Context(), tx, principal, credential); err != nil {
		return session{}, "", err
	}
	if previous.hash != "" {
		var valid bool
		if err = tx.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM donor_sessions WHERE token_hash=? AND expires>?)", previous.hash, time.Now().Unix()).Scan(&valid); err != nil || !valid {
			return session{}, "", ErrUnauthorized
		}
	}
	// There is one administrator on this installation. Its link remains stable
	// across key replacement and CLI recovery, preserving ordinary donor history.
	var user User
	err = tx.QueryRowContext(r.Context(), `SELECT u.id,u.display_name,u.created_at
FROM donor_identity_links l JOIN donor_users u ON u.id=l.user_id WHERE l.source=?`, administratorSource).Scan(&user.ID, &user.DisplayName, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		user.ID, err = randomToken()
		if err != nil {
			return session{}, "", err
		}
		user.CreatedAt = time.Now().Unix()
		if _, err = tx.ExecContext(r.Context(), "INSERT INTO donor_users(id,display_name,created_at) VALUES(?,?,?)", user.ID, user.DisplayName, user.CreatedAt); err != nil {
			return session{}, "", err
		}
		if _, err = tx.ExecContext(r.Context(), "INSERT INTO donor_identity_links(source,user_id) VALUES(?,?)", administratorSource, user.ID); err != nil {
			return session{}, "", err
		}
	} else if err != nil {
		return session{}, "", err
	}
	full, token, err := m.newSession(tx, user, previous.hash)
	if err != nil {
		return full, "", err
	}
	if err = tx.Commit(); err != nil {
		return full, "", err
	}
	return full, token, nil
}
