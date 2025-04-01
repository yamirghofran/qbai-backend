package handlers

import (
	"bytes" // Added for Discord notification payload
	"context"
	"encoding/json" // Added for activity log details &amp; Discord payload

	// Added for error formatting
	"io"       // Added for Discord response reading
	"log"      // Added for logging errors
	"net/http" // Added for Discord notification
	"time"     // Added for response struct timestamps &amp; Discord timeout

	"quizbuilderai/internal/db"
	"quizbuilderai/internal/gemini"
	"quizbuilderai/internal/youtube"

	"github.com/google/uuid"         // Added for user ID
	"github.com/jackc/pgx/v5/pgtype" // Added for pgtype.Text &amp; pgtype.UUID
	"golang.org/x/oauth2"
)

// UserProfile stores information about the authenticated user.
type UserProfile struct {
	DatabaseID    uuid.UUID `json:"-"`  // Our internal DB UUID (omit from JSON response to client)
	GoogleID      string    `json:"id"` // Google's ID (keep as 'id' in JSON)
	Email         string    `json:"email"`
	VerifiedEmail bool      `json:"verified_email"`
	Name          string    `json:"name"`
	GivenName     string    `json:"given_name"`
	FamilyName    string    `json:"family_name"`
	Picture       string    `json:"picture"`
	Locale        string    `json:"locale"`
}

// Constants for session keys - keep these consistent
// Exported constants start with an uppercase letter.
const (
	OauthStateSessionKey = "oauthstate"
	ProfileSessionKey    = "profile"
	discordWebhookURL    = "https://discord.com/api/webhooks/1356553549256986725/9v9vVxGCLQhvOJtMmC5MZKXdR-AiJuS_a_NTyo1U6ItTPM9kzcQusw31GxR3UvxmUYN3" // Added Discord Webhook URL (keep unexported if only used internally)
)

// Handler contains the API handlers dependencies
type Handler struct {
	OauthConfig *oauth2.Config
	StoreName   string
	DB          *db.DB
	Gemini      *gemini.Client
	Youtube     *youtube.YoutubeTranscript
	// R2Client    *r2.Client // Removed Cloudflare R2 client field
	DiscordClient *http.Client // Added HTTP client for Discord
}

// NewHandler creates a new Handler
func NewHandler(oauth *oauth2.Config, store string, db *db.DB, gemini *gemini.Client) *Handler {
	// Create a dedicated HTTP client for Discord with a timeout
	discordClient := &http.Client{
		Timeout: 5 * time.Second, // Set a 5-second timeout for Discord requests
	}

	return &Handler{
		OauthConfig:   oauth,
		StoreName:     store,
		DB:            db,
		Gemini:        gemini,
		Youtube:       youtube.New(),
		DiscordClient: discordClient, // Initialize Discord client
	}
}

// sendDiscordNotification sends a message to the configured Discord webhook.
// It runs asynchronously to avoid blocking the main request flow.
func (h *Handler) sendDiscordNotification(message string) {
	go func() { // Run in a goroutine
		if discordWebhookURL == "" {
			// log.Println("WARN: Discord webhook URL not configured, skipping notification.")
			return // Silently return if not configured
		}

		payload := map[string]string{"content": message}
		jsonPayload, err := json.Marshal(payload)
		if err != nil {
			log.Printf("ERROR: Failed to marshal Discord payload: %v", err)
			return
		}

		req, err := http.NewRequest("POST", discordWebhookURL, bytes.NewBuffer(jsonPayload))
		if err != nil {
			log.Printf("ERROR: Failed to create Discord request: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := h.DiscordClient.Do(req) // Use the handler's client with timeout
		if err != nil {
			log.Printf("ERROR: Failed to send Discord notification: %v", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 300 {
			bodyBytes, _ := io.ReadAll(resp.Body)
			log.Printf("ERROR: Discord notification failed with status %d: %s", resp.StatusCode, string(bodyBytes))
		} else {
			log.Printf("INFO: Sent Discord notification: %s", message)
		}
	}()
}

// logActivity is a helper function to create activity log entries.
func (h *Handler) logActivity(ctx context.Context, userID uuid.UUID, action db.ActivityAction, targetType db.NullActivityTargetType, targetID pgtype.UUID, details map[string]interface{}) {
	var detailsJSON []byte
	var err error

	if details != nil {
		detailsJSON, err = json.Marshal(details)
		if err != nil {
			log.Printf("ERROR: Failed to marshal activity log details for user %s, action %s: %v", userID, action, err)
			// Decide if you want to log without details or skip logging
			detailsJSON = nil // Log without details on marshal error
		}
	}

	logParams := db.CreateActivityLogParams{
		UserID:     pgtype.UUID{Bytes: userID, Valid: true},
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Details:    detailsJSON,
	}

	_, err = h.DB.Queries.CreateActivityLog(ctx, logParams)
	if err != nil {
		// Log the error but don't block the main request flow
		log.Printf("ERROR: Failed to create activity log for user %s, action %s: %v", userID, action, err)
	} else {
		log.Printf("INFO: Activity logged for user %s: %s", userID, action)
	}
}
