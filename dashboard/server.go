package dashboard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/downloader/telegram-cloud-transfer/database"
	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
)

type Server struct {
	addr       string
	sessions   sync.Map // Map[token]username
	rateLimits sync.Map // Map[userID][]time.Time for rate limiting
}

func generateSessionToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func NewServer(addr string) *Server {
	return &Server{addr: addr}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	
	// API routes
	mux.HandleFunc("/api/status", s.authMiddleware(s.handleStatus))
	mux.HandleFunc("/api/tasks", s.authMiddleware(s.handleTasks))
	mux.HandleFunc("/api/cancel", s.authMiddleware(s.handleTaskCancel))
	mux.HandleFunc("/api/settings", s.authMiddleware(s.handleSettings))
	mux.HandleFunc("/api/logout", s.authMiddleware(s.handleLogout))
	
	// Public API routes
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/auth/google/login", s.authMiddleware(s.handleGoogleLogin))
	mux.HandleFunc("/api/auth/google/callback", s.handleGoogleCallback)
	
	// Bridge API routes (token-based auth via header)
	mux.HandleFunc("/api/bridge/send-link", s.corsMiddleware(s.handleBridgeSendLink))

	// Bridge token management (dashboard session auth)
	mux.HandleFunc("/api/token/generate", s.authMiddleware(s.handleTokenGenerate))
	mux.HandleFunc("/api/token/revoke", s.authMiddleware(s.handleTokenRevoke))
	mux.HandleFunc("/api/token/status", s.authMiddleware(s.handleTokenStatus))
	
	// Static files with static auth
	mux.HandleFunc("/", s.staticAuthMiddleware)
	
	log.Printf("Starting Web Dashboard on %s\n", s.addr)
	return http.ListenAndServe(s.addr, mux)
}

func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("auth_token")
		if err != nil {
			http.Error(w, `{"error": "Unauthorized"}`, http.StatusUnauthorized)
			return
		}

		if _, ok := s.sessions.Load(cookie.Value); !ok {
			http.Error(w, `{"error": "Unauthorized session"}`, http.StatusUnauthorized)
			return
		}
		
		next(w, r)
	}
}

func (s *Server) staticAuthMiddleware(w http.ResponseWriter, r *http.Request) {
	// Let login.html pass freely
	if strings.Contains(r.URL.Path, "login.html") || strings.Contains(r.URL.Path, "style.css") {
		http.FileServer(http.Dir("./dashboard/static")).ServeHTTP(w, r)
		return
	}

	// Verify cookie before serving the static dashboard or JS
	cookie, err := r.Cookie("auth_token")
	if err != nil {
		http.Redirect(w, r, "/login.html", http.StatusTemporaryRedirect)
		return
	}

	if _, ok := s.sessions.Load(cookie.Value); !ok {
		http.Redirect(w, r, "/login.html", http.StatusTemporaryRedirect)
		return
	}
	
	http.FileServer(http.Dir("./dashboard/static")).ServeHTTP(w, r)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	
	downloads, uploads, err := database.GetStatusSummary()
	if err != nil {
		w.Write([]byte(`{"status": "error", "active_downloads": 0, "active_uploads": 0}`))
		return
	}
	
	response := map[string]interface{}{
		"status": "ok",
		"active_downloads": downloads,
		"active_uploads": uploads,
	}
	json.NewEncoder(w).Encode(response)
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	
	tasks, err := database.GetAllTasks()
	if err != nil || tasks == nil {
		w.Write([]byte(`[]`))
		return
	}
	
	json.NewEncoder(w).Encode(tasks)
}

