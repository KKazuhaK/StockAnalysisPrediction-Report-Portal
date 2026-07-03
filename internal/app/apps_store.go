package app

import (
	"database/sql"
	"sort"
	"strings"
)

// App is an installed downloadable app: its manifest plus (separately, in
// app_files) its self-contained frontend assets. See docs/adr/0003-downloadable-apps.md.
type App struct {
	ID      string
	Name    string
	Icon    string
	Version string
	Entry   string   // relative path of the HTML entry point, e.g. "index.html"
	Scopes  []string // API scopes the app declares it needs (e.g. "query")
	Created string
}

// AppFile is one asset of an app bundle (the raw bytes and the content type to
// serve them with).
type AppFile struct {
	Ctype   string
	Content []byte
}

// InstallApp writes an app's manifest and files, fully replacing any prior
// install of the same id (so a re-install never leaves stale files behind).
func (s *Store) InstallApp(app App, files map[string]AppFile) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(s.bind("DELETE FROM app_files WHERE app_id=?"), app.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(s.bind("DELETE FROM apps WHERE id=?"), app.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(s.bind(`INSERT INTO apps(id,name,icon,version,entry,scopes,created_at) VALUES(?,?,?,?,?,?,?)`),
		app.ID, app.Name, app.Icon, app.Version, app.Entry, strings.Join(app.Scopes, ","), nowStr()); err != nil {
		return err
	}
	stmt, err := tx.Prepare(s.bind("INSERT INTO app_files(app_id,path,ctype,content) VALUES(?,?,?,?)"))
	if err != nil {
		return err
	}
	defer stmt.Close()
	for path, f := range files {
		if _, err := stmt.Exec(app.ID, path, f.Ctype, f.Content); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// scanApp reads one apps row (scopes joined into a comma string) into an App.
func scanApp(scan func(dest ...any) error) (App, error) {
	var a App
	var icon, version, entry, scopes, created sql.NullString
	if err := scan(&a.ID, &a.Name, &icon, &version, &entry, &scopes, &created); err != nil {
		return App{}, err
	}
	a.Icon, a.Version, a.Entry, a.Created = icon.String, version.String, entry.String, created.String
	a.Scopes = splitCSV(scopes.String)
	return a, nil
}

// ListApps returns all installed apps, newest first.
func (s *Store) ListApps() []App {
	rows, err := s.query("SELECT id,name,icon,version,entry,scopes,created_at FROM apps ORDER BY created_at DESC, id")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []App
	for rows.Next() {
		a, err := scanApp(rows.Scan)
		if err != nil {
			continue
		}
		out = append(out, a)
	}
	return out
}

// GetApp returns one app's manifest by id.
func (s *Store) GetApp(id string) (App, bool) {
	a, err := scanApp(s.queryRow("SELECT id,name,icon,version,entry,scopes,created_at FROM apps WHERE id=?", id).Scan)
	if err != nil {
		return App{}, false
	}
	return a, true
}

// DeleteApp uninstalls an app and removes all of its files.
func (s *Store) DeleteApp(id string) error {
	if _, err := s.exec("DELETE FROM app_files WHERE app_id=?", id); err != nil {
		return err
	}
	_, err := s.exec("DELETE FROM apps WHERE id=?", id)
	return err
}

// AppFile returns one file's content type and bytes, or ok=false if absent.
func (s *Store) AppFile(id, path string) (ctype string, content []byte, ok bool) {
	var ct sql.NullString
	var c []byte
	if err := s.queryRow("SELECT ctype,content FROM app_files WHERE app_id=? AND path=?", id, path).Scan(&ct, &c); err != nil {
		return "", nil, false
	}
	return ct.String, c, true
}

// splitCSV splits a comma-separated list, trimming spaces and dropping empties.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
