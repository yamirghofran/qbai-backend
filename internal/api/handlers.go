package api

import (
	"bytes" // Added for Discord notification payload
	"context"
	"crypto/rand"
	"database/sql" // Added for sql.ErrNoRows
	"encoding/base64"
	"encoding/json" // Added for activity log details &amp; Discord payload
	"errors"        // Import the standard errors package
	"fmt"           // Added for error formatting
	"io"            // Added for file operations
	"log"           // Added for logging errors
	"mime/multipart"
	"net/http" // Added for Discord notification
	"os"
	"time" // Added for response struct timestamps &amp; Discord timeout

	// Removed unused imports: mime/multipart, path/filepath, strings

	"quizbuilderai/internal/db"
	"quizbuilderai/internal/gemini"

	// "quizbuilderai/internal/r2" // Removed Cloudflare R2 client import
	"quizbuilderai/internal/youtube"

	"github.com/google/uuid" // Added for user ID

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype" // Added for pgtype.Text
	"golang.org/x/oauth2"
	oauth2api "google.golang.org/api/oauth2/v2"
	"google.golang.org/api/option"
)

// UserProfile stores information about the authenticated user.
// Keep this definition accessible, perhaps in a models.go file within internal/ or internal/api/
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
const (
	oauthStateSessionKey = "oauthstate"
	profileSessionKey    = "profile"
	discordWebhookURL    = "https://discord.com/api/webhooks/1356553549256986725/9v9vVxGCLQhvOJtMmC5MZKXdR-AiJuS_a_NTyo1U6ItTPM9kzcQusw31GxR3UvxmUYN3" // Added Discord Webhook URL
)

// Handler contains the API handlers
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
	// Removed R2 client initialization

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
		// R2Client:    nil, // R2 client removed
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

// Auth Handlers
// HandleGoogleLogin: Initiates the Google OAuth flow.
func (h *Handler) HandleGoogleLogin(c *gin.Context) {
	session := sessions.Default(c)

	stateBytes := make([]byte, 16)
	_, err := rand.Read(stateBytes)
	if err != nil {
		log.Printf("ERROR: Failed to generate state: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate state"})
		return
	}
	oauthStateString := base64.URLEncoding.EncodeToString(stateBytes)

	session.Set(oauthStateSessionKey, oauthStateString)
	err = session.Save()
	if err != nil {
		log.Printf("ERROR: Failed to save session: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save session"})
		return
	}

	url := h.OauthConfig.AuthCodeURL(oauthStateString, oauth2.AccessTypeOffline)
	c.Redirect(http.StatusTemporaryRedirect, url)
}

// HandleGoogleCallback: Handles the redirect back from Google.
func (h *Handler) HandleGoogleCallback(c *gin.Context) {
	session := sessions.Default(c)
	retrievedState := session.Get(oauthStateSessionKey)
	originalState := c.Query("state")

	if originalState == "" || retrievedState == nil || retrievedState.(string) != originalState {
		log.Printf("WARN: Invalid state parameter. Session state: %v, Query state: %s", retrievedState, originalState)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid state parameter."})
		return
	}

	code := c.Query("code")
	token, err := h.OauthConfig.Exchange(context.Background(), code)
	if err != nil {
		log.Printf("ERROR: Failed to exchange code: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to exchange code"})
		return
	}

	if !token.Valid() {
		log.Printf("WARN: Retrieved invalid token.")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Retrieved invalid token"})
		return
	}

	client := h.OauthConfig.Client(context.Background(), token)
	oauth2Service, err := oauth2api.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		log.Printf("ERROR: Failed to create OAuth2 service: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create OAuth2 service"})
		return
	}

	userinfo, err := oauth2Service.Userinfo.V2.Me.Get().Do()
	if err != nil {
		log.Printf("ERROR: Failed to get user info: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get user info"})
		return
	}

	// --- Database Interaction ---
	ctx := c.Request.Context()                                      // Use request context
	dbUser, err := h.DB.Queries.GetUserByEmail(ctx, userinfo.Email) // Corrected: Use h.DB.Queries

	isNewUser := false // Flag to track if it's a signup

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) { // Use errors.Is for robust checking
			// User doesn't exist, create them
			log.Printf("INFO: User with email %s not found, creating new user.", userinfo.Email)
			isNewUser = true // Mark as new user
			// Corrected CreateUserParams based on db/users.sql.go and regenerated code
			createUserParams := db.CreateUserParams{
				Email:    userinfo.Email,
				Name:     pgtype.Text{String: userinfo.Name, Valid: userinfo.Name != ""},
				GoogleID: pgtype.Text{String: userinfo.Id, Valid: userinfo.Id != ""},
				Picture:  pgtype.Text{String: userinfo.Picture, Valid: userinfo.Picture != ""}, // Added Picture field
			}
			dbUser, err = h.DB.Queries.CreateUser(ctx, createUserParams) // Corrected: Use h.DB.Queries
			if err != nil {
				log.Printf("ERROR: Failed to create user: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create user profile"})
				return
			}
			log.Printf("INFO: Created user with ID %s for email %s", dbUser.ID, dbUser.Email) // Changed %d to %s for UUID

			// Log signup activity
			h.logActivity(ctx, dbUser.ID, db.ActivityActionLogin, // Assuming signup implies login
				db.NullActivityTargetType{ActivityTargetType: db.ActivityTargetTypeUser, Valid: true},
				pgtype.UUID{Bytes: dbUser.ID, Valid: true},
				map[string]interface{}{"email": dbUser.Email, "signup": true}) // Add signup flag

			// Send Discord notification for signup
			h.sendDiscordNotification(fmt.Sprintf("🎉 New Signup: %s (%s)", dbUser.Name.String, dbUser.Email))

		} else {
			// Other database error
			log.Printf("ERROR: Failed to get user by email: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error checking user profile"})
			return
		}
	} else {
		// User exists, potentially update? (Optional)
		// For now, just log that the user was found.
		log.Printf("INFO: Found existing user with ID %s for email %s", dbUser.ID, dbUser.Email) // Changed %d to %s for UUID

		// Log login activity for existing user
		h.logActivity(ctx, dbUser.ID, db.ActivityActionLogin,
			db.NullActivityTargetType{ActivityTargetType: db.ActivityTargetTypeUser, Valid: true},
			pgtype.UUID{Bytes: dbUser.ID, Valid: true},
			map[string]interface{}{"email": dbUser.Email, "signup": false}) // No signup flag

		// Send Discord notification for login (only if not a new user signup)
		if !isNewUser {
			h.sendDiscordNotification(fmt.Sprintf("✅ User Login: %s (%s)", dbUser.Name.String, dbUser.Email))
		}

		// Example update (if you have an UpdateUser method and want to refresh data):
		/*
			// Corrected commented UpdateUserParams based on db/users.sql.go
			updateParams := db.UpdateUserParams{
				ID:       dbUser.ID,
				Email:    userinfo.Email, // UpdateUserParams requires Email
				Name:     pgtype.Text{String: userinfo.Name, Valid: userinfo.Name != ""},
				GoogleID: pgtype.Text{String: userinfo.Id, Valid: userinfo.Id != ""},
				// Note: GivenName, FamilyName, Picture, Locale, VerifiedEmail are not in UpdateUserParams
			}
			_, err = h.DB.UpdateUser(ctx, updateParams) // Assuming UpdateUser exists
			if err != nil {
				log.Printf("ERROR: Failed to update user %s: %v", dbUser.ID, err) // Changed %d to %s for UUID
				// Decide if this is a critical error - maybe just log and continue?
			}
		*/
	}
	// --- End Database Interaction ---

	// Create the UserProfile directly from Google's userinfo, as our DB doesn't store all these fields.
	// The dbUser variable now holds our internal user record (either newly created or existing).
	profile := UserProfile{
		DatabaseID:    dbUser.ID,   // Use the internal DB UUID
		GoogleID:      userinfo.Id, // Google's ID
		Email:         userinfo.Email,
		VerifiedEmail: userinfo.VerifiedEmail != nil && *userinfo.VerifiedEmail, // Corrected: Safely dereference pointer
		Name:          userinfo.Name,
		GivenName:     userinfo.GivenName,
		FamilyName:    userinfo.FamilyName,
		Picture:       userinfo.Picture,
		Locale:        userinfo.Locale,
	}
	// We have dbUser.ID (our internal UUID) available here if needed for other logic or future session storage.
	log.Printf("INFO: User %s mapped to internal ID %s", profile.Email, dbUser.ID)

	session.Set(profileSessionKey, profile) // Store the potentially updated profile
	session.Delete(oauthStateSessionKey)

	err = session.Save()
	if err != nil {
		log.Printf("ERROR: Failed to save session after login: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save session"})
		return
	}

	// Redirect to a relative path, letting the browser handle the full URL
	// Redirect to frontend URL - this should likely be configurable
	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		frontendURL = "/" // Default fallback
	}
	log.Printf("Redirecting user %s to frontend: %s", profile.Email, frontendURL)
	c.Redirect(http.StatusTemporaryRedirect, frontendURL)
}

