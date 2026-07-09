// Minimal Go HTTP starter scaffolded by Rigger.
// Rigger builds this with templates/dockerfiles/go/Dockerfile (static binary → alpine)
// and routes traffic to $PORT. Edit freely, push, and Rigger rebuilds + deploys.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"app":     "rigger-go-starter",
			"status":  "ok",
			"version": version,
		})
	})
	log.Printf("listening on :%s (version %s)", port, version)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
