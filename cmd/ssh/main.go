// Command ssh serves the news feed as an SSH-accessible terminal app: run
// `ssh -p 23234 localhost` and a Bubble Tea TUI of the feed appears in your
// terminal. There's no password and no token — wish hands us the client's SSH
// PUBLIC KEY on connect, and we look it up to identify the user. The SSH key IS
// the login. Actions (post/like/unlike/comment/delete/follow) call the SAME
// internal/service functions the HTTP API uses — fan-out, the hot-counter, the
// outbox, ownership checks, comment threading all apply here too.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	bm "github.com/charmbracelet/wish/bubbletea"
	"github.com/charmbracelet/wish/activeterm"
	"github.com/charmbracelet/wish/logging"
	"github.com/jackc/pgx/v5/pgxpool"
	gossh "golang.org/x/crypto/ssh"

	"github.com/omkar619-dev/news-feed-go/internal/cache"
	"github.com/omkar619-dev/news-feed-go/internal/db"
	"github.com/omkar619-dev/news-feed-go/internal/repository/postgres/sqlc"
	"github.com/omkar619-dev/news-feed-go/internal/service"
)

const (
	host = "localhost"
	port = "23234"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pool, err := db.New(ctx)
	if err != nil {
		log.Fatalf("db setup failed: %v", err)
	}
	defer pool.Close()
	queries := sqlc.New(pool)

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6380"
	}
	counter, err := cache.New(redisAddr)
	if err != nil {
		log.Fatalf("cache setup failed: %v", err)
	}
	defer counter.Close()

	// Host-key path is env-configurable so the deployed pod can write it somewhere
	// writable (/tmp); locally it falls back to the cwd.
	hostKeyPath := os.Getenv("HOST_KEY_PATH")
	if hostKeyPath == "" {
		hostKeyPath = "news_feed_host_ed25519"
	}

	srv, err := wish.NewServer(
		// Bind ALL interfaces (":" → 0.0.0.0). "localhost" would only be reachable
		// inside the pod, so the k8s Service couldn't route SSH traffic to it.
		wish.WithAddress(net.JoinHostPort("", port)),
		wish.WithHostKeyPath(hostKeyPath),
		wish.WithPublicKeyAuth(func(_ ssh.Context, _ ssh.PublicKey) bool { return true }),
		wish.WithMiddleware(
			bm.Middleware(newTeaHandler(pool, queries, counter)),
			activeterm.Middleware(),
			logging.Middleware(),
		),
	)
	if err != nil {
		log.Fatalf("ssh server setup failed: %v", err)
	}

	go func() {
		log.Printf("SSH feed listening — connect with: ssh -p %s %s", port, host)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			log.Fatalf("ssh server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down ssh server")
	shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
		log.Printf("shutdown failed: %v", err)
	}
}

func newTeaHandler(pool *pgxpool.Pool, queries *sqlc.Queries, counter *cache.Client) func(ssh.Session) (tea.Model, []tea.ProgramOption) {
	return func(s ssh.Session) (tea.Model, []tea.ProgramOption) {
		var userID int64
		username := "guest"
		if pk := s.PublicKey(); pk != nil {
			authKey := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(pk)))
			if u, err := queries.GetUserBySSHKey(s.Context(), authKey); err == nil {
				userID = u.ID
				username = u.Username
			}
		}

		posts, err := queries.ListRecentPosts(s.Context(), 50)
		if err != nil {
			log.Printf("ssh: load feed failed: %v", err)
		}

		ti := textinput.New() // the text box (used for both new posts and comments)
		ti.CharLimit = 280
		ti.Width = 60

		return model{
			ctx:      s.Context(),
			pool:     pool,
			queries:  queries,
			counter:  counter,
			userID:   userID,
			username: username,
			posts:    posts,
			input:    ti,
			// mode defaults to modeList (the zero value)
		}, []tea.ProgramOption{tea.WithAltScreen(), tea.WithMouseCellMotion()}
	}
}

// ── Modes: the model is a tiny state machine. Update/View branch on m.mode. ──
type viewMode int

const (
	modeList     viewMode = iota // the feed (default)
	modeInput                    // typing a new post or comment
	modeComments                 // reading a post's comment thread
)

// inputKind says what the open text box is for, so Enter knows which service to call.
type inputKind int

const (
	inputPost inputKind = iota
	inputComment
)

