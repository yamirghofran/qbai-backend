package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"quizbuilderai/internal/models"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// quizResponseSchema returns the JSON schema for the quiz response structure.
func quizResponseSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"title": map[string]any{
				"type":        "string",
				"description": "Descriptive, concise quiz title based on the main subject matter of the documents",
			},
			"questions": map[string]any{
				"type":        "array",
				"description": "List of quiz questions covering the document content",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"text": map[string]any{
							"type":        "string",
							"description": "The question text - should be self-contained and understandable without referring back to the documents",
						},
						"topic": map[string]any{
							"type":        "string",
							"description": "The topic this question is about, for grouping questions by topic",
						},
						"options": map[string]any{
							"type":        "array",
							"description": "Exactly 4 answer options with exactly ONE correct answer",
							"items": map[string]any{
								"type":                 "object",
								"additionalProperties": false,
								"properties": map[string]any{
									"text": map[string]any{
										"type":        "string",
										"description": "The option text",
									},
									"is_correct": map[string]any{
										"type":        "boolean",
										"description": "Whether this option is the correct answer (exactly one option per question should be true)",
									},
									"explanation": map[string]any{
										"type":        "string",
										"description": "Concise explanation of why this option is correct or incorrect, based on the source documents. State the fact, not 'this is correct/incorrect'",
									},
								},
								"required": []string{"text", "is_correct", "explanation"},
							},
						},
					},
					"required": []string{"text", "topic", "options"},
				},
			},
		},
		"required": []string{"title", "questions"},
	}
}

// BuildQuizPrompt generates a customized prompt based on optional parameters.
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
   - Avoid obvious wrong answers or "joke" options.
