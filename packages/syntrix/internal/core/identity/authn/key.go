package authn

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
)

func LoadPrivateKey(path string) (*rsa.PrivateKey, error) {
	// Check file permissions on Unix systems
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err == nil {
			mode := info.Mode().Perm()
			// Check if group or others have any permissions
			if mode&0077 != 0 {
				slog.Warn("Private key file has insecure permissions",
					"path", path,
					"mode", fmt.Sprintf("%04o", mode),
					"recommended", "0600",
				)
			}
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("failed to decode PEM block containing private key")
	}

	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func EnsurePrivateKey(path string) (*rsa.PrivateKey, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		log.Printf("[Warning][AuthN] Private key not found at %s, generating new key...", path)
		// Generate new key
		key, err := GeneratePrivateKey()
		if err != nil {
			return nil, fmt.Errorf("failed to generate key: %w", err)
		}

		// Ensure directory exists
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory: %w", err)
		}

		// Save key
		if err := SavePrivateKey(path, key); err != nil {
			return nil, fmt.Errorf("failed to save key: %w", err)
		}
		return key, nil
	}

	// Load existing key
	return LoadPrivateKey(path)
}

func SavePrivateKey(path string, key *rsa.PrivateKey) error {
	// Create file with secure permissions (0600 = owner read/write only)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer file.Close()

	privateKeyBytes := x509.MarshalPKCS1PrivateKey(key)
	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privateKeyBytes,
	}

	return pem.Encode(file, block)
}

func GeneratePrivateKey() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, 2048)
}