type model struct {
	ctx      context.Context
	pool     *pgxpool.Pool
	queries  *sqlc.Queries
	counter  *cache.Client
	userID   int64
	username string

	posts  []sqlc.ListRecentPostsRow
	cursor int
	height int
	status string

	mode       viewMode
	inputKind  inputKind
	input      textinput.Model
	returnMode viewMode // where to go when the text box closes (list or thread)

	comments      []sqlc.GetCommentTreeRow // loaded thread for modeComments
	commentsFor   int64                    // which post that thread belongs to
	commentCursor int                      // selected comment within the thread
	commentPostID int64                    // post the pending comment/reply targets
	commentParent *int64                   // parent comment id for a reply (nil = top-level)
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = msg.Height
		if msg.Width > 10 {
			m.input.Width = msg.Width - 6
		}
		return m, nil
	case tea.KeyMsg:
		// Route the keypress to the handler for the CURRENT mode.
		switch m.mode {
		case modeInput:
			return m.updateInput(msg)
		case modeComments:
			return m.updateComments(msg)
		default:
			return m.updateList(msg)
		}
	case tea.MouseMsg:
		if m.mode == modeList {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				if m.cursor > 0 {
					m.cursor--
				}
			case tea.MouseButtonWheelDown:
				if m.cursor < len(m.posts)-1 {
					m.cursor++
				}
			}
		}
		return m, nil
	}
	return m, nil
}

// updateList handles keys while browsing the feed.
func (m model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.posts)-1 {
			m.cursor++
		}
	case "l":
		m = m.toggleLike(1)
	case "u":
		m = m.toggleLike(-1)
	case "d":
		m = m.deleteSelected()
	case "f":
		m = m.followAuthor()
	case "n": // compose a new post
		if m.userID == 0 {
			m.status = "register your SSH key to act"
			break
		}
		m.mode = modeInput
		m.inputKind = inputPost
		m.input.Placeholder = "what's on your mind?"
		m.input.SetValue("")
		m.input.Focus()
		return m, textinput.Blink
	case "c": // top-level comment on the selected post
		if m.userID == 0 {
			m.status = "register your SSH key to act"
			break
		}
		p, ok := m.selected()
		if !ok {
			break
		}
		m.commentPostID = p.ID
		m.commentParent = nil // top-level
		m.returnMode = modeList
		m.mode = modeInput
		m.inputKind = inputComment
		m.input.Placeholder = "your comment"
		m.input.SetValue("")
		m.input.Focus()
		return m, textinput.Blink
	case "enter": // open the selected post's comment thread
		p, ok := m.selected()
		if !ok {
			break
		}
		rows, err := service.CommentTree(m.ctx, m.queries, p.ID)
		if err != nil {
			m.status = "could not load comments"
			break
		}
		m.comments = rows
		m.commentsFor = p.ID
		m.commentCursor = 0
		m.mode = modeComments
	}
	return m, nil
}

// updateInput handles keys while the text box is open.
func (m model) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc": // cancel
		m.input.Blur()
		m.mode = modeList
		return m, nil
	case "enter": // submit
		content := strings.TrimSpace(m.input.Value())
		m.input.Blur()
		if content == "" {
			m.mode = m.returnMode
			m.status = "nothing to send"
			return m, nil
		}
		if m.inputKind == inputPost {
			m = m.submitPost(content)
			m.mode = modeList
		} else {
			m = m.submitComment(content)
			m.mode = m.returnMode // back to the list OR the thread we replied in
		}
		return m, nil
	}
	// Any other key → let the text box edit itself (insert char, move cursor…).
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// updateComments handles keys while reading a thread: navigate, reply, comment, back.
func (m model) updateComments(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q", "backspace", "left", "h":
		m.mode = modeList
	case "up", "k":
		if m.commentCursor > 0 {
			m.commentCursor--
		}
	case "down", "j":
		if m.commentCursor < len(m.comments)-1 {
			m.commentCursor++
		}
	case "r": // REPLY to the selected comment → a nested reply
		if m.userID == 0 {
			m.status = "register your SSH key to act"
			break
		}
		if len(m.comments) == 0 {
			break
		}
		parentID := m.comments[m.commentCursor].ID
		m.commentPostID = m.commentsFor
		m.commentParent = &parentID // this reply hangs UNDER the selected comment
		m.returnMode = modeComments
		m.mode = modeInput
		m.inputKind = inputComment
		m.input.Placeholder = "your reply"
		m.input.SetValue("")
		m.input.Focus()
		return m, textinput.Blink
	case "c": // top-level comment on this post (from inside the thread)
		if m.userID == 0 {
			m.status = "register your SSH key to act"
			break
		}
		m.commentPostID = m.commentsFor
		m.commentParent = nil
		m.returnMode = modeComments
		m.mode = modeInput
		m.inputKind = inputComment
		m.input.Placeholder = "your comment"
		m.input.SetValue("")
		m.input.Focus()
		return m, textinput.Blink
	}
	return m, nil
}

