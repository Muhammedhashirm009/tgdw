package dashboard

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/downloader/telegram-cloud-transfer/database"
	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
)

type Server struct {
	addr string
}

func NewServer(addr string) *Server {
	return &Server{addr: addr}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	
	// API routes
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/tasks", s.handleTasks)
	mux.HandleFunc("/api/cancel", s.handleTaskCancel)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/auth/google/login", s.handleGoogleLogin)
	mux.HandleFunc("/api/auth/google/callback", s.handleGoogleCallback)
	
	// Static files
	fs := http.FileServer(http.Dir("./dashboard/static"))
	mux.Handle("/", fs)
	
	log.Printf("Starting Web Dashboard on %s\n", s.addr)
	return http.ListenAndServe(s.addr, mux)
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
	// TODO: Handle auth securely
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
