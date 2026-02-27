package health

import (
	"encoding/json"
	"net/http"
)

// Checker reports readiness of a subsystem.
type Checker interface {
	Ready() bool
}

type response struct {
	Status         string `json:"status"`
	TsnetConnected bool   `json:"tsnet_connected"`
}

// Handler returns the health check HTTP handler.
func Handler(checker Checker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ready := checker.Ready()

		resp := response{
			TsnetConnected: ready,
		}
		if ready {
			resp.Status = "ok"
		} else {
			resp.Status = "degraded"
		}

		w.Header().Set("Content-Type", "application/json")
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(resp)
	})
}