// ── Action helpers: value receiver in, NEW model out. ──

func (m model) selected() (sqlc.ListRecentPostsRow, bool) {
	if len(m.posts) == 0 || m.cursor >= len(m.posts) {
		return sqlc.ListRecentPostsRow{}, false
	}
	return m.posts[m.cursor], true
}

func (m model) reloadFeed() model {
	if posts, err := m.queries.ListRecentPosts(m.ctx, 50); err == nil {
		m.posts = posts
	}
	m.cursor = 0
	return m
}

// submitPost creates a post the SAME way the API does: post + outbox event in ONE
// transaction. No idempotency key (the TUI doesn't need it). Then reload the feed.
func (m model) submitPost(content string) model {
	tx, err := m.pool.Begin(m.ctx)
	if err != nil {
		m.status = "could not start transaction"
		return m
	}
	defer tx.Rollback(m.ctx)

	post, err := service.CreatePost(m.ctx, sqlc.New(tx), m.userID, content)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrEmptyContent):
			m.status = "content is required"
		case errors.Is(err, service.ErrContentTooLong):
			m.status = "too long (max 280)"
		default:
			m.status = "could not post"
		}
		return m
	}
	if err := tx.Commit(m.ctx); err != nil {
		m.status = "could not commit"
		return m
	}
	m.status = fmt.Sprintf("posted! (#%d)", post.ID)
	return m.reloadFeed()
}

// submitComment adds a comment OR a reply. The target post and parent (nil for a
// top-level comment, a comment id for a reply) were captured when the box opened.
func (m model) submitComment(content string) model {
	if _, err := service.AddComment(m.ctx, m.queries, m.commentPostID, m.userID, m.commentParent, content); err != nil {
		switch {
		case errors.Is(err, service.ErrEmptyContent):
			m.status = "content is required"
		case errors.Is(err, service.ErrCommentTargetMissing):
			m.status = "post or parent not found"
		default:
			m.status = "could not comment"
		}
		return m
	}
	m.status = "comment added"
	// If we're returning to the thread, reload it so the new reply shows (nested).
	if m.returnMode == modeComments {
		if rows, err := service.CommentTree(m.ctx, m.queries, m.commentsFor); err == nil {
			m.comments = rows
		}
	}
	return m
}

func (m model) toggleLike(delta int) model {
	if m.userID == 0 {
		m.status = "register your SSH key to act"
		return m
	}
	p, ok := m.selected()
	if !ok {
		return m
	}
	var res service.LikeResult
	var err error
	if delta > 0 {
		res, err = service.Like(m.ctx, m.queries, m.counter, m.userID, p.ID)
	} else {
		res, err = service.Unlike(m.ctx, m.queries, m.counter, m.userID, p.ID)
	}
	if err != nil {
		m.status = "could not update like"
		return m
	}
	m.posts[m.cursor].LikeCount = res.Count
	if delta > 0 {
		m.status = fmt.Sprintf("liked · ♥ %d", res.Count)
	} else {
		m.status = fmt.Sprintf("unliked · ♥ %d", res.Count)
	}
	return m
}

func (m model) deleteSelected() model {
	if m.userID == 0 {
		m.status = "register your SSH key to act"
		return m
	}
	p, ok := m.selected()
	if !ok {
		return m
	}
	if err := service.DeletePost(m.ctx, m.queries, m.userID, p.ID); err != nil {
		switch {
		case errors.Is(err, service.ErrNotPostOwner):
			m.status = "you can only delete your own posts"
		case errors.Is(err, service.ErrPostNotFound):
			m.status = "post not found"
		default:
			m.status = "could not delete"
		}
		return m
	}
	m.posts = append(m.posts[:m.cursor], m.posts[m.cursor+1:]...)
	if m.cursor >= len(m.posts) && m.cursor > 0 {
		m.cursor--
	}
	m.status = "post deleted"
	return m
}

