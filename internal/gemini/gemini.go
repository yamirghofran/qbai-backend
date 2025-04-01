package gemini

//TODO - UPDATE TO MATCH NEW SCHEMA

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log" // Added for logging
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"quizbuilderai/internal/models"

	"github.com/google/generative-ai-go/genai"
	"github.com/google/uuid"
	"google.golang.org/api/option"
)

// QuizPrompt is the prompt used to generate quizzes
const QuizPrompt = `Generate a comprehensive multiple-choice quiz based on the content of these documents. Follow these requirements exactly:

1. Create a descriptive title for the quiz that accurately reflects the main subject matter of the documents
2. Create questions covering ALL main topics and subtopics in the documents, ensuring no significant concept is omitted. Include the topic for each question (so that questions can be grouped by topic later.)
3. Include a balanced distribution of question types:
   - Basic factual recall questions
   - Comprehension questions that require understanding concepts
   - Application/analysis questions that require:
     * Applying principles to new scenarios
     * Analyzing relationships between concepts
     * Connecting ideas across different sections
   - Synthesis/evaluation questions that require:
     * Evaluating implications or consequences of key ideas
     * Comparing competing perspectives or approaches
     * Predicting outcomes based on document principles
     * Identifying unstated assumptions underlying concepts
4. For analytical questions, prioritize second and third-order thinking by asking about:
   - "What would happen if..." scenarios
   - Underlying mechanisms or reasons behind facts
   - How concepts interact in complex systems
   - Potential exceptions or limitations to stated principles
5. Each question must have exactly 4 options with exactly one correct answer
6. For EACH answer option:
   - Provide a concise "explanation" field detailing WHY the option is correct OR incorrect based on the source documents. Don't state "This is incorrect/correct". Just say the explanation. e.g."Gravity was discovered by Isaac Newton"
   - Make incorrect options (distractors) highly plausible by using common misconceptions or partial understandings.
   - Ensure all options have approximately the same length and level of detail.
   - Maintain consistent grammar, style, and tone across all options.
   - Avoid obvious wrong answers or "joke" options.

Format your response as a JSON object with the following structure:
{
  "title": "Descriptive Quiz Title Based on Document Content",
  "questions": [
    {
      "text": "Question text here?",
      "topic": "the topic this question is about.",
      "options": [
        {"text": "Option A", "is_correct": false, "explanation": "Explanation why A is incorrect."},
        {"text": "Option B", "is_correct": true, "explanation": "Explanation why B is correct."},
        {"text": "Option C", "is_correct": false, "explanation": "Explanation why C is incorrect."},
        {"text": "Option D", "is_correct": false, "explanation": "Explanation why D is incorrect."}
      ]
    },
    ...more questions...
  ]
}
`

const (
	// MaxInlineSize is the maximum size for inline PDF data (20MB)
	MaxInlineSize = 20 * 1024 * 1024
	// ModelName is the Gemini model to use
	ModelName = "gemini-2.0-flash"
)

// Client wraps the Gemini client
type Client struct {
	client *genai.Client
	model  *genai.GenerativeModel
}

// NewClient creates a new Gemini client
func NewClient() (*Client, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY environment variable not set")
	}

	client, err := genai.NewClient(context.Background(), option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	model := client.GenerativeModel(ModelName)
	model.ResponseMIMEType = "application/json"

	return &Client{
		client: client,
		model:  model,
	}, nil
}

// Close closes the Gemini client
func (c *Client) Close() {
	c.client.Close()
}

