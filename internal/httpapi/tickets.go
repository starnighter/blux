package httpapi

import (
	"blux/internal/store"
	"net/http"
)

func (s *Server) registerTicketRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/tasks/{task}/ticket", func(w http.ResponseWriter, r *http.Request) {
		t, err := s.Store.Ticket(r.Context(), r.PathValue("task"))
		if err != nil {
			s.storeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, t)
	})
	mux.HandleFunc("POST /api/v1/tasks/{task}/ticket/actions", func(w http.ResponseWriter, r *http.Request) {
		key, ok := idempotencyKey(w, r)
		if !ok {
			return
		}
		var a store.TicketAction
		if !readJSON(w, r, &a) {
			return
		}
		t, err := s.Store.ActOnTicket(r.Context(), r.PathValue("task"), key, a)
		if err != nil {
			s.storeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, t)
	})
}