func (m model) followAuthor() model {
	if m.userID == 0 {
		m.status = "register your SSH key to act"
		return m
	}
	p, ok := m.selected()
	if !ok {
		return m
	}
	if err := service.FollowUser(m.ctx, m.queries, m.userID, p.AuthorID); err != nil {
		if errors.Is(err, service.ErrCannotFollowSelf) {
			m.status = "that's you 🙂"
		} else {
			m.status = "could not follow"
		}
		return m
	}
	m.status = fmt.Sprintf("now following @%s", p.Author)
	return m
}

var (
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("229"))
	authorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	metaStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	helpStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	statusStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("84"))
)

// View dispatches to the renderer for the current mode.
func (m model) View() string {
	switch m.mode {
	case modeInput:
		return m.viewInput()
	case modeComments:
		return m.viewComments()
	default:
		return m.viewList()
	}
}

func (m model) viewList() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render(fmt.Sprintf("📰 news-feed-go  ·  signed in as %s", m.username)))
	b.WriteString("\n\n")

	if len(m.posts) == 0 {
		b.WriteString("  (no posts yet)\n\n")
		b.WriteString(helpStyle.Render("  n new post · q quit"))
		return b.String()
	}

	visible := 8
	if m.height > 0 {
		if v := (m.height - 6) / 3; v >= 1 {
			visible = v
		}
	}
	start := 0
	if m.cursor >= visible {
		start = m.cursor - visible + 1
	}
	end := start + visible
	if end > len(m.posts) {
		end = len(m.posts)
	}

	for i := start; i < end; i++ {
		p := m.posts[i]
		content := truncate(strings.ReplaceAll(p.Content, "\n", " "), 72)
		if i == m.cursor {
			b.WriteString(selectedStyle.Render(fmt.Sprintf("▶ @%s   ♥ %d", p.Author, p.LikeCount)))
			b.WriteString("\n    " + selectedStyle.Render(content) + "\n\n")
		} else {
			b.WriteString("  " + authorStyle.Render("@"+p.Author) + metaStyle.Render(fmt.Sprintf("   ♥ %d", p.LikeCount)))
			b.WriteString("\n    " + content + "\n\n")
		}
	}

	b.WriteString(helpStyle.Render(fmt.Sprintf("↑/↓ move · enter view · n post · c comment · l like · u unlike · d delete · f follow · q quit   (%d/%d)", m.cursor+1, len(m.posts))))
	if m.status != "" {
		b.WriteString("\n" + statusStyle.Render("  "+m.status))
	}
	return b.String()
}

func (m model) viewInput() string {
	title := "New post"
	if m.inputKind == inputComment {
		title = "New comment"
	}
	var b strings.Builder
	b.WriteString(headerStyle.Render("📰 news-feed-go  ·  " + title))
	b.WriteString("\n\n  ")
	b.WriteString(m.input.View()) // the text box renders itself (with its cursor)
	b.WriteString("\n\n")
	b.WriteString(helpStyle.Render("  Enter to send · Esc to cancel"))
	return b.String()
}

func (m model) viewComments() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render(fmt.Sprintf("💬 comments on post #%d", m.commentsFor)))
	b.WriteString("\n\n")

	if len(m.comments) == 0 {
		b.WriteString("  (no comments yet — press c to add one)\n\n")
	}
	for i, c := range m.comments {
		indent := strings.Repeat("  ", int(c.Depth)) // depth-first tree → indent by depth
		who := fmt.Sprintf("user #%d", c.AuthorID)
		body := truncate(strings.ReplaceAll(c.Content, "\n", " "), 66)
		if i == m.commentCursor {
			b.WriteString("  " + indent + selectedStyle.Render("▶ "+who))
			b.WriteString("\n    " + indent + selectedStyle.Render(body) + "\n\n")
		} else {
			b.WriteString("    " + indent + authorStyle.Render(who))
			b.WriteString("\n    " + indent + body + "\n\n")
		}
	}

	b.WriteString(helpStyle.Render("  ↑/↓ move · r reply · c comment · Esc back"))
	if m.status != "" {
		b.WriteString("\n" + statusStyle.Render("  "+m.status))
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