// ProcessDocuments processes multiple document files and generates a quiz
// It now processes files in chunks concurrently.
func (c *Client) ProcessDocuments(ctx context.Context, files []DocumentFile) (*models.GeminiQuizResponse, error) {
	// Add a timeout to the context
	// Increased overall timeout from 10 to 20 minutes
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	// Define the number of concurrent workers and the chunk size
	numWorkers := 6 // Increased from 4 to 6
	chunkSize := 1  // Reduced from 2 to 1

	// Create channels for tasks, results, and errors
	fileChunks := make(chan []DocumentFile, (len(files)+chunkSize-1)/chunkSize) // buffered channel
	results := make(chan *models.GeminiQuizResponse, len(files)/chunkSize+1)    // buffered channel
	errChan := make(chan error, len(files)/chunkSize+1)                         // buffered channel
	var wg sync.WaitGroup

	// Split files into chunks and send them to the fileChunks channel
	for i := 0; i < len(files); i += chunkSize {
		end := i + chunkSize
		if end > len(files) {
			end = len(files)
		}
		fileChunks <- files[i:end]
	}
	close(fileChunks) // close the channel after sending all chunks

	// Launch worker goroutines
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for chunk := range fileChunks {
				// Process each chunk of files
				quizResponse, err := c.processChunk(ctx, chunk)
				if err != nil {
					errChan <- fmt.Errorf("failed to process chunk: %w", err)
					return // Exit worker on first error
				}
				results <- quizResponse // Send result to results channel
			}
		}()
	}

	// Close results channel when all workers are done
	go func() {
		wg.Wait()
		close(results)
		close(errChan) // close error channel as well
	}()

	// Collect results and errors
	var combinedQuizResponse *models.GeminiQuizResponse
	var titles []string

	for result := range results {
		if result == nil {
			continue
		}

		// Collect titles for later processing
		if result.Title != "" {
			titles = append(titles, result.Title)
		}

		if combinedQuizResponse == nil {
			combinedQuizResponse = result
		} else {
			combinedQuizResponse.Questions = append(combinedQuizResponse.Questions, result.Questions...)
		}
	}

	// Check for errors
	if err := <-errChan; err != nil {
		return nil, err // Return the first error encountered
	}

	// If we have multiple titles, generate a combined title
	if len(titles) > 1 && combinedQuizResponse != nil {
		// Use the first title as the base, or generate a new one if needed
		if combinedQuizResponse.Title == "" && len(titles) > 0 {
			combinedQuizResponse.Title = titles[0]
		}
	}

	// If we still don't have a title, create a generic one
	if combinedQuizResponse != nil && combinedQuizResponse.Title == "" {
		combinedQuizResponse.Title = fmt.Sprintf("Quiz Generated on %s", time.Now().Format("January 2, 2006"))
	}

	return combinedQuizResponse, nil
}

// processChunk processes a chunk of document files and generates a quiz response.
func (c *Client) processChunk(ctx context.Context, files []DocumentFile) (*models.GeminiQuizResponse, error) {
	// Check if we should use the file API
	totalSize := int64(0)
	for _, file := range files {
		totalSize += file.Size
	}

	// If we have multiple files that together exceed half the max inline size, process them individually
	if len(files) > 1 && totalSize > MaxInlineSize/2 {
		return c.processFilesIndividually(ctx, files)
	}

	if totalSize > MaxInlineSize {
		return c.processWithFileAPI(ctx, files)
	}

	return c.processInline(ctx, files)
}

// processFilesIndividually processes files in small batches and combines the results
func (c *Client) processFilesIndividually(ctx context.Context, files []DocumentFile) (*models.GeminiQuizResponse, error) {
	// Group files into batches based on size
	batches := createFileBatches(files, MaxInlineSize/4) // Use 1/4 of max size as batch threshold

	// Create a worker pool with limited concurrency
	maxConcurrent := 15 // Limit concurrent requests to Gemini
	sem := make(chan struct{}, maxConcurrent)

	// Create channels for results and errors
	resultCh := make(chan *models.GeminiQuizResponse, len(batches))
	errCh := make(chan error, len(batches))

	var wg sync.WaitGroup

	// Process each batch concurrently but with limited parallelism
	for i, batch := range batches {
		wg.Add(1)
		go func(batchNum int, batchFiles []DocumentFile) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			// Create a sub-context with timeout for this batch
			// Increased batch timeout from 3 to 15 minutes
			batchCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()

			// Process this batch of files
			quizResponse, err := c.processChunk(batchCtx, batchFiles)
			if err != nil {
				fileNames := make([]string, len(batchFiles))
				for i, f := range batchFiles {
					fileNames[i] = f.Name
				}
				errCh <- fmt.Errorf("failed to process batch %d (%s): %w",
					batchNum, strings.Join(fileNames, ", "), err)
				return
			}

			resultCh <- quizResponse
		}(i, batch)
	}

	// Wait for all goroutines to complete
	go func() {
		wg.Wait()
		close(resultCh)
		close(errCh)
	}()

	// Collect results and errors
	var allQuestions []models.GeminiQuestion
	var errs []string

	// Process results
	for result := range resultCh {
		if result != nil && len(result.Questions) > 0 {
			// Take a subset of questions from each batch to avoid overwhelming responses
			maxQuestionsPerBatch := 40
			if len(result.Questions) > maxQuestionsPerBatch {
				result.Questions = result.Questions[:maxQuestionsPerBatch]
			}

			allQuestions = append(allQuestions, result.Questions...)
		}
	}

	// Process errors
	for err := range errCh {
		if err != nil {
			errs = append(errs, err.Error())
		}
	}

	// If any errors occurred during batch processing, return an error immediately.
	// This prevents returning partial results if some batches timed out or failed.
	if len(errs) > 0 {
		return nil, fmt.Errorf("failed to process one or more batches: %s", strings.Join(errs, "; "))
	}

	if len(allQuestions) == 0 {
		return nil, fmt.Errorf("no questions generated from any files")
	}

	// Shuffle questions to mix topics from different files
	rand.Shuffle(len(allQuestions), func(i, j int) {
		allQuestions[i], allQuestions[j] = allQuestions[j], allQuestions[i]
	})

	// Limit total questions to a reasonable number
	maxTotalQuestions := 100
	if len(allQuestions) > maxTotalQuestions {
		allQuestions = allQuestions[:maxTotalQuestions]
	}

	return &models.GeminiQuizResponse{Questions: allQuestions}, nil
}

