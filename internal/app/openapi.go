package app

import (
	_ "embed"
	"net/http"
)

// openapiJSON is the OpenAPI 3.1 spec for the v1 machine API — the single source of
// truth, served at GET /api/openapi.json (public) and rendered by the in-app 接口说明.
//
//go:embed openapi.json
var openapiJSON []byte

func (s *Server) apiOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(openapiJSON)
}
