Phase 1: Setup New Directory Structure
Objective: Create the new folder structure without modifying existing code
Steps:

1. Create internal/domain/ directory
   - domain.go (domain entities)
   - errors.go (domain-specific errors)
2. Create internal/repository/ directory
   - quiz_repository.go
   - attempt_repository.go
   - material_repository.go
   - topic_repository.go
   - user_repository.go
   - token_repository.go
3. Create internal/service/ directory
   - quiz_service.go
   - attempt_service.go
   - material_service.go
   - topic_service.go
   - user_service.go
   - token_service.go
4. Create internal/service/notification/ directory
   - discord_service.go
5. Create internal/api/handlers/dto/ directory
   - quiz_dto.go
   - attempt_dto.go
   - auth_dto.go
   - feedback_dto.go
6. Create internal/pkg/ directory
   - activity/ with activity_logger.go
   - validator/ with validator.go
     Expected Outcome: New empty directories ready for implementation

---

Phase 2: Implement Domain Layer
Objective: Define domain entities and domain errors
Steps:
2.1 Create Domain Errors (internal/domain/errors.go)

- Create custom error types: NotFoundError, ValidationError, AuthorizationError, BusinessLogicError
- Add helper methods to extract HTTP status codes
  2.2 Create Domain Entities (internal/domain/domain.go)
- Quiz entity with methods: Validate(), IsPublic(), CanAccess(userID)
- Question entity with: Validate(), HasCorrectAnswer()
- Option entity with: Validate()
- QuizAttempt entity with: IsFinished(), CanSubmitAnswer()
- Material entity with: Validate(), IsVideo(), IsFile()
- Topic entity with: Validate()
- User entity with: HasEnoughTokens(), DeductTokens()
- TokenTransaction entity
  Expected Outcome: Pure domain entities with business logic, no external dependencies

---

Phase 3: Implement Repository Layer
Objective: Create data access layer using sqlc queries
Steps:
3.1 Create User Repository (internal/repository/user_repository.go)

- Interface: UserRepository
- Methods: GetByID(), GetByEmail(), Create(), UpdateTokenBalance()
- Implementation: wrap sqlc queries, convert db.User to \*domain.User
  3.2 Create Quiz Repository (internal/repository/quiz_repository.go)
- Interface: QuizRepository
- Methods: Create(), GetByID(), ListByCreator(), ListPublic(), Delete()
- Implementation: handle pgtype conversions, map to domain entities
  3.3 Create Question Repository (internal/repository/question_repository.go)
- Interface: QuestionRepository
- Methods: Create(), ListByQuizID(), DeleteByQuizID()
  3.4 Create Material Repository (internal/repository/material_repository.go)
- Interface: MaterialRepository
- Methods: Create(), GetByID(), ListByUserID(), Delete()
  3.5 Create Topic Repository (internal/repository/topic_repository.go)
- Interface: TopicRepository
- Methods: GetOrCreateByTitleAndUser(), GetByID()
  3.6 Create Attempt Repository (internal/repository/attempt_repository.go)
- Interface: AttemptRepository
- Methods: Create(), GetByID(), ListByUser(), UpdateScoreAndEndTime(), UpsertAnswer(), GetAnswerCorrectness()
  3.7 Create Token Repository (internal/repository/token_repository.go)
- Interface: TokenRepository
- Methods: CreateTransaction()
  Expected Outcome: Repository interfaces and implementations using sqlc-generated queries

---

Phase 4: Implement Shared Utilities
Objective: Extract cross-cutting concerns
Steps:
4.1 Create Validator Package (internal/pkg/validator/validator.go)

- Create Validator struct wrapping go-playground/validator
- Add methods: ValidateStruct(), ValidateVar()
- Register custom validators if needed (e.g., UUID format)
  4.2 Create Activity Logger (internal/pkg/activity/activity_logger.go)
- Move logActivity from handler.go to this package
- Create ActivityLogger struct with database dependency
- Methods: LogUserAction(), LogError(), LogQuizActivity()
  Expected Outcome: Reusable utilities for validation and activity logging

---

Phase 5: Implement Notification Service
Objective: Extract Discord notification logic
Steps:
5.1 Create Discord Service (internal/service/notification/discord_service.go)

