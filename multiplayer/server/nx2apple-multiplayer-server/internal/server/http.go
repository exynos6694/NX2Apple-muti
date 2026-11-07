package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type API struct{ store *Store }

func NewAPI(store *Store) *API { return &API{store: store} }

func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("POST /v1/presence", a.presence)
	mux.HandleFunc("POST /v1/heartbeat", a.heartbeat)
	mux.HandleFunc("DELETE /v1/presence/{clientID}", a.leave)
	mux.HandleFunc("GET /v1/players", a.players)
	mux.HandleFunc("POST /v1/signals", a.addSignal)
	mux.HandleFunc("GET /v1/signals", a.signals)
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) presence(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClientID string `json:"client_id"`
		Nickname string `json:"nickname"`
		Game     Game   `json:"game"`
	}
	if !decode(w, r, &req) {
		return
	}
	p := Player{ClientID: req.ClientID, Nickname: req.Nickname, Game: req.Game}
	if err := a.store.Register(p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_presence")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"player": p})
}

func (a *API) heartbeat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClientID string `json:"client_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := a.store.Heartbeat(req.ClientID); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
}

func (a *API) leave(w http.ResponseWriter, r *http.Request) {
	a.store.Leave(r.PathValue("clientID"))
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) players(w http.ResponseWriter, r *http.Request) {
	titleID := r.URL.Query().Get("title_id")
	version := r.URL.Query().Get("game_version")
	protocol := r.URL.Query().Get("protocol")
	if titleID == "" || version == "" || protocol == "" {
		writeError(w, http.StatusBadRequest, "title_id_game_version_and_protocol_required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"players": a.store.Players(titleID, version, protocol)})
}

func (a *API) addSignal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From string         `json:"from"`
		To   string         `json:"to"`
		Kind string         `json:"kind"`
		Data map[string]any `json:"data"`
	}
	if !decode(w, r, &req) {
		return
	}
	item, err := a.store.AddSignal(req.From, req.To, req.Kind, req.Data)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, item)
}

func (a *API) signals(w http.ResponseWriter, r *http.Request) {
	clientID := r.URL.Query().Get("client_id")
	after, err := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	if err != nil && r.URL.Query().Get("after") != "" {
		writeError(w, http.StatusBadRequest, "invalid_after")
		return
	}
	items, err := a.store.Signals(clientID, after)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"signals": items})
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return false
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return false
	}
	return true
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request")
	case errors.Is(err, ErrMissing):
		writeError(w, http.StatusNotFound, "client_not_registered")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error")
	}
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			w.Header().Set("Vary", "Origin")
		}
		next.ServeHTTP(w, r)
	})
}