func (s *Server) handleTaskCancel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error": "Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	// Parse JSON body like {"id": 123}
	var req struct {
		ID int `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error": "Invalid JSON"}`, http.StatusBadRequest)
		return
	}

	cancelled := database.CancelTask(req.ID)
	if cancelled {
		database.UpdateTaskStatus(req.ID, "Cancelled", "", "", "")
		w.Write([]byte(`{"success": true}`))
	} else {
		w.Write([]byte(`{"success": false, "error": "Task not found or not active"}`))
	}
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodGet {
		settings, err := database.GetSettings()
		if err != nil {
			http.Error(w, `{"error": "Failed to load settings"}`, http.StatusInternalServerError)
			return
		}
		
		response := struct {
			database.Settings
			IsGoogleConnected bool `json:"is_google_connected"`
		}{
			Settings:          settings,
			IsGoogleConnected: settings.AccessToken != "",
		}

		// Hide secrets for security in frontend response, unless necessary.
		response.GoogleClientSecret = ""
		response.BotToken = "" // Keep it hidden from UI once set
		response.AccessToken = ""
		response.RefreshToken = ""
		// Telegram API Hash is semi-secret, however we need it visible to edit it or we can leave it hidden 
		// if the user requests it. For now, exposing it so the input populates correctly.
		response.AccessToken = ""
		response.RefreshToken = ""

		json.NewEncoder(w).Encode(response)
		return
	}

	if r.Method == http.MethodPost {
		var newSettings database.Settings
		if err := json.NewDecoder(r.Body).Decode(&newSettings); err != nil {
			http.Error(w, `{"error": "Invalid request payload"}`, http.StatusBadRequest)
			return
		}

		err := database.UpdateSettings(newSettings)
		if err != nil {
			http.Error(w, `{"error": "Failed to save settings"}`, http.StatusInternalServerError)
			return
		}

		w.Write([]byte(`{"success": true}`))
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error": "Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error": "Invalid JSON"}`, http.StatusBadRequest)
		return
	}

	if database.VerifyUser(req.Username, req.Password) {
		token := generateSessionToken()
		s.sessions.Store(token, req.Username)

		http.SetCookie(w, &http.Cookie{
			Name:     "auth_token",
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			Expires:  time.Now().Add(24 * time.Hour),
		})

		w.Write([]byte(`{"success": true}`))
	} else {
		w.Write([]byte(`{"success": false, "error": "Invalid username or password"}`))
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error": "Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	cookie, err := r.Cookie("auth_token")
	if err == nil {
		s.sessions.Delete(cookie.Value)
	}

	// Delete client-side cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success": true}`))
}

func getOAuthConfig(settings database.Settings, r *http.Request) *oauth2.Config {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	redirectURL := scheme + "://" + r.Host + "/api/auth/google/callback"

	return &oauth2.Config{
		ClientID:     settings.GoogleClientID,
		ClientSecret: settings.GoogleClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		},
		RedirectURL: redirectURL,
		Scopes:      []string{drive.DriveFileScope, drive.DriveMetadataReadonlyScope},
	}
}

func (s *Server) handleGoogleLogin(w http.ResponseWriter, r *http.Request) {
	settings, err := database.GetSettings()
	if err != nil || settings.GoogleClientID == "" || settings.GoogleClientSecret == "" {
		http.Error(w, "Google OAuth is not configured in settings", http.StatusBadRequest)
		return
	}

	config := getOAuthConfig(settings, r)
	// Use offline access to prompt for a refresh token
	url := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

func (s *Server) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "Code not found in request", http.StatusBadRequest)
		return
	}

	settings, err := database.GetSettings()
	if err != nil {
		http.Error(w, "Failed to load settings", http.StatusInternalServerError)
		return
	}

	config := getOAuthConfig(settings, r)
	token, err := config.Exchange(context.Background(), code)
	if err != nil {
		http.Error(w, "Failed to exchange token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	err = database.UpdateOAuthTokens(settings.ID, token.AccessToken, token.RefreshToken, token.Expiry)
	if err != nil {
		http.Error(w, "Failed to save tokens to database", http.StatusInternalServerError)
		return
	}

	// Redirect back to dashboard successfully
	http.Redirect(w, r, "/#", http.StatusTemporaryRedirect)
}

// ===== CORS Middleware for Extension =====

func (s *Server) corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next(w, r)
	}
}

// ===== Rate Limiter =====

func (s *Server) checkRateLimit(userID int) bool {
	now := time.Now()
	window := 1 * time.Minute
	maxRequests := 30

	val, _ := s.rateLimits.LoadOrStore(userID, []time.Time{})
	times := val.([]time.Time)

	// Filter to only times within the window
	var recent []time.Time
	for _, t := range times {
		if now.Sub(t) < window {
			recent = append(recent, t)
		}
	}

	if len(recent) >= maxRequests {
		s.rateLimits.Store(userID, recent)
		return false
	}

	recent = append(recent, now)
	s.rateLimits.Store(userID, recent)
	return true
}

// ===== Bridge Token Management Endpoints =====

