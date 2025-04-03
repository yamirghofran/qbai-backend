package models

import (
	"time"

	"github.com/google/uuid"
)

// Quiz represents a quiz generated from PDFs
type Quiz struct {
	ID        uuid.UUID  `json:"id"`
	Title     string     `json:"title"`
	Questions []Question `json:"questions,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Question represents a question in a quiz
type Question struct {
	ID        uuid.UUID `json:"id"`
	QuizID    uuid.UUID `json:"quiz_id,omitempty"`
	Text      string    `json:"text"`
	Options   []Option  `json:"options,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Option represents an answer option for a question
type Option struct {
	ID         uuid.UUID `json:"id"`
	QuestionID uuid.UUID `json:"question_id,omitempty"`
	Text       string    `json:"text"`
	IsCorrect  bool      `json:"is_correct"`
	CreatedAt  time.Time `json:"created_at"`
}

// File represents a PDF file uploaded for quiz generation
type File struct {
	ID        uuid.UUID `json:"id"`
	QuizID    uuid.UUID `json:"quiz_id"`
	FileName  string    `json:"file_name"`
	FileSize  int64     `json:"file_size"`
	CreatedAt time.Time `json:"created_at"`
}

// PDFFile represents a PDF file to be processed
type PDFFile struct {
	Name string
	Path string
	Size int64
}

// GeminiQuizResponse represents the structured JSON response from Gemini
type GeminiQuizResponse struct {
	Title     string           `json:"title"`
	Questions []GeminiQuestion `json:"questions"`
}

// GeminiQuestion represents a question in the Gemini response
type GeminiQuestion struct {
	Text    string         `json:"text"`
	Topic   string         `json:"topic"` // Added field for topic assignment
	Options []GeminiOption `json:"options"`
}

// GeminiOption represents an option in the Gemini response
type GeminiOption struct {
	Text        string `json:"text"`
	IsCorrect   bool   `json:"is_correct"`
	Explanation string `json:"explanation"` // Added explanation field
}

// QuizListResponse represents the response for listing quizzes
type QuizListResponse struct {
	Quizzes []Quiz `json:"quizzes"`
	Total   int64  `json:"total"`
}

// UploadResponse represents the response for the upload endpoint
type UploadResponse struct {
	QuizID  uuid.UUID `json:"quiz_id"`
	Title   string    `json:"title"`
	Message string    `json:"message"`
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error string `json:"error"`
}
