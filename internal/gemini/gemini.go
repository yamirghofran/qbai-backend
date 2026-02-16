package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"quizbuilderai/internal/models"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/generative-ai-go/genai"
	"github.com/google/uuid"
	"google.golang.org/api/option"
)

// quizResponseSchema returns the JSON schema for the quiz response structure
// This enables Gemini's structured output feature for guaranteed schema-compliant JSON
func quizResponseSchema() *genai.Schema {
	return &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"title": {
				Type:        genai.TypeString,
				Description: "Descriptive, concise quiz title based on the main subject matter of the documents",
			},
			"questions": {
				Type:        genai.TypeArray,
				Description: "List of quiz questions covering the document content",
				Items: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"text": {
							Type:        genai.TypeString,
							Description: "The question text - should be self-contained and understandable without referring back to the documents",
						},
						"topic": {
							Type:        genai.TypeString,
							Description: "The topic this question is about, for grouping questions by topic",
						},
						"options": {
							Type:        genai.TypeArray,
							Description: "Exactly 4 answer options with exactly ONE correct answer",
							Items: &genai.Schema{
								Type: genai.TypeObject,
								Properties: map[string]*genai.Schema{
									"text": {
										Type:        genai.TypeString,
										Description: "The option text",
									},
									"is_correct": {
										Type:        genai.TypeBoolean,
										Description: "Whether this option is the correct answer (exactly one option per question should be true)",
									},
									"explanation": {
										Type:        genai.TypeString,
										Description: "Concise explanation of why this option is correct or incorrect, based on the source documents. State the fact, not 'this is correct/incorrect'",
									},
								},
								Required: []string{"text", "is_correct", "explanation"},
							},
						},
					},
					Required: []string{"text", "topic", "options"},
				},
			},
		},
		Required: []string{"title", "questions"},
	}
}

// BuildQuizPrompt generates a customized prompt based on optional parameters
func BuildQuizPrompt(maxQuestions *int, difficulty *string, customPrompt *string) string {
	basePrompt := `Generate a comprehensive multiple-choice quiz based on the content of these documents.

Follow these requirements exactly:

1. Create a descriptive title for the quiz that accurately reflects the main subject matter of the documents`

	// Add max questions constraint if provided
	if maxQuestions != nil && *maxQuestions > 0 {
		basePrompt += fmt.Sprintf(`
2. Create UP TO %d questions covering main topics and subtopics in the documents`, *maxQuestions)
	} else {
		basePrompt += `
2. Create questions covering ALL main topics and subtopics in the documents`
	}

	basePrompt += `, ensuring no significant concept is omitted. Include the topic for each question (so that questions can be grouped by topic later.). DON'T reference the documents in the questions or options. The questions should be self-contained and understandable without needing to refer back to the documents.`

	// Add difficulty-specific instructions if provided
	if difficulty != nil && *difficulty != "" {
		basePrompt += "\n" + getDifficultyInstructions(*difficulty)
	} else {
		// Use default balanced distribution
		basePrompt += `
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
     * Identifying unstated assumptions underlying concepts`
	}

	// Add custom prompt if provided
	if customPrompt != nil && *customPrompt != "" {
		basePrompt += fmt.Sprintf(`

SPECIAL INSTRUCTIONS FROM USER:
%s`, *customPrompt)
	}

	// Continue with the rest of the standard requirements
	basePrompt += `
4. For analytical questions, prioritize second and third-order thinking by asking about:
   - "What would happen if..." scenarios
   - Underlying mechanisms or reasons behind facts
   - How concepts interact in complex systems
   - Potential exceptions or limitations to stated principles
5. Each question must have exactly 4 options with EXACTLY ONE correct answer
6. For EACH answer option:
   - Provide a concise "explanation" field detailing WHY the option is correct OR incorrect based on the source documents. Don't state "This is incorrect/correct". Just say the explanation. e.g."Gravity was discovered by Isaac Newton"
   - Make incorrect options (distractors) highly plausible by using common misconceptions or partial understandings.
   - Ensure all options have approximately the same length and level of detail.
   - Maintain consistent grammar, style, and tone across all options.
   - Avoid obvious wrong answers or "joke" options.`

	return basePrompt
}

// getDifficultyInstructions returns difficulty-specific instructions
func getDifficultyInstructions(difficulty string) string {
	switch strings.ToLower(difficulty) {
	case "easy":
		return `3. DIFFICULTY LEVEL: Easy
   - Focus primarily on basic factual recall and simple comprehension
   - Use straightforward, clear language
   - Questions should test fundamental concepts and definitions
   - Correct answers should be clearly identifiable to someone who studied the material
   - Distractors should be plausible but obviously incorrect to someone familiar with the content
   - Avoid complex multi-step reasoning or deep analysis`

	case "medium":
		return `3. DIFFICULTY LEVEL: Medium
   - Balance between factual recall (40%) and application/comprehension (60%)
   - Include questions that require understanding relationships between concepts
   - Some questions should require applying knowledge to new but straightforward scenarios
   - Use clear language but test deeper understanding than surface-level facts
   - Distractors should be plausible and require careful thinking to eliminate
   - Include some analytical questions but keep reasoning steps manageable`

	case "hard":
		return `3. DIFFICULTY LEVEL: Hard
   - Emphasize application (30%), analysis (40%), and synthesis (30%)
   - Include complex scenarios requiring multiple steps of reasoning
   - Test deep understanding and ability to connect disparate concepts
   - Questions should require integration of knowledge from different sections
   - Distractors should be highly plausible, representing sophisticated misconceptions
   - Include edge cases and situations requiring careful consideration
   - Minimize simple recall questions`

	case "extreme":
		return `3. DIFFICULTY LEVEL: Extreme
   - Focus heavily on synthesis, evaluation, and advanced application
   - Questions should test edge cases, exceptions, and nuanced understanding
   - Require integration of multiple complex concepts simultaneously
   - Include questions about unstated assumptions, implications, and predictions
   - Test ability to evaluate competing approaches and identify subtle flaws
   - Distractors should differ only in nuanced details that require expert-level understanding
   - All options should appear correct to someone with only surface-level knowledge
   - Avoid simple factual recall entirely`

	default:
		// Return default balanced distribution
		return `3. Include a balanced distribution of question types:
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
     * Identifying unstated assumptions underlying concepts`
	}
}

