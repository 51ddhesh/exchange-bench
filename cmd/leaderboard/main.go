package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ── DB row ────────────────────────────────────────────────────────────────────

type runScore struct {
	SubmissionID   string
	TeamID         string
	Attempt        int
	Language       string
	PeakTPS        float64
	CapacityTPS    float64
	P50Us          int64
	P90Us          int64
	P99Us          int64
	Correctness    float64
	CompositeScore float64
	CriticalFlag   bool
}

// ── broadcast types ───────────────────────────────────────────────────────────

type leaderboardEntry struct {
	Rank           int     `json:"rank"`
	TeamID         string  `json:"team_id"`
	Language       string  `json:"language"`
	CompositeScore float64 `json:"composite_score"`
	PeakTPS        float64 `json:"peak_tps"`
	P99Us          int64   `json:"p99_us"`
	Correctness    float64 `json:"correctness"`
	CriticalFlag   bool    `json:"critical_flag"`
}

type broadcast struct {
	UpdatedAt string                        `json:"updated_at"`
	Tiers     map[string][]leaderboardEntry `json:"tiers"`
}

// ── WebSocket hub ─────────────────────────────────────────────────────────────

type hub struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]struct{}
}

func newHub() *hub {
	return &hub{clients: make(map[*websocket.Conn]struct{})}
}

func (h *hub) register(c *websocket.Conn) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *hub) unregister(c *websocket.Conn) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

func (h *hub) send(data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if err := c.Write(context.Background(), websocket.MessageText, data); err != nil {
			delete(h.clients, c)
			c.Close(websocket.StatusGoingAway, "") //nolint:errcheck
		}
	}
}

// ── tier assignment ───────────────────────────────────────────────────────────

func tierFor(language string) string {
	switch language {
	case "cpp", "rust", "zig":
		return "systems"
	case "python":
		return "interpreted"
	default: // go, unknown
		return "gc"
	}
}

// ── composite scoring ─────────────────────────────────────────────────────────

// buildTiers groups rows by tier, computes composite scores with
// intra-tier normalisation, sorts by composite score descending, and
// assigns ranks. Returns the three-tier map ready for broadcast.
func buildTiers(rows []runScore) map[string][]leaderboardEntry {
	grouped := map[string][]runScore{
		"systems":     {},
		"gc":          {},
		"interpreted": {},
	}
	for _, r := range rows {
		t := tierFor(r.Language)
		grouped[t] = append(grouped[t], r)
	}

	result := make(map[string][]leaderboardEntry, 3)
	for tier, members := range grouped {
		if len(members) == 0 {
			result[tier] = []leaderboardEntry{}
			continue
		}

		// Find per-tier maxima for normalisation.
		var maxCapTPS, maxP99 float64
		for _, m := range members {
			if m.CapacityTPS > maxCapTPS {
				maxCapTPS = m.CapacityTPS
			}
			if float64(m.P99Us) > maxP99 {
				maxP99 = float64(m.P99Us)
			}
		}

		entries := make([]leaderboardEntry, 0, len(members))
		for _, m := range members {
			var normCap, normLat float64
			if maxCapTPS > 0 {
				normCap = m.CapacityTPS / maxCapTPS
			}
			if maxP99 > 0 {
				normLat = 1 - float64(m.P99Us)/maxP99
			}
			composite := m.Correctness*0.4 + normCap*0.4 + normLat*0.2
			entries = append(entries, leaderboardEntry{
				TeamID:         m.TeamID,
				Language:       m.Language,
				CompositeScore: composite,
				PeakTPS:        m.PeakTPS,
				P99Us:          m.P99Us,
				Correctness:    m.Correctness,
				CriticalFlag:   m.CriticalFlag,
			})
		}

		// Sort descending by composite; critical_flag submissions cannot rank first.
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].CriticalFlag != entries[j].CriticalFlag {
				return !entries[i].CriticalFlag // non-flagged floats up
			}
			return entries[i].CompositeScore > entries[j].CompositeScore
		})

		for i := range entries {
			entries[i].Rank = i + 1
		}
		result[tier] = entries
	}
	return result
}

// ── poller ────────────────────────────────────────────────────────────────────

func poll(ctx context.Context, db *pgxpool.Pool, h *hub, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastJSON []byte

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rows, err := queryBestPerTeam(ctx, db)
			if err != nil {
				log.Printf("leaderboard: poll query: %v", err)
				continue
			}

			tiers := buildTiers(rows)
			payload := broadcast{
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
				Tiers:     tiers,
			}
			data, err := json.Marshal(payload)
			if err != nil {
				log.Printf("leaderboard: marshal: %v", err)
				continue
			}

			// Only broadcast on change.
			if string(data) == string(lastJSON) {
				continue
			}
			lastJSON = data
			h.send(data)
		}
	}
}

