// cmd/app/main.go
//
// Example REST API — used as the TYA demo target.
//
// Features:
//   - SQLite persistence (mattn/go-sqlite3)
//   - JWT authentication (golang-jwt/jwt/v5)
//   - POST /auth/register  — create an account
//   - POST /auth/login     — get access + refresh tokens
//   - POST /auth/refresh   — exchange refresh token for new access token
//   - GET    /persons      — list all persons        (requires auth)
//   - POST   /persons      — create a person         (requires auth)
//   - GET    /persons/:id  — get a person by ID      (requires auth)
//   - PUT    /persons/:id  — replace a person        (requires auth)
//   - PATCH  /persons/:id  — partial update          (requires auth)
//   - DELETE /persons/:id  — delete a person         (requires auth)
//
// Run:
//   go mod init app
//   go get github.com/mattn/go-sqlite3
//   go get github.com/golang-jwt/jwt/v5
//   go run cmd/app/main.go
//
// Environment variables (all optional, have defaults):
//   PORT            HTTP listen port          (default: 8080)
//   JWT_SECRET      HMAC-SHA256 signing key   (default: change-me-in-prod)
//   DB_PATH         SQLite file path          (default: ./app.db)
//   ACCESS_TTL      Access token TTL          (default: 15m)
//   REFRESH_TTL     Refresh token TTL         (default: 7d)

package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

