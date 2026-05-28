package main

import (
	"log"
	"net/http"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	log.Println("api listening on :3000")
	if err := http.ListenAndServe(":3000", mux); err != nil {
		log.Fatal(err)
	}
}