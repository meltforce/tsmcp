package auth

import (
	"encoding/json"
	"net/http"
)

type protectedResourceMetadata struct {
	Resource                string   `json:"resource"`
	AuthorizationServers    []string `json:"authorization_servers"`
	BearerMethodsSupported  []string `json:"bearer_methods_supported"`
}

// MetadataHandler returns an HTTP handler that serves the RFC 9728
// OAuth Protected Resource Metadata document.
func MetadataHandler(resource string, authorizationServers []string) http.Handler {
	resp := protectedResourceMetadata{
		Resource:               resource,
		AuthorizationServers:   authorizationServers,
		BearerMethodsSupported: []string{"header"},
	}
	body, _ := json.Marshal(resp)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Write(body)
	})
}