// HandleUserProfile: Displays the user's profile information.
func (h *Handler) HandleUserProfile(c *gin.Context) {
	session := sessions.Default(c)
	profileData := session.Get(profileSessionKey)

	profile, ok := profileData.(UserProfile)
	if !ok || profileData == nil {
		// Use Unauthorized status for API endpoints when auth fails
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Not authenticated or session invalid"})
		return
	}

	c.JSON(http.StatusOK, profile)
}

// HandleLogout: Clears the session.
func (h *Handler) HandleLogout(c *gin.Context) {
	session := sessions.Default(c)
	// Get user profile from CONTEXT *before* clearing the session to log the correct user ID
	userID := uuid.Nil // Default to Nil UUID if not found
	userName := "Unknown User"
	userEmail := ""
	userProfileValue, profileExists := c.Get("userProfile") // Use context key

	if profileExists {
		profile, profileOk := userProfileValue.(UserProfile)
		if profileOk {
			userID = profile.DatabaseID
			userName = profile.Name
			userEmail = profile.Email
			// Ensure name isn't empty
			if userName == "" {
				userName = "User"
			}
			log.Printf("INFO: Logging out user %s (ID: %s)", profile.Email, userID)
		} else {
			log.Printf("ERROR: Value found for key 'userProfile' in context is not UserProfile during logout. Type: %T", userProfileValue)
			// Attempt to get userID from context directly if profile assertion failed
			userIDValueCtx, idExists := c.Get("userID")
			if idExists {
				if id, idOk := userIDValueCtx.(uuid.UUID); idOk {
					userID = id
					log.Printf("WARN: Using userID %s directly from context for logout logging.", userID)
				}
			}
		}
	} else {
		log.Printf("WARN: User profile key 'userProfile' not found in context during logout.")
		// Attempt to get userID from context directly if profile key missing
		userIDValueCtx, idExists := c.Get("userID")
		if idExists {
			if id, idOk := userIDValueCtx.(uuid.UUID); idOk {
				userID = id
				log.Printf("WARN: Using userID %s directly from context for logout logging.", userID)
			}
		}
	}

	session.Clear()
	session.Options(sessions.Options{MaxAge: -1})

	err := session.Save()
	if err != nil {
		// Log the error but still attempt to respond
		log.Printf("ERROR: Failed to save session during logout for user %s: %v", userID, err)
		// Consider returning an error if session saving is critical
		// c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to clear session"})
		// return
	}

	// Log logout activity if user ID was found
	if userID != uuid.Nil {
		h.logActivity(c.Request.Context(), userID, db.ActivityActionLogout,
			db.NullActivityTargetType{ActivityTargetType: db.ActivityTargetTypeUser, Valid: true},
			pgtype.UUID{Bytes: userID, Valid: true},
			nil) // No specific details needed for logout

		// Send Discord notification for logout
		h.sendDiscordNotification(fmt.Sprintf("🚪 User Logout: %s (%s)", userName, userEmail))
	}

	// Instead of redirecting, send a success response.
	// The frontend will handle UI updates/reload based on this success.
	log.Printf("User session cleared successfully for user ID: %s", userID)
	c.Status(http.StatusOK) // Or http.StatusNoContent
}

// contains checks if a string is in a slice
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// Helper function to clean up temporary files
func cleanupTempFile(path string) error {
	return os.Remove(path)
}

