package app

import (
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
)

const stockFavoriteLimit = 500

var (
	errStockFavoriteLimit      = errors.New("stock favorite limit reached")
	errStockFavoriteSetChanged = errors.New("stock favorite set changed")
	errStockFavoriteOwnerGone  = errors.New("stock favorite owner no longer exists")
)

type FavoriteIdentity struct {
	Market string `json:"market"`
	Symbol string `json:"symbol"`
}

type StockFavorite struct {
	Market    string `json:"market"`
	Symbol    string `json:"symbol"`
	Ord       int    `json:"ord"`
	CreatedAt string `json:"createdAt"`
}

func (f StockFavorite) Key() string { return f.Market + f.Symbol }

// lockStockFavoriteOwner serializes count-and-insert and reorder operations per account. The
// no-value UPDATE is intentional: it takes the user-row write lock on both supported drivers while
// also proving that a session owner was not deleted between authentication and this transaction.
func (s *Store) lockStockFavoriteOwner(tx *sql.Tx, username string) error {
	res, err := tx.Exec(s.bind("UPDATE users SET session_rev=COALESCE(session_rev,0) WHERE username=?"), username)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errStockFavoriteOwnerGone
	}
	return nil
}

func (s *Store) StockFavorites(username string) []StockFavorite {
	out, _ := s.stockFavorites(username)
	return out
}

func (s *Store) stockFavorites(username string) ([]StockFavorite, error) {
	rows, err := s.query(`SELECT market,symbol,ord,created_at FROM user_stock_favorites
		WHERE username=? ORDER BY ord,created_at,market,symbol`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]StockFavorite, 0)
	for rows.Next() {
		var f StockFavorite
		if err := rows.Scan(&f.Market, &f.Symbol, &f.Ord, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// AddStockFavorite is idempotent and enforces the per-account bound in the same transaction as the
// insert. Locking the owner row prevents two concurrent 500th inserts from both observing 499.
func (s *Store) AddStockFavorite(username, market, symbol string) (StockFavorite, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return StockFavorite{}, false, err
	}
	defer tx.Rollback()
	if err := s.lockStockFavoriteOwner(tx, username); err != nil {
		return StockFavorite{}, false, err
	}

	var existing StockFavorite
	err = tx.QueryRow(s.bind(`SELECT market,symbol,ord,created_at FROM user_stock_favorites
		WHERE username=? AND market=? AND symbol=?`), username, market, symbol).
		Scan(&existing.Market, &existing.Symbol, &existing.Ord, &existing.CreatedAt)
	if err == nil {
		return existing, false, tx.Commit()
	}
	if err != sql.ErrNoRows {
		return StockFavorite{}, false, err
	}

	var count, ord int
	if err := tx.QueryRow(s.bind("SELECT COUNT(*),COALESCE(MAX(ord),-1)+1 FROM user_stock_favorites WHERE username=?"), username).
		Scan(&count, &ord); err != nil {
		return StockFavorite{}, false, err
	}
	if count >= stockFavoriteLimit {
		return StockFavorite{}, false, errStockFavoriteLimit
	}
	f := StockFavorite{Market: market, Symbol: symbol, Ord: ord, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if _, err := tx.Exec(s.bind(`INSERT INTO user_stock_favorites(username,market,symbol,ord,created_at)
		VALUES(?,?,?,?,?)`), username, f.Market, f.Symbol, f.Ord, f.CreatedAt); err != nil {
		return StockFavorite{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return StockFavorite{}, false, err
	}
	return f, true, nil
}

func (s *Store) RemoveStockFavorite(username, market, symbol string) error {
	_, err := s.exec("DELETE FROM user_stock_favorites WHERE username=? AND market=? AND symbol=?", username, market, symbol)
	return err
}

// ReorderStockFavorites updates positions only when the submitted identities are exactly the
// caller's current set. The check and all writes share one owner-row-locked transaction, so a stale
// browser cannot drop an item that another tab added or recreate one another tab removed.
func (s *Store) ReorderStockFavorites(username string, wanted []FavoriteIdentity) error {
	if len(wanted) > stockFavoriteLimit {
		return errStockFavoriteSetChanged
	}
	wantSet := make(map[string]bool, len(wanted))
	for _, item := range wanted {
		key := item.Market + "\x00" + item.Symbol
		if item.Market == "" || item.Symbol == "" || wantSet[key] {
			return errStockFavoriteSetChanged
		}
		wantSet[key] = true
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.lockStockFavoriteOwner(tx, username); err != nil {
		return err
	}
	rows, err := tx.Query(s.bind("SELECT market,symbol FROM user_stock_favorites WHERE username=?"), username)
	if err != nil {
		return err
	}
	stored := map[string]bool{}
	for rows.Next() {
		var market, symbol string
		if err := rows.Scan(&market, &symbol); err != nil {
			rows.Close()
			return err
		}
		stored[market+"\x00"+symbol] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(stored) != len(wantSet) {
		return errStockFavoriteSetChanged
	}
	for key := range wantSet {
		if !stored[key] {
			return errStockFavoriteSetChanged
		}
	}
	for ord, item := range wanted {
		res, err := tx.Exec(s.bind(`UPDATE user_stock_favorites SET ord=?
			WHERE username=? AND market=? AND symbol=?`), ord, username, item.Market, item.Symbol)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return errStockFavoriteSetChanged
		}
	}
	return tx.Commit()
}

// LatestReportsForFavoriteSymbols returns one newest reader-visible, non-plumbing report per
// A-share code. It is a single bounded query rather than one NewBySymbol call per favorite.
func (s *Store) LatestReportsForFavoriteSymbols(symbols []string, sc *ownerScope) (map[string]Rep, error) {
	out := map[string]Rep{}
	seen := map[string]bool{}
	unique := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		if symbol != "" && !seen[symbol] {
			seen[symbol] = true
			unique = append(unique, symbol)
		}
	}
	if len(unique) == 0 {
		return out, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(unique)), ",")
	args := make([]any, 0, len(unique)+len(internalTypes)+8)
	for _, symbol := range unique {
		args = append(args, symbol)
	}
	where := "r.symbol IN (" + marks + ")"
	types := make([]string, 0, len(internalTypes))
	for rtype := range internalTypes {
		types = append(types, rtype)
	}
	sort.Strings(types)
	if len(types) > 0 {
		where += " AND r.rtype NOT IN (" + strings.TrimSuffix(strings.Repeat("?,", len(types)), ",") + ")"
		for _, rtype := range types {
			args = append(args, rtype)
		}
	}
	if frag, fargs := sc.where("r."); frag != "" {
		where += " AND " + frag
		args = append(args, fargs...)
	}
	q := `WITH ranked AS (
		SELECT r.id,r.title,r.symbol,r.name,r.rtype,r.rdate,r.kind,r.run_id,r.source,r.sent_at,
			COALESCE(r.version,'') AS version,
			ROW_NUMBER() OVER(PARTITION BY r.symbol ORDER BY r.rdate DESC,r.sent_at DESC,r.id DESC) AS favorite_rank
		FROM reports r WHERE ` + where + `)
		SELECT id,title,symbol,name,rtype,rdate,kind,run_id,source,sent_at,version
		FROM ranked WHERE favorite_rank=1`
	rows, err := s.query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		rep := scanNewRow(rows)
		out[rep.Symbol] = rep
	}
	return out, rows.Err()
}
