package main

import (
	"embed"
	"flag"
	"fmt"
	"log"
	"net/http"
)

//go:embed static/*
var staticFiles embed.FS

func main() {
	hubURL := flag.String("hub", "http://localhost:9000", "Momagrid Hub URL")
	port := flag.Int("port", 9080, "mgui server port")
	flag.Parse()

	handler := NewHandler(*hubURL)
	mux := http.NewServeMux()

	// Serve static files from embedded FS
	mux.Handle("/", http.FileServer(http.FS(staticFiles)))

	mux.HandleFunc("/api/probe", handler.Probe)
	mux.HandleFunc("/api/join", handler.Join)
	mux.HandleFunc("/api/chat", handler.Chat)
	mux.HandleFunc("/api/providers", handler.handleListProviders)
	mux.HandleFunc("/api/hub/", handler.HubProxy)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("mgui starting on http://localhost%s (hub: %s)", addr, *hubURL)
	log.Fatal(http.ListenAndServe(addr, mux))
}
