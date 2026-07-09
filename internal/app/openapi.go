package app

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"
)

// openapiJSON is the OpenAPI 3.1 spec for the v1 machine API — the single source of
// truth, served at GET /api/openapi.json (public) and rendered by the in-app 接口说明.
// The served response can localize human-readable fields with ?lang=... or Accept-Language.
//
//go:embed openapi.json
var openapiJSON []byte

func (s *Server) apiOpenAPI(w http.ResponseWriter, r *http.Request) {
	lang := openAPILang(r)
	body := openapiJSON
	if localized, err := localizedOpenAPIJSON(lang); err == nil {
		body = localized
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Language", lang)
	w.Header().Add("Vary", "Accept-Language")
	w.Write(body)
}

func openAPILang(r *http.Request) string {
	if lang := normalizeOpenAPILang(r.URL.Query().Get("lang")); lang != "" {
		return lang
	}
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag := strings.TrimSpace(strings.Split(part, ";")[0])
		if lang := normalizeOpenAPILang(tag); lang != "" {
			return lang
		}
	}
	return "zh-CN"
}

func normalizeOpenAPILang(raw string) string {
	tag := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(raw), "_", "-"))
	if tag == "" {
		return ""
	}
	parts := strings.Split(tag, "-")
	switch parts[0] {
	case "en":
		return "en-US"
	case "zh":
		for _, p := range parts[1:] {
			switch p {
			case "tw", "hk", "mo", "hant":
				return "zh-TW"
			case "cn", "sg", "hans":
				return "zh-CN"
			}
		}
		return "zh-CN"
	}
	return ""
}

func localizedOpenAPIJSON(lang string) ([]byte, error) {
	var spec any
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		return nil, err
	}
	applyOpenAPILocale(spec, lang)
	return json.MarshalIndent(spec, "", "  ")
}

func applyOpenAPILocale(v any, lang string) {
	switch x := v.(type) {
	case map[string]any:
		if raw, ok := x["x-i18n"].(map[string]any); ok {
			if patch, ok := raw[lang].(map[string]any); ok {
				mergeOpenAPILocale(x, patch)
			}
			delete(x, "x-i18n")
		}
		for _, child := range x {
			applyOpenAPILocale(child, lang)
		}
	case []any:
		for _, child := range x {
			applyOpenAPILocale(child, lang)
		}
	}
}

func mergeOpenAPILocale(dst, patch map[string]any) {
	for k, pv := range patch {
		pm, pok := pv.(map[string]any)
		dm, dok := dst[k].(map[string]any)
		if pok && dok {
			mergeOpenAPILocale(dm, pm)
			continue
		}
		dst[k] = pv
	}
}