- Move Discord structs from handler.go to this file
- Create DiscordService struct with HTTP client
- Methods: NotifyQuizCreated(), NotifyQuizDeleted(), NotifyAttemptStarted(), NotifyAttemptFinished(), NotifyError(), NotifySignup(), NotifyLogin(), NotifyLogout()
- Keep the goroutine-based async notification pattern
  Expected Outcome: Centralized notification service

---

Phase 6: Implement Token Service
Objective: Handle token balance and transactions
Steps:
6.1 Create Token Service (internal/service/token_service.go)

- Create TokenService struct with TokenRepository and UserRepository
- Methods:
  - CheckBalance(ctx, userID, requiredTokens) error
  - RecordUsage(ctx, userID, promptTokens, candidateTokens, totalTokens) error
  - AddBalance(ctx, userID, amount) error
    Expected Outcome: Service for all token-related operations

---

Phase 7: Implement Material Service
Objective: Handle file and video material processing
Steps:
7.1 Create Material Service (internal/service/material_service.go)

- Create MaterialService struct with MaterialRepository, UserRepository, YoutubeTranscript
- Methods:
  - ProcessUploadedFiles(ctx, userID, fileHeaders) ([]gemini.DocumentFile, error)
    - Validate files (size, type)
    - Save to temp directory
    - Create material records in DB
  - ProcessVideoURLs(ctx, userID, urls) ([]gemini.DocumentFile, error)
    - Fetch transcripts from YouTube
    - Save transcripts to temp files
    - Create material records with video URLs
  - CleanupTempFiles(tempPaths) error
  - CreateMaterialRecord(ctx, userID, title, url) (\*domain.Material, error)
    Expected Outcome: Service handling all material processing logic

---

Phase 8: Implement Topic Service
Objective: Handle topic management
Steps:
8.1 Create Topic Service (internal/service/topic_service.go)

- Create TopicService struct with TopicRepository
- Methods:
  - GetOrCreate(ctx, userID, title) (\*domain.Topic, error) - Check if topic exists for user - Create if not exists - Use caching within transaction to avoid duplicate creations
    Expected Outcome: Service for topic get-or-create logic

---

Phase 9: Implement Quiz Service (Part 1 - Basic CRUD)
Objective: Implement basic quiz operations without generation
Steps:
9.1 Create Quiz Service (internal/service/quiz_service.go)

- Create QuizService struct with dependencies:
  - QuizRepository
  - QuestionRepository
  - MaterialRepository
  - UserRepository
  - TopicService
  - TokenService
  - DiscordService
  - ActivityLogger
  - Validator
- Methods:
  - GetQuiz(ctx, quizID) (\*domain.Quiz, error)
  - ListUserQuizzes(ctx, userID) ([]\*domain.Quiz, error)
  - ListPublicQuizzes(ctx) ([]\*domain.Quiz, error)
  - DeleteQuiz(ctx, userID, quizID) error - Verify ownership - Delete quiz - Send notification - Log activity
    Expected Outcome: Basic quiz CRUD operations

---

Phase 10: Implement Quiz Service (Part 2 - Generation)
Objective: Implement the complex quiz generation logic
Steps:
10.1 Add to Quiz Service (internal/service/quiz_service.go)

- Method: GenerateQuiz(ctx, userID, req GenerateQuizRequest) (\*domain.Quiz, error)
  - Validate request (max questions, difficulty, custom prompt)
  - Process uploaded files (using MaterialService)
  - Process video URLs (using MaterialService)
  - Check if any valid content processed
  - Call Gemini.ProcessDocuments() to generate quiz
  - Validate Gemini response (has questions, correct answers)
  - Begin transaction
  - Record token usage (using TokenService)
  - Create quiz record
  - Create and link materials
  - Process questions:
    - For each question, get or create topic (using TopicService)
    - Create question
    - Create answers
    - Validate exactly one correct answer
  - Commit transaction
  - Log activity
  - Send Discord notification
  - Return created quiz
- Helper method: ValidateDifficulty(difficulty string) error
- Helper method: ValidateCustomPrompt(prompt string) error
  Expected Outcome: Complete quiz generation service with all orchestrations

---

Phase 11: Implement Attempt Service
Objective: Handle quiz attempt operations
Steps:
11.1 Create Attempt Service (internal/service/attempt_service.go)

- Create AttemptService struct with:
  - AttemptRepository
  - QuizRepository
  - UserRepository
  - DiscordService
  - ActivityLogger