func (s *Server) handleTokenGenerate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error": "Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	// Use admin user ID 1 (single-user system)
	userID := 1
	rawToken, err := database.GenerateBridgeToken(userID)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "Failed to generate token: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"token":   rawToken,
		"message": "Token generated. Copy it now — it won't be shown again.",
	})
}

func (s *Server) handleTokenRevoke(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodDelete {
		http.Error(w, `{"error": "Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	userID := 1
	err := database.RevokeBridgeToken(userID)
	if err != nil {
		http.Error(w, `{"error": "Failed to revoke token"}`, http.StatusInternalServerError)
		return
	}

	w.Write([]byte(`{"success": true, "message": "Token revoked successfully."}`))
}

func (s *Server) handleTokenStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error": "Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	userID := 1
	bt, err := database.GetBridgeTokenStatus(userID)

	// Get bridge logs regardless of token status
	logs, logsErr := database.GetBridgeLogs(userID, 20)
	if logsErr != nil {
		logs = []database.BridgeLog{}
	}
	if logs == nil {
		logs = []database.BridgeLog{}
	}

	if err != nil {
		// No token exists
		json.NewEncoder(w).Encode(map[string]interface{}{
			"has_token": false,
			"logs":      logs,
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"has_token":    true,
		"token_prefix": bt.TokenPrefix,
		"last_used":    bt.LastUsed,
		"created_at":   bt.CreatedAt,
		"logs":         logs,
	})
}

// ===== Bridge Send Link Endpoint =====

func (s *Server) handleBridgeSendLink(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error": "Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	// Extract bearer token from Authorization header
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		http.Error(w, `{"error": "Missing or invalid Authorization header"}`, http.StatusUnauthorized)
		return
	}
	rawToken := strings.TrimPrefix(authHeader, "Bearer ")

	// Validate token
	userID, err := database.ValidateBridgeToken(rawToken)
	if err != nil {
		http.Error(w, `{"error": "Invalid or expired token"}`, http.StatusUnauthorized)
		return
	}

	// Rate limit check
	if !s.checkRateLimit(userID) {
		http.Error(w, `{"error": "Rate limit exceeded. Max 30 requests per minute."}`, http.StatusTooManyRequests)
		return
	}

	// Parse request body
	var req struct {
		URL        string `json:"url"`
		SourceSite string `json:"source_site"`
		Filename   string `json:"filename"`
		FileSize   string `json:"file_size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error": "Invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	// Validate URL
	if req.URL == "" {
		http.Error(w, `{"error": "URL is required"}`, http.StatusBadRequest)
		return
	}
	parsedURL, err := url.Parse(req.URL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		http.Error(w, `{"error": "Invalid URL format"}`, http.StatusBadRequest)
		return
	}

	// Default filename from URL if not provided
	if req.Filename == "" {
		parts := strings.Split(parsedURL.Path, "/")
		if len(parts) > 0 {
			req.Filename = parts[len(parts)-1]
		}
		if req.Filename == "" {
			req.Filename = fmt.Sprintf("download_%d", time.Now().Unix())
		}
	}

	// Create task in database
	taskID, err := database.CreateTask(userID, req.Filename, 0, "Bridge Extension")
	if err != nil {
		database.LogBridgeRequest(userID, req.URL, req.SourceSite, req.Filename, req.FileSize, "failed", 0)
		http.Error(w, `{"error": "Failed to create download task"}`, http.StatusInternalServerError)
		return
	}

	// Log the bridge request
	database.LogBridgeRequest(userID, req.URL, req.SourceSite, req.Filename, req.FileSize, "sent", taskID)

	log.Printf("Bridge: Received link from extension for user %d: %s (source: %s)", userID, req.URL, req.SourceSite)

	// Get settings and start bot download
	settings, err := database.GetSettings()
	if err != nil || settings.AccessToken == "" {
		database.UpdateTaskStatus(taskID, "Failed", "", "", "")
		http.Error(w, `{"error": "Google Drive not configured. Please connect via Dashboard."}`, http.StatusServiceUnavailable)
		return
	}

	// Get admin telegram IDs to send notification
	var chatID int64
	if settings.AdminTelegramIDs != "" {
		ids := strings.Split(settings.AdminTelegramIDs, ",")
		if len(ids) > 0 {
			fmt.Sscanf(strings.TrimSpace(ids[0]), "%d", &chatID)
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":          true,
		"message":          "Link sent to download queue",
		"task_id":          taskID,
		"telegram_chat_id": chatID,
	})
}