// createFileBatches groups files into batches based on size
func createFileBatches(files []DocumentFile, maxBatchSize int64) [][]DocumentFile {
	// Sort files by size (largest first) to optimize batching
	sortedFiles := make([]DocumentFile, len(files))
	copy(sortedFiles, files)
	sort.Slice(sortedFiles, func(i, j int) bool {
		return sortedFiles[i].Size > sortedFiles[j].Size
	})

	var batches [][]DocumentFile
	var currentBatch []DocumentFile
	var currentSize int64

	// Process each file
	for _, file := range sortedFiles {
		// If file is very large, put it in its own batch
		if file.Size > maxBatchSize/2 {
			batches = append(batches, []DocumentFile{file})
			continue
		}

		// If adding this file would exceed the batch size, start a new batch
		if currentSize+file.Size > maxBatchSize || len(currentBatch) >= 3 {
			if len(currentBatch) > 0 {
				batches = append(batches, currentBatch)
				currentBatch = []DocumentFile{}
				currentSize = 0
			}
		}

		// Add file to current batch
		currentBatch = append(currentBatch, file)
		currentSize += file.Size
	}

	// Add the last batch if it's not empty
	if len(currentBatch) > 0 {
		batches = append(batches, currentBatch)
	}

	return batches
}

// processInline processes documents by sending them inline in the request
func (c *Client) processInline(ctx context.Context, files []DocumentFile) (*models.GeminiQuizResponse, error) {
	parts := []genai.Part{}

	// Add prompt text
	parts = append(parts, genai.Text(QuizPrompt))

	// Add document files as blobs
	for _, file := range files {
		data, err := os.ReadFile(file.Path)
		if err != nil {
			return nil, fmt.Errorf("failed to read file %s: %w", file.Name, err)
		}

		// Validate file is not empty
		if len(data) == 0 {
			return nil, fmt.Errorf("file %s is empty", file.Name)
		}

		// Determine MIME type based on file extension
		mimeType := getMimeType(file.Name)

		parts = append(parts, genai.Blob{
			MIMEType: mimeType,
			Data:     data,
		})
	}

	// Ensure we have at least one file
	if len(files) == 0 {
		return nil, fmt.Errorf("no files provided for processing")
	}

	return c.generateQuiz(ctx, parts)
}