- Methods:
  - CreateAttempt(ctx, userID, quizID) (\*domain.QuizAttempt, error)
    - Verify quiz exists
    - Create attempt
    - Log activity
    - Send notification
  - GetAttempt(ctx, userID, attemptID) (\*domain.QuizAttempt, error)
    - Verify ownership
    - Fetch attempt with answers
  - SaveAnswer(ctx, userID, attemptID, questionID, answerID) error
    - Verify ownership and attempt not finished
    - Get answer correctness
    - Upsert answer
  - FinishAttempt(ctx, userID, attemptID) (score int32, error)
    - Verify ownership and not finished
    - Calculate score
    - Update end time
    - Log activity
    - Send notification
  - ListUserAttempts(ctx, userID) ([]\*domain.QuizAttempt, error)
    Expected Outcome: Complete attempt management service

---

Phase 12: Implement User Service
Objective: Handle user operations
Steps:
12.1 Create User Service (internal/service/user_service.go)

- Create UserService struct with UserRepository
- Methods:
  - GetOrCreateUser(ctx, googleUserInfo) (\*domain.User, bool, error) - Check if user exists by email - Create if not exists - Return user and isNewUser flag
    Expected Outcome: User management service

---

Phase 13: Create DTOs
Objective: Define request/response DTOs for API layer
Steps:
13.1 Create Quiz DTOs (internal/api/handlers/dto/quiz_dto.go)

- GenerateQuizRequest: files, videoUrls, maxQuestions, difficulty, customPrompt
- GetQuizResponse, ListQuizzesResponse
- Helper functions: FromDomainQuiz(), ToDomainQuizGenerateRequest()
  13.2 Create Attempt DTOs (internal/api/handlers/dto/attempt_dto.go)
- CreateAttemptRequest, SaveAnswerRequest
- AttemptResponse, AttemptListResponse
- Helper functions: FromDomainAttempt(), ToDomainSaveAnswerRequest()
  13.3 Create Auth DTOs (internal/api/handlers/dto/auth_dto.go)
- UserProfileResponse
- Helper functions: FromDomainUser()
  13.4 Create Feedback DTOs (internal/api/handlers/dto/feedback_dto.go)
- CreateFeedbackRequest, FeedbackResponse
  Expected Outcome: All DTOs defined with validation tags

---

Phase 14: Create New Feedback Handler
Objective: Migrate feedback functionality first (simplest handler)
Steps:
14.1 Create Feedback Handler (internal/api/handlers/feedback_handler.go)

- Create FeedbackHandler struct with FeedbackService (simplify - use repos directly for now)
- Method: HandleCreateFeedback(c \*gin.Context)
  - Parse request with validation
  - Get user from context
  - Call service/repository
  - Log activity
  - Send Discord notification
  - Return response
    14.2 Update Routes (internal/api/routes.go)
- Update route to use new handler
  Expected Outcome: First handler refactored and tested

---

Phase 15: Create New Auth Handler
Objective: Refactor authentication handlers
Steps:
15.1 Create Auth Handler (internal/api/handlers/auth_handler.go)

- Create AuthHandler struct with UserService, OauthConfig
- Methods:
  - HandleGoogleLogin(c \*gin.Context) - mostly unchanged
  - HandleGoogleCallback(c \*gin.Context)
    - Call UserService.GetOrCreateUser()
    - Log activity
    - Send notification (signup/login)
    - Set session
  - HandleLogout(c \*gin.Context)
    - Get user from context
    - Clear session
    - Log activity
    - Send notification
  - HandleUserProfile(c \*gin.Context) - return profile DTO
  - HandleAuthStatus(c \*gin.Context) - check authentication
    15.2 Update Routes (internal/api/routes.go)
- Update routes to use new handler
  Expected Outcome: Auth handlers refactored

---

Phase 16: Create New Attempt Handler
Objective: Refactor attempt handlers
Steps:
16.1 Create Attempt Handler (internal/api/handlers/attempt_handler.go)

- Create AttemptHandler struct with AttemptService
- Methods:
  - HandleCreateQuizAttempt(c \*gin.Context)
    - Parse quiz ID from params
    - Call service
    - Return response
  - HandleGetQuizAttempt(c \*gin.Context)
  - HandleSaveAttemptAnswer(c \*gin.Context)
  - HandleFinishQuizAttempt(c \*gin.Context)
  - HandleListUserAttempts(c \*gin.Context)
    16.2 Update Routes (internal/api/routes.go)
- Update routes to use new handler
  Expected Outcome: Attempt handlers refactored and simplified