func queryBestPerTeam(ctx context.Context, db *pgxpool.Pool) ([]runScore, error) {
	const q = `
		SELECT DISTINCT ON (team_id)
			submission_id, team_id, attempt, language,
			peak_tps, capacity_tps,
			p50_us, p90_us, p99_us,
			correctness, composite_score, critical_flag
		FROM run_scores
		ORDER BY team_id, composite_score DESC
	`
	pgRows, err := db.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer pgRows.Close()

	var out []runScore
	for pgRows.Next() {
		var r runScore
		if err := pgRows.Scan(
			&r.SubmissionID, &r.TeamID, &r.Attempt, &r.Language,
			&r.PeakTPS, &r.CapacityTPS,
			&r.P50Us, &r.P90Us, &r.P99Us,
			&r.Correctness, &r.CompositeScore, &r.CriticalFlag,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, pgRows.Err()
}

// ── HTTP handlers ─────────────────────────────────────────────────────────────

func handleWS(h *hub, w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	h.register(conn)
	// Block until the client disconnects.
	for {
		if _, _, err := conn.Read(r.Context()); err != nil {
			break
		}
	}
	h.unregister(conn)
	conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck
}

func handleTeamHistory(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	teamID := strings.TrimPrefix(r.URL.Path, "/api/teams/")
	if teamID == "" {
		http.Error(w, "team_id required", http.StatusBadRequest)
		return
	}

	const q = `
		SELECT submission_id, team_id, attempt, language,
		       peak_tps, capacity_tps,
		       p50_us, p90_us, p99_us,
		       correctness, composite_score, critical_flag,
		       completed_at
		FROM run_scores
		WHERE team_id = $1
		ORDER BY attempt DESC
	`
	rows, err := db.Query(r.Context(), q, teamID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type historyRow struct {
		SubmissionID   string  `json:"submission_id"`
		TeamID         string  `json:"team_id"`
		Attempt        int     `json:"attempt"`
		Language       string  `json:"language"`
		PeakTPS        float64 `json:"peak_tps"`
		CapacityTPS    float64 `json:"capacity_tps"`
		P50Us          int64   `json:"p50_us"`
		P90Us          int64   `json:"p90_us"`
		P99Us          int64   `json:"p99_us"`
		Correctness    float64 `json:"correctness"`
		CompositeScore float64 `json:"composite_score"`
		CriticalFlag   bool    `json:"critical_flag"`
		CompletedAt    string  `json:"completed_at"`
	}

	var history []historyRow
	for rows.Next() {
		var h historyRow
		var completedAt time.Time
		if err := rows.Scan(
			&h.SubmissionID, &h.TeamID, &h.Attempt, &h.Language,
			&h.PeakTPS, &h.CapacityTPS,
			&h.P50Us, &h.P90Us, &h.P99Us,
			&h.Correctness, &h.CompositeScore, &h.CriticalFlag,
			&completedAt,
		); err != nil {
			http.Error(w, "scan error", http.StatusInternalServerError)
			return
		}
		h.CompletedAt = completedAt.UTC().Format(time.RFC3339)
		history = append(history, h)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "rows error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"team_id": teamID,
		"history": history,
	})
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	listen := flag.String("listen", ":8080", "HTTP listen address")
	dsn := flag.String("dsn", "", "TimescaleDB connection string (required)")
	pollInterval := flag.Duration("poll", time.Second, "leaderboard poll interval")
	flag.Parse()

	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "error: --dsn is required")
		flag.Usage()
		os.Exit(1)
	}

	db, err := pgxpool.New(context.Background(), *dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "leaderboard: db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	h := newHub()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go poll(ctx, db, h, *pollInterval)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handleWS(h, w, r)
	})
	mux.HandleFunc("/api/teams/", func(w http.ResponseWriter, r *http.Request) {
		handleTeamHistory(db, w, r)
	})
	mux.Handle("/", http.FileServer(http.Dir("cmd/leaderboard/static")))

	log.Printf("[leaderboard] listening on %s  poll=%s", *listen, *pollInterval)
	if err := http.ListenAndServe(*listen, mux); err != nil {
		fmt.Fprintf(os.Stderr, "leaderboard: %v\n", err)
		os.Exit(1)
	}
}
