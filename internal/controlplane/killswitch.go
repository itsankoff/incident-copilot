package controlplane

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/itsankoff/incident-copilot/internal/audit"
)

// KillSwitchHandler lets an operator read and set the kill switch, which caps
// every later decision at its level with no restart (ADR 0003 D3). L0 to L2
// set it and "" clears it. It has no authentication, so like the Evidence
// debug handler it is served only on the loopback debug listener.
func KillSwitchHandler(ks KillSwitchStore, log *audit.Log, now func() time.Time) http.Handler {
	type body struct {
		Level string `json:"level"`
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/kill-switch", func(w http.ResponseWriter, r *http.Request) {
		level, err := ks.KillSwitch(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, body{level})
	})
	mux.HandleFunc("PUT /debug/kill-switch", func(w http.ResponseWriter, r *http.Request) {
		var b body
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&b); err != nil {
			http.Error(w, "unparseable body", http.StatusBadRequest)
			return
		}
		if l, err := ParseLevel(b.Level); b.Level != "" && (err != nil || l > L2) {
			http.Error(w, `level: want L0, L1, L2, or "" to clear`, http.StatusBadRequest)
			return
		}
		previous, err := ks.KillSwitch(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := ks.SetKillSwitch(r.Context(), b.Level); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := log.Append(now(), "killswitch.set", 0, map[string]string{"level": b.Level, "previous": previous, "remote": r.RemoteAddr}); err != nil {
			http.Error(w, "audit", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}