type config struct {
	Port       string
	JWTSecret  []byte
	DBPath     string
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

func loadConfig() config {
	secret := getEnv("JWT_SECRET", "change-me-in-prod")
	accessTTL, _ := time.ParseDuration(getEnv("ACCESS_TTL", "15m"))
	refreshTTL, _ := time.ParseDuration(getEnv("REFRESH_TTL", "168h")) // 7 days

	return config{
		Port:       getEnv("PORT", "8080"),
		JWTSecret:  []byte(secret),
		DBPath:     getEnv("DB_PATH", "./app.db"),
		AccessTTL:  accessTTL,
		RefreshTTL: refreshTTL,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ---------------------------------------------------------------------------
// Database
// ---------------------------------------------------------------------------

func openDB(path string) *sql.DB {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		log.Fatalf("open db: %v", err)
	}

	schema := `
	CREATE TABLE IF NOT EXISTS users (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		email      TEXT    NOT NULL UNIQUE,
		password   TEXT    NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS refresh_tokens (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		token      TEXT    NOT NULL UNIQUE,
		expires_at DATETIME NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS persons (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		first_name TEXT    NOT NULL,
		last_name  TEXT    NOT NULL,
		email      TEXT    NOT NULL UNIQUE,
		phone      TEXT,
		birth_date TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	`

	if _, err = db.Exec(schema); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	return db
}

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

type User struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

type Person struct {
	ID        int64     `json:"id"`
	FirstName string    `json:"first_name"`
	LastName  string    `json:"last_name"`
	Email     string    `json:"email"`
	Phone     *string   `json:"phone,omitempty"`
	BirthDate *string   `json:"birth_date,omitempty"` // YYYY-MM-DD
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// JWT helpers
// ---------------------------------------------------------------------------

type claims struct {
	UserID int64  `json:"uid"`
	Kind   string `json:"kind"` // "access" | "refresh"
	jwt.RegisteredClaims
}

func (cfg config) signToken(userID int64, kind string, ttl time.Duration) (string, error) {
	now := time.Now()
	c := claims{
		UserID: userID,
		Kind:   kind,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(cfg.JWTSecret)
}

func (cfg config) parseToken(raw string) (*claims, error) {
	tok, err := jwt.ParseWithClaims(raw, &claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return cfg.JWTSecret, nil
	})
	if err != nil {
		return nil, err
	}
	c, ok := tok.Claims.(*claims)
	if !ok || !tok.Valid {
		return nil, errors.New("invalid token")
	}
	return c, nil
}

// ---------------------------------------------------------------------------
// Application — bundles deps together
// ---------------------------------------------------------------------------

type app struct {
	cfg config
	db  *sql.DB
	mux *http.ServeMux
}

func newApp(cfg config, db *sql.DB) *app {
	a := &app{cfg: cfg, db: db, mux: http.NewServeMux()}
	a.routes()
	return a
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mux.ServeHTTP(w, r)
}

func (a *app) routes() {
	// Auth
	a.mux.HandleFunc("POST /auth/register", a.handleRegister)
	a.mux.HandleFunc("POST /auth/login", a.handleLogin)
	a.mux.HandleFunc("POST /auth/refresh", a.handleRefresh)

	// Persons — all protected
	a.mux.HandleFunc("GET /persons", a.protected(a.handleListPersons))
	a.mux.HandleFunc("POST /persons", a.protected(a.handleCreatePerson))
	a.mux.HandleFunc("GET /persons/{id}", a.protected(a.handleGetPerson))
	a.mux.HandleFunc("PUT /persons/{id}", a.protected(a.handleReplacePerson))
	a.mux.HandleFunc("PATCH /persons/{id}", a.protected(a.handleUpdatePerson))
	a.mux.HandleFunc("DELETE /persons/{id}", a.protected(a.handleDeletePerson))
}

// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
// Auth middleware

// protected wraps a handler requiring a valid Bearer access token.
func (a *app) protected(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if raw == "" {
			writeError(w, http.StatusUnauthorized, "missing token")
			return
		}
		c, err := a.cfg.parseToken(raw)
		if err != nil || c.Kind != "access" {
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		// Stash user ID in context via request header trick (simple, no extra dep)
		r.Header.Set("X-User-ID", strconv.FormatInt(c.UserID, 10))
		next(w, r)
	}
}

// ---------------------------------------------------------------------------// ---------------------------------------------------------------------------
// Auth handlers
// ---------------------------------------------------------------------------

type registerReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (a *app) handleRegister(w http.ResponseWriter, r *http.Request) {
	var body registerReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Email == "" || body.Password == "" {
		writeError(w, http.StatusUnprocessableEntity, "email and password are required")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}

	var userID int64
	err = a.db.QueryRowContext(r.Context(),
		`INSERT INTO users (email, password) VALUES (?, ?) RETURNING id`,
		body.Email, string(hash),
	).Scan(&userID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, http.StatusConflict, "email already registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"id": userID, "email": body.Email})
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (a *app) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body loginReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	var userID int64
	var hash string
	err := a.db.QueryRowContext(r.Context(),
		`SELECT id, password FROM users WHERE email = ?`, body.Email,
	).Scan(&userID, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	if err = bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Password)); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	a.issueTokenPair(w, r, userID)
}

type refreshReq struct {
	RefreshToken string `json:"refresh_token"`
}

func (a *app) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var body refreshReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// 1. Validate JWT signature + expiry
	c, err := a.cfg.parseToken(body.RefreshToken)
	if err != nil || c.Kind != "refresh" {
		writeError(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}

	// 2. Check it exists in the DB (single-use rotation)
	var storedID int64
	err = a.db.QueryRowContext(r.Context(),
		`SELECT id FROM refresh_tokens WHERE token = ? AND expires_at > CURRENT_TIMESTAMP`,
		body.RefreshToken,
	).Scan(&storedID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "refresh token revoked or expired")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	// 3. Rotate: delete old token, issue new pair
	_, _ = a.db.ExecContext(r.Context(),
		`DELETE FROM refresh_tokens WHERE id = ?`, storedID,
	)

	a.issueTokenPair(w, r, c.UserID)
}

// issueTokenPair signs both tokens and persists the refresh token.
func (a *app) issueTokenPair(w http.ResponseWriter, r *http.Request, userID int64) {
	accessToken, err := a.cfg.signToken(userID, "access", a.cfg.AccessTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not sign access token")
		return
	}

	refreshToken, err := a.cfg.signToken(userID, "refresh", a.cfg.RefreshTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not sign refresh token")
		return
	}

	expiresAt := time.Now().Add(a.cfg.RefreshTTL)
	_, err = a.db.ExecContext(r.Context(),
		`INSERT INTO refresh_tokens (user_id, token, expires_at) VALUES (?, ?, ?)`,
		userID, refreshToken, expiresAt,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not store refresh token")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"token_type":    "Bearer",
		"expires_in":    int(a.cfg.AccessTTL.Seconds()),
	})
}

// ---------------------------------------------------------------------------
// Person handlers
// ---------------------------------------------------------------------------

func (a *app) handleListPersons(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(),
		`SELECT id, first_name, last_name, email, phone, birth_date, created_at, updated_at
		 FROM persons ORDER BY id`,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close() //nolint:errcheck

	persons := []Person{}
	for rows.Next() {
		var p Person
		if err := rows.Scan(
			&p.ID, &p.FirstName, &p.LastName, &p.Email,
			&p.Phone, &p.BirthDate, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "scan error")
			return
		}
		persons = append(persons, p)
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": persons, "count": len(persons)})
}

type personReq struct {
	FirstName string  `json:"first_name"`
	LastName  string  `json:"last_name"`
	Email     string  `json:"email"`
	Phone     *string `json:"phone"`
	BirthDate *string `json:"birth_date"`
}

func (a *app) handleCreatePerson(w http.ResponseWriter, r *http.Request) {
	var body personReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.FirstName == "" || body.LastName == "" || body.Email == "" {
		writeError(w, http.StatusUnprocessableEntity, "first_name, last_name and email are required")
		return
	}

	var p Person
	err := a.db.QueryRowContext(r.Context(),
		`INSERT INTO persons (first_name, last_name, email, phone, birth_date)
		 VALUES (?, ?, ?, ?, ?)
		 RETURNING id, first_name, last_name, email, phone, birth_date, created_at, updated_at`,
		body.FirstName, body.LastName, body.Email, body.Phone, body.BirthDate,
	).Scan(&p.ID, &p.FirstName, &p.LastName, &p.Email, &p.Phone, &p.BirthDate, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, http.StatusConflict, "email already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	writeJSON(w, http.StatusCreated, p)
}

func (a *app) handleGetPerson(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var p Person
	err := a.db.QueryRowContext(r.Context(),
		`SELECT id, first_name, last_name, email, phone, birth_date, created_at, updated_at
		 FROM persons WHERE id = ?`, id,
	).Scan(&p.ID, &p.FirstName, &p.LastName, &p.Email, &p.Phone, &p.BirthDate, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "person not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	writeJSON(w, http.StatusOK, p)
}

func (a *app) handleReplacePerson(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var body personReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.FirstName == "" || body.LastName == "" || body.Email == "" {
		writeError(w, http.StatusUnprocessableEntity, "first_name, last_name and email are required")
		return
	}

	var p Person
	err := a.db.QueryRowContext(r.Context(),
		`UPDATE persons
		 SET first_name=?, last_name=?, email=?, phone=?, birth_date=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=?
		 RETURNING id, first_name, last_name, email, phone, birth_date, created_at, updated_at`,
		body.FirstName, body.LastName, body.Email, body.Phone, body.BirthDate, id,
	).Scan(&p.ID, &p.FirstName, &p.LastName, &p.Email, &p.Phone, &p.BirthDate, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "person not found")
		return
	}
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, http.StatusConflict, "email already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	writeJSON(w, http.StatusOK, p)
}

// handleUpdatePerson applies only the fields present in the request body (PATCH).
func (a *app) handleUpdatePerson(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	// Decode into a generic map to detect which fields were actually sent.
	var raw map[string]any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if len(raw) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "no fields provided")
		return
	}

	setClauses := []string{}
	args := []any{}

	allowed := map[string]string{
		"first_name": "first_name",
		"last_name":  "last_name",
		"email":      "email",
		"phone":      "phone",
		"birth_date": "birth_date",
	}
	for key, col := range allowed {
		if val, exists := raw[key]; exists {
			setClauses = append(setClauses, col+"=?")
			args = append(args, val)
		}
	}
	if len(setClauses) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "no valid fields to update")
		return
	}

	setClauses = append(setClauses, "updated_at=CURRENT_TIMESTAMP")
	args = append(args, id)

	query := fmt.Sprintf(
		`UPDATE persons SET %s WHERE id=?
		 RETURNING id, first_name, last_name, email, phone, birth_date, created_at, updated_at`,
		strings.Join(setClauses, ", "),
	)

	var p Person
	err := a.db.QueryRowContext(r.Context(), query, args...).
		Scan(&p.ID, &p.FirstName, &p.LastName, &p.Email, &p.Phone, &p.BirthDate, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "person not found")
		return
	}
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, http.StatusConflict, "email already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	writeJSON(w, http.StatusOK, p)
}

func (a *app) handleDeletePerson(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	result, err := a.db.ExecContext(r.Context(), `DELETE FROM persons WHERE id = ?`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "person not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

func main() {
	cfg := loadConfig()
	db := openDB(cfg.DBPath)
	defer db.Close() //nolint:errcheck

	server := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      newApp(cfg, db),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("TYA example API listening on :%s", cfg.Port)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server: %v", err)
	}
}