// processWithFileAPI processes documents using the Gemini File API
func (c *Client) processWithFileAPI(ctx context.Context, files []DocumentFile) (*models.GeminiQuizResponse, error) {
	// Ensure we have at least one file
	if len(files) == 0 {
		return nil, fmt.Errorf("no files provided for processing")
	}

	var wg sync.WaitGroup
	fileDataCh := make(chan *genai.FileData, len(files))
	errorCh := make(chan error, len(files))

	// Upload files in parallel
	for _, file := range files {
		wg.Add(1)
		go func(file DocumentFile) {
			defer wg.Done()

			// Check if file exists and is not empty
			fileInfo, err := os.Stat(file.Path)
			if err != nil {
				errorCh <- fmt.Errorf("failed to access file %s: %w", file.Name, err)
				return
			}

			if fileInfo.Size() == 0 {
				errorCh <- fmt.Errorf("file %s is empty", file.Name)
				return
			}

			fileData, err := c.client.UploadFileFromPath(ctx, file.Path, nil)
			if err != nil {
				errorCh <- fmt.Errorf("failed to upload file %s: %w", file.Name, err)
				return
			}

			fileDataCh <- &genai.FileData{URI: fileData.URI}
		}(file)
	}

	// Wait for all uploads to complete
	wg.Wait()
	close(fileDataCh)
	close(errorCh)

	// Check for errors
	for err := range errorCh {
		if err != nil {
			return nil, err
		}
	}

	// Collect uploaded files
	var fileDataList []*genai.FileData
	for fileData := range fileDataCh {
		fileDataList = append(fileDataList, fileData)
	}

	// Ensure we have at least one file uploaded
	if len(fileDataList) == 0 {
		return nil, fmt.Errorf("no files were successfully uploaded")
	}

	// Create parts with prompt and file references
	parts := []genai.Part{
		genai.Text(QuizPrompt),
	}

	// Add file references
	for _, fileData := range fileDataList {
		parts = append(parts, fileData)
	}

	// Generate quiz
	quiz, err := c.generateQuiz(ctx, parts)

	// Clean up uploaded files
	for _, fileData := range fileDataList {
		if err := c.client.DeleteFile(ctx, fileData.URI); err != nil {
			fmt.Printf("Warning: failed to delete file %s: %v\n", fileData.URI, err)
		}
	}

	return quiz, err
}

// generateQuiz sends the request to Gemini and parses the response
func (c *Client) generateQuiz(ctx context.Context, parts []genai.Part) (*models.GeminiQuizResponse, error) {
	// Set a longer timeout for the context
	// Increased API call timeout from 5 to 15 minutes
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	// Configure model parameters for more reliable responses
	c.model.SetTemperature(0.2) // Lower temperature for more deterministic output
	c.model.SetTopK(40)
	c.model.SetTopP(0.95)
	c.model.SetMaxOutputTokens(int32(8192)) // Increase max tokens to handle larger responses

	// Try up to 3 times to get a valid response
	var lastErr error
	// Removed unused variables bestResponse and maxQuestions

	for attempts := 0; attempts < 3; attempts++ {
		// Adjust parameters for retry attempts
		if attempts > 0 {
			// Reduce the expected output size on retry
			c.model.SetMaxOutputTokens(int32(4096 - attempts*1000))

			// Add instruction to limit number of questions on retries
			maxQs := 50 - attempts*15 // Progressively reduce question count
			limitedPrompt := fmt.Sprintf("%s\n\nIMPORTANT: Due to size constraints, please limit your response to no more than %d questions.",
				QuizPrompt, maxQs)

			// Replace the prompt part with the limited version
			for i, part := range parts {
				if _, ok := part.(genai.Text); ok {
					parts[i] = genai.Text(limitedPrompt)
					break
				}
			}
		}

		resp, err := c.model.GenerateContent(ctx, parts...)
		if err != nil {
			lastErr = fmt.Errorf("failed to generate content (attempt %d): %w", attempts+1, err)
			time.Sleep(2 * time.Second) // Wait before retrying
			continue
		}

		if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
			lastErr = fmt.Errorf("no content generated (attempt %d)", attempts+1)
			time.Sleep(2 * time.Second)
			continue
		}

		// Extract JSON response
		jsonText := ""
		for _, part := range resp.Candidates[0].Content.Parts {
			if text, ok := part.(genai.Text); ok {
				jsonText += string(text)
			}
		}

		// Try to extract JSON from the response if it's embedded in markdown or other text
		jsonText = extractJSONFromText(jsonText)

		if jsonText == "" {
			lastErr = fmt.Errorf("no JSON content found in response (attempt %d)", attempts+1)
			time.Sleep(2 * time.Second)
			continue
		}

		// Parse JSON response
		var quizResponse models.GeminiQuizResponse
		decoder := json.NewDecoder(strings.NewReader(jsonText))
		decoder.DisallowUnknownFields() // Strict parsing to catch errors

		// Configure the decoder to handle large numbers properly
		decoder.UseNumber()

		if err := decoder.Decode(&quizResponse); err != nil {
			// Log the problematic raw JSON text for debugging EOF errors
			log.Printf("DEBUG: Raw JSON text received (attempt %d) before parse error: %s", attempts+1, jsonText) // Added logging for raw text
			fmt.Printf("Invalid JSON (attempt %d): %s\n", attempts+1, jsonText)

			// Removed partial JSON extraction on decode error.
			// If JSON is invalid, treat it as a failure for this attempt.

			lastErr = fmt.Errorf("failed to parse JSON response (attempt %d): %w. Raw text logged.", attempts+1, err) // Updated error message
			time.Sleep(2 * time.Second)
			continue
		}

		// Validate the response structure
		if len(quizResponse.Questions) == 0 {
			lastErr = fmt.Errorf("quiz response contained no questions (attempt %d)", attempts+1)
			time.Sleep(2 * time.Second)
			continue
		}

		// Limit the number of questions to prevent issues with large responses
		quizResponse = *limitQuizSize(&quizResponse, 200)

		// Success
		return &quizResponse, nil
	}

	// Removed final check for partial response.
	// If all attempts fail, return the last encountered error.

	return nil, fmt.Errorf("failed to generate quiz after multiple attempts: %w", lastErr)
}

