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

// Struct to hold results from concurrent processing, including token counts
type processResult struct {
	quizResponse    *models.GeminiQuizResponse
	promptTokens    int32
	candidateTokens int32
	totalTokens     int32
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
// It now processes files in chunks concurrently and returns aggregated token counts.
// Returns quiz response, prompt tokens, candidate tokens, total tokens, error
func (c *Client) ProcessDocuments(ctx context.Context, files []DocumentFile) (*models.GeminiQuizResponse, int32, int32, int32, error) {
	// Add a timeout to the context
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	// Define the number of concurrent workers and the chunk size
	numWorkers := 6
	chunkSize := 1

	// Create channels for tasks, results, and errors
	fileChunks := make(chan []DocumentFile, (len(files)+chunkSize-1)/chunkSize)
	results := make(chan processResult, len(files)/chunkSize+1) // Use processResult struct
	errChan := make(chan error, len(files)/chunkSize+1)
	var wg sync.WaitGroup

	// Split files into chunks and send them to the fileChunks channel
	for i := 0; i < len(files); i += chunkSize {
		end := i + chunkSize
		if end > len(files) {
			end = len(files)
		}
		fileChunks <- files[i:end]
	}
	close(fileChunks)

	// Launch worker goroutines
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for chunk := range fileChunks {
				// Process each chunk of files, receive quiz and tokens
				quizResponse, pTokens, cTokens, tTokens, err := c.processChunk(ctx, chunk)
				if err != nil {
					errChan <- fmt.Errorf("failed to process chunk: %w", err)
					// Send zero tokens if chunk processing failed entirely before Gemini call
					// If error happened during/after Gemini, processChunk should return counts
					results <- processResult{nil, pTokens, cTokens, tTokens} // Send result even on error to aggregate tokens
					return                                                   // Exit worker on first error
				}
				// Send result struct to results channel
				results <- processResult{quizResponse, pTokens, cTokens, tTokens}
			}
		}()
	}

	// Close results channel when all workers are done
	go func() {
		wg.Wait()
		close(results)
		close(errChan)
	}()

	// Collect results and errors
	var combinedQuizResponse *models.GeminiQuizResponse
	var titles []string
	var aggPromptTokens int32
	var aggCandidateTokens int32
	var aggTotalTokens int32

	for result := range results {
		// Aggregate tokens from every result, even if quizResponse is nil
		aggPromptTokens += result.promptTokens
		aggCandidateTokens += result.candidateTokens
		aggTotalTokens += result.totalTokens

		if result.quizResponse == nil {
			continue // Skip merging quiz data if it's nil
		}

		// Collect titles for later processing
		if result.quizResponse.Title != "" {
			titles = append(titles, result.quizResponse.Title)
		}

		if combinedQuizResponse == nil {
			combinedQuizResponse = result.quizResponse
		} else {
			combinedQuizResponse.Questions = append(combinedQuizResponse.Questions, result.quizResponse.Questions...)
		}
	}

	// Check for errors after processing all results
	if err := <-errChan; err != nil {
		// Return aggregated tokens even if there was an error processing a chunk
		return nil, aggPromptTokens, aggCandidateTokens, aggTotalTokens, err
	}

	// If we have multiple titles, generate a combined title
	if len(titles) > 1 && combinedQuizResponse != nil {
		if combinedQuizResponse.Title == "" && len(titles) > 0 {
			combinedQuizResponse.Title = titles[0]
		}
	}

	// If we still don't have a title, create a generic one
	if combinedQuizResponse != nil && combinedQuizResponse.Title == "" {
		combinedQuizResponse.Title = fmt.Sprintf("Quiz Generated on %s", time.Now().Format("January 2, 2006"))
	}

	// Return combined quiz and aggregated tokens
	return combinedQuizResponse, aggPromptTokens, aggCandidateTokens, aggTotalTokens, nil
}

// processChunk processes a chunk of document files and generates a quiz response.
// Returns quiz response, prompt tokens, candidate tokens, total tokens, error
func (c *Client) processChunk(ctx context.Context, files []DocumentFile) (*models.GeminiQuizResponse, int32, int32, int32, error) {
	totalSize := int64(0)
	for _, file := range files {
		totalSize += file.Size
	}

	if len(files) > 1 && totalSize > MaxInlineSize/2 {
		// processFilesIndividually now returns token counts
		return c.processFilesIndividually(ctx, files)
	}

	if totalSize > MaxInlineSize {
		// processWithFileAPI now returns token counts
		return c.processWithFileAPI(ctx, files)
	}

	// processInline now returns token counts
	return c.processInline(ctx, files)
}

