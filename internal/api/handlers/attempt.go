package handlers

import (
	"database/sql" // Added for sql.ErrNoRows
	"errors"       // Import the standard errors package
	"fmt"          // Added for error formatting
	"log"          // Added for logging errors
	"net/http"
	"time" // Added for time.Now()

	"quizbuilderai/internal/db"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"         // Added for user ID
	"github.com/jackc/pgx/v5/pgtype" // Added for pgtype types
)

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
