package admin

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// FinishNativeAccountPurges runs between desktop service lifetimes. The native
// daemon owns this registry and its per-account sidecars while it is running.
func FinishNativeAccountPurges(root, primary string) error {
	if !filepath.IsAbs(root) || primary == "" {
		return errors.New("native deployment identity required")
	}
	directory := filepath.Join(root, "admin", "accounts.json")
	data, err := os.ReadFile(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var records []accountRecord
	if err = json.Unmarshal(data, &records); err != nil {
		return err
	}
	pending := false
	for _, r := range records {
		if r.Purged && r.Removed {
			pending = true
			break
		}
	}
	if !pending {
		return nil
	}
	lease, err := lockNativeForCleanup(filepath.Join(root, "owner.lock"))
	if err != nil {
		return err
	}
	defer lease.Close()
	path := filepath.Join(root, "registry.sqlite")
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("native registry is not a regular file")
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=rw"}
	if filepath.VolumeName(path) != "" {
		u.Path = "/" + u.Path
	}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != 2 {
		return errors.New("unsupported native account registry version")
	}
	if _, err = db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return err
	}
	base, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer base.Close()
	keep := make([]accountRecord, 0, len(records))
	for _, r := range records {
		if !r.Purged || !r.Removed {
			keep = append(keep, r)
			continue
		}
		if err = purgeOfflineProfile(db, base, primary, r.ID); err != nil {
			return err
		}
	}
	data, err = json.Marshal(keep)
	if err != nil {
		return err
	}
	return writePrivate(directory, data)
}

var nativeProfileID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func purgeOfflineProfile(db *sql.DB, base *os.Root, primary, id string) error {
	if !nativeProfileID.MatchString(id) || !filepath.IsLocal(id) || strings.EqualFold(id, primary) {
		return errors.New("refusing invalid or primary account")
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var mode, profile string
	err = tx.QueryRow("SELECT storage_mode,profile_path FROM accounts WHERE id=?", id).Scan(&mode, &profile)
	if errors.Is(err, sql.ErrNoRows) {
		// Never erase an unregistered directory based on its name alone.
		_, err = base.Lstat(filepath.Join("accounts", id))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errors.New("unregistered native account directory requires inspection")
	}
	if err != nil {
		return err
	}
	var conflicts int
	err = tx.QueryRow(`SELECT (SELECT count(*) FROM accounts WHERE id=? COLLATE NOCASE AND id<>?) +
  (SELECT count(*) FROM account_settings WHERE account_id=? AND alias=? COLLATE NOCASE)`, id, id, id, primary).Scan(&conflicts)
	if err != nil {
		return err
	}
	if conflicts != 0 {
		return errors.New("ambiguous or primary native account")
	}
	switch mode {
	case "managed":
		expected := filepath.Join(base.Name(), "accounts", id, "profile")
		if !strings.EqualFold(filepath.Clean(strings.TrimPrefix(profile, `\\?\`)), filepath.Clean(expected)) {
			return errors.New("unexpected native profile location")
		}
	case "direct", "official": // Only our sidecar is owned; never delete an external profile.
	default:
		return fmt.Errorf("unsupported account storage mode %q", mode)
	}
	// Preserve tokens shared with other accounts. Removing the only scope must
	// delete the token instead of turning it into an unrestricted credential.
	if _, err = tx.Exec(`DELETE FROM access_tokens WHERE EXISTS(SELECT 1 FROM json_each(accounts_json) WHERE value=?) AND NOT EXISTS(SELECT 1 FROM json_each(accounts_json) WHERE value<>?)`, id, id); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE access_tokens SET accounts_json=(SELECT json_group_array(value) FROM json_each(accounts_json) WHERE value<>?) WHERE EXISTS(SELECT 1 FROM json_each(accounts_json) WHERE value=?)`, id, id); err != nil {
		return err
	}
	if err = eraseAccountChild(base, "accounts", id); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM account_settings WHERE account_id=?", id); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM accounts WHERE id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}
