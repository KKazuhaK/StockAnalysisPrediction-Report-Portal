package app

import (
	"errors"
	"log"
	"net/http"
	"strings"
)

type favoriteReportView struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Title string `json:"title"`
	Date  string `json:"date"`
}

type favoriteItemView struct {
	Market string              `json:"market"`
	Symbol string              `json:"symbol"`
	Key    string              `json:"key"`
	Ord    int                 `json:"ord"`
	Report *favoriteReportView `json:"report,omitempty"`
}

type favoriteListResponse struct {
	Items []favoriteItemView `json:"items"`
	Limit int                `json:"limit"`
}

func (s *Server) favoriteView(favorite StockFavorite, latest map[string]Rep) favoriteItemView {
	item := favoriteItemView{Market: favorite.Market, Symbol: favorite.Symbol, Key: favorite.Key(), Ord: favorite.Ord}
	if rep, ok := latest[favorite.Symbol]; ok && marketPrefix(favorite.Symbol) == favorite.Market {
		name := rep.Name
		if s.names != nil {
			name = firstNonEmpty(s.names.Get(rep.Symbol), name)
		}
		item.Report = &favoriteReportView{ID: rep.ID, Name: name, Title: s.repDisplayTitle(&rep), Date: rep.Date}
	}
	return item
}

func favoriteTarget(r *http.Request) (quoteTarget, error) {
	return quoteTargetFor(strings.ToLower(strings.TrimSpace(r.PathValue("market"))), r.PathValue("symbol"))
}

func (s *Server) favoriteList(user string) (favoriteListResponse, error) {
	favorites, err := s.st.stockFavorites(user)
	if err != nil {
		return favoriteListResponse{}, err
	}
	reportSymbols := make([]string, 0, len(favorites))
	for _, favorite := range favorites {
		// Reports use unqualified A-share codes. Match only the market the bare code resolves to, so
		// an explicitly favorited Shanghai index such as sh:000001 cannot borrow Shenzhen's report.
		if marketPrefix(favorite.Symbol) == favorite.Market {
			reportSymbols = append(reportSymbols, favorite.Symbol)
		}
	}
	latest, err := s.st.LatestReportsForFavoriteSymbols(reportSymbols, s.viewerScope(user))
	if err != nil {
		return favoriteListResponse{}, err
	}
	out := favoriteListResponse{Items: make([]favoriteItemView, 0, len(favorites)), Limit: stockFavoriteLimit}
	for _, favorite := range favorites {
		out.Items = append(out.Items, s.favoriteView(favorite, latest))
	}
	return out, nil
}

func (s *Server) writeFavoriteList(w http.ResponseWriter, user string) {
	out, err := s.favoriteList(user)
	if err != nil {
		log.Printf("favorite list failed: %v", err)
		jsonErrorCode(w, http.StatusInternalServerError, "favorite_read_failed", "读取收藏失败")
		return
	}
	writeJSON(w, out)
}

func (s *Server) apiFavorites(w http.ResponseWriter, _ *http.Request, user string) {
	s.writeFavoriteList(w, user)
}

func (s *Server) apiFavoriteAdd(w http.ResponseWriter, r *http.Request, user string) {
	target, err := favoriteTarget(r)
	if err != nil {
		jsonErrorCode(w, http.StatusBadRequest, "favorite_bad_symbol", "收藏的股票代码无效")
		return
	}
	favorite, _, err := s.st.AddStockFavorite(user, target.Market.id, target.Code)
	switch {
	case errors.Is(err, errStockFavoriteLimit):
		jsonErrorCode(w, http.StatusConflict, "favorite_limit", "收藏数量已达到上限")
		return
	case errors.Is(err, errStockFavoriteOwnerGone):
		jsonErrorCode(w, http.StatusUnauthorized, "session_expired", "登录已过期，请重新登录")
		return
	case err != nil:
		log.Printf("favorite add failed: %v", err)
		jsonErrorCode(w, http.StatusInternalServerError, "favorite_write_failed", "保存收藏失败")
		return
	}
	latest := map[string]Rep{}
	if marketPrefix(favorite.Symbol) == favorite.Market {
		if found, readErr := s.st.LatestReportsForFavoriteSymbols([]string{favorite.Symbol}, s.viewerScope(user)); readErr != nil {
			// The preference is already durable. Report decoration is optional, so a failed lookup must
			// not turn a successful idempotent write into an apparent failure that the browser rolls back.
			log.Printf("favorite report lookup after add failed: %v", readErr)
		} else {
			latest = found
		}
	}
	writeJSON(w, s.favoriteView(favorite, latest))
}

func (s *Server) apiFavoriteDelete(w http.ResponseWriter, r *http.Request, user string) {
	target, err := favoriteTarget(r)
	if err != nil {
		jsonErrorCode(w, http.StatusBadRequest, "favorite_bad_symbol", "收藏的股票代码无效")
		return
	}
	if err := s.st.RemoveStockFavorite(user, target.Market.id, target.Code); err != nil {
		log.Printf("favorite delete failed: %v", err)
		jsonErrorCode(w, http.StatusInternalServerError, "favorite_write_failed", "取消收藏失败")
		return
	}
	writeJSON(w, okJSON)
}

func (s *Server) apiFavoriteReorder(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Items []FavoriteIdentity `json:"items"`
	}
	if err := readJSON(r, &in); err != nil || len(in.Items) > stockFavoriteLimit {
		jsonErrorCode(w, http.StatusBadRequest, "favorite_bad_order", "收藏排序无效")
		return
	}
	canonical := make([]FavoriteIdentity, 0, len(in.Items))
	seen := map[string]bool{}
	for _, item := range in.Items {
		target, err := quoteTargetFor(strings.ToLower(strings.TrimSpace(item.Market)), item.Symbol)
		if err != nil {
			jsonErrorCode(w, http.StatusBadRequest, "favorite_bad_symbol", "收藏的股票代码无效")
			return
		}
		key := target.Symbol
		if seen[key] {
			jsonErrorCode(w, http.StatusBadRequest, "favorite_bad_order", "收藏排序包含重复股票")
			return
		}
		seen[key] = true
		canonical = append(canonical, FavoriteIdentity{Market: target.Market.id, Symbol: target.Code})
	}
	err := s.st.ReorderStockFavorites(user, canonical)
	switch {
	case errors.Is(err, errStockFavoriteSetChanged):
		jsonErrorCode(w, http.StatusConflict, "favorite_set_changed", "收藏列表已变化，请刷新后重试")
		return
	case errors.Is(err, errStockFavoriteOwnerGone):
		jsonErrorCode(w, http.StatusUnauthorized, "session_expired", "登录已过期，请重新登录")
		return
	case err != nil:
		log.Printf("favorite reorder failed: %v", err)
		jsonErrorCode(w, http.StatusInternalServerError, "favorite_write_failed", "保存收藏排序失败")
		return
	}
	s.writeFavoriteList(w, user)
}