const (
	// MaxInlineSize is the maximum size for inline PDF data (20MB)
	MaxInlineSize = 20 * 1024 * 1024
	// ModelName is the Gemini model to use
	ModelName = "gemini-3-flash-preview"
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
	model.ResponseSchema = quizResponseSchema()

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
func (c *Client) ProcessDocuments(ctx context.Context, files []DocumentFile, maxQuestions *int, difficulty *string, customPrompt *string) (*models.GeminiQuizResponse, int32, int32, int32, error) {
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
				quizResponse, pTokens, cTokens, tTokens, err := c.processChunk(ctx, chunk, maxQuestions, difficulty, customPrompt)
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
func (c *Client) processChunk(ctx context.Context, files []DocumentFile, maxQuestions *int, difficulty *string, customPrompt *string) (*models.GeminiQuizResponse, int32, int32, int32, error) {
	totalSize := int64(0)
	for _, file := range files {
		totalSize += file.Size
	}

	if len(files) > 1 && totalSize > MaxInlineSize/2 {
		// processFilesIndividually now returns token counts
		return c.processFilesIndividually(ctx, files, maxQuestions, difficulty, customPrompt)
	}

	if totalSize > MaxInlineSize {
		// processWithFileAPI now returns token counts
		return c.processWithFileAPI(ctx, files, maxQuestions, difficulty, customPrompt)
	}

	// processInline now returns token counts
	return c.processInline(ctx, files, maxQuestions, difficulty, customPrompt)
}

// processFilesIndividually processes files in small batches and combines the results
// Returns quiz response, prompt tokens, candidate tokens, total tokens, error
func (c *Client) processFilesIndividually(ctx context.Context, files []DocumentFile, maxQuestions *int, difficulty *string, customPrompt *string) (*models.GeminiQuizResponse, int32, int32, int32, error) {
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
			quizResponse, pTokens, cTokens, tTokens, err := c.processChunk(batchCtx, batchFiles, maxQuestions, difficulty, customPrompt)
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
func (c *Client) processInline(ctx context.Context, files []DocumentFile, maxQuestions *int, difficulty *string, customPrompt *string) (*models.GeminiQuizResponse, int32, int32, int32, error) {
	parts := []genai.Part{}
	prompt := BuildQuizPrompt(maxQuestions, difficulty, customPrompt)
	parts = append(parts, genai.Text(prompt))

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
	return c.generateQuiz(ctx, parts, maxQuestions, difficulty, customPrompt)
}

// Returns quiz response, prompt tokens, candidate tokens, total tokens, error
func (c *Client) processWithFileAPI(ctx context.Context, files []DocumentFile, maxQuestions *int, difficulty *string, customPrompt *string) (*models.GeminiQuizResponse, int32, int32, int32, error) {
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

	prompt := BuildQuizPrompt(maxQuestions, difficulty, customPrompt)
	parts := []genai.Part{genai.Text(prompt)}
	for _, fileData := range fileDataList {
		parts = append(parts, fileData)
	}

	quiz, pTokens, cTokens, tTokens, err := c.generateQuiz(ctx, parts, maxQuestions, difficulty, customPrompt)

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
func (c *Client) generateQuiz(ctx context.Context, parts []genai.Part, maxQuestions *int, difficulty *string, customPrompt *string) (*models.GeminiQuizResponse, int32, int32, int32, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	c.model.SetTemperature(0.95)
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
			adjustedMax := &maxQs
			limitedPrompt := BuildQuizPrompt(adjustedMax, difficulty, customPrompt)
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

		// Extract JSON text from response - structured output guarantees valid JSON
		jsonText := ""
		for _, part := range resp.Candidates[0].Content.Parts {
			if text, ok := part.(genai.Text); ok {
				jsonText += string(text)
			}
		}

		if jsonText == "" {
			lastErr = fmt.Errorf("no JSON content found in response (attempt %d)", attempts+1)
			time.Sleep(2 * time.Second)
			continue
		}

		var quizResponse models.GeminiQuizResponse
		if err := json.Unmarshal([]byte(jsonText), &quizResponse); err != nil {
			log.Printf("WARN: JSON parsing failed (attempt %d): %v", attempts+1, err)
			log.Printf("DEBUG: Raw JSON text: %s", jsonText)
			lastErr = fmt.Errorf("failed to parse JSON response (attempt %d): %w", attempts+1, err)
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
