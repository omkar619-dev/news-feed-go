-- Eval corpus: 24 realistic posts across 6 topics, with EXPLICIT ids (1001+) so
-- the labeled query set (labels.json) can reference them deterministically.
-- author_id 7 must exist (the load-test user). Re-runnable via ON CONFLICT.
--
-- Run with:
--   docker exec -i news-feed-go-postgres-1 psql -U newsfeed -d newsfeed < eval/seed.sql
--
-- The content is deliberately worded to stress BOTH search arms:
--   * exact-term wins: "goroutine leak", "biryani", "reciprocal rank fusion"
--   * meaning-only wins: a query for "cat" must find the "kitten" post (1017);
--     "hiking in the mountains" must find the "Himalayas" post (1014).

INSERT INTO posts (id, author_id, content) VALUES
  -- Go / backend
  (1001, 7, 'Debugging a goroutine leak that slowly ate all our memory in production'),
  (1002, 7, 'Why I switched from database/sql to pgx for our Postgres workloads'),
  (1003, 7, 'Sizing the pgx connection pool: max conns, idle conns, and connection lifetime'),
  (1004, 7, 'Understanding Go channels and select for everyday concurrency'),
  (1005, 7, 'Context cancellation patterns for graceful shutdown in a Go service'),
  (1006, 7, 'Profiling CPU and memory with pprof to hunt down hot paths'),
  -- Databases
  (1007, 7, 'Postgres indexing: when a B-tree helps and when you reach for a GIN index'),
  (1008, 7, 'Killing N+1 queries by collapsing them into a single JOIN'),
  (1009, 7, 'How write-ahead logging keeps a database durable after a crash'),
  -- Cooking
  (1010, 7, 'My foolproof recipe for a creamy mushroom risotto'),
  (1011, 7, 'Slow-cooked Hyderabadi biryani with saffron and crispy fried onions'),
  (1012, 7, 'Baking sourdough: managing the starter and the long overnight cold proof'),
  (1013, 7, 'A quick weeknight pasta with garlic, chilli flakes and olive oil'),
  -- Travel
  (1014, 7, 'Backpacking through the Himalayas: gear, layering and altitude tips'),
  (1015, 7, 'A slow week on the quieter beaches of Goa, away from the crowds'),
  (1016, 7, 'Solo trip planning: budgeting for long-distance trains across India'),
  -- Pets
  (1017, 7, 'Adopted the fluffiest kitten this weekend, everyone meet Biscuit'),
  (1018, 7, 'Training a new puppy to stop chewing the furniture'),
  (1019, 7, 'Why cats knead soft blankets — the science behind the behaviour'),
  -- Fitness
  (1020, 7, 'Couch to 5k: how I started running without completely hating it'),
  (1021, 7, 'Building a simple home gym on a tight budget'),
  -- Infra / misc
  (1022, 7, 'Setting up Prometheus and Grafana to watch service latency over time'),
  (1023, 7, 'Reciprocal Rank Fusion explained: blending two search rankings into one'),
  (1024, 7, 'Cutting API latency by putting a Redis cache in front of Postgres'),
  -- Rare / opaque-token posts: identifiers the embedding model has no good
  -- vector for. These are exactly where the keyword arm should beat semantic.
  (1025, 7, 'Fixed the ORA-12154 TNS could not resolve error by correcting the tnsnames.ora entry'),
  (1026, 7, 'Upgrading to pgx v5.7.1 quietly changed the CopyFrom method signature'),
  (1027, 7, 'Our SKU-4417X widget keeps failing the warehouse barcode scan'),
  (1028, 7, 'Setting GOMAXPROCS=1 surfaced a data race we had been missing for months')
ON CONFLICT (id) DO NOTHING;

-- Keep the serial sequence ahead of our explicit ids so future API inserts
-- (POST /posts) don't eventually collide with the 1001+ block.
SELECT setval(pg_get_serial_sequence('posts', 'id'), (SELECT MAX(id) FROM posts));
