package app

// Existing donations remain general donations. Project assignment is never
// guessed from a payment method, account, name, or email.
func (a *App) migrateProjects() error {
	tx, err := a.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE donations SET id=id WHERE 0"); err != nil {
		return err
	}
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS projects (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, url TEXT NOT NULL DEFAULT '',
 currency TEXT NOT NULL, target_minor INTEGER NOT NULL CHECK(target_minor>0),
 active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0,1)),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);`); err != nil {
		return err
	}
	rows, err := tx.Query("PRAGMA table_info(donations)")
	if err != nil {
		return err
	}
	exists := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		exists[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !exists["project_id"] {
		if _, err = tx.Exec("ALTER TABLE donations ADD COLUMN project_id TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	if !exists["public_thanks"] {
		if _, err = tx.Exec("ALTER TABLE donations ADD COLUMN public_thanks INTEGER NOT NULL DEFAULT 0 CHECK(public_thanks IN (0,1))"); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`CREATE INDEX IF NOT EXISTS donation_project_totals
ON donations(project_id,status,currency,paid_at,amount_minor) WHERE project_id<>'';
CREATE TRIGGER IF NOT EXISTS donation_project_immutable
BEFORE UPDATE OF project_id ON donations WHEN NEW.project_id<>OLD.project_id
BEGIN SELECT RAISE(ABORT,'a donation project cannot change'); END;`); err != nil {
		return err
	}
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < 4 {
		if _, err = tx.Exec("PRAGMA user_version=4"); err != nil {
			return err
		}
	}
	return tx.Commit()
}
