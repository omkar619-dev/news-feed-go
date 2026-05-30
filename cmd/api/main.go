package main

import (
    "context"
    "encoding/json"
    "errors"
    "log"
    "net/http"
    "os"
    "os/signal"
    "syscall"
    "time"

    "github.com/omkar619-dev/news-feed-go/internal/db"
    "github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
	"github.com/omkar619-dev/news-feed-go/internal/user"
	"github.com/omkar619-dev/news-feed-go/internal/auth"

)

func main() {
    log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)

    rootCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer cancel()

    pool, err := db.New(rootCtx)
    if err != nil {
        log.Fatalf("db setup failed: %v", err)
    }
    defer pool.Close()
    log.Println("connected to postgres")

    queries := sqlc.New(pool)
    // _ = queries
	jwtSecret := os.Getenv("JWT_SECRET")
if jwtSecret == "" {
    jwtSecret = "dev-secret-change-me-in-production"
}
tokenManager := auth.NewTokenManager(jwtSecret, 24*time.Hour)

    mux := http.NewServeMux()

    mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
        ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
        defer cancel()

        if err := pool.Ping(ctx); err != nil {
            w.Header().Set("Content-Type", "application/json")
            w.WriteHeader(http.StatusServiceUnavailable)
            json.NewEncoder(w).Encode(map[string]string{
                "status": "unhealthy",
                "error":  err.Error(),
            })
            return
        }

        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusOK)
        json.NewEncoder(w).Encode(map[string]string{
            "status": "ok",
        })
    })
	mux.Handle("/signup", user.NewSignupHandler(queries))
	mux.Handle("/login", user.NewLoginHandler(queries, tokenManager))

    server := &http.Server{
        Addr:              ":3000",
        Handler:           mux,
        ReadHeaderTimeout: 5 * time.Second,
        ReadTimeout:       10 * time.Second,
        WriteTimeout:      10 * time.Second,
        IdleTimeout:       60 * time.Second,
    }

    serverErr := make(chan error, 1)
    go func() {
        log.Println("listening on :3000")
        if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
            serverErr <- err
        }
    }()

    select {
    case err := <-serverErr:
        log.Fatalf("server error: %v", err)
    case <-rootCtx.Done():
        log.Println("shutdown signal received")
    }

    shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer shutdownCancel()

    if err := server.Shutdown(shutdownCtx); err != nil {
        log.Printf("graceful shutdown failed: %v", err)
    } else {
        log.Println("server stopped cleanly")
    }
}