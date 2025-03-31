package api

import (
	"log"      // Added for logging
	"net/http" // Added for http status codes
	"os"       // Import os package to read environment variables
	"strings"  // Import strings package for TrimSuffix

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid" // Added for uuid.Nil check
)

// CORSMiddleware adds CORS headers to allow cross-origin requests
func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Read the allowed origin from environment variable
		frontendURL := os.Getenv("FRONTEND_URL")
		if frontendURL == "" {
			// Fallback or log an error if not set, depending on requirements
			// For development, you might fallback to a common dev URL,
			// but in production, it should likely be a fatal error if not set.
			// Using "*" here would defeat the purpose of the fix.
			// Let's fallback to the typical Vite dev server URL for now.
			frontendURL = "http://localhost:5173" // Or log fatal
		}
		// Trim trailing slash if present before setting the header
		c.Writer.Header().Set("Access-Control-Allow-Origin", strings.TrimSuffix(frontendURL, "/"))
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// AuthRequired is middleware to ensure the user is authenticated.
// It checks for the presence and validity of the user profile in the session
// and adds the internal DatabaseID (UUID) to the context.
func AuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		session := sessions.Default(c)
		profileValue := session.Get(profileSessionKey)

		profileData, ok := profileValue.(UserProfile)
		// Check if profile exists in session AND if the DatabaseID is valid (not Nil)
		if !ok || profileValue == nil || profileData.DatabaseID == uuid.Nil {
			log.Printf("WARN: AuthRequired failed - profile not found, invalid type, or missing DatabaseID in session.")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authentication required or session invalid"})
			return
		}

		// Set the INTERNAL DATABASE USER ID (which is a uuid.UUID) into the context
		c.Set("userID", profileData.DatabaseID)
		// Optionally set other useful info
		c.Set("userProfile", profileData) // Keep original profile if needed

		log.Printf("INFO: AuthRequired successful for user %s (DB ID: %s)", profileData.Email, profileData.DatabaseID)
		c.Next()
	}
}