7. Return ONLY a JSON object that matches the schema exactly.`

	return basePrompt
}

// getDifficultyInstructions returns difficulty-specific instructions.
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
	// MaxInlineSize is the threshold used to split large multi-file requests.
	MaxInlineSize = 20 * 1024 * 1024

	DefaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"
	DefaultModelName         = "minimax/minimax-m2.5"
	DefaultPDFEngine         = "mistral-ocr"
)

// Client wraps the OpenAI Go client configured for OpenRouter.
type Client struct {
	client    openai.Client
	model     string
	pdfEngine string
}

// Struct to hold results from concurrent processing, including token counts.
type processResult struct {
	quizResponse    *models.LLMQuizResponse
	promptTokens    int32
	candidateTokens int32
	totalTokens     int32
}

// NewClient creates a new OpenAI client configured for OpenRouter.
func NewClient() (*Client, error) {
	apiKey := firstNonEmpty(
		strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")),
		strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
	)
	if apiKey == "" {
		return nil, fmt.Errorf("OPENROUTER_API_KEY (or OPENAI_API_KEY) environment variable not set")
	}

	baseURL := firstNonEmpty(
		strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL")),
		strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")),
		DefaultOpenRouterBaseURL,
	)

	model := firstNonEmpty(
		strings.TrimSpace(os.Getenv("OPENROUTER_MODEL")),
		strings.TrimSpace(os.Getenv("OPENAI_MODEL")),
		DefaultModelName,
	)

	pdfEngine := strings.TrimSpace(os.Getenv("OPENROUTER_PDF_ENGINE"))
	if pdfEngine == "" {
		pdfEngine = DefaultPDFEngine
	}

	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithBaseURL(baseURL),
	}

	if referer := strings.TrimSpace(os.Getenv("OPENROUTER_HTTP_REFERER")); referer != "" {
		opts = append(opts, option.WithHeader("HTTP-Referer", referer))
	}
	if appTitle := strings.TrimSpace(os.Getenv("OPENROUTER_X_TITLE")); appTitle != "" {
		opts = append(opts, option.WithHeader("X-Title", appTitle))
	}

	client := openai.NewClient(opts...)

	return &Client{
		client:    client,
		model:     model,
		pdfEngine: pdfEngine,
	}, nil
}

// Close is a no-op for the OpenAI client.
func (c *Client) Close() {}

// ProcessDocuments processes multiple document files and generates a quiz.
// It processes files in chunks concurrently and returns aggregated token counts.
func (c *Client) ProcessDocuments(ctx context.Context, files []DocumentFile, maxQuestions *int, difficulty *string, customPrompt *string) (*models.LLMQuizResponse, int32, int32, int32, error) {
	// Add a timeout to the context
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	// Define the number of concurrent workers and the chunk size
	numWorkers := 6
	chunkSize := 1

	// Create channels for tasks, results, and errors
	fileChunks := make(chan []DocumentFile, (len(files)+chunkSize-1)/chunkSize)
	results := make(chan processResult, len(files)/chunkSize+1)
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
				quizResponse, pTokens, cTokens, tTokens, err := c.processChunk(ctx, chunk, maxQuestions, difficulty, customPrompt)
				if err != nil {
					errChan <- fmt.Errorf("failed to process chunk: %w", err)
					results <- processResult{nil, pTokens, cTokens, tTokens}
					return
				}
				results <- processResult{quizResponse, pTokens, cTokens, tTokens}
			}
		}()
	}

	// Close channels when all workers are done
	go func() {
		wg.Wait()
		close(results)
		close(errChan)
	}()

	var combinedQuizResponse *models.LLMQuizResponse
	var titles []string
	var aggPromptTokens int32
	var aggCandidateTokens int32
	var aggTotalTokens int32

	for result := range results {
		aggPromptTokens += result.promptTokens
		aggCandidateTokens += result.candidateTokens
		aggTotalTokens += result.totalTokens

		if result.quizResponse == nil {
			continue
		}

		if result.quizResponse.Title != "" {
			titles = append(titles, result.quizResponse.Title)
		}

		if combinedQuizResponse == nil {
			combinedQuizResponse = result.quizResponse
		} else {
			combinedQuizResponse.Questions = append(combinedQuizResponse.Questions, result.quizResponse.Questions...)
		}
	}

	if err := <-errChan; err != nil {
		return nil, aggPromptTokens, aggCandidateTokens, aggTotalTokens, err
	}

	if len(titles) > 1 && combinedQuizResponse != nil {
		if combinedQuizResponse.Title == "" && len(titles) > 0 {
			combinedQuizResponse.Title = titles[0]
		}
	}

	if combinedQuizResponse != nil && combinedQuizResponse.Title == "" {
		combinedQuizResponse.Title = fmt.Sprintf("Quiz Generated on %s", time.Now().Format("January 2, 2006"))
	}

	return combinedQuizResponse, aggPromptTokens, aggCandidateTokens, aggTotalTokens, nil
}

// processChunk processes a chunk of files and returns quiz + token usage.
func (c *Client) processChunk(ctx context.Context, files []DocumentFile, maxQuestions *int, difficulty *string, customPrompt *string) (*models.LLMQuizResponse, int32, int32, int32, error) {
	totalSize := int64(0)
	for _, file := range files {
		totalSize += file.Size
	}

	if len(files) > 1 && totalSize > MaxInlineSize/2 {
		return c.processFilesIndividually(ctx, files, maxQuestions, difficulty, customPrompt)
	}

	return c.processInline(ctx, files, maxQuestions, difficulty, customPrompt)
}

// processFilesIndividually processes files in smaller batches and combines results.
func (c *Client) processFilesIndividually(ctx context.Context, files []DocumentFile, maxQuestions *int, difficulty *string, customPrompt *string) (*models.LLMQuizResponse, int32, int32, int32, error) {
	batches := createFileBatches(files, MaxInlineSize/4)

	maxConcurrent := 15
	sem := make(chan struct{}, maxConcurrent)

	resultCh := make(chan processResult, len(batches))
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

			quizResponse, pTokens, cTokens, tTokens, err := c.processChunk(batchCtx, batchFiles, maxQuestions, difficulty, customPrompt)
			if err != nil {
				fileNames := make([]string, len(batchFiles))
				for i, f := range batchFiles {
					fileNames[i] = f.Name
				}
				errCh <- fmt.Errorf("failed to process batch %d (%s): %w", batchNum, strings.Join(fileNames, ", "), err)
				resultCh <- processResult{nil, pTokens, cTokens, tTokens}
				return
			}

			resultCh <- processResult{quizResponse, pTokens, cTokens, tTokens}
		}(i, batch)
	}

	go func() {
		wg.Wait()
		close(resultCh)
		close(errCh)
	}()

	var allQuestions []models.LLMQuestion
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
		return nil, aggPromptTokens, aggCandidateTokens, aggTotalTokens, fmt.Errorf("failed to process one or more batches: %s", strings.Join(errs, "; "))
	}

	if len(allQuestions) == 0 {
		return nil, aggPromptTokens, aggCandidateTokens, aggTotalTokens, fmt.Errorf("no questions generated from any files")
	}

	rand.Shuffle(len(allQuestions), func(i, j int) {
		allQuestions[i], allQuestions[j] = allQuestions[j], allQuestions[i]
	})

	maxTotalQuestions := 100
	if len(allQuestions) > maxTotalQuestions {
		allQuestions = allQuestions[:maxTotalQuestions]
	}

	return &models.LLMQuizResponse{Questions: allQuestions}, aggPromptTokens, aggCandidateTokens, aggTotalTokens, nil
}

// createFileBatches groups files into batches based on size.
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

// processInline encodes files as data URLs and sends them in a chat completion request.
func (c *Client) processInline(ctx context.Context, files []DocumentFile, maxQuestions *int, difficulty *string, customPrompt *string) (*models.LLMQuizResponse, int32, int32, int32, error) {
	if len(files) == 0 {
		return nil, 0, 0, 0, fmt.Errorf("no files provided for processing")
	}

	parts := make([]openai.ChatCompletionContentPartUnionParam, 0, len(files)+1)
	prompt := BuildQuizPrompt(maxQuestions, difficulty, customPrompt)
	parts = append(parts, openai.ChatCompletionContentPartUnionParam{
		OfText: &openai.ChatCompletionContentPartTextParam{Text: prompt},
	})

	hasPDF := false
	for _, file := range files {
		data, err := os.ReadFile(file.Path)
		if err != nil {
			return nil, 0, 0, 0, fmt.Errorf("failed to read file %s: %w", file.Name, err)
		}
		if len(data) == 0 {
			return nil, 0, 0, 0, fmt.Errorf("file %s is empty", file.Name)
		}

		mimeType := getMimeType(file.Name)
		if strings.EqualFold(filepath.Ext(file.Name), ".pdf") {
			hasPDF = true
		}
		dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(data))

		parts = append(parts, openai.ChatCompletionContentPartUnionParam{
			OfFile: &openai.ChatCompletionContentPartFileParam{
				File: openai.ChatCompletionContentPartFileFileParam{
					Filename: openai.String(file.Name),
					FileData: openai.String(dataURL),
				},
			},
		})
	}

	return c.generateQuiz(ctx, parts, hasPDF, maxQuestions, difficulty, customPrompt)
}

// generateQuiz sends the request and parses the response.
func (c *Client) generateQuiz(ctx context.Context, parts []openai.ChatCompletionContentPartUnionParam, hasPDF bool, maxQuestions *int, difficulty *string, customPrompt *string) (*models.LLMQuizResponse, int32, int32, int32, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	var lastErr error
	var promptTokens int32
	var candidateTokens int32
	var totalTokens int32

	for attempts := 0; attempts < 3; attempts++ {
		maxOutputTokens := int64(8192 - attempts*1000)
		if maxOutputTokens < 1024 {
			maxOutputTokens = 1024
		}

		attemptParts := append([]openai.ChatCompletionContentPartUnionParam(nil), parts...)
		if attempts > 0 {
			maxQs := 50 - attempts*15
			if maxQs < 5 {
				maxQs = 5
			}
			if maxQuestions != nil && *maxQuestions > 0 && *maxQuestions < maxQs {
				maxQs = *maxQuestions
			}
			adjustedMax := maxQs
			attemptParts[0] = openai.ChatCompletionContentPartUnionParam{
				OfText: &openai.ChatCompletionContentPartTextParam{Text: BuildQuizPrompt(&adjustedMax, difficulty, customPrompt)},
			}
		}

		params := openai.ChatCompletionNewParams{
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.UserMessage(attemptParts),
			},
			Model:       openai.ChatModel(c.model),
			Temperature: openai.Float(0.95),
			TopP:        openai.Float(0.95),
			MaxTokens:   openai.Int(maxOutputTokens),
			ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
				OfJSONSchema: &openai.ResponseFormatJSONSchemaParam{
					JSONSchema: openai.ResponseFormatJSONSchemaJSONSchemaParam{
						Name:        "quiz_response",
						Description: openai.String("Structured quiz response with title, questions, options, and explanations"),
						Strict:      openai.Bool(true),
						Schema:      quizResponseSchema(),
					},
				},
			},
		}

		if hasPDF {
			params.SetExtraFields(map[string]any{
				"plugins": []map[string]any{
					{
						"id": "file-parser",
						"pdf": map[string]any{
							"engine": c.pdfEngine,
						},
					},
				},
			})
		}

		resp, err := c.client.Chat.Completions.New(ctx, params)
		if err != nil {
			lastErr = fmt.Errorf("failed to generate content (attempt %d): %w", attempts+1, err)
			time.Sleep(2 * time.Second)
			continue
		}

		promptTokens = safeInt32(resp.Usage.PromptTokens)
		candidateTokens = safeInt32(resp.Usage.CompletionTokens)
		totalTokens = safeInt32(resp.Usage.TotalTokens)
		log.Printf("INFO: LLM Token Usage (Attempt %d): Prompt=%d, Completion=%d, Total=%d", attempts+1, promptTokens, candidateTokens, totalTokens)

		if len(resp.Choices) == 0 {
			lastErr = fmt.Errorf("no choices generated (attempt %d)", attempts+1)
			time.Sleep(2 * time.Second)
			continue
		}

		jsonText := strings.TrimSpace(resp.Choices[0].Message.Content)
		if jsonText == "" {
			lastErr = fmt.Errorf("no JSON content found in response (attempt %d)", attempts+1)
			time.Sleep(2 * time.Second)
			continue
		}
		jsonText = stripCodeFences(jsonText)

		var quizResponse models.LLMQuizResponse
		if err := json.Unmarshal([]byte(jsonText), &quizResponse); err != nil {
			log.Printf("WARN: JSON parsing failed (attempt %d): %v", attempts+1, err)
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

	return nil, 0, 0, 0, fmt.Errorf("failed to generate quiz after multiple attempts: %w", lastErr)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func safeInt32(v int64) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}

func stripCodeFences(s string) string {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimPrefix(trimmed, "json")
	trimmed = strings.TrimSpace(trimmed)
	if idx := strings.LastIndex(trimmed, "```"); idx >= 0 {
		trimmed = strings.TrimSpace(trimmed[:idx])
	}
	return trimmed
}

// limitQuizSize ensures the quiz response isn't too large by limiting the number of questions.
func limitQuizSize(quizResponse *models.LLMQuizResponse, maxQuestions int) *models.LLMQuizResponse {
	if quizResponse == nil || len(quizResponse.Questions) <= maxQuestions {
		return quizResponse
	}
	limitedResponse := &models.LLMQuizResponse{
		Questions: quizResponse.Questions[:maxQuestions],
	}
	return limitedResponse
}

// SaveTempFile saves a file to a temporary location.
func SaveTempFile(data []byte, filename string) (string, error) {
	tempDir := os.TempDir()
	tempFile := filepath.Join(tempDir, uuid.New().String()+"_"+filename)
	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		return "", fmt.Errorf("failed to save temporary file: %w", err)
	}
	return tempFile, nil
}

// DocumentFile represents a file to be processed.
type DocumentFile struct {
	Name string
	Path string
	Size int64
}

// NewDocumentFile creates a new DocumentFile from a file.
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

// getMimeType returns the MIME type for a file based on its extension.
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
