package minio_storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	storage_interfaces "github.com/evolution-foundation/evolution-go/pkg/storage/interfaces"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type MinioMediaStorage struct {
	client     *minio.Client
	bucketName string
}

func (m *MinioMediaStorage) Health(ctx context.Context) error {
	if m == nil || m.client == nil || strings.TrimSpace(m.bucketName) == "" || ctx == nil {
		return errors.New("legacy media storage is unavailable")
	}
	exists, err := m.client.BucketExists(ctx, m.bucketName)
	if err != nil {
		return fmt.Errorf("check legacy media storage health: %w", err)
	}
	if !exists {
		return errors.New("legacy media bucket is unavailable")
	}
	return nil
}

const legacyMediaPrefix = "evolution-go-medias/"

var legacyMediaNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$`)

// generateFilePath creates a simple media folder structure
// Format: evolution-go-medias/{filename}
func generateFilePath(fileName string) (string, error) {
	if !legacyMediaNamePattern.MatchString(fileName) || fileName == "." || fileName == ".." {
		return "", errors.New("invalid legacy media object name")
	}
	return legacyMediaPrefix + fileName, nil
}

// resolveFilePath accepts either a validated legacy filename or the same name
// under the fixed compatibility prefix. Other namespaces are rejected.
func (m *MinioMediaStorage) resolveFilePath(_ context.Context, fileNameOrPath string) (string, error) {
	if strings.HasPrefix(fileNameOrPath, legacyMediaPrefix) {
		return generateFilePath(strings.TrimPrefix(fileNameOrPath, legacyMediaPrefix))
	}
	return generateFilePath(fileNameOrPath)
}

func NewMinioMediaStorage(
	endpoint,
	accessKeyID,
	secretAccessKey,
	bucketName,
	region string,
	useSSL bool,
) (storage_interfaces.MediaStorage, error) {
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
		Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create MinIO client: %w", err)
	}

	return &MinioMediaStorage{
		client:     client,
		bucketName: bucketName,
	}, nil
}

func (m *MinioMediaStorage) Store(ctx context.Context, data []byte, fileName string, contentType string) (string, error) {
	// Generate organized file path
	filePath, err := generateFilePath(fileName)
	if err != nil {
		return "", err
	}
	reader := bytes.NewReader(data)

	_, err = m.client.PutObject(ctx, m.bucketName, filePath, reader, int64(len(data)), minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return "", fmt.Errorf("failed to store object: %w", err)
	}

	// Gerando URL assinada com validade de 7 dias
	reqParams := make(url.Values)
	presignedURL, err := m.client.PresignedGetObject(ctx, m.bucketName, filePath, time.Hour*24*7, reqParams)
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	return presignedURL.String(), nil
}

func (m *MinioMediaStorage) Delete(ctx context.Context, fileName string) error {
	// Resolve the full path for the file
	filePath, err := m.resolveFilePath(ctx, fileName)
	if err != nil {
		return fmt.Errorf("failed to resolve file path: %w", err)
	}

	err = m.client.RemoveObject(ctx, m.bucketName, filePath, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete object: %w", err)
	}
	return nil
}

func (m *MinioMediaStorage) GetURL(ctx context.Context, fileName string) (string, error) {
	// Resolve the full path for the file
	filePath, err := m.resolveFilePath(ctx, fileName)
	if err != nil {
		return "", fmt.Errorf("failed to resolve file path: %w", err)
	}

	// Check if object exists
	_, err = m.client.StatObject(ctx, m.bucketName, filePath, minio.StatObjectOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get object stats: %w", err)
	}

	// Gerando URL assinada com validade de 7 dias
	reqParams := make(url.Values)
	presignedURL, err := m.client.PresignedGetObject(ctx, m.bucketName, filePath, time.Hour*24*7, reqParams)
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	return presignedURL.String(), nil
}
