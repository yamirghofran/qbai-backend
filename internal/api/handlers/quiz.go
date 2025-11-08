package handlers

import (
	"database/sql" // Added for sql.ErrNoRows
	"errors"       // Import the standard errors package
	"fmt"          // Added for error formatting
	"io"           // Added for file operations
	"log"          // Added for logging errors
	"mime/multipart"
	"net/http"
	"os"
	"time" // Added for response struct timestamps

	"quizbuilderai/internal/db"
	"quizbuilderai/internal/gemini"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"         // Added for user ID
	"github.com/jackc/pgx/v5/pgtype" // Added for pgtype.Text &amp; pgtype.UUID
)

// Define response structures matching frontend/src/types/index.ts
type ResponseOption struct {
	ID          uuid.UUID `json:"id"`
	Text        string    `json:"text"`
	IsCorrect   bool      `json:"is_correct"`
	Explanation *string   `json:"explanation,omitempty"` // Use pointer for optional string
}

type ResponseQuestion struct {
	ID         uuid.UUID        `json:"id"`
	Text       string           `json:"text"`
	TopicTitle *string          `json:"topic_title,omitempty"` // Use pointer for optional string
	Options    []ResponseOption `json:"options"`
}

// ResponseQuizDetail represents the detailed quiz data sent to the frontend, including creator info.
// Note: We use pointers for optional fields to allow null/omitted values in JSON.
type ResponseQuizDetail struct {
	ID             uuid.UUID          `json:"id"`
	Title          string             `json:"title"`
	Description    *string            `json:"description,omitempty"` // Use pointer for optional string
	Visibility     db.QuizVisibility  `json:"visibility"`
	Questions      []ResponseQuestion `json:"questions"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
	CreatorName    *string            `json:"creator_name,omitempty"`    // Add creator name (optional)
	CreatorPicture *string            `json:"creator_picture,omitempty"` // Add creator picture (optional)
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
	startTime := time.Now() // Record start time
	ctx := c.Request.Context()
	// _ = ctx // Mark ctx as used to avoid compiler error, will be used later

	// 1. Get User ID from context (set by AuthRequired middleware)
	userIDValue, exists := c.Get("userID")
	if !exists {
		// Use handleErrorAndNotify
		h.handleErrorAndNotify(c, uuid.Nil, http.StatusUnauthorized, "User ID not found in context for quiz generation", errors.New("user not authenticated"))
		return
	}

	userID, ok := userIDValue.(uuid.UUID)
	if !ok {
		// Use handleErrorAndNotify
		h.handleErrorAndNotify(c, uuid.Nil, http.StatusInternalServerError, "User ID in context is not UUID for quiz generation", errors.New("invalid user ID type in context"))
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
		// Use handleErrorAndNotify
		h.handleErrorAndNotify(c, userID, http.StatusBadRequest, "Failed to parse multipart form", err)
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
			// Use handleErrorAndNotify
			h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, fmt.Sprintf("Failed to open uploaded file %s", fileHeader.Filename), err)
			return // Stop processing on error
		}
		// Ensure file is closed (although saving to temp might make this redundant)
		defer file.Close()

		// Read file content (needed for SaveTempFile)
		fileBytes, err := io.ReadAll(file)
		if err != nil {
			// Use handleErrorAndNotify
			h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, fmt.Sprintf("Failed to read uploaded file %s", fileHeader.Filename), err)
			return
		}

		// Save to temporary location using gemini helper
		// Note: SaveTempFile expects []byte
		tempPath, err := gemini.SaveTempFile(fileBytes, fileHeader.Filename)
		if err != nil {
			// Use handleErrorAndNotify
			h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, fmt.Sprintf("Failed to save temporary file for %s", fileHeader.Filename), err)
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
			// Use handleErrorAndNotify
			h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, fmt.Sprintf("Failed to save temporary transcript file for %s", url), err)
			return
		}
		tempFilePaths = append(tempFilePaths, tempPath) // Add path for deferred cleanup
		log.Printf("INFO: Saved transcript for %s temporarily to %s", url, tempPath)

		// Get file info for size
		fileInfo, err := os.Stat(tempPath)
		if err != nil {
			// Use handleErrorAndNotify
			h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, fmt.Sprintf("Failed to get file info for temporary transcript %s", tempPath), err)
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
		// Use handleErrorAndNotify
		h.handleErrorAndNotify(c, userID, http.StatusBadRequest, "No valid files or video URLs were processed", errors.New("no valid content provided or processed. Please check files and URLs"))
		return
	}

	// 5. Call Gemini to generate the quiz
	log.Printf("INFO: Calling Gemini to process %d documents for user %s", len(documentFiles), userID)
	// Receive token counts from ProcessDocuments
	geminiResponse, promptTokens, candidateTokens, totalTokens, err := h.Gemini.ProcessDocuments(ctx, documentFiles)
	if err != nil {
		// Use handleErrorAndNotify
		h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, "Gemini processing failed", err)
		return
	}

	// Log received token counts (even if quiz generation failed partially)
	log.Printf("INFO: Gemini Token Usage Reported: User=%s, Prompt=%d, Candidates=%d, Total=%d", userID, promptTokens, candidateTokens, totalTokens)

	if geminiResponse == nil || len(geminiResponse.Questions) == 0 {
		// Use handleErrorAndNotify
		h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, "Gemini returned no questions", errors.New("quiz generation resulted in no questions"))
		return
	}

	log.Printf("INFO: Gemini generated quiz titled '%s' with %d questions for user %s", geminiResponse.Title, len(geminiResponse.Questions), userID)

	// 6. Process Gemini Response &amp; DB Insertion (Transaction)
	var createdQuiz db.Quize // Variable to hold the created quiz

	// Start transaction using the connection pool from the DB struct
	tx, err := h.DB.Pool.Begin(ctx)
	if err != nil {
		// Use handleErrorAndNotify
		h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, "Failed to begin database transaction", err)
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
			// Use handleErrorAndNotify
			h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, "Failed to create token transaction record", tokenErr)
			return // Rollback happens via defer
		}

		// Update user's token balance
		_, balanceErr := qtx.UpdateUserTokenBalance(ctx, db.UpdateUserTokenBalanceParams{
			ID:                  userID,
			InputTokensBalance:  promptTokens,    // Amount to decrement input balance by
			OutputTokensBalance: candidateTokens, // Amount to decrement output balance by
		})
		if balanceErr != nil {
			// Use handleErrorAndNotify
			h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, "Failed to update token balance", balanceErr)
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
		// Use handleErrorAndNotify
		h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, "Failed to create quiz record", err)
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
			// Use handleErrorAndNotify
			h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, fmt.Sprintf("Failed to create material record for file %s", fileHeader.Filename), err)
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
			// Use handleErrorAndNotify
			h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, fmt.Sprintf("Failed to link material %s to quiz %s", material.ID, createdQuiz.ID), linkErr)
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
			// Use handleErrorAndNotify
			h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, fmt.Sprintf("Failed to create material record for video %s", url), err)
			return // Rollback happens via defer
		}

		// Link material to quiz
		_, linkErr := qtx.LinkQuizMaterial(ctx, db.LinkQuizMaterialParams{
			QuizID:     createdQuiz.ID,
			MaterialID: material.ID,
		})
		if linkErr != nil {
			// Use handleErrorAndNotify
			h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, fmt.Sprintf("Failed to link video material %s to quiz %s", material.ID, createdQuiz.ID), linkErr)
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
						// Use handleErrorAndNotify
						h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, fmt.Sprintf("Failed to create topic '%s'", topicTitle), err)
						return
					}
					topicID = newTopic.ID
					topicCache[topicTitle] = topicID
					log.Printf("INFO: Created topic '%s' with ID %s for user %s", topicTitle, topicID, userID)
				} else {
					// Other database error
					// Use handleErrorAndNotify
					h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, fmt.Sprintf("Database error checking topic '%s'", topicTitle), err)
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
			// Use handleErrorAndNotify
			h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, fmt.Sprintf("Failed to create question for quiz %s", createdQuiz.ID), err)
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
				// Use handleErrorAndNotify
				h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, fmt.Sprintf("Failed to create answer for question %s", dbQuestion.ID), err)
				return
			}
		}

		// Validate that exactly one correct answer was provided by Gemini
		if correctAnswerCount != 1 {
			// Log the problematic question structure for debugging
			// Use handleErrorAndNotify
			errInvalidAnswers := fmt.Errorf("invalid number of correct answers (%d) for question: %s", correctAnswerCount, geminiQuestion.Text)
			log.Printf("ERROR: %v. Rolling back. Question Details: %+v", errInvalidAnswers, geminiQuestion)
			h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, "Invalid question data received from AI", errInvalidAnswers)
			return
		}
	}

	// Commit the transaction
	err = tx.Commit(ctx)
	if err != nil {
		// Use handleErrorAndNotify
		h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, fmt.Sprintf("Failed to commit transaction for quiz %s", createdQuiz.ID), err)
		return
	}

	log.Printf("INFO: Successfully created quiz %s with %d questions for user %s", createdQuiz.ID, len(geminiResponse.Questions), userID)

	// Calculate duration
	duration := time.Since(startTime)
	log.Printf("INFO: Quiz %s generation took %s", createdQuiz.ID, duration)

	// Log quiz creation activity
	h.logActivity(ctx, userID, db.ActivityActionQuizCreate,
		db.NullActivityTargetType{ActivityTargetType: db.ActivityTargetTypeQuiz, Valid: true},
		pgtype.UUID{Bytes: createdQuiz.ID, Valid: true},
		map[string]interface{}{
			"title":            createdQuiz.Title,
			"question_count":   len(geminiResponse.Questions),
			"material_count":   processedMaterialCount,
			"prompt_tokens":    promptTokens,            // Add token info
			"candidate_tokens": candidateTokens,         // Add token info
			"total_tokens":     totalTokens,             // Add token info
			"duration_ms":      duration.Milliseconds(), // Add duration
		}) // Add token and duration details to the log

	// Send Discord notification for quiz creation using Embed
	quizEmbed := DiscordEmbed{
		Title: "📝 Quiz Created",
		Color: 0x4CAF50, // Green color
		Fields: []DiscordEmbedField{
			{Name: "Title", Value: createdQuiz.Title, Inline: true},
			{Name: "Questions", Value: fmt.Sprintf("%d", len(geminiResponse.Questions)), Inline: true},
			{Name: "Materials", Value: fmt.Sprintf("%d", processedMaterialCount), Inline: true},
			{Name: "Tokens Used", Value: fmt.Sprintf("%d", totalTokens), Inline: true},
			{Name: "Time Taken", Value: fmt.Sprintf("%.2fs", duration.Seconds()), Inline: true},
			{Name: "Created By", Value: fmt.Sprintf("%s (%s)", userName, userEmail), Inline: false},
			{Name: "Quiz ID", Value: fmt.Sprintf("`%s`", createdQuiz.ID.String()), Inline: false},
		},
		Timestamp: time.Now().Format(time.RFC3339),
	}
	h.sendDiscordNotification(quizEmbed)

	// 7. Return Response
	c.JSON(http.StatusOK, gin.H{
		"message": "Quiz generated successfully!",
		"quizId":  createdQuiz.ID.String(), // Return the new quiz ID as a string
	})
}

// HandleGetQuiz retrieves a specific quiz by its ID, including its questions, answers, and creator info.
func (h *Handler) HandleGetQuiz(c *gin.Context) {
	ctx := c.Request.Context()
	quizIDStr := c.Param("quizId")

	// 1. Parse UUID
	quizID, err := uuid.Parse(quizIDStr)
	if err != nil {
		// Use handleErrorAndNotify (userID is not available here, pass Nil)
		h.handleErrorAndNotify(c, uuid.Nil, http.StatusBadRequest, fmt.Sprintf("Invalid Quiz ID format '%s'", quizIDStr), err)
		return
	}
	log.Printf("INFO: Handling request for quiz ID: %s", quizID)

	// 2. Fetch Quiz details including creator info
	// GetQuizByID now returns db.GetQuizByIDRow which includes creator_name and creator_picture
	dbQuizData, err := h.DB.Queries.GetQuizByID(ctx, quizID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Use handleErrorAndNotify (userID is not available here, pass Nil)
			h.handleErrorAndNotify(c, uuid.Nil, http.StatusNotFound, fmt.Sprintf("Quiz not found: %s", quizID), err)
		} else {
			// Use handleErrorAndNotify (userID is not available here, pass Nil)
			h.handleErrorAndNotify(c, uuid.Nil, http.StatusInternalServerError, fmt.Sprintf("Failed to get quiz %s", quizID), err)
		}
		return
	}

	// 3. Fetch Questions for the Quiz
	dbQuestions, err := h.DB.Queries.ListQuestionsByQuizID(ctx, quizID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) { // It's okay if a quiz has no questions yet
		// Use handleErrorAndNotify (userID is not available here, pass Nil)
		h.handleErrorAndNotify(c, uuid.Nil, http.StatusInternalServerError, fmt.Sprintf("Failed to get questions for quiz %s", quizID), err)
		return
	}
	log.Printf("INFO: Found %d questions for quiz %s", len(dbQuestions), quizID)

	// 4. Fetch Answers for each Question and build response questions
	responseQuestions := make([]ResponseQuestion, 0, len(dbQuestions))
	for _, dbQ := range dbQuestions {
		dbAnswers, err := h.DB.Queries.ListAnswersByQuestionID(ctx, dbQ.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			log.Printf("WARN: Failed to get answers for question %s (quiz %s): %v", dbQ.ID, quizID, err)
			// Continue processing other questions, this one will have no options
		}

		// Map db.Answer to ResponseOption
		responseOptions := make([]ResponseOption, 0, len(dbAnswers))
		for _, dbA := range dbAnswers {
			// Handle nullable Explanation
			var explanation *string
			if dbA.Explanation.Valid {
				explanationStr := dbA.Explanation.String // Assign to temp variable
				explanation = &explanationStr
			}
			responseOptions = append(responseOptions, ResponseOption{
				ID:          dbA.ID,
				Text:        dbA.Answer, // Use 'Answer' field from db.Answer
				IsCorrect:   dbA.IsCorrect,
				Explanation: explanation, // Use the *string variable
			})
		}

		// Handle nullable TopicTitle
		var topicTitle *string
		if dbQ.TopicTitle.Valid {
			topicTitleStr := dbQ.TopicTitle.String // Assign to temp variable
			topicTitle = &topicTitleStr
		}

		// Map db.Question to ResponseQuestion
		responseQuestions = append(responseQuestions, ResponseQuestion{
			ID:         dbQ.ID,
			Text:       dbQ.Question, // Use 'Question' field from db.Question
			TopicTitle: topicTitle,   // Use the *string variable
			Options:    responseOptions,
		})
	}

	// 5. Structure the final response using ResponseQuizDetail
	// Handle nullable Description, CreatorName, CreatorPicture
	var description *string
	if dbQuizData.Description.Valid {
		descStr := dbQuizData.Description.String
		description = &descStr
	}
	var creatorName *string
	if dbQuizData.CreatorName.Valid {
		nameStr := dbQuizData.CreatorName.String
		creatorName = &nameStr
	}
	var creatorPicture *string
	if dbQuizData.CreatorPicture.Valid {
		picStr := dbQuizData.CreatorPicture.String
		creatorPicture = &picStr
	}

	response := ResponseQuizDetail{
		ID:             dbQuizData.ID,
		Title:          dbQuizData.Title,
		Description:    description,
		Visibility:     dbQuizData.Visibility,
		CreatedAt:      dbQuizData.CreatedAt,
		UpdatedAt:      dbQuizData.UpdatedAt,
		CreatorName:    creatorName,
		CreatorPicture: creatorPicture,
		Questions:      responseQuestions, // Assign the processed questions
	}

	log.Printf("INFO: Successfully prepared detailed response for quiz %s", quizID)
	// 6. Return JSON response
	c.JSON(http.StatusOK, response)
}

// HandleListUserQuizzes retrieves all quizzes created by the currently authenticated user.
func (h *Handler) HandleListUserQuizzes(c *gin.Context) {
	ctx := c.Request.Context()

	// 1. Get User ID from context (set by AuthRequired middleware)
	userIDValue, exists := c.Get("userID")
	if !exists {
		// Use handleErrorAndNotify
		h.handleErrorAndNotify(c, uuid.Nil, http.StatusUnauthorized, "User ID not found in context for listing quizzes", errors.New("user not authenticated"))
		return
	}

	userID, ok := userIDValue.(uuid.UUID)
	if !ok {
		// Use handleErrorAndNotify
		h.handleErrorAndNotify(c, uuid.Nil, http.StatusInternalServerError, "User ID in context is not UUID for listing quizzes", errors.New("invalid user ID type in context"))
		return
	}
	log.Printf("INFO: Handling request to list quizzes for user ID: %s", userID)

	// 2. Fetch Quizzes from DB
	quizzes, err := h.DB.Queries.ListQuizzesByCreator(ctx, pgtype.UUID{Bytes: userID, Valid: true})
	if err != nil {
		// Use handleErrorAndNotify
		// It's not an error if the user simply hasn't created any quizzes yet.
		// sql.ErrNoRows is not typically returned by List methods in sqlc, it returns an empty slice.
		// So, we only log and return error for actual database problems.
		h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, fmt.Sprintf("Failed to list quizzes for user %s", userID), err)
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
	var userID uuid.UUID // Declare userID
	userIDValue, exists := c.Get("userID")
	if !exists {
		// Use handleErrorAndNotify
		h.handleErrorAndNotify(c, uuid.Nil, http.StatusUnauthorized, fmt.Sprintf("User ID not found in context for deleting quiz %s", quizIDStr), errors.New("user not authenticated"))
		return
	}
	var ok bool
	userID, ok = userIDValue.(uuid.UUID) // Assign to declared userID
	if !ok {
		// Use handleErrorAndNotify
		h.handleErrorAndNotify(c, uuid.Nil, http.StatusInternalServerError, fmt.Sprintf("User ID in context is not UUID for deleting quiz %s", quizIDStr), errors.New("invalid user ID type in context"))
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
		// Use handleErrorAndNotify
		h.handleErrorAndNotify(c, userID, http.StatusBadRequest, fmt.Sprintf("Invalid Quiz ID format '%s' for deletion", quizIDStr), err)
		return
	}
	log.Printf("INFO: Handling request to delete quiz ID: %s for user ID: %s", quizID, userID)

	// 3. Verify Quiz Ownership (Fetch the quiz first)
	dbQuiz, err := h.DB.Queries.GetQuizByID(ctx, quizID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Use handleErrorAndNotify
			h.handleErrorAndNotify(c, userID, http.StatusNotFound, fmt.Sprintf("Quiz not found for deletion: %s", quizID), err)
		} else {
			// Use handleErrorAndNotify
			h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, fmt.Sprintf("Failed to get quiz %s for ownership check during deletion", quizID), err)
		}
		return
	}

	// Check if the CreatorID (which is pgtype.UUID) matches the userID from context
	if !dbQuiz.CreatorID.Valid || dbQuiz.CreatorID.Bytes != userID {
		// Use handleErrorAndNotify
		h.handleErrorAndNotify(c, userID, http.StatusForbidden, fmt.Sprintf("User %s attempted to delete quiz %s owned by %s", userID, quizID, dbQuiz.CreatorID.Bytes), errors.New("you do not have permission to delete this quiz"))
		return
	}

	// 4. Delete the Quiz using the existing query
	err = h.DB.Queries.DeleteQuiz(ctx, quizID)
	if err != nil {
		// Use handleErrorAndNotify
		h.handleErrorAndNotify(c, userID, http.StatusInternalServerError, fmt.Sprintf("Failed to delete quiz %s", quizID), err)
		return
	}

	log.Printf("INFO: Successfully deleted quiz %s by user %s", quizID, userID)

	// Log quiz deletion activity
	h.logActivity(ctx, userID, db.ActivityActionQuizDelete,
		db.NullActivityTargetType{ActivityTargetType: db.ActivityTargetTypeQuiz, Valid: true},
		pgtype.UUID{Bytes: quizID, Valid: true},
		map[string]interface{}{"title": dbQuiz.Title}) // Include title from the fetched quiz

	// Send Discord notification for quiz deletion using Embed
	deleteEmbed := DiscordEmbed{
		Title: "🗑️ Quiz Deleted",
		Color: 0xF44336, // Red color
		Fields: []DiscordEmbedField{
			{Name: "Title", Value: dbQuiz.Title, Inline: true},
			{Name: "Quiz ID", Value: fmt.Sprintf("`%s`", quizID.String()), Inline: true},
			{Name: "Deleted By", Value: fmt.Sprintf("%s (%s)", userName, userEmail), Inline: false},
		},
		Timestamp: time.Now().Format(time.RFC3339),
	}
	h.sendDiscordNotification(deleteEmbed)

	// 5. Return Success Response
	c.Status(http.StatusNoContent) // 204 No Content is standard for successful DELETE
}