---

Phase 17: Create New Quiz Handler (Part 1 - Read Operations)
Objective: Refactor quiz read operations first
Steps:
17.1 Create Quiz Handler (internal/api/handlers/quiz_handler.go)

- Create QuizHandler struct with QuizService
- Methods:
  - HandleGetQuiz(c \*gin.Context)
    - Parse quiz ID
    - Call service
    - Convert to DTO
    - Return response
  - HandleListUserQuizzes(c \*gin.Context)
  - HandleListPublicQuizzes(c \*gin.Context)
  - HandleDeleteQuiz(c \*gin.Context)
    17.2 Update Routes (internal/api/routes.go)
- Update routes to use new handler
  Expected Outcome: Quiz read operations refactored

---

Phase 18: Create New Quiz Handler (Part 2 - Generate Operation)
Objective: Refactor the complex quiz generation handler
Steps:
18.1 Add to Quiz Handler (internal/api/handlers/quiz_handler.go)

- Method: HandleGenerateQuiz(c \*gin.Context)
  - Get user from context
  - Parse multipart form
  - Bind request DTO with validation
  - Call QuizService.GenerateQuiz()
  - Return response
    18.2 Update Routes (internal/api/routes.go)
- Update route to use new handler
  Expected Outcome: Quiz generation handler refactored and simplified

---

Phase 19: Update Dependency Injection
Objective: Wire all dependencies in main.go
Steps:
19.1 Update cmd/server/main.go

- Initialize repositories
- Initialize services with dependencies
- Initialize handlers with services
- Update api.SetupRoutes() to accept individual handlers instead of one big handler struct
  19.2 Update api/routes.go
- Accept individual handler parameters
- Setup all routes with new handlers
  Expected Outcome: Complete dependency injection with clean architecture

---

Phase 20: Cleanup and Remove Old Code
Objective: Remove refactored code and update imports
Steps:
20.1 Remove old handler files

- Delete or archive old quiz.go, attempt.go, auth.go, handler.go
  20.2 Clean up unused code
- Remove unused imports across files
- Remove internal/models/models.go if redundant with domain entities
- Keep internal/models for any remaining shared DTOs if needed
  20.3 Update documentation
- Add comments to explain the architecture
- Update any README if exists
  Expected Outcome: Clean codebase with old handlers removed

---

Phase 21: Final Verification
Objective: Ensure everything compiles and runs
Steps:
21.1 Build verification

- Run go build ./cmd/server
- Fix any compilation errors
  21.2 Import cleanup
- Remove unused imports
- Format code with gofmt
  21.3 Basic smoke test
- Start server
- Test basic endpoints (auth status, quiz listing)
  Expected Outcome: Working server with new architecture

---

Estimated Timeline
| Phase | Description | Complexity |
|-------|-------------|-------------|
| 1 | Setup directories | Low |
| 2 | Domain layer | Low |
| 3 | Repository layer | Medium |
| 4 | Shared utilities | Low |
| 5 | Notification service | Low |
| 6 | Token service | Low |
| 7 | Material service | Medium |
| 8 | Topic service | Low |
| 9 | Quiz service (CRUD) | Low |
| 10 | Quiz service (generation) | High |
| 11 | Attempt service | Medium |
| 12 | User service | Low |
| 13 | DTOs | Low |
| 14 | Feedback handler | Low |
| 15 | Auth handler | Medium |
| 16 | Attempt handler | Low |
| 17 | Quiz handler (reads) | Low |
| 18 | Quiz handler (generate) | Medium |
| 19 | Dependency injection | Medium |
| 20 | Cleanup | Low |
| 21 | Verification | Low |
Total Phases: 21
Most Complex: Phase 10 (Quiz Generation Service)

---

Dependencies Between Phases
Phase 1 (Setup)
↓
Phase 2 (Domain)
↓
Phase 3 (Repository Layer)
↓
Phase 4 (Utilities) → Phase 5 (Notification)
↓ ↓
Phase 6 (Token) ────→ Phase 7 (Material)
↓ ↓
Phase 8 (Topic) ──────→ Phase 9-10 (Quiz Service)
↓ ↓
Phase 11 (Attempt) ────→ Phase 12 (User Service)
↓ ↓
Phase 13 (DTOs) ──────→ Phases 14-18 (Handlers)
↓
Phase 19 (Dependency Injection)
↓
Phase 20-21 (Cleanup & Verification)
