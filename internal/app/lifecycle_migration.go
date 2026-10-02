package app

import "time"

func (a *App) migrateCheckoutExpiry() error {
	tx, err := a.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE donations SET id=id WHERE 0"); err != nil {
		return err
	}
	rows, err := tx.Query("PRAGMA table_info(donations)")
	if err != nil {
		return err
	}
	exists := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "expires_at" {
			exists = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec("ALTER TABLE donations ADD COLUMN expires_at TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
		// Backfill once from the original creation time. Reinitializing the app
		// or opening it for CLI backup must never extend a checkout deadline.
		rows, err = tx.Query("SELECT id,created_at FROM donations WHERE source='checkout' AND method_type<>'custom' AND status='pending'")
		if err != nil {
			return err
		}
		type deadline struct{ id, expiry string }
		deadlines := []deadline{}
		for rows.Next() {
			var id, created string
			if err = rows.Scan(&id, &created); err != nil {
				rows.Close()
				return err
			}
			started, parseErr := time.Parse(time.RFC3339Nano, created)
			if parseErr != nil {
				rows.Close()
				return parseErr
			}
			deadlines = append(deadlines, deadline{id: id, expiry: started.UTC().Add(hostedCheckoutLifetime).Format(time.RFC3339Nano)})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, d := range deadlines {
			if _, err = tx.Exec("UPDATE donations SET expires_at=? WHERE id=? AND status='pending' AND source='checkout' AND method_type<>'custom'", d.expiry, d.id); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec("CREATE INDEX IF NOT EXISTS donation_checkout_expiry ON donations(expires_at) WHERE status='pending' AND source='checkout' AND method_type<>'custom'"); err != nil {
		return err
	}
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < 3 {
		if _, err = tx.Exec("PRAGMA user_version=3"); err != nil {
			return err
		}
	}
	return tx.Commit()
}