// extractValidQuestionsFromPartialJSON attempts to extract valid questions from a partial JSON response
func extractValidQuestionsFromPartialJSON(jsonText string) *models.GeminiQuizResponse {
	// Try to extract the title
	titlePattern := regexp.MustCompile(`"title"(?:\s*):(?:\s*)"([^"]*)"`)
	titleMatch := titlePattern.FindStringSubmatch(jsonText)

	var title string
	if len(titleMatch) > 1 {
		title = titleMatch[1]
	}

	// Try to extract individual questions, now including the topic
	questionPattern := regexp.MustCompile(`\{(?s)(?:\s*)"text"(?:\s*):(?:\s*)"([^"]*)"(?:\s*),(?:\s*)"topic"(?:\s*):(?:\s*)"([^"]*)"(?:\s*),(?:\s*)"options"(?:\s*):(?:\s*)\[(.*?)\](?:\s*)\}`)
	matches := questionPattern.FindAllStringSubmatch(jsonText, -1)

	if len(matches) == 0 {
		return nil
	}

	var validQuestions []models.GeminiQuestion

	for _, match := range matches {
		// Now expect 4 capture groups: full match, text, topic, options
		if len(match) < 4 {
			continue
		}

		questionText := match[1]
		topicText := match[2]   // Extract topic
		optionsText := match[3] // Options are now in the 3rd group

		// Extract options
		optionPattern := regexp.MustCompile(`\{(?s)(?:\s*)"text"(?:\s*):(?:\s*)"([^"]*)"(?:\s*),(?:\s*)"is_correct"(?:\s*):(?:\s*)(true|false)(?:\s*),(?:\s*)"explanation"(?:\s*):(?:\s*)"([^"]*)"(?:\s*)\}`)
		optionMatches := optionPattern.FindAllStringSubmatch(optionsText, -1)

		// Only use questions with exactly 4 options and one correct answer
		if len(optionMatches) != 4 {
			continue
		}

		var options []models.GeminiOption
		correctCount := 0

		for _, optionMatch := range optionMatches {
			// Now expect 4 capture groups: full match, text, is_correct, explanation
			if len(optionMatch) < 4 {
				continue
			}

			optionText := optionMatch[1]
			isCorrect := optionMatch[2] == "true"
			explanationText := optionMatch[3] // Extract explanation

			if isCorrect {
				correctCount++
			}

			options = append(options, models.GeminiOption{
				Text:        optionText,
				IsCorrect:   isCorrect,
				Explanation: explanationText, // Add explanation
			})
		}

		// Only use questions with exactly one correct answer
		if correctCount != 1 || len(options) != 4 {
			continue
		}

		validQuestions = append(validQuestions, models.GeminiQuestion{
			Text:    questionText,
			Topic:   topicText, // Add extracted topic
			Options: options,
		})
	}

	if len(validQuestions) == 0 {
		return nil
	}

	return &models.GeminiQuizResponse{
		Title:     title,
		Questions: validQuestions,
	}
}

