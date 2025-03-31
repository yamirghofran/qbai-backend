package api

import (
	"github.com/gin-gonic/gin"
)

// SetupRoutes sets up the API routes
func SetupRoutes(router *gin.Engine, handler *Handler) {
	// Apply CORS middleware
	router.Use(CORSMiddleware())

	// --- Public Auth Routes ---
	router.GET("/login", handler.HandleGoogleLogin)                   // Initiates OAuth flow
	router.GET("/auth/google/callback", handler.HandleGoogleCallback) // Handles the redirect from Google

	// --- API Routes ---
	api := router.Group("/api")
	{
		// Public API routes (e.g., status check)
		api.GET("/auth/status", handler.HandleAuthStatus) // Check if user is logged in

		// Protected API routes - Apply AuthRequired middleware
		authorized := api.Group("/")
		authorized.Use(AuthRequired())
		{
			// Routes that require authentication go here
			authorized.GET("/user/profile", handler.HandleUserProfile) // Get current user's profile
			authorized.POST("/logout", handler.HandleLogout)           // Log the user out

			// Add other protected application routes below
			authorized.POST("/quizzes/generate", handler.HandleGenerateQuiz) // Generate quiz from uploaded content
			authorized.GET("/quizzes/:quizId", handler.HandleGetQuiz)        // Get a specific quiz by ID
			authorized.GET("/quizzes", handler.HandleListUserQuizzes)        // Get quizzes created by the current user
			authorized.DELETE("/quizzes/:quizId", handler.HandleDeleteQuiz)  // Delete a specific quiz

			// --- Quiz Attempt Routes ---
			authorized.POST("/quizzes/:quizId/attempts", handler.HandleCreateQuizAttempt)    // Start a new attempt for a quiz
			authorized.GET("/attempts/:attemptId", handler.HandleGetQuizAttempt)             // Get details of a specific attempt (including saved answers)
			authorized.POST("/attempts/:attemptId/answers", handler.HandleSaveAttemptAnswer) // Save/update an answer for an attempt
			authorized.POST("/attempts/:attemptId/finish", handler.HandleFinishQuizAttempt)  // Mark an attempt as finished and calculate score
			authorized.GET("/attempts", handler.HandleListUserAttempts)                      // List all attempts for the current user

			// Example:
			// authorized.POST("/quizzes", handler.HandleCreateQuiz) // Create quiz manually (if needed)
			// authorized.GET("/topics", handler.HandleGetTopics)
		}
	}

	// --- Optional: Top-level protected routes (if needed) ---
	// Example: If /user/profile was not under /api
	// protected := router.Group("/") // Or specific path like "/user"
	// protected.Use(AuthRequired())
	// {
	//  protected.GET("/profile", handler.HandleUserProfile) // Example: /profile instead of /api/user/profile
	//  protected.POST("/logout", handler.HandleLogout) // Example: /logout instead of /api/logout
	// }
}