// HandleGenerateQuiz handles the request to generate a quiz from uploaded content
func (h *Handler) HandleGenerateQuiz(c *gin.Context) {
	ctx := c.Request.Context()
	// _ = ctx // Mark ctx as used to avoid compiler error, will be used later

	// 1. Get User ID from context (set by AuthRequired middleware)
	userIDValue, exists := c.Get("userID")
	if !exists {
		log.Printf("ERROR: User ID not found in context")
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated or context missing user ID"})
		return
	}

	userID, ok := userIDValue.(uuid.UUID)
	if !ok {
		log.Printf("ERROR: User ID in context is not of type uuid.UUID")
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Internal server error: Invalid user ID type in context"})
		return
	}
	log.Printf("INFO: Handling quiz generation request for user ID: %s", userID)

	// Get user details for notifications
	userName := "Unknown User"                              // Default value
	userEmail := ""                                         // Default value
	userProfileValue, profileExists := c.Get("userProfile") // Use the key set by middleware

	if profileExists {
		profile, profileOk := userProfileValue.(UserProfile) // Check type assertion
		if profileOk {
			// Successfully retrieved and asserted profile
			userName = profile.Name
			userEmail = profile.Email
			// Ensure name isn't empty, fallback if needed
			if userName == "" {
				userName = "User" // Use a slightly better default if name is empty but profile exists
			}
			log.Printf("INFO: Retrieved user profile from context for notification: Name=%s, Email=%s", userName, userEmail)
		} else {
			// Profile key exists, but type assertion failed
			log.Printf("ERROR: Value found for key '%s' in context is not of type UserProfile. Type: %T. UserID: %s", "userProfile", userProfileValue, userID)
			// userName and userEmail will keep their default values ("Unknown User", "")
		}
	} else {
		// Profile key does not exist in context
		log.Printf("ERROR: User profile key '%s' not found in context for quiz generation notification. UserID: %s", "userProfile", userID)
		// userName and userEmail will keep their default values ("Unknown User", "")
	}
	// 2. Parse Multipart Form Data
	// Set a reasonable limit (e.g., 64 MB) for memory storage of parts
	// Adjust this based on expected file sizes and server resources
	err := c.Request.ParseMultipartForm(64 << 20) // 64 MB
	if err != nil {
		log.Printf("ERROR: Failed to parse multipart form: %v", err)
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Failed to parse form data: %v", err)})
		return
	}

	// Structure to hold info about uploaded files for later processing (DB)
	type uploadedFileInfo struct {
		Header   *multipart.FileHeader // Keep header for original filename, size etc.
		TempPath string                // Path to the temporary file on disk
	}
	var uploadedFiles []uploadedFileInfo // Holds info only for actual file uploads

	// Slice to hold info about processed documents for Gemini (files + transcripts)
	var documentFiles []gemini.DocumentFile
	// Slice to hold paths of ALL temporary files (files + transcripts) for cleanup
	var tempFilePaths []string

	// Defer cleanup of all temporary files
	defer func() {
		for _, path := range tempFilePaths {
			log.Printf("INFO: Cleaning up temporary file: %s", path)
			if err := os.Remove(path); err != nil {
				log.Printf("WARN: Failed to remove temporary file %s: %v", path, err)
			}
		}
	}()

	// 3. Process Uploaded Files
	files := c.Request.MultipartForm.File["files"] // Key matches frontend FormData
	log.Printf("INFO: Received %d files for processing", len(files))

	for _, fileHeader := range files {
		log.Printf("INFO: Processing file: %s (Size: %d)", fileHeader.Filename, fileHeader.Size)

		// Basic validation (optional, add more as needed)
		if fileHeader.Size == 0 {
			log.Printf("WARN: Skipping empty file: %s", fileHeader.Filename)
			continue
		}
		// Add size limit check if necessary
		// Add MIME type check if necessary

		file, err := fileHeader.Open()
		if err != nil {
			log.Printf("ERROR: Failed to open uploaded file %s: %v", fileHeader.Filename, err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to open file %s", fileHeader.Filename)})
			return // Stop processing on error
		}
		// Ensure file is closed (although saving to temp might make this redundant)
		defer file.Close()

		// Read file content (needed for SaveTempFile)
		fileBytes, err := io.ReadAll(file)
		if err != nil {
			log.Printf("ERROR: Failed to read uploaded file %s: %v", fileHeader.Filename, err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to read file %s", fileHeader.Filename)})
			return
		}

		// Save to temporary location using gemini helper
		// Note: SaveTempFile expects []byte
		tempPath, err := gemini.SaveTempFile(fileBytes, fileHeader.Filename)
		if err != nil {
			log.Printf("ERROR: Failed to save temporary file for %s: %v", fileHeader.Filename, err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to save temporary file for %s", fileHeader.Filename)})
			return
		}
		tempFilePaths = append(tempFilePaths, tempPath) // Add path for deferred cleanup
		log.Printf("INFO: Saved file %s temporarily to %s", fileHeader.Filename, tempPath)

		// Store info needed for DB processing within the transaction
		uploadedFiles = append(uploadedFiles, uploadedFileInfo{
			Header:   fileHeader,
			TempPath: tempPath,
		})
		// Note: We no longer need the placeholder materialIDs slice here,
		// as materials will be created and linked within the transaction directly.

		// Prepare document for Gemini processing
		documentFiles = append(documentFiles, gemini.DocumentFile{
			Name: fileHeader.Filename,
			Path: tempPath,
			Size: fileHeader.Size,
		})
	}

	// 4. Process Video URLs
	videoURLs := c.Request.MultipartForm.Value["videoUrls"] // Key matches frontend FormData
	log.Printf("INFO: Received %d video URLs for processing", len(videoURLs))
	log.Printf("DEBUG: Video URLs received: %v", videoURLs) // Log the actual URLs received

	for _, url := range videoURLs {
		if url == "" {
			log.Printf("WARN: Skipping empty video URL")
			continue
		}
		log.Printf("INFO: Processing video URL: %s", url)

		// Fetch transcript (pass empty string for default language)
		log.Printf("DEBUG: Calling GetTranscript for URL: %s", url)
		transcript, err := h.Youtube.GetTranscript(url, "") // Corrected: Removed ctx
		if err != nil {
			// Log error but continue processing other URLs/files? Or abort?
			// For now, let's log and continue, but return an error later if *no* content was processed.
			log.Printf("WARN: Failed to get transcript for URL %s: %v. Skipping this URL.", url, err)
			// Optionally: Add this error to a list to return to the user later
			log.Printf("DEBUG: Skipping URL %s due to GetTranscript error: %v", url, err)
			continue
		}

		if transcript == "" {
			log.Printf("WARN: Skipping URL %s as transcript was empty.", url)
			continue
		}
		log.Printf("DEBUG: Successfully fetched transcript for URL %s (length: %d)", url, len(transcript))
		// Removed extra closing brace from here

		// Save transcript to temporary file
		transcriptFilename := fmt.Sprintf("transcript_%s.txt", uuid.New().String()) // Unique temp name
		tempPath, err := gemini.SaveTempFile([]byte(transcript), transcriptFilename)
		if err != nil {
			log.Printf("ERROR: Failed to save temporary transcript file for %s: %v", url, err)
			// Abort here as this is an internal error
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to save temporary transcript for %s", url)})
			return
		}
		tempFilePaths = append(tempFilePaths, tempPath) // Add path for deferred cleanup
		log.Printf("INFO: Saved transcript for %s temporarily to %s", url, tempPath)

		// Get file info for size
		fileInfo, err := os.Stat(tempPath)
		if err != nil {
			log.Printf("ERROR: Failed to get file info for temporary transcript %s: %v", tempPath, err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to get file info for transcript %s", url)})
			return
		}

		// Note: Transcripts are processed for Gemini but NOT stored in uploadedFiles
		// The material record for transcripts will be created in the transaction using the video URL.

		// Prepare document for Gemini processing
		documentFiles = append(documentFiles, gemini.DocumentFile{
			Name: transcriptFilename, // Use the temp filename
			Path: tempPath,
			Size: fileInfo.Size(),
		})
		log.Printf("DEBUG: Added transcript from URL %s as document file: %s", url, transcriptFilename)

	} // <-- Moved this closing brace after the log message

	// Check if any content was processed
	if len(documentFiles) == 0 {
		log.Printf("ERROR: No valid files or video URLs were processed for user %s", userID)
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "No valid content provided or processed. Please check files and URLs."})
		return
	}

	// 5. Call Gemini to generate the quiz
	log.Printf("INFO: Calling Gemini to process %d documents for user %s", len(documentFiles), userID)
	// Receive token counts from ProcessDocuments
	geminiResponse, promptTokens, candidateTokens, totalTokens, err := h.Gemini.ProcessDocuments(ctx, documentFiles)
	if err != nil {
		log.Printf("ERROR: Gemini processing failed for user %s: %v", userID, err)
		// Consider more specific error mapping if Gemini provides codes/types
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to generate quiz content: %v", err)})
		return
	}

	// Log received token counts (even if quiz generation failed partially)
	log.Printf("INFO: Gemini Token Usage Reported: User=%s, Prompt=%d, Candidates=%d, Total=%d", userID, promptTokens, candidateTokens, totalTokens)

	if geminiResponse == nil || len(geminiResponse.Questions) == 0 {
		log.Printf("ERROR: Gemini returned no questions for user %s", userID)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Quiz generation resulted in no questions."})
		return
	}

	log.Printf("INFO: Gemini generated quiz titled '%s' with %d questions for user %s", geminiResponse.Title, len(geminiResponse.Questions), userID)

	// 6. Process Gemini Response &amp; DB Insertion (Transaction)
	var createdQuiz db.Quize // Variable to hold the created quiz

	// Start transaction using the connection pool from the DB struct
	tx, err := h.DB.Pool.Begin(ctx)
	if err != nil {
		log.Printf("ERROR: Failed to begin database transaction for user %s: %v", userID, err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to start database transaction"})
		return
	}
	// Ensure rollback on error
	defer tx.Rollback(ctx) // Rollback is ignored if Commit() succeeds

	qtx := h.DB.Queries.WithTx(tx)

	// --- Token Transaction and Balance Update (Inside Transaction) ---
	// Create token usage record (negative amount for consumption)
	if totalTokens > 0 { // Only record if tokens were used
		_, tokenErr := qtx.CreateTokenTransaction(ctx, db.CreateTokenTransactionParams{
			UserID: userID,
			Amount: -totalTokens, // Use negative value for usage
			// Type is automatically set to 'usage' by the query
		})
		if tokenErr != nil {
			log.Printf("ERROR: Failed to create token transaction record for user %s: %v", userID, tokenErr)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to record token usage"})
			return // Rollback happens via defer
		}

		// Update user's token balance
		_, balanceErr := qtx.UpdateUserTokenBalance(ctx, db.UpdateUserTokenBalanceParams{
			ID:                  userID,
			InputTokensBalance:  promptTokens,    // Amount to decrement input balance by
			OutputTokensBalance: candidateTokens, // Amount to decrement output balance by
		})
		if balanceErr != nil {
			log.Printf("ERROR: Failed to update token balance for user %s: %v", userID, balanceErr)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to update token balance"})
			return // Rollback happens via defer
		}
		log.Printf("INFO: Recorded token usage and updated balance for user %s: Prompt=%d, Candidates=%d, Total=%d", userID, promptTokens, candidateTokens, totalTokens)
	}
	// --- End Token Transaction ---

	// Create the main Quiz record
	quizParams := db.CreateQuizParams{
		CreatorID: pgtype.UUID{Bytes: userID, Valid: true},
		Title:     geminiResponse.Title,
		// Description: pgtype.Text{String: "Generated by AI", Valid: true}, // Optional description
		Visibility: db.QuizVisibilityPublic, // Default visibility set to public
	}
	createdQuiz, err = qtx.CreateQuiz(ctx, quizParams)
	if err != nil {
		log.Printf("ERROR: Failed to create quiz record for user %s: %v", userID, err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to save quiz"})
		return
	}
	log.Printf("INFO: Created quiz with ID %s for user %s", createdQuiz.ID, userID)

	// Create and Link Materials to the Quiz (Inside Transaction)
	processedMaterialCount := 0

	// Process uploaded files (DB record creation and linking)
	for _, uploadedFile := range uploadedFiles {
		fileHeader := uploadedFile.Header
		// tempPath := uploadedFile.TempPath // Removed as it's no longer used after R2 removal

		// 1. Create Material Record (URL will remain empty/null as R2 is removed)
		materialParams := db.CreateMaterialParams{
			UserID: userID,
			Title:  fileHeader.Filename,
			// Url is NULL/empty
		}
		material, err := qtx.CreateMaterial(ctx, materialParams)
		if err != nil {
			log.Printf("ERROR: Failed to create material record for file %s in transaction: %v", fileHeader.Filename, err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to create database record for file %s", fileHeader.Filename)})
			return // Rollback happens via defer
		}
		log.Printf("INFO: Created material record %s for file %s", material.ID, fileHeader.Filename)

		// 2. R2 Upload Logic Removed
		// The material URL will remain empty/null in the database.

		// 4. Link Material to Quiz
		_, linkErr := qtx.LinkQuizMaterial(ctx, db.LinkQuizMaterialParams{
			QuizID:     createdQuiz.ID,
			MaterialID: material.ID,
		})
		if linkErr != nil {
			log.Printf("ERROR: Failed to link material %s to quiz %s: %v", material.ID, createdQuiz.ID, linkErr)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to link materials to quiz"})
			return // Rollback happens via defer
		}
		processedMaterialCount++
	} // End loop for uploaded files

	// Process video URLs (Create material with YouTube URL, link to quiz)
	for _, url := range videoURLs {
		if url == "" {
			continue
		} // Skip empty ones again

		// Simple title generation
		videoTitle := fmt.Sprintf("YouTube Transcript Source: %s", url)
		if len(videoTitle) > 255 {
			videoTitle = videoTitle[:252] + "..."
		}

		// Create material record with the original YouTube URL
		materialParams := db.CreateMaterialParams{
			UserID: userID,
			Title:  videoTitle,
			Url:    pgtype.Text{String: url, Valid: true}, // Store the YouTube URL
		}
		material, err := qtx.CreateMaterial(ctx, materialParams)
		if err != nil {
			log.Printf("ERROR: Failed to create material record for video %s in transaction: %v", url, err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to create database record for video %s", url)})
			return // Rollback happens via defer
		}

		// Link material to quiz
		_, linkErr := qtx.LinkQuizMaterial(ctx, db.LinkQuizMaterialParams{
			QuizID:     createdQuiz.ID,
			MaterialID: material.ID,
		})
		if linkErr != nil {
			log.Printf("ERROR: Failed to link video material %s to quiz %s: %v", material.ID, createdQuiz.ID, linkErr)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to link video materials to quiz"})
			return // Rollback happens via defer
		}
		processedMaterialCount++
	} // End loop for video URLs

	log.Printf("INFO: Created and linked %d total materials (files + videos) to quiz %s", processedMaterialCount, createdQuiz.ID)
	// Process Questions and Answers
	topicCache := make(map[string]uuid.UUID) // Cache found/created topic IDs

	for _, geminiQuestion := range geminiResponse.Questions {
		if geminiQuestion.Text == "" || len(geminiQuestion.Options) != 4 {
			log.Printf("WARN: Skipping invalid question from Gemini: %+v", geminiQuestion)
			continue
		}

		// Get or Create Topic
		topicTitle := geminiQuestion.Topic
		if topicTitle == "" {
			topicTitle = "General" // Default topic if Gemini didn't provide one
			log.Printf("WARN: Gemini question missing topic, using default: '%s'", topicTitle)
		}

		topicID, found := topicCache[topicTitle]
		if !found {
			topic, err := qtx.GetTopicByTitleAndUser(ctx, db.GetTopicByTitleAndUserParams{
				Title:     topicTitle,
				CreatorID: pgtype.UUID{Bytes: userID, Valid: true},
			})
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					// Topic doesn't exist, create it
					log.Printf("INFO: Topic '%s' not found for user %s, creating new topic.", topicTitle, userID)
					newTopic, err := qtx.CreateTopic(ctx, db.CreateTopicParams{
						CreatorID: pgtype.UUID{Bytes: userID, Valid: true},
						Title:     topicTitle,
						// Description: pgtype.Text{}, // Optional description
					})
					if err != nil {
						log.Printf("ERROR: Failed to create topic '%s' for user %s: %v", topicTitle, userID, err)
						c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to create topic '%s'", topicTitle)})
						return
					}
					topicID = newTopic.ID
					topicCache[topicTitle] = topicID
					log.Printf("INFO: Created topic '%s' with ID %s for user %s", topicTitle, topicID, userID)
				} else {
					// Other database error
					log.Printf("ERROR: Failed to get topic '%s' for user %s: %v", topicTitle, userID, err)
					c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Database error checking topic '%s'", topicTitle)})
					return
				}
			} else {
				// Topic found
				topicID = topic.ID
				topicCache[topicTitle] = topicID
				log.Printf("INFO: Found existing topic '%s' with ID %s for user %s", topicTitle, topicID, userID)
			}
		}

		// Create Question
		dbQuestion, err := qtx.CreateQuestion(ctx, db.CreateQuestionParams{
			QuizID:   createdQuiz.ID,
			TopicID:  topicID,
			Question: geminiQuestion.Text,
		})
		if err != nil {
			log.Printf("ERROR: Failed to create question for quiz %s: %v", createdQuiz.ID, err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to save question"})
			return
		}

		// Create Answers
		correctAnswerCount := 0
		for _, geminiOption := range geminiQuestion.Options {
			if geminiOption.IsCorrect {
				correctAnswerCount++
			}
			_, err = qtx.CreateAnswer(ctx, db.CreateAnswerParams{
				QuestionID:  dbQuestion.ID,
				Answer:      geminiOption.Text,
				IsCorrect:   geminiOption.IsCorrect,
				Explanation: pgtype.Text{String: geminiOption.Explanation, Valid: geminiOption.Explanation != ""}, // Add explanation from Gemini
			})
			if err != nil {
				log.Printf("ERROR: Failed to create answer for question %s: %v", dbQuestion.ID, err)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to save answer"})
				return
			}
		}

		// Validate that exactly one correct answer was provided by Gemini
		if correctAnswerCount != 1 {
			// Log the problematic question structure for debugging
			log.Printf("ERROR: Invalid number of correct answers (%d) for question from Gemini. Rolling back. Question Details: %+v", correctAnswerCount, geminiQuestion)
			// No need to explicitly call Rollback, defer handles it. Just return error.
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Invalid question data received from AI: '%s'", geminiQuestion.Text)})
			return
		}
	}

	// Commit the transaction
	err = tx.Commit(ctx)
	if err != nil {
		log.Printf("ERROR: Failed to commit transaction for quiz %s: %v", createdQuiz.ID, err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to finalize saving quiz"})
		return
	}

	log.Printf("INFO: Successfully created quiz %s with %d questions for user %s", createdQuiz.ID, len(geminiResponse.Questions), userID)

	// Log quiz creation activity
	h.logActivity(ctx, userID, db.ActivityActionQuizCreate,
		db.NullActivityTargetType{ActivityTargetType: db.ActivityTargetTypeQuiz, Valid: true},
		pgtype.UUID{Bytes: createdQuiz.ID, Valid: true},
		map[string]interface{}{
			"title":            createdQuiz.Title,
			"question_count":   len(geminiResponse.Questions),
			"material_count":   processedMaterialCount,
			"prompt_tokens":    promptTokens,    // Add token info
			"candidate_tokens": candidateTokens, // Add token info
			"total_tokens":     totalTokens,     // Add token info
		}) // Add token details to the log

	// Send Discord notification for quiz creation
	h.sendDiscordNotification(fmt.Sprintf("📝 Quiz Created: '%s' (%d questions, %d materials, %d tokens used) by %s (%s)",
		createdQuiz.Title, len(geminiResponse.Questions), processedMaterialCount, totalTokens, userName, userEmail))

	// 7. Return Response
	c.JSON(http.StatusOK, gin.H{
		"message": "Quiz generated successfully!",
		"quizId":  createdQuiz.ID.String(), // Return the new quiz ID as a string
	})
}

// Define response structures matching frontend/src/types/index.ts
type ResponseOption struct {
	ID          uuid.UUID   `json:"id"`
	Text        string      `json:"text"`
	IsCorrect   bool        `json:"is_correct"`
	Explanation pgtype.Text `json:"explanation"` // Add explanation field (nullable)
}

type ResponseQuestion struct {
	ID         uuid.UUID        `json:"id"`
	Text       string           `json:"text"`
	TopicTitle pgtype.Text      `json:"topic_title"` // Added topic title (nullable)
	Options    []ResponseOption `json:"options"`
}

type ResponseQuiz struct {
	ID          uuid.UUID          `json:"id"`
	Title       string             `json:"title"`
	Description pgtype.Text        `json:"description"` // Added description
	Visibility  db.QuizVisibility  `json:"visibility"`  // Added visibility
	Questions   []ResponseQuestion `json:"questions"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

// HandleGetQuiz retrieves a specific quiz by its ID, including its questions and answers.
func (h *Handler) HandleGetQuiz(c *gin.Context) {
	ctx := c.Request.Context()
	quizIDStr := c.Param("quizId")

	// 1. Parse UUID
	quizID, err := uuid.Parse(quizIDStr)
	if err != nil {
		log.Printf("ERROR: Invalid Quiz ID format '%s': %v", quizIDStr, err)
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid Quiz ID format"})
		return
	}
	log.Printf("INFO: Handling request for quiz ID: %s", quizID)

	// 2. Fetch Quiz details
	// Use the correct function name: GetQuizByID
	dbQuiz, err := h.DB.Queries.GetQuizByID(ctx, quizID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("WARN: Quiz not found: %s", quizID)
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "Quiz not found"})
		} else {
			log.Printf("ERROR: Failed to get quiz %s: %v", quizID, err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve quiz"})
		}
		return
	}

	// 3. Fetch Questions for the Quiz
	// Use the correct function name: ListQuestionsByQuizID
	dbQuestions, err := h.DB.Queries.ListQuestionsByQuizID(ctx, quizID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) { // It's okay if a quiz has no questions yet
		log.Printf("ERROR: Failed to get questions for quiz %s: %v", quizID, err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve quiz questions"})
		return
	}
	log.Printf("INFO: Found %d questions for quiz %s", len(dbQuestions), quizID)

	// 4. Structure the response
	response := ResponseQuiz{
		ID:          dbQuiz.ID,
		Title:       dbQuiz.Title,
		Description: dbQuiz.Description, // Added description
		Visibility:  dbQuiz.Visibility,  // Added visibility
		// Ensure CreatedAt and UpdatedAt are handled correctly (assuming pgtype.Timestamptz)
		CreatedAt: dbQuiz.CreatedAt,
		UpdatedAt: dbQuiz.UpdatedAt,
		Questions: make([]ResponseQuestion, 0, len(dbQuestions)), // Pre-allocate slice
	}

	// 5. Fetch Answers for each Question and populate response
	for _, dbQ := range dbQuestions {
		// Use the correct function name: ListAnswersByQuestionID
		dbAnswers, err := h.DB.Queries.ListAnswersByQuestionID(ctx, dbQ.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) { // It's okay if a question has no answers (though unlikely for MCQs)
			log.Printf("WARN: Failed to get answers for question %s (quiz %s): %v", dbQ.ID, quizID, err)
			// Continue processing other questions, this one will have no options
		}

		respQ := ResponseQuestion{
			ID:         dbQ.ID,
			Text:       dbQ.Question,
			TopicTitle: dbQ.TopicTitle,                            // Added topic title from the joined query result
			Options:    make([]ResponseOption, 0, len(dbAnswers)), // Pre-allocate
		}

		for _, dbA := range dbAnswers {
			respOpt := ResponseOption{
				ID:          dbA.ID,
				Text:        dbA.Answer, // Assuming the field name in db.Answer is 'Answer'
				IsCorrect:   dbA.IsCorrect,
				Explanation: dbA.Explanation, // Map the explanation from the DB
			}
			respQ.Options = append(respQ.Options, respOpt)
		}
		response.Questions = append(response.Questions, respQ)
	}

	log.Printf("INFO: Successfully prepared response for quiz %s", quizID)
	// 6. Return JSON response
	c.JSON(http.StatusOK, response)
}

// HandleAuthStatus checks if a user is currently authenticated via session.
func (h *Handler) HandleAuthStatus(c *gin.Context) {
	session := sessions.Default(c)
	profileData := session.Get(profileSessionKey)

	profile, ok := profileData.(UserProfile)
	if !ok || profileData == nil {
		// Not authenticated
		c.JSON(http.StatusUnauthorized, gin.H{"authenticated": false})
		return
	}

	// Authenticated, return profile data
	// Ensure sensitive data isn't exposed if not needed by the frontend status check
	c.JSON(http.StatusOK, gin.H{
		"authenticated": true,
		"user":          profile, // Send the whole profile or just necessary fields
	})
}

// HandleListUserQuizzes retrieves all quizzes created by the currently authenticated user.
func (h *Handler) HandleListUserQuizzes(c *gin.Context) {
	ctx := c.Request.Context()

	// 1. Get User ID from context (set by AuthRequired middleware)
	userIDValue, exists := c.Get("userID")
	if !exists {
		log.Printf("ERROR: User ID not found in context for listing quizzes")
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated or context missing user ID"})
		return
	}

	userID, ok := userIDValue.(uuid.UUID)
	if !ok {
		log.Printf("ERROR: User ID in context is not of type uuid.UUID for listing quizzes")
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Internal server error: Invalid user ID type in context"})
		return
	}
	log.Printf("INFO: Handling request to list quizzes for user ID: %s", userID)

	// 2. Fetch Quizzes from DB
	quizzes, err := h.DB.Queries.ListQuizzesByCreator(ctx, pgtype.UUID{Bytes: userID, Valid: true})
	if err != nil {
		// It's not an error if the user simply hasn't created any quizzes yet.
		// sql.ErrNoRows is not typically returned by List methods in sqlc, it returns an empty slice.
		// So, we only log and return error for actual database problems.
		log.Printf("ERROR: Failed to list quizzes for user %s: %v", userID, err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve quizzes"})
		return
	}

	// Handle case where no quizzes are found (returns empty slice, not error)
	if quizzes == nil {
		quizzes = []db.ListQuizzesByCreatorRow{} // Ensure we return an empty array, not null
	}

	log.Printf("INFO: Found %d quizzes for user %s", len(quizzes), userID)

	// 3. Return JSON response
	// The db.ListQuizzesByCreatorRow struct is suitable for the response.
	c.JSON(http.StatusOK, quizzes)
}

// HandleDeleteQuiz handles the deletion of a specific quiz.
func (h *Handler) HandleDeleteQuiz(c *gin.Context) {
	ctx := c.Request.Context()
	quizIDStr := c.Param("quizId")

	// 1. Get User ID from context
	userIDValue, exists := c.Get("userID")
	if !exists {
		log.Printf("ERROR: User ID not found in context for deleting quiz %s", quizIDStr)
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	userID, ok := userIDValue.(uuid.UUID)
	if !ok {
		log.Printf("ERROR: User ID in context is not UUID for deleting quiz %s", quizIDStr)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Internal server error: Invalid user ID type"})
		return
	}

	// Get user details for notifications
	userName := "Unknown User"                              // Default value
	userEmail := ""                                         // Default value
	userProfileValue, profileExists := c.Get("userProfile") // Use context key

	if profileExists {
		profile, profileOk := userProfileValue.(UserProfile)
		if profileOk {
			userName = profile.Name
			userEmail = profile.Email
			if userName == "" {
				userName = "User"
			}
			log.Printf("INFO: Retrieved user profile from context for delete notification: Name=%s, Email=%s", userName, userEmail)
		} else {
			log.Printf("ERROR: Value found for key 'userProfile' in context is not UserProfile during delete. Type: %T. UserID: %s", userProfileValue, userID)
		}
	} else {
		log.Printf("ERROR: User profile key 'userProfile' not found in context for delete notification. UserID: %s", userID)
	}

	// 2. Parse Quiz ID
	quizID, err := uuid.Parse(quizIDStr)
	if err != nil {
		log.Printf("ERROR: Invalid Quiz ID format '%s' for deletion: %v", quizIDStr, err)
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid Quiz ID format"})
		return
	}
	log.Printf("INFO: Handling request to delete quiz ID: %s for user ID: %s", quizID, userID)

	// 3. Verify Quiz Ownership (Fetch the quiz first)
	dbQuiz, err := h.DB.Queries.GetQuizByID(ctx, quizID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("WARN: Quiz not found for deletion: %s", quizID)
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "Quiz not found"})
		} else {
			log.Printf("ERROR: Failed to get quiz %s for ownership check during deletion: %v", quizID, err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve quiz details"})
		}
		return
	}

	// Check if the CreatorID (which is pgtype.UUID) matches the userID from context
	if !dbQuiz.CreatorID.Valid || dbQuiz.CreatorID.Bytes != userID {
		log.Printf("WARN: User %s attempted to delete quiz %s owned by %s", userID, quizID, dbQuiz.CreatorID.Bytes)
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "You do not have permission to delete this quiz"})
		return
	}

	// 4. Delete the Quiz using the existing query
	err = h.DB.Queries.DeleteQuiz(ctx, quizID)
	if err != nil {
		log.Printf("ERROR: Failed to delete quiz %s for user %s: %v", quizID, userID, err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete quiz"})
		return
	}

	log.Printf("INFO: Successfully deleted quiz %s by user %s", quizID, userID)

	// Log quiz deletion activity
	h.logActivity(ctx, userID, db.ActivityActionQuizDelete,
		db.NullActivityTargetType{ActivityTargetType: db.ActivityTargetTypeQuiz, Valid: true},
		pgtype.UUID{Bytes: quizID, Valid: true},
		map[string]interface{}{"title": dbQuiz.Title}) // Include title from the fetched quiz

	// Send Discord notification for quiz deletion
	h.sendDiscordNotification(fmt.Sprintf("🗑️ Quiz Deleted: '%s' (ID: %s) by %s (%s)", dbQuiz.Title, quizID, userName, userEmail))

	// 5. Return Success Response
	c.Status(http.StatusNoContent) // 204 No Content is standard for successful DELETE
}

// --- Quiz Attempt Handlers ---

// HandleCreateQuizAttempt starts a new attempt for a given quiz.
func (h *Handler) HandleCreateQuizAttempt(c *gin.Context) {
	ctx := c.Request.Context()
	quizIDStr := c.Param("quizId")

	// 1. Get User ID from context
	userIDValue, exists := c.Get("userID")
	if !exists {
		log.Printf("ERROR: User ID not found in context for creating quiz attempt")
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	userID, ok := userIDValue.(uuid.UUID)
	if !ok {
		log.Printf("ERROR: User ID in context is not UUID for creating quiz attempt")
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Internal server error: Invalid user ID type"})
		return
	}

	// Get user details for notifications
	userName := "Unknown User"                              // Default value
	userEmail := ""                                         // Default value
	userProfileValue, profileExists := c.Get("userProfile") // Use the key set by middleware

	if profileExists {
		profile, profileOk := userProfileValue.(UserProfile) // Check type assertion
		if profileOk {
			// Successfully retrieved and asserted profile
			userName = profile.Name
			userEmail = profile.Email
			// Ensure name isn't empty, fallback if needed
			if userName == "" {
				userName = "User" // Use a slightly better default if name is empty but profile exists
			}
			log.Printf("INFO: Retrieved user profile from context for attempt start notification: Name=%s, Email=%s", userName, userEmail)
		} else {
			// Profile key exists, but type assertion failed
			log.Printf("ERROR: Value found for key '%s' in context is not of type UserProfile during attempt start. Type: %T. UserID: %s", "userProfile", userProfileValue, userID)
			// userName and userEmail will keep their default values ("Unknown User", "")
		}
	} else {
		// Profile key does not exist in context
		log.Printf("ERROR: User profile key '%s' not found in context for attempt start notification. UserID: %s", "userProfile", userID)
		// userName and userEmail will keep their default values ("Unknown User", "")
	}

	// 2. Parse Quiz ID
	quizID, err := uuid.Parse(quizIDStr)
	if err != nil {
		log.Printf("ERROR: Invalid Quiz ID format '%s' for creating attempt: %v", quizIDStr, err)
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid Quiz ID format"})
		return
	}
	log.Printf("INFO: Handling request to create attempt for quiz ID: %s by user ID: %s", quizID, userID)

	// 3. Verify Quiz Exists (Optional but good practice)
	dbQuiz, err := h.DB.Queries.GetQuizByID(ctx, quizID) // Fetch quiz to get title
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("WARN: Attempt to start quiz attempt for non-existent quiz %s by user %s", quizID, userID)
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "Quiz not found"})
		} else {
			log.Printf("ERROR: Failed to verify quiz %s existence for attempt creation: %v", quizID, err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify quiz details"})
		}
		return
	}

	// 4. Create Quiz Attempt record
	attemptParams := db.CreateQuizAttemptParams{
		QuizID: quizID,
		UserID: userID,
	}
	newAttempt, err := h.DB.Queries.CreateQuizAttempt(ctx, attemptParams)
	if err != nil {
		log.Printf("ERROR: Failed to create quiz attempt for quiz %s, user %s: %v", quizID, userID, err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to start quiz attempt"})
		return
	}

	log.Printf("INFO: Created quiz attempt %s for quiz %s, user %s", newAttempt.ID, quizID, userID)

	// Log attempt start activity
	h.logActivity(ctx, userID, db.ActivityActionQuizAttemptStart,
		db.NullActivityTargetType{ActivityTargetType: db.ActivityTargetTypeQuizAttempt, Valid: true},
		pgtype.UUID{Bytes: newAttempt.ID, Valid: true},
		map[string]interface{}{"quiz_id": quizID.String()})

	// Send Discord notification for attempt start
	h.sendDiscordNotification(fmt.Sprintf("🚀 Quiz Attempt Started: '%s' (QuizID: %s, AttemptID: %s) by %s (%s)",
		dbQuiz.Title, quizID, newAttempt.ID, userName, userEmail))

	// 5. Return the new attempt ID
	c.JSON(http.StatusCreated, gin.H{"attemptId": newAttempt.ID.String()})
}

// ResponseAttemptAnswer matches the structure needed by the frontend
type ResponseAttemptAnswer struct {
	QuestionID       uuid.UUID `json:"question_id"`
	SelectedAnswerID uuid.UUID `json:"selected_answer_id"`
	IsCorrect        bool      `json:"is_correct"`
}

// ResponseQuizAttempt includes the basic attempt info and saved answers
type ResponseQuizAttempt struct {
	ID        uuid.UUID               `json:"id"`
	QuizID    uuid.UUID               `json:"quiz_id"`
	UserID    uuid.UUID               `json:"user_id"`
	Score     pgtype.Int4             `json:"score"` // Use pgtype for nullable int
	StartTime time.Time               `json:"start_time"`
	EndTime   pgtype.Timestamptz      `json:"end_time"` // Use pgtype for nullable timestamp
	Answers   []ResponseAttemptAnswer `json:"answers"`
}

// HandleGetQuizAttempt retrieves details and saved answers for a specific attempt.
func (h *Handler) HandleGetQuizAttempt(c *gin.Context) {
	ctx := c.Request.Context()
	attemptIDStr := c.Param("attemptId")

	// 1. Get User ID from context
	userIDValue, exists := c.Get("userID")
	if !exists {
		log.Printf("ERROR: User ID not found in context for getting quiz attempt %s", attemptIDStr)
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	userID, ok := userIDValue.(uuid.UUID)
	if !ok {
		log.Printf("ERROR: User ID in context is not UUID for getting quiz attempt %s", attemptIDStr)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Internal server error: Invalid user ID type"})
		return
	}

	// 2. Parse Attempt ID
	attemptID, err := uuid.Parse(attemptIDStr)
	if err != nil {
		log.Printf("ERROR: Invalid Attempt ID format '%s': %v", attemptIDStr, err)
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid Attempt ID format"})
		return
	}
	log.Printf("INFO: Handling request to get attempt ID: %s for user ID: %s", attemptID, userID)

	// 3. Fetch Attempt details and Verify Ownership
	dbAttempt, err := h.DB.Queries.GetQuizAttempt(ctx, attemptID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("WARN: Quiz attempt not found: %s", attemptID)
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "Quiz attempt not found"})
		} else {
			log.Printf("ERROR: Failed to get quiz attempt %s: %v", attemptID, err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve quiz attempt"})
		}
		return
	}

	// Verify ownership
	if dbAttempt.UserID != userID {
		log.Printf("WARN: User %s attempted to access quiz attempt %s owned by user %s", userID, attemptID, dbAttempt.UserID)
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "You do not have permission to access this quiz attempt"})
		return
	}

	// 4. Fetch Saved Answers for the Attempt
	dbAnswers, err := h.DB.Queries.ListAttemptAnswersByAttempt(ctx, attemptID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) { // It's okay if there are no answers yet
		log.Printf("ERROR: Failed to get answers for attempt %s: %v", attemptID, err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve attempt answers"})
		return
	}
	if dbAnswers == nil {
		dbAnswers = []db.AttemptAnswer{} // Ensure empty slice, not null
	}

	// 5. Structure the response
	responseAnswers := make([]ResponseAttemptAnswer, len(dbAnswers))
	for i, dbA := range dbAnswers {
		responseAnswers[i] = ResponseAttemptAnswer{
			QuestionID:       dbA.QuestionID,
			SelectedAnswerID: dbA.SelectedAnswerID.Bytes, // Extract UUID bytes from pgtype.UUID
			IsCorrect:        dbA.IsCorrect.Bool,         // Extract bool from pgtype.Bool
		}
	}

	response := ResponseQuizAttempt{
		ID:        dbAttempt.ID,
		QuizID:    dbAttempt.QuizID,
		UserID:    dbAttempt.UserID,
		Score:     dbAttempt.Score,
		StartTime: dbAttempt.StartTime,
		EndTime:   dbAttempt.EndTime,
		Answers:   responseAnswers,
	}

	log.Printf("INFO: Successfully prepared response for quiz attempt %s", attemptID)
	// 6. Return JSON response
	c.JSON(http.StatusOK, response)
}

// SaveAttemptAnswerRequest defines the expected JSON body for saving an answer
type SaveAttemptAnswerRequest struct {
	QuestionID       uuid.UUID `json:"questionId" binding:"required"`
	SelectedAnswerID uuid.UUID `json:"selectedAnswerId" binding:"required"`
}

// HandleSaveAttemptAnswer saves or updates a user's answer for a specific question in an attempt.
func (h *Handler) HandleSaveAttemptAnswer(c *gin.Context) {
	ctx := c.Request.Context()
	attemptIDStr := c.Param("attemptId")

	// 1. Get User ID from context
	userIDValue, exists := c.Get("userID")
	if !exists {
		log.Printf("ERROR: User ID not found in context for saving answer to attempt %s", attemptIDStr)
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	userID, ok := userIDValue.(uuid.UUID)
	if !ok {
		log.Printf("ERROR: User ID in context is not UUID for saving answer to attempt %s", attemptIDStr)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Internal server error: Invalid user ID type"})
		return
	}

	// 2. Parse Attempt ID
	attemptID, err := uuid.Parse(attemptIDStr)
	if err != nil {
		log.Printf("ERROR: Invalid Attempt ID format '%s' for saving answer: %v", attemptIDStr, err)
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid Attempt ID format"})
		return
	}

	// 3. Bind JSON Request Body
	var req SaveAttemptAnswerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("ERROR: Invalid request body for saving answer to attempt %s: %v", attemptID, err)
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid request body: %v", err)})
		return
	}
	log.Printf("INFO: Handling request to save answer (Q: %s, A: %s) for attempt ID: %s by user ID: %s", req.QuestionID, req.SelectedAnswerID, attemptID, userID)

	// 4. Verify Attempt Ownership and Status (Attempt must exist and belong to user)
	dbAttempt, err := h.DB.Queries.GetQuizAttempt(ctx, attemptID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("WARN: Quiz attempt not found when saving answer: %s", attemptID)
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "Quiz attempt not found"})
		} else {
			log.Printf("ERROR: Failed to get quiz attempt %s when saving answer: %v", attemptID, err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve quiz attempt"})
		}
		return
	}
	if dbAttempt.UserID != userID {
		log.Printf("WARN: User %s attempted to save answer to attempt %s owned by user %s", userID, attemptID, dbAttempt.UserID)
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "You do not have permission to modify this quiz attempt"})
		return
	}
	// Optional: Check if attempt is already finished (dbAttempt.EndTime.Valid)
	if dbAttempt.EndTime.Valid {
		log.Printf("WARN: User %s attempted to save answer to already finished attempt %s", userID, attemptID)
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "This quiz attempt has already been finished"})
		return
	}

	// 5. Check if the selected answer is correct
	isCorrect, err := h.DB.Queries.GetAnswerCorrectness(ctx, req.SelectedAnswerID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("ERROR: Selected answer ID %s not found when saving answer for attempt %s", req.SelectedAnswerID, attemptID)
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid selected answer ID"})
		} else {
			log.Printf("ERROR: Failed to check answer correctness for answer %s, attempt %s: %v", req.SelectedAnswerID, attemptID, err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify answer correctness"})
		}
		return
	}

	// 6. Upsert the Attempt Answer
	upsertParams := db.UpsertAttemptAnswerParams{
		QuizAttemptID:    attemptID,
		QuestionID:       req.QuestionID,
		SelectedAnswerID: pgtype.UUID{Bytes: req.SelectedAnswerID, Valid: true},
		IsCorrect:        pgtype.Bool{Bool: isCorrect, Valid: true},
	}
	_, err = h.DB.Queries.UpsertAttemptAnswer(ctx, upsertParams)
	if err != nil {
		// TODO: Add more specific error handling, e.g., foreign key violation if question_id is invalid for the quiz
		log.Printf("ERROR: Failed to upsert attempt answer for attempt %s, question %s: %v", attemptID, req.QuestionID, err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to save answer"})
		return
	}

	log.Printf("INFO: Successfully saved/updated answer for attempt %s, question %s", attemptID, req.QuestionID)
	// 7. Return Success Response
	c.Status(http.StatusOK) // Or return the saved answer data if needed
}

// HandleFinishQuizAttempt marks an attempt as finished and calculates the score.
func (h *Handler) HandleFinishQuizAttempt(c *gin.Context) {
	ctx := c.Request.Context()
	attemptIDStr := c.Param("attemptId")

	// 1. Get User ID from context
	userIDValue, exists := c.Get("userID")
	if !exists {
		log.Printf("ERROR: User ID not found in context for finishing attempt %s", attemptIDStr)
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	userID, ok := userIDValue.(uuid.UUID)
	if !ok {
		log.Printf("ERROR: User ID in context is not UUID for finishing attempt %s", attemptIDStr)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Internal server error: Invalid user ID type"})
		return
	}

	// Get user details for notifications
	userName := "Unknown User"                              // Default value
	userEmail := ""                                         // Default value
	userProfileValue, profileExists := c.Get("userProfile") // Use context key

	if profileExists {
		profile, profileOk := userProfileValue.(UserProfile)
		if profileOk {
			userName = profile.Name
			userEmail = profile.Email
			if userName == "" {
				userName = "User"
			}
			log.Printf("INFO: Retrieved user profile from context for finish notification: Name=%s, Email=%s", userName, userEmail)
		} else {
			log.Printf("ERROR: Value found for key 'userProfile' in context is not UserProfile during finish. Type: %T. UserID: %s", userProfileValue, userID)
		}
	} else {
		log.Printf("ERROR: User profile key 'userProfile' not found in context for finish notification. UserID: %s", userID)
	}

	// 2. Parse Attempt ID
	attemptID, err := uuid.Parse(attemptIDStr)
	if err != nil {
		log.Printf("ERROR: Invalid Attempt ID format '%s' for finishing: %v", attemptIDStr, err)
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid Attempt ID format"})
		return
	}
	log.Printf("INFO: Handling request to finish attempt ID: %s by user ID: %s", attemptID, userID)

	// 3. Verify Attempt Ownership and Status (Must exist, belong to user, not be finished)
	dbAttempt, err := h.DB.Queries.GetQuizAttempt(ctx, attemptID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("WARN: Quiz attempt not found when finishing: %s", attemptID)
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "Quiz attempt not found"})
		} else {
			log.Printf("ERROR: Failed to get quiz attempt %s when finishing: %v", attemptID, err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve quiz attempt"})
		}
		return
	}
	if dbAttempt.UserID != userID {
		log.Printf("WARN: User %s attempted to finish attempt %s owned by user %s", userID, attemptID, dbAttempt.UserID)
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "You do not have permission to finish this quiz attempt"})
		return
	}
	if dbAttempt.EndTime.Valid {
		log.Printf("WARN: User %s attempted to finish already finished attempt %s", userID, attemptID)
		// Maybe return the existing score instead of an error? For now, return error.
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "This quiz attempt has already been finished"})
		return
	}

	// Fetch Quiz Title for notification
	dbQuiz, quizErr := h.DB.Queries.GetQuizByID(ctx, dbAttempt.QuizID)
	quizTitle := "Unknown Quiz"
	if quizErr == nil {
		quizTitle = dbQuiz.Title
	} else {
		log.Printf("WARN: Could not fetch quiz title for attempt %s notification: %v", attemptID, quizErr)
	}

	// 4. Calculate Score
	score, err := h.DB.Queries.CalculateQuizAttemptScore(ctx, attemptID)
	if err != nil {
		// This shouldn't usually fail if the attempt exists, but handle defensively
		log.Printf("ERROR: Failed to calculate score for attempt %s: %v", attemptID, err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to calculate score"})
		return
	}

	// 5. Update Attempt Record with Score and End Time
	updateParams := db.UpdateQuizAttemptScoreAndEndTimeParams{
		ID:      attemptID,
		Score:   pgtype.Int4{Int32: int32(score), Valid: true},
		EndTime: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	updatedAttempt, err := h.DB.Queries.UpdateQuizAttemptScoreAndEndTime(ctx, updateParams)
	if err != nil {
		log.Printf("ERROR: Failed to update attempt %s with score and end time: %v", attemptID, err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to finalize quiz attempt"})
		return
	}

	log.Printf("INFO: Successfully finished attempt %s for user %s with score %d", attemptID, userID, updatedAttempt.Score.Int32)

	// Log attempt finish activity
	h.logActivity(ctx, userID, db.ActivityActionQuizAttemptFinish,
		db.NullActivityTargetType{ActivityTargetType: db.ActivityTargetTypeQuizAttempt, Valid: true},
		pgtype.UUID{Bytes: updatedAttempt.ID, Valid: true},
		map[string]interface{}{
			"quiz_id": updatedAttempt.QuizID.String(),
			"score":   updatedAttempt.Score.Int32,
		})

	// Send Discord notification for attempt finish
	h.sendDiscordNotification(fmt.Sprintf("🏁 Quiz Attempt Finished: '%s' (Score: %d, AttemptID: %s) by %s (%s)",
		quizTitle, updatedAttempt.Score.Int32, updatedAttempt.ID, userName, userEmail))

	// 6. Return Success Response (e.g., the final score)
	c.JSON(http.StatusOK, gin.H{
		"message": "Quiz attempt finished successfully!",
		"score":   updatedAttempt.Score.Int32,
		// Optionally return the full updated attempt object
		// "attempt": updatedAttempt,
	})
}

// HandleListUserAttempts retrieves a list of all attempts made by the current user, including quiz names.
func (h *Handler) HandleListUserAttempts(c *gin.Context) {
	ctx := c.Request.Context()

	// 1. Get User ID from context
	userIDValue, exists := c.Get("userID")
	if !exists {
		log.Printf("ERROR: User ID not found in context for listing user attempts")
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	userID, ok := userIDValue.(uuid.UUID)
	if !ok {
		log.Printf("ERROR: User ID in context is not UUID for listing user attempts")
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Internal server error: Invalid user ID type"})
		return
	}
	log.Printf("INFO: Handling request to list attempts for user ID: %s", userID)

	// 2. Fetch Attempts from DB using the new query
	attempts, err := h.DB.Queries.ListUserAttemptsWithQuizName(ctx, userID)
	if err != nil {
		// sql.ErrNoRows is not typically returned by List methods in sqlc, it returns an empty slice.
		// Log and return error only for actual database problems.
		log.Printf("ERROR: Failed to list attempts for user %s: %v", userID, err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve attempts"})
		return
	}

	// Handle case where no attempts are found (returns empty slice, not error)
	if attempts == nil {
		attempts = []db.ListUserAttemptsWithQuizNameRow{} // Ensure we return an empty array, not null
	}

	log.Printf("INFO: Found %d attempts for user %s", len(attempts), userID)

	// 3. Return JSON response
	// The db.ListUserAttemptsWithQuizNameRow struct is suitable for the response.
	c.JSON(http.StatusOK, attempts)
}