// processFilesIndividually processes files in small batches and combines the results
// Returns quiz response, prompt tokens, candidate tokens, total tokens, error
func (c *Client) processFilesIndividually(ctx context.Context, files []DocumentFile) (*models.GeminiQuizResponse, int32, int32, int32, error) {
	batches := createFileBatches(files, MaxInlineSize/4)

	maxConcurrent := 15
	sem := make(chan struct{}, maxConcurrent)

	resultCh := make(chan processResult, len(batches)) // Use processResult struct
	errCh := make(chan error, len(batches))
	var wg sync.WaitGroup

	for i, batch := range batches {
		wg.Add(1)
		go func(batchNum int, batchFiles []DocumentFile) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			batchCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()

			// Receive all 5 return values from processChunk
			quizResponse, pTokens, cTokens, tTokens, err := c.processChunk(batchCtx, batchFiles)
			if err != nil {
				fileNames := make([]string, len(batchFiles))
				for i, f := range batchFiles {
					fileNames[i] = f.Name
				}
				errCh <- fmt.Errorf("failed to process batch %d (%s): %w", batchNum, strings.Join(fileNames, ", "), err)
				// Send zero tokens if chunk processing failed entirely before Gemini call
				resultCh <- processResult{nil, pTokens, cTokens, tTokens} // Send result even on error
				return
			}
			// Send the result struct containing quiz and tokens
			resultCh <- processResult{quizResponse, pTokens, cTokens, tTokens}
		}(i, batch)
	}

	go func() {
		wg.Wait()
		close(resultCh)
		close(errCh)
	}()

	var allQuestions []models.GeminiQuestion
	var errs []string
	var aggPromptTokens int32
	var aggCandidateTokens int32
	var aggTotalTokens int32

	for result := range resultCh {
		aggPromptTokens += result.promptTokens
		aggCandidateTokens += result.candidateTokens
		aggTotalTokens += result.totalTokens

		if result.quizResponse != nil && len(result.quizResponse.Questions) > 0 {
			maxQuestionsPerBatch := 40
			if len(result.quizResponse.Questions) > maxQuestionsPerBatch {
				result.quizResponse.Questions = result.quizResponse.Questions[:maxQuestionsPerBatch]
			}
			allQuestions = append(allQuestions, result.quizResponse.Questions...)
		}
	}

	for err := range errCh {
		if err != nil {
			errs = append(errs, err.Error())
		}
	}

	if len(errs) > 0 {
		// Return aggregated tokens even on error
		return nil, aggPromptTokens, aggCandidateTokens, aggTotalTokens, fmt.Errorf("failed to process one or more batches: %s", strings.Join(errs, "; "))
	}

	if len(allQuestions) == 0 {
		// Return aggregated tokens even if no questions generated
		return nil, aggPromptTokens, aggCandidateTokens, aggTotalTokens, fmt.Errorf("no questions generated from any files")
	}

	rand.Shuffle(len(allQuestions), func(i, j int) {
		allQuestions[i], allQuestions[j] = allQuestions[j], allQuestions[i]
	})

	maxTotalQuestions := 100
	if len(allQuestions) > maxTotalQuestions {
		allQuestions = allQuestions[:maxTotalQuestions]
	}

	// Return combined quiz and aggregated tokens
	return &models.GeminiQuizResponse{Questions: allQuestions}, aggPromptTokens, aggCandidateTokens, aggTotalTokens, nil
}

// createFileBatches groups files into batches based on size
func createFileBatches(files []DocumentFile, maxBatchSize int64) [][]DocumentFile {
	sortedFiles := make([]DocumentFile, len(files))
	copy(sortedFiles, files)
	sort.Slice(sortedFiles, func(i, j int) bool {
		return sortedFiles[i].Size > sortedFiles[j].Size
	})

	var batches [][]DocumentFile
	var currentBatch []DocumentFile
	var currentSize int64

	for _, file := range sortedFiles {
		if file.Size > maxBatchSize/2 {
			batches = append(batches, []DocumentFile{file})
			continue
		}
		if currentSize+file.Size > maxBatchSize || len(currentBatch) >= 3 {
			if len(currentBatch) > 0 {
				batches = append(batches, currentBatch)
				currentBatch = []DocumentFile{}
				currentSize = 0
			}
		}
		currentBatch = append(currentBatch, file)
		currentSize += file.Size
	}
	if len(currentBatch) > 0 {
		batches = append(batches, currentBatch)
	}
	return batches
}

