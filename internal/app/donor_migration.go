package app

// Existing guest donations stay unclaimed. A shared name or email is never an
// account proof, so this migration adds no automatic ownership backfill.
func (a *App) migrateDonorOwnership() error {
	tx, err := a.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Obtain the SQLite writer lock before inspecting the schema. Server and CLI
	// initialization may overlap while both use the same database.
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
		if name == "donor_user_id" {
			exists = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec("ALTER TABLE donations ADD COLUMN donor_user_id TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("CREATE INDEX IF NOT EXISTS donation_donor_history ON donations(donor_user_id,created_at DESC,id DESC) WHERE donor_user_id<>''"); err != nil {
		return err
	}
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < 2 {
		if _, err = tx.Exec("PRAGMA user_version=2"); err != nil {
			return err
		}
	}
	return tx.Commit()
}