// extractJSONFromText attempts to extract a JSON object from text that might contain
// markdown or other formatting, and tries to recover from incomplete JSON
func extractJSONFromText(text string) string {
	// Look for JSON object pattern
	jsonPattern := regexp.MustCompile(`(?s)\{.*"questions".*\}`)
	matches := jsonPattern.FindString(text)
	if matches != "" {
		return matches
	}

	// Try to find JSON between code blocks
	codeBlockPattern := regexp.MustCompile("```(?:json)?\\s*(\\{.*?\\})\\s*```")
	if matches := codeBlockPattern.FindStringSubmatch(text); len(matches) > 1 {
		return matches[1]
	}

	// Try to recover incomplete JSON
	if strings.Contains(text, `{"questions"`) {
		// Extract the partial JSON
		startIdx := strings.Index(text, `{"questions"`)
		if startIdx >= 0 {
			partialJSON := text[startIdx:]

			// Count opening and closing braces to try to balance them
			openBraces := 0
			closeBraces := 0
			inString := false
			escaped := false

			for _, char := range partialJSON {
				if escaped {
					escaped = false
					continue
				}

				if char == '\\' {
					escaped = true
					continue
				}

				if char == '"' && !escaped {
					inString = !inString
					continue
				}

				if !inString {
					if char == '{' {
						openBraces++
					} else if char == '}' {
						closeBraces++
					}
				}
			}

			// If we have more opening braces than closing, add the missing closing braces
			if openBraces > closeBraces {
				for i := 0; i < openBraces-closeBraces; i++ {
					partialJSON += "}"
				}
			}

			// Try to parse the recovered JSON
			var test map[string]interface{}
			if err := json.Unmarshal([]byte(partialJSON), &test); err == nil {
				return partialJSON
			}

			// If that didn't work, try a more aggressive approach: extract just the questions array
			questionsPattern := regexp.MustCompile(`"questions"\s*:\s*\[(.*?)\]`)
			if matches := questionsPattern.FindStringSubmatch(partialJSON); len(matches) > 1 {
				// Wrap the questions array in a proper JSON object
				fixedJSON := `{"questions":[` + matches[1]

				// If the last question is incomplete, try to fix it
				if !strings.HasSuffix(fixedJSON, "}]") {
					lastBraceIdx := strings.LastIndex(fixedJSON, "}")
					if lastBraceIdx > 0 {
						fixedJSON = fixedJSON[:lastBraceIdx+1] + "]}"
					} else {
						fixedJSON += "]}"
					}
				} else {
					fixedJSON += "}"
				}

				// Verify the fixed JSON is valid
				var test map[string]interface{}
				if err := json.Unmarshal([]byte(fixedJSON), &test); err == nil {
					return fixedJSON
				}
			}
		}
	}

	return text
}

// limitQuizSize ensures the quiz response isn't too large by limiting the number of questions
// This helps prevent issues with large responses being truncated
func limitQuizSize(quizResponse *models.GeminiQuizResponse, maxQuestions int) *models.GeminiQuizResponse {
	if quizResponse == nil || len(quizResponse.Questions) <= maxQuestions {
		return quizResponse
	}

	// Create a new response with limited questions
	limitedResponse := &models.GeminiQuizResponse{
		Questions: quizResponse.Questions[:maxQuestions],
	}

	return limitedResponse
}

// SaveTempFile saves a file to a temporary location
func SaveTempFile(data []byte, filename string) (string, error) {
	tempDir := os.TempDir()
	tempFile := filepath.Join(tempDir, uuid.New().String()+"_"+filename)

	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		return "", fmt.Errorf("failed to save temporary file: %w", err)
	}

	return tempFile, nil
}

// DocumentFile represents a file to be processed
type DocumentFile struct {
	Name string
	Path string
	Size int64
}

// NewDocumentFile creates a new DocumentFile from a file
func NewDocumentFile(file io.Reader, filename string, size int64) (*DocumentFile, error) {
	// Check if file size is zero
	if size == 0 {
		return nil, fmt.Errorf("file %s is empty", filename)
	}

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	// Double-check that we actually got data
	if len(data) == 0 {
		return nil, fmt.Errorf("file %s is empty", filename)
	}

	tempPath, err := SaveTempFile(data, filename)
	if err != nil {
		return nil, err
	}

	return &DocumentFile{
		Name: filename,
		Path: tempPath,
		Size: size,
	}, nil
}

// getMimeType returns the MIME type for a file based on its extension
func getMimeType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".txt":
		return "text/plain"
	case ".md":
		return "text/markdown"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	default:
		return "application/octet-stream"
	}
}
