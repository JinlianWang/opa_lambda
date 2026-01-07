package policyloader

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"

	log "github.com/sirupsen/logrus"
)

// S3PolicyLoader loads policies from S3.
type S3PolicyLoader struct {
	bucketName string
	s3Client   s3iface.S3API
	mu         sync.RWMutex
	cache      map[string]string
}

// NewS3PolicyLoader creates a new S3PolicyLoader.
func NewS3PolicyLoader(bucketName string) (*S3PolicyLoader, error) {
	config := aws.Config{
		Region: aws.String(os.Getenv("AWS_REGION")),
	}

	sess, err := session.NewSession(&config)
	if err != nil {
		return nil, err
	}

	s3Client := s3.New(sess)
	return &S3PolicyLoader{
		bucketName: bucketName,
		s3Client:   s3Client,
		cache:      make(map[string]string),
	}, nil
}

// NewS3PolicyLoaderWithClient creates a new S3PolicyLoader with a custom S3 client.
func NewS3PolicyLoaderWithClient(s3Client s3iface.S3API, bucketName string) *S3PolicyLoader {
	return &S3PolicyLoader{
		bucketName: bucketName,
		s3Client:   s3Client,
		cache:      make(map[string]string),
	}
}

// LoadPolicy loads a policy from S3.
func (loader *S3PolicyLoader) LoadPolicy(ctx context.Context, policyName string) (string, error) {
	objectKey, err := KeyToFilename(policyName)
	if err != nil {
		return "", err
	}

	// Serve from in-memory cache when available to avoid repeated S3 calls on warm invocations.
	loader.mu.RLock()
	if cached, ok := loader.cache[policyName]; ok {
		loader.mu.RUnlock()
		return cached, nil
	}
	loader.mu.RUnlock()

	input := &s3.GetObjectInput{
		Bucket: aws.String(loader.bucketName),
		Key:    aws.String(objectKey),
	}

	result, err := loader.s3Client.GetObjectWithContext(ctx, input)
	if err != nil {
		log.Errorf("failed to get policy %s from S3: %v", policyName, err)
		return "", errors.New("failed to get policy from S3")
	}
	defer result.Body.Close()

	content, err := io.ReadAll(result.Body)
	if err != nil {
		log.Errorf("failed to read policy content from %s: %v", policyName, err)
		return "", errors.New("failed to read policy content from S3")
	}

	policy := string(content)

	// Cache the freshly fetched policy for subsequent invocations.
	loader.mu.Lock()
	loader.cache[policyName] = policy
	loader.mu.Unlock()

	return policy, nil
}
