package handlers

import (
	"context"
	"crypto/rand"
	"database/sql" // Added for sql.ErrNoRows
	"encoding/base64"
	"errors" // Import the standard errors package
	"fmt"    // Added for error formatting
	"log"    // Added for logging errors
	"net/http"
	"os"

	"quizbuilderai/internal/db"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"         // Added for user ID
	"github.com/jackc/pgx/v5/pgtype" // Added for pgtype.Text &amp; pgtype.UUID
	"golang.org/x/oauth2"
	oauth2api "google.golang.org/api/oauth2/v2"
	"google.golang.org/api/option"
)

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

	session.Set(OauthStateSessionKey, oauthStateString) // Use capitalized constant
	err = session.Save()
	if err != nil {
		log.Printf("ERROR: Failed to save session: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save session"})
		return
	}
	log.Printf("DEBUG: Saved session state '%s' for session ID %s", oauthStateString, session.ID()) // Added logging

	url := h.OauthConfig.AuthCodeURL(oauthStateString, oauth2.AccessTypeOffline)
	c.Redirect(http.StatusTemporaryRedirect, url)
}

// HandleGoogleCallback: Handles the redirect back from Google.
func (h *Handler) HandleGoogleCallback(c *gin.Context) {
	session := sessions.Default(c)
	retrievedState := session.Get(OauthStateSessionKey) // Use capitalized constant
	originalState := c.Query("state")
	log.Printf("DEBUG: Callback received. Session ID: %s, Query state: '%s', Retrieved session state: %v", session.ID(), originalState, retrievedState) // Added logging

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

	session.Set(ProfileSessionKey, profile) // Store the potentially updated profile & use capitalized constant
	session.Delete(OauthStateSessionKey)    // Use capitalized constant

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
	profileData := session.Get(ProfileSessionKey) // Use capitalized constant

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

// HandleAuthStatus checks if a user is currently authenticated via session.
func (h *Handler) HandleAuthStatus(c *gin.Context) {
	session := sessions.Default(c)
	profileData := session.Get(ProfileSessionKey) // Use capitalized constant

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
