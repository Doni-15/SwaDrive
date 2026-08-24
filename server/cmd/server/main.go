package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
)

func newHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		if err := json.NewEncoder(w).Encode(map[string]string{
			"status": "ok",
		}); err != nil {
			log.Printf("write health response: %v", err)
		}
	})

	return mux
}

func main() {
	server := &http.Server{
		Addr:              ":8080",
		Handler:           newHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("SwaDrive server listening on %s", server.Addr)

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
