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

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/omkar619-dev/news-feed-go/internal/auth"
	"github.com/omkar619-dev/news-feed-go/internal/cache"
	"github.com/omkar619-dev/news-feed-go/internal/comment"
	"github.com/omkar619-dev/news-feed-go/internal/db"
	"github.com/omkar619-dev/news-feed-go/internal/embed"
	"github.com/omkar619-dev/news-feed-go/internal/feed"
	"github.com/omkar619-dev/news-feed-go/internal/follow"
	"github.com/omkar619-dev/news-feed-go/internal/logging"
	"github.com/omkar619-dev/news-feed-go/internal/metrics"
	"github.com/omkar619-dev/news-feed-go/internal/post"
	"github.com/omkar619-dev/news-feed-go/internal/ratelimit"
	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
	"github.com/omkar619-dev/news-feed-go/internal/search"
	"github.com/omkar619-dev/news-feed-go/internal/user"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)
	logging.Setup() // slog → JSON; used by RequestLogger and any slog.* calls

	rootCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pool, err := db.New(rootCtx)
	if err != nil {
		log.Fatalf("db setup failed: %v", err)
	}
	defer pool.Close()
	log.Println("connected to postgres")

	queries := sqlc.New(pool)

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = "dev-secret-change-me-in-production"
	}
	tokenManager := auth.NewTokenManager(jwtSecret, 24*time.Hour)

	// NOTE: the API no longer talks to RabbitMQ. POST /posts now writes a
	// "post.created" row to the OUTBOX table in the SAME transaction as the post
	// (see internal/post/create.go); the separate relay process (cmd/relay)
	// publishes outbox rows to the broker. So the web tier only depends on Postgres.

	// Connect to Redis — used for cache-aside on the feed (host 6380).
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6380"
	}
	cacheClient, err := cache.New(redisAddr)
	if err != nil {
		log.Fatalf("cache setup failed: %v", err)
	}
	defer cacheClient.Close()
	log.Println("connected to redis")

	// Sliding-window rate limiter, sharing the cache's Redis pool.
	// 5 posts per minute per user.
	postLimiter := ratelimit.New(cacheClient.Redis(), 5, time.Minute)

	// Embedding client (Ollama) for semantic search queries.
	ollamaURL := os.Getenv("OLLAMA_URL")
	if ollamaURL == "" {
		ollamaURL = "http://localhost:11434"
	}
	embedClient := embed.New(ollamaURL, "all-minilm")

	// chi.NewRouter() returns a *chi.Mux, which itself implements http.Handler.
	// So it drops into http.Server.Handler in the exact slot the old mux used.
	r := chi.NewRouter()

	// ── Global middleware: runs on EVERY request, in the order listed ──
	r.Use(middleware.RequestID) // tag each request with a unique ID (handy in logs)
	r.Use(logging.RequestLogger) // structured (JSON) request log: method, route, status, duration
	r.Use(middleware.Recoverer) // recover from a panic in any handler → 500, not a crash
	r.Use(metrics.Middleware)   // record Prometheus request count + latency for every request

	// ── Public routes (no token required) ──
	r.Get("/healthz", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
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
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// NewSignupHandler/NewLoginHandler return http.Handler values, so we use
	// r.Method(verb, pattern, handler). (r.Post wants an http.HandlerFunc, which
	// is a slightly different shape — r.Method takes a full http.Handler.)
	r.Method(http.MethodPost, "/signup", user.NewSignupHandler(queries))
	r.Method(http.MethodPost, "/login", user.NewLoginHandler(queries, tokenManager))

	// GET /posts/{id} — read a single post. PUBLIC: registered here, OUTSIDE
	// the auth group, so no token is needed (anyone can read a post). Contrast
	// with POST /posts below, which lives inside the protected group.
	r.Get("/posts/{id}", post.NewGetPostHandler(queries))

	// GET /users/{id}/posts — list a user's posts, newest first, cursor-paginated.
	// PUBLIC: anyone can browse a user's posts. Supports ?limit= and ?cursor=.
	r.Get("/users/{id}/posts", post.NewListUserPostsHandler(queries))

	// GET followers / following — PUBLIC views of the social graph (id+username only).
	r.Get("/users/{id}/followers", follow.NewListFollowersHandler(queries))
	r.Get("/users/{id}/following", follow.NewListFollowingHandler(queries))

	// GET /users/{username} — PUBLIC profile by username (basic info + counts).
	r.Get("/users/{username}", user.NewProfileHandler(queries))

	// Semantic search + "related posts" — PUBLIC, powered by pgvector.
	r.Get("/search", search.NewSearchHandler(queries, embedClient))
	// Hybrid search — PUBLIC. Keyword (full-text) + semantic (vector), fused
	// with Reciprocal Rank Fusion. Catches both exact terms and meaning.
	r.Get("/search/hybrid", search.NewHybridSearchHandler(queries, embedClient))
	r.Get("/posts/{id}/related", search.NewRelatedHandler(queries))
	// Near-duplicate detection — PUBLIC. Posts within a cosine-distance threshold
	// of {id}: the astroturfing / spam-cluster defence (reuses the embeddings).
	r.Get("/posts/{id}/duplicates", search.NewDuplicatesHandler(queries))

	// GET /posts/{id}/comments — PUBLIC: the threaded comment tree.
	r.Get("/posts/{id}/comments", comment.NewGetCommentsHandler(queries))

	// Prometheus scrape endpoint (PUBLIC) — exposes the metrics the middleware records.
	r.Handle("/metrics", promhttp.Handler())

	// ── Protected routes: auth applied ONCE to the whole group ──
	// Every route registered inside this func runs behind RequireAuth. Add
	// POST /posts, DELETE /posts/{id}, /follow, etc. here and they're guarded
	// automatically — no per-route wrapping.
	r.Group(func(pr chi.Router) {
		pr.Use(tokenManager.RequireAuth)

		pr.Get("/me", func(w http.ResponseWriter, req *http.Request) {
			userID, ok := auth.UserIDFromContext(req.Context())
			if !ok {
				// Unreachable behind RequireAuth, but we defend anyway.
				http.Error(w, "no user in context", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]int64{"user_id": userID})
		})

		// POST /posts — create a post as the authenticated user. Lives inside
		// this group, so RequireAuth runs first and the handler can trust the
		// userID in context. NewCreatePostHandler returns an http.HandlerFunc,
		// so pr.Post accepts it directly.
		// POST /posts is rate-limited: .With() applies the limiter middleware to
		// JUST this route (it runs after the group's RequireAuth, so the userID
		// is already in context for per-user limiting).
		pr.With(postLimiter.Middleware("post")).Post("/posts", post.NewCreatePostHandler(pool))

		// DELETE /posts/{id} — protected AND owner-only. Same path as the
		// public GET /posts/{id}, but a different method, so chi routes them
		// separately: GET is public, DELETE runs behind RequireAuth here.
		pr.Delete("/posts/{id}", post.NewDeletePostHandler(queries))

		// Likes (protected): the authenticated user likes / unlikes a post.
		pr.Post("/posts/{id}/like", post.NewLikeHandler(queries, cacheClient))
		pr.Delete("/posts/{id}/like", post.NewUnlikeHandler(queries, cacheClient))

		// Comments (protected): add a comment/reply on a post, delete your own.
		pr.Post("/posts/{id}/comments", comment.NewCreateCommentHandler(queries))
		pr.Delete("/comments/{id}", comment.NewDeleteCommentHandler(queries))

		// Social graph (protected): the authenticated user follows / unfollows
		// user {id}. The follower is always the token's userID.
		pr.Post("/users/{id}/follow", follow.NewFollowHandler(queries))
		pr.Delete("/users/{id}/follow", follow.NewUnfollowHandler(queries))

		// The home feed (protected): posts from everyone the user follows.
		pr.Get("/feed", feed.NewFeedHandler(queries, cacheClient))
	})

	server := &http.Server{
		Addr:              ":3000",
		Handler:           r,
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
