package handlers

import (
	"bytes" // Added for Discord notification payload
	"context"
	"encoding/json" // Added for activity log details & Discord payload
	"errors"        // Added for creating validation errors
	"fmt"           // Added for error formatting & Sprintf

	"io" // Added for Discord response reading
	// "io" // Duplicate import, already imported above
	"log"      // Added for logging errors
	"net/http" // Added for Discord notification &amp; status codes
	"time"     // Added for response struct timestamps &amp; Discord timeout

	"quizbuilderai/internal/db"
	"quizbuilderai/internal/gemini"
	"quizbuilderai/internal/youtube"

	"github.com/gin-gonic/gin"       // Added for gin.Context, gin.H
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

// Discord Embed Structures (based on documentation)
type DiscordEmbedFooter struct {
	Text    string `json:"text,omitempty"`
	IconURL string `json:"icon_url,omitempty"`
}

type DiscordEmbedImage struct {
	URL string `json:"url,omitempty"`
}

type DiscordEmbedThumbnail struct {
	URL string `json:"url,omitempty"`
}

type DiscordEmbedAuthor struct {
	Name    string `json:"name,omitempty"`
	URL     string `json:"url,omitempty"`
	IconURL string `json:"icon_url,omitempty"`
}

type DiscordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type DiscordEmbed struct {
	Title       string                 `json:"title,omitempty"`
	Description string                 `json:"description,omitempty"`
	URL         string                 `json:"url,omitempty"`
	Timestamp   string                 `json:"timestamp,omitempty"` // ISO8601 timestamp
	Color       int                    `json:"color,omitempty"`     // Decimal color code
	Footer      *DiscordEmbedFooter    `json:"footer,omitempty"`
	Image       *DiscordEmbedImage     `json:"image,omitempty"`
	Thumbnail   *DiscordEmbedThumbnail `json:"thumbnail,omitempty"`
	Author      *DiscordEmbedAuthor    `json:"author,omitempty"`
	Fields      []DiscordEmbedField    `json:"fields,omitempty"`
}

// WebhookPayload is the structure Discord expects for webhook requests with embeds
type WebhookPayload struct {
	Username  string         `json:"username,omitempty"`   // Optional: Override webhook username
	AvatarURL string         `json:"avatar_url,omitempty"` // Optional: Override webhook avatar
	Content   string         `json:"content,omitempty"`    // Optional: Message content outside embed
	Embeds    []DiscordEmbed `json:"embeds"`
}

// Handler contains the API handlers dependencies
type Handler struct {
	OauthConfig   *oauth2.Config
	StoreName     string
	DB            *db.DB
	Gemini        *gemini.Client
	Youtube       *youtube.YoutubeTranscript
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

// sendDiscordNotification sends an embed message to the configured Discord webhook.
// It runs asynchronously to avoid blocking the main request flow.
func (h *Handler) sendDiscordNotification(embed DiscordEmbed) {
	go func() { // Run in a goroutine
		if discordWebhookURL == "" {
			// log.Println("WARN: Discord webhook URL not configured, skipping notification.")
			return // Silently return if not configured
		}

		// Set timestamp if not already set
		if embed.Timestamp == "" {
			embed.Timestamp = time.Now().Format(time.RFC3339)
		}

		// Default bot name if not overriding
		botUsername := "QuizBuilderAI Notifier"

		payload := WebhookPayload{
			Username: botUsername,
			Embeds:   []DiscordEmbed{embed},
		}

		jsonPayload, err := json.Marshal(payload)
		if err != nil {
			log.Printf("ERROR: Failed to marshal Discord embed payload: %v", err)
			return
		}

		req, err := http.NewRequest("POST", discordWebhookURL, bytes.NewBuffer(jsonPayload))
		if err != nil {
			log.Printf("ERROR: Failed to create Discord embed request: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := h.DiscordClient.Do(req) // Use the handler's client with timeout
		if err != nil {
			log.Printf("ERROR: Failed to send Discord embed notification: %v", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 300 {
			bodyBytes, _ := io.ReadAll(resp.Body)
			log.Printf("ERROR: Discord embed notification failed with status %d: %s", resp.StatusCode, string(bodyBytes))
		} else {
			log.Printf("INFO: Sent Discord embed notification: %s", embed.Title) // Log title for brevity
		}
	}()
}

// handleErrorAndNotify logs an error, sends a Discord notification, logs to activity table, and aborts the request.
func (h *Handler) handleErrorAndNotify(c *gin.Context, userID uuid.UUID, statusCode int, errorContext string, err error) {
	// 1. Log to console (as before)
	log.Printf("ERROR: %s: %v (UserID: %s)", errorContext, err, userID)

	// 2. Log activity
	// Ensure logActivity is called correctly. Pass userID directly, let logActivity handle pgtype conversion.
	h.logActivity(c.Request.Context(), userID, db.ActivityActionError,
		db.NullActivityTargetType{}, // No specific target type for general errors
		pgtype.UUID{},               // No specific target ID for general errors
		map[string]interface{}{
			"action_attempted": errorContext, // Added: Action user was trying to perform
			"error_context":    errorContext, // Kept original context for consistency
			"error_message":    err.Error(),
			"user_id":          userID.String(), // Include user ID string in details if available
			"request_path":     c.Request.URL.Path,
			"http_status":      statusCode,
		})

	// 3. Send Discord notification
	errorEmbed := DiscordEmbed{
		Title:       fmt.Sprintf("🚨 API Error: %s", errorContext),             // Include context in title
		Description: fmt.Sprintf("**Error Details:**\n```%s```", err.Error()), // Simplified description focusing on the error message
		Color:       0xFF0000,                                                 // Bright Red
		Fields:      []DiscordEmbedField{
			// Only include User ID field if it's valid
		},
		Timestamp: time.Now().Format(time.RFC3339),
	}
	// Add User ID field conditionally
	if userID != uuid.Nil {
		errorEmbed.Fields = append(errorEmbed.Fields, DiscordEmbedField{Name: "User ID", Value: fmt.Sprintf("`%s`", userID.String()), Inline: true})
	}
	// Add Status and Path fields
	errorEmbed.Fields = append(errorEmbed.Fields, DiscordEmbedField{Name: "HTTP Status", Value: fmt.Sprintf("%d", statusCode), Inline: true})
	errorEmbed.Fields = append(errorEmbed.Fields, DiscordEmbedField{Name: "Path", Value: c.Request.URL.Path, Inline: false})

	h.sendDiscordNotification(errorEmbed)

	// 4. Abort request with JSON response
	c.AbortWithStatusJSON(statusCode, gin.H{"error": fmt.Sprintf("%s: %v", errorContext, err)})
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
		// Ensure UserID is handled correctly for logging (use Valid flag)
		UserID:     pgtype.UUID{Bytes: userID, Valid: userID != uuid.Nil},
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

// --- Feedback Handlers ---

// CreateFeedbackRequest defines the structure for the feedback creation request body.
type CreateFeedbackRequest struct {
	Content string `json:"content" binding:"required"`
	Rating  int32  `json:"rating" binding:"required,min=1,max=5"` // Assuming a 1-5 rating scale
}

// CreateFeedbackHandler handles the creation of new feedback.
func (h *Handler) CreateFeedbackHandler(c *gin.Context) {
	// 1. Get User Profile from context (set by AuthRequired middleware)
	userProfileValue, exists := c.Get("userProfile") // Use context key "userProfile"
	if !exists {
		// This should ideally not happen if AuthRequired middleware ran successfully
		log.Printf("ERROR: userProfile not found in context for CreateFeedbackHandler")
		h.handleErrorAndNotify(c, uuid.Nil, http.StatusUnauthorized, "Get User Profile from Context", errors.New("user profile not found in context"))
		return
	}
	userProfile, ok := userProfileValue.(UserProfile) // Direct type assertion
	if !ok {
		// This indicates a programming error (wrong type set in middleware or retrieved here)
		log.Printf("ERROR: Invalid user profile type in context for CreateFeedbackHandler")
		h.handleErrorAndNotify(c, uuid.Nil, http.StatusInternalServerError, "Get User Profile from Context", errors.New("invalid user profile type in context"))
		return
	}
	// No need to check DatabaseID == uuid.Nil here, as AuthRequired middleware already did that.
	// Removed extra closing brace that caused syntax error
	userID := userProfile.DatabaseID

	// 2. Bind and Validate Request Body
	var req CreateFeedbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.handleErrorAndNotify(c, userID, http.StatusBadRequest, "Bind Feedback Request", err)
		return
	}

	// Basic validation (already handled by binding tags, but good practice)
	if req.Content == "" {
		h.handleErrorAndNotify(c, userID, http.StatusBadRequest, "Validate Feedback Request", errors.New("feedback content cannot be empty"))
		return
	}
	if req.Rating < 1 || req.Rating > 5 {
		h.handleErrorAndNotify(c, userID, http.StatusBadRequest, "Validate Feedback Request", errors.New("rating must be between 1 and 5"))
		return
	}

	// 3. Prepare Database Parameters
	params := db.CreateFeedbackParams{
		UserID:  pgtype.UUID{Bytes: userID, Valid: true},
		Content: req.Content, // Assuming sqlc generated this as string
		Rating:  pgtype.Int4{Int32: req.Rating, Valid: true},
	}

	// 4. Execute Database Query
	feedback, err := h.DB.Queries.CreateFeedback(c.Request.Context(), params)
	if err != nil {
		h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, "Create Feedback in DB", err)
		return
	}

	// 5. Log Activity
	// Convert feedback.ID (uuid.UUID) to pgtype.UUID for logging
	pgFeedbackID := pgtype.UUID{Bytes: feedback.ID, Valid: true}
	h.logActivity(c.Request.Context(), userID, db.ActivityActionFeedbackCreate,
		db.NullActivityTargetType{ActivityTargetType: db.ActivityTargetTypeFeedback, Valid: true},
		pgFeedbackID, // Use the converted pgtype.UUID
		map[string]interface{}{
			"feedback_id": feedback.ID.String(), // Use feedback.ID directly
			"rating":      feedback.Rating.Int32,
			"content_preview": func() string { // Add a preview of the content
				if len(req.Content) > 50 {
					return req.Content[:50] + "..."
				}
				return req.Content
			}(),
		})

	// 6. Send Discord Notification
	discordEmbed := DiscordEmbed{
		Title: "📝 New Feedback Submitted",
		Color: 0x00FF00, // Green
		Fields: []DiscordEmbedField{
			{Name: "User", Value: fmt.Sprintf("%s (`%s`)", userProfile.Name, userID.String()), Inline: false},
			{Name: "Rating", Value: fmt.Sprintf("%d / 5", feedback.Rating.Int32), Inline: true},
			{Name: "Content", Value: req.Content, Inline: false},
		},
		Timestamp: time.Now().Format(time.RFC3339),
		Footer: &DiscordEmbedFooter{
			Text: "Feedback submitted via QuizBuilderAI",
		},
	}
	if userProfile.Picture != "" {
		discordEmbed.Author = &DiscordEmbedAuthor{
			Name:    userProfile.Name,
			IconURL: userProfile.Picture,
		}
	}
	h.sendDiscordNotification(discordEmbed)

	// 7. Return Success Response
	c.JSON(http.StatusCreated, feedback)
}
