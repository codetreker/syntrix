package authn

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

const (
	AlgoArgon2id = "argon2id"
	AlgoBcrypt   = "bcrypt"
)

// Argon2id params
var (
	argonTime    = uint32(3)
	argonMemory  = uint32(64 * 1024)
	argonThreads = uint8(1)
	argonKeyLen  = uint32(32)
	saltLen      = 16
)

// HashPassword hashes a password using Argon2id (default) or Bcrypt
func HashPassword(password string) (string, string, error) {
	// Default to Argon2id
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", "", err
	}

	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	// Format: $argon2id$v=19$m=65536,t=3,p=1$salt$hash
	encodedSalt := base64.RawStdEncoding.EncodeToString(salt)
	encodedHash := base64.RawStdEncoding.EncodeToString(hash)
	fullHash := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads, encodedSalt, encodedHash)

	return fullHash, AlgoArgon2id, nil
}

// VerifyPassword verifies a password against a hash
func VerifyPassword(password, hash, algo string) (bool, error) {
	switch algo {
	case AlgoArgon2id:
		return verifyArgon2id(password, hash)
	case AlgoBcrypt:
		return verifyBcrypt(password, hash)
	default:
		return false, errors.New("unsupported password algorithm")
	}
}

func verifyArgon2id(password, hash string) (bool, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 {
		return false, errors.New("invalid argon2id hash format")
	}

	// parts[0] is empty
	// parts[1] is "argon2id"
	// parts[2] is "v=19"
	// parts[3] is "m=...,t=...,p=..."
	// parts[4] is salt
	// parts[5] is hash

	var version int
	_, err := fmt.Sscanf(parts[2], "v=%d", &version)
	if err != nil {
		return false, err
	}
	if version != argon2.Version {
		return false, errors.New("incompatible argon2 version")
	}

	var memory uint32
	var timeParam uint32
	var threads uint8
	_, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeParam, &threads)
	if err != nil {
		return false, err
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}

	decodedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}

	newHash := argon2.IDKey([]byte(password), salt, timeParam, memory, threads, uint32(len(decodedHash)))

	// Use constant-time comparison to prevent timing attacks
	if subtle.ConstantTimeCompare(newHash, decodedHash) == 1 {
		return true, nil
	}
	return false, nil
}

func verifyBcrypt(password, hash string) (bool, error) {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if err == nil {
		return true, nil
	}
	if err == bcrypt.ErrMismatchedHashAndPassword {
		return false, nil
	}
	return false, err
}
