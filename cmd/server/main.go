package main

import (
	"context"
	"database/sql" // Added for session store connection
	"encoding/gob"

	// "fmt" // Removed unused import
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"quizbuilderai/internal/api"
	"quizbuilderai/internal/api/handlers" // Add import for the new handlers package
	"quizbuilderai/internal/db"
	"quizbuilderai/internal/gemini"

	sessions "github.com/gin-contrib/sessions"           // Added base sessions import
	gsessions "github.com/gin-contrib/sessions/postgres" // Re-added this import
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	// "github.com/antonlindstrom/pgstore" // Removed unused import
	_ "github.com/jackc/pgx/v5/stdlib" // Import pgx driver for database/sql
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

var (
	GoogleOauthConfig *oauth2.Config
	// Session secret key will be loaded in init() after godotenv
	// Store name
	storeName = "quizbuilderai_session"
)

var sessionSecretKey []byte // Declare here so it's accessible in main()

func init() {
	// Load environment variables FIRST
	log.Println("Attempting to load .env file...")
	err := godotenv.Load()
	if err != nil {
		// Only treat "file not found" as a warning, other errors are fatal
		if !os.IsNotExist(err) {
			log.Fatalf("FATAL: Error loading .env file: %v", err)
		} else {
			log.Println("Warning: .env file not found. Relying on system environment variables.")
		}
	} else {
		log.Println(".env file loaded successfully.")
	}

	// Load and log session secret AFTER loading .env
	secret := os.Getenv("SESSION_SECRET")
	if secret == "" {
		log.Println("WARNING: SESSION_SECRET environment variable is not set or empty!")
		// Consider making this fatal if the secret is absolutely required and not found
		// log.Fatal("FATAL: SESSION_SECRET must be set.")
	} else {
		log.Printf("DEBUG: SESSION_SECRET loaded with length: %d", len(secret))
	}
	sessionSecretKey = []byte(secret) // Assign to the package-level variable

	// Register types needed for session storage
	// Register the *new* type from the handlers package. Gob needs to know about the concrete type.
	// If sessions were saved with the old type path, clearing sessions might be necessary.
	// For now, ensure the new type is registered correctly.
	gob.Register(handlers.UserProfile{})

	// --- Google OAuth Configuration ---
	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	redirectURL := os.Getenv("GOOGLE_REDIRECT_URL") // e.g., "http://localhost:8080/auth/google/callback"

	if clientID == "" || clientSecret == "" || redirectURL == "" {
		log.Fatal("FATAL: GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET, and GOOGLE_REDIRECT_URL environment variables must be set.")
	}

	GoogleOauthConfig = &oauth2.Config{
		RedirectURL:  redirectURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes: []string{
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
		},
		Endpoint: google.Endpoint,
	}
}

func main() {
	// Environment variables are now loaded in init()

	// Set up context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connect to database
	database, err := db.NewDB(ctx)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	// Initialize Gemini client
	geminiClient, err := gemini.NewClient()
	if err != nil {
		log.Fatalf("Failed to initialize Gemini client: %v", err)
	}
	defer geminiClient.Close()

	// Set up Gin router
	router := gin.Default()

	// --- Session Configuration ---
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("FATAL: DATABASE_URL environment variable must be set.")
	}

	// Create a standard sql.DB connection pool specifically for the session store
	// using the pgx driver via the stdlib adapter.
	sessionDB, err := sql.Open("pgx", dbURL)
	if err != nil {
		log.Fatalf("Failed to open database connection for session store: %v", err)
	}
	defer sessionDB.Close() // Ensure the session DB connection is closed

	// Ping the database to verify the connection.
	if err := sessionDB.Ping(); err != nil {
		log.Fatalf("Failed to ping database for session store: %v", err)
	}

	// Use the constructor from gin-contrib/sessions/postgres, passing the *sql.DB pool.
	log.Printf("DEBUG: Initializing session store with key length: %d", len(sessionSecretKey))
	store, err := gsessions.NewStore(sessionDB, sessionSecretKey)
	if err != nil {
		// Check if the error is specifically about the hash key
		if err.Error() == "securecookie: hash key is not set" {
			log.Fatalf("FATAL: Failed to create postgres session store because SESSION_SECRET is missing or empty after loading env vars. Key length provided: %d", len(sessionSecretKey))
		}
		log.Fatalf("Failed to create postgres session store: %v", err)
	}
	// Note: Cleanup for expired sessions in gsessions might require calling
	// store.Cleanup() periodically or relying on its internal mechanism.
	// Check gsessions documentation if cleanup is needed.
	// defer store.Close() // Check if gsessions.Store has a Close method if needed.

	// Set session options using the wrapper's Options method
	store.Options(sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7, // Example: 7 days
		Secure:   false,     // TODO: Set Secure=true in production (requires HTTPS) - Get from ENV?
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode, // Use http.SameSite constants
	})

	// Use the session middleware globally, passing the wrapper store (*gsessions.Store)
	router.Use(sessions.Sessions(storeName, store))

	// Set up API handlers
	handler := handlers.NewHandler(GoogleOauthConfig, storeName, database, geminiClient) // Use NewHandler from handlers package
	api.SetupRoutes(router, handler)

	// Get port from environment variable or use default
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Create HTTP server
	server := &http.Server{
		Addr:    ":" + port,
		Handler: router,
	}

	// Start server in a goroutine
	go func() {
		log.Printf("Server listening on port %s", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	// Set up graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	// Give server 5 seconds to shut down gracefully
	ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited properly")
}