// Returns quiz response, prompt tokens, candidate tokens, total tokens, error
func (c *Client) processInline(ctx context.Context, files []DocumentFile) (*models.GeminiQuizResponse, int32, int32, int32, error) {
	parts := []genai.Part{}
	parts = append(parts, genai.Text(QuizPrompt))

	for _, file := range files {
		data, err := os.ReadFile(file.Path)
		if err != nil {
			return nil, 0, 0, 0, fmt.Errorf("failed to read file %s: %w", file.Name, err)
		}
		if len(data) == 0 {
			return nil, 0, 0, 0, fmt.Errorf("file %s is empty", file.Name)
		}
		mimeType := getMimeType(file.Name)
		parts = append(parts, genai.Blob{MIMEType: mimeType, Data: data})
	}

	if len(files) == 0 {
		return nil, 0, 0, 0, fmt.Errorf("no files provided for processing")
	}
	return c.generateQuiz(ctx, parts)
}

// Returns quiz response, prompt tokens, candidate tokens, total tokens, error
func (c *Client) processWithFileAPI(ctx context.Context, files []DocumentFile) (*models.GeminiQuizResponse, int32, int32, int32, error) {
	if len(files) == 0 {
		return nil, 0, 0, 0, fmt.Errorf("no files provided for processing")
	}

	var wg sync.WaitGroup
	fileDataCh := make(chan *genai.FileData, len(files))
	errorCh := make(chan error, len(files))

	for _, file := range files {
		wg.Add(1)
		go func(file DocumentFile) {
			defer wg.Done()
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

	wg.Wait()
	close(fileDataCh)
	close(errorCh)

	for err := range errorCh {
		if err != nil {
			return nil, 0, 0, 0, err // Return 0 tokens on error
		}
	}

	var fileDataList []*genai.FileData
	for fileData := range fileDataCh {
		fileDataList = append(fileDataList, fileData)
	}

	if len(fileDataList) == 0 {
		return nil, 0, 0, 0, fmt.Errorf("no files were successfully uploaded")
	}

	parts := []genai.Part{genai.Text(QuizPrompt)}
	for _, fileData := range fileDataList {
		parts = append(parts, fileData)
	}

	quiz, pTokens, cTokens, tTokens, err := c.generateQuiz(ctx, parts)

	// Clean up uploaded files
	for _, fileData := range fileDataList {
		if err := c.client.DeleteFile(ctx, fileData.URI); err != nil {
			fmt.Printf("Warning: failed to delete file %s: %v\n", fileData.URI, err)
		}
	}
	return quiz, pTokens, cTokens, tTokens, err
}

// generateQuiz sends the request to Gemini and parses the response
// Returns quiz response, prompt tokens, candidate tokens, total tokens, error
func (c *Client) generateQuiz(ctx context.Context, parts []genai.Part) (*models.GeminiQuizResponse, int32, int32, int32, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	c.model.SetTemperature(0.2)
	c.model.SetTopK(40)
	c.model.SetTopP(0.95)
	c.model.SetMaxOutputTokens(int32(8192))

	var lastErr error
	var promptTokens int32
	var candidateTokens int32
	var totalTokens int32

	for attempts := 0; attempts < 3; attempts++ {
		if attempts > 0 {
			c.model.SetMaxOutputTokens(int32(4096 - attempts*1000))
			maxQs := 50 - attempts*15
			limitedPrompt := fmt.Sprintf("%s\n\nIMPORTANT: Due to size constraints, please limit your response to no more than %d questions.", QuizPrompt, maxQs)
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
			time.Sleep(2 * time.Second)
			continue
		}

		// --- Token Usage ---
		if resp.UsageMetadata != nil {
			promptTokens = resp.UsageMetadata.PromptTokenCount
			candidateTokens = resp.UsageMetadata.CandidatesTokenCount
			totalTokens = resp.UsageMetadata.TotalTokenCount
			log.Printf("INFO: Gemini Token Usage (Attempt %d): Prompt=%d, Candidates=%d, Total=%d", attempts+1, promptTokens, candidateTokens, totalTokens)
		} else {
			log.Printf("WARN: Gemini UsageMetadata was nil (Attempt %d)", attempts+1)
		}
		// --- End Token Usage ---

		if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
			lastErr = fmt.Errorf("no content generated (attempt %d)", attempts+1)
			time.Sleep(2 * time.Second)
			continue
		}

		jsonText := ""
		for _, part := range resp.Candidates[0].Content.Parts {
			if text, ok := part.(genai.Text); ok {
				jsonText += string(text)
			}
		}
		jsonText = extractJSONFromText(jsonText)

		if jsonText == "" {
			lastErr = fmt.Errorf("no JSON content found in response (attempt %d)", attempts+1)
			time.Sleep(2 * time.Second)
			continue
		}

		var quizResponse models.GeminiQuizResponse
		decoder := json.NewDecoder(strings.NewReader(jsonText))
		decoder.DisallowUnknownFields()
		decoder.UseNumber()

		if err := decoder.Decode(&quizResponse); err != nil {
			log.Printf("DEBUG: Raw JSON text received (attempt %d) before parse error: %s", attempts+1, jsonText)
			fmt.Printf("Invalid JSON (attempt %d): %s\n", attempts+1, jsonText)
			lastErr = fmt.Errorf("failed to parse JSON response (attempt %d): %w. Raw text logged.", attempts+1, err)
			time.Sleep(2 * time.Second)
			continue
		}

		if len(quizResponse.Questions) == 0 {
			lastErr = fmt.Errorf("quiz response contained no questions (attempt %d)", attempts+1)
			time.Sleep(2 * time.Second)
			continue
		}

		quizResponse = *limitQuizSize(&quizResponse, 200)
		return &quizResponse, promptTokens, candidateTokens, totalTokens, nil
	}

	// Return 0 tokens on final failure
	return nil, 0, 0, 0, fmt.Errorf("failed to generate quiz after multiple attempts: %w", lastErr)
}

// extractValidQuestionsFromPartialJSON attempts to extract valid questions from a partial JSON response
func extractValidQuestionsFromPartialJSON(jsonText string) *models.GeminiQuizResponse {
	titlePattern := regexp.MustCompile(`"title"(?:\s*):(?:\s*)"([^"]*)"`)
	titleMatch := titlePattern.FindStringSubmatch(jsonText)
	var title string
	if len(titleMatch) > 1 {
		title = titleMatch[1]
	}

	questionPattern := regexp.MustCompile(`\{(?s)(?:\s*)"text"(?:\s*):(?:\s*)"([^"]*)"(?:\s*),(?:\s*)"topic"(?:\s*):(?:\s*)"([^"]*)"(?:\s*),(?:\s*)"options"(?:\s*):(?:\s*)\[(.*?)\](?:\s*)\}`)
	matches := questionPattern.FindAllStringSubmatch(jsonText, -1)
	if len(matches) == 0 {
		return nil
	}

	var validQuestions []models.GeminiQuestion
	for _, match := range matches {
		if len(match) < 4 {
			continue
		}
		questionText := match[1]
		topicText := match[2]
		optionsText := match[3]

		optionPattern := regexp.MustCompile(`\{(?s)(?:\s*)"text"(?:\s*):(?:\s*)"([^"]*)"(?:\s*),(?:\s*)"is_correct"(?:\s*):(?:\s*)(true|false)(?:\s*),(?:\s*)"explanation"(?:\s*):(?:\s*)"([^"]*)"(?:\s*)\}`)
		optionMatches := optionPattern.FindAllStringSubmatch(optionsText, -1)
		if len(optionMatches) != 4 {
			continue
		}

		var options []models.GeminiOption
		correctCount := 0
		for _, optionMatch := range optionMatches {
			if len(optionMatch) < 4 {
				continue
			}
			optionText := optionMatch[1]
			isCorrect := optionMatch[2] == "true"
			explanationText := optionMatch[3]
			if isCorrect {
				correctCount++
			}
			options = append(options, models.GeminiOption{
				Text:        optionText,
				IsCorrect:   isCorrect,
				Explanation: explanationText,
			})
		}

		if correctCount != 1 || len(options) != 4 {
			continue
		}
		validQuestions = append(validQuestions, models.GeminiQuestion{
			Text:    questionText,
			Topic:   topicText,
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
	jsonPattern := regexp.MustCompile(`(?s)\{.*"questions".*\}`)
	matches := jsonPattern.FindString(text)
	if matches != "" {
		return matches
	}

	codeBlockPattern := regexp.MustCompile("```(?:json)?\\s*(\\{.*?\\})\\s*```")
	if matches := codeBlockPattern.FindStringSubmatch(text); len(matches) > 1 {
		return matches[1]
	}

	if strings.Contains(text, `{"questions"`) {
		startIdx := strings.Index(text, `{"questions"`)
		if startIdx >= 0 {
			partialJSON := text[startIdx:]
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
			if openBraces > closeBraces {
				for i := 0; i < openBraces-closeBraces; i++ {
					partialJSON += "}"
				}
			}
			var test map[string]interface{}
			if err := json.Unmarshal([]byte(partialJSON), &test); err == nil {
				return partialJSON
			}

			questionsPattern := regexp.MustCompile(`"questions"\s*:\s*\[(.*?)\]`)
			if matches := questionsPattern.FindStringSubmatch(partialJSON); len(matches) > 1 {
				fixedJSON := `{"questions":[` + matches[1]
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
func limitQuizSize(quizResponse *models.GeminiQuizResponse, maxQuestions int) *models.GeminiQuizResponse {
	if quizResponse == nil || len(quizResponse.Questions) <= maxQuestions {
		return quizResponse
	}
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
	if size == 0 {
		return nil, fmt.Errorf("file %s is empty", filename)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
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
