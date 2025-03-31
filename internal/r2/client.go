package r2

import (
	"context"
	"fmt"
	"io"
	"log"
	"mime" // Add mime package
	"net/url"
	"os"
	"path"          // Use path for URL joining
	"path/filepath" // Add filepath package

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types" // Add types package
	"github.com/google/uuid"
)

// Client holds the necessary configuration for interacting with Cloudflare R2.
type Client struct {
	s3Client   *s3.Client
	bucketName string
	publicURL  string // Base public URL for the bucket (e.g., https://pub-xxxxxxxx.r2.dev)
}

// NewClient creates and configures a new R2 client instance using environment variables.
// It returns (nil, nil) if R2 environment variables are not fully configured,
// allowing the application to proceed with R2 uploads disabled.
func NewClient() (*Client, error) {
	accountID := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	bucketName := os.Getenv("R2_BUCKET_NAME")
	accessKeyID := os.Getenv("R2_ACCESS_KEY_ID")
	secretAccessKey := os.Getenv("R2_SECRET_ACCESS_KEY")
	publicURL := os.Getenv("R2_PUBLIC_URL")

	// Check if all required R2 variables are set
	if accountID == "" || bucketName == "" || accessKeyID == "" || secretAccessKey == "" || publicURL == "" {
		log.Println("WARN: Cloudflare R2 environment variables not fully configured (CLOUDFLARE_ACCOUNT_ID, R2_BUCKET_NAME, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY, R2_PUBLIC_URL). R2 uploads will be skipped.")
		return nil, nil // Indicate optional setup by returning nil client and nil error
	}

	// Custom endpoint resolver for Cloudflare R2
	r2Resolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		// R2 endpoint format: https://<ACCOUNT_ID>.r2.cloudflarestorage.com
		return aws.Endpoint{
			URL: fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID),
		}, nil
	})

	// Load AWS SDK configuration
	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithEndpointResolverWithOptions(r2Resolver),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
		config.WithRegion("auto"), // R2 is region-agnostic, 'auto' is a common setting
	)
	if err != nil {
		// Return an error here as this indicates a problem loading the config itself
		return nil, fmt.Errorf("failed to load AWS SDK config for R2: %w", err)
	}

	// Create the S3 client from the configuration
	s3Client := s3.NewFromConfig(cfg)

	log.Printf("INFO: R2 Client initialized for bucket '%s'", bucketName)
	return &Client{
		s3Client:   s3Client,
		bucketName: bucketName,
		publicURL:  publicURL,
	}, nil
}

// UploadFile uploads content from an io.Reader to a specified path within the R2 bucket.
// The object key is constructed as "material/<userID>/<materialID>/<filename>".
// It returns the publicly accessible URL of the uploaded file or an error.
func (c *Client) UploadFile(ctx context.Context, userID uuid.UUID, materialID uuid.UUID, filename string, content io.Reader) (string, error) {
	// Check if the client was initialized (it might be nil if env vars were missing)
	if c == nil || c.s3Client == nil {
		return "", fmt.Errorf("R2 client not initialized, skipping upload")
	}

	// Construct the object key using the desired structure
	objectKey := fmt.Sprintf("material/%s/%s/%s", userID.String(), materialID.String(), filename)

	// Determine Content-Type
	contentType := mime.TypeByExtension(filepath.Ext(filename))
	if contentType == "" {
		contentType = "application/octet-stream" // Default if extension is unknown
	}

	// Perform the upload using PutObject
	_, err := c.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(c.bucketName),
		Key:         aws.String(objectKey),
		Body:        content,
		ACL:         types.ObjectCannedACLPublicRead, // Explicitly set ACL for public access
		ContentType: aws.String(contentType),         // Set Content-Type
	})

	if err != nil {
		// Return specific error for upload failure
		return "", fmt.Errorf("failed to upload file to R2 (key: %s): %w", objectKey, err)
	}

	// Construct the public URL safely
	baseURL, err := url.Parse(c.publicURL)
	if err != nil {
		// This should ideally not happen if publicURL env var is validated or correct
		log.Printf("ERROR: Failed to parse R2 public base URL '%s': %v", c.publicURL, err)
		return "", fmt.Errorf("invalid R2 public base URL configured")
	}
	// Use path.Join to handle slashes correctly, then ensure it's URL encoded if needed (usually path handles this)
	baseURL.Path = path.Join(baseURL.Path, objectKey)

	publicFileURL := baseURL.String()
	log.Printf("INFO: Successfully uploaded file to R2: %s", publicFileURL)
	return publicFileURL, nil
}
