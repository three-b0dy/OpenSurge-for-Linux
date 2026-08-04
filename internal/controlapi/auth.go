package controlapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	adminRecordFilename = "admin.json"
	adminSaltLength     = 16
	adminKeyLength      = 32
	adminArgonTime      = 3
	adminArgonMemory    = 64 * 1024
	adminArgonThreads   = 4
)

var (
	ErrAdminAlreadyInitialized = errors.New("administrator is already initialized")
	ErrAdminNotInitialized     = errors.New("administrator is not initialized")
	ErrInvalidCredentials      = errors.New("invalid administrator credentials")
)

// AdminStore persists the one administrator account used by the LAN control
// plane. Implementations must never expose the password or its derived key.
type AdminStore interface {
	Initialized() (bool, error)
	Set(username, password string) error
	Authenticate(username, password string) error
}

type AuthStatus struct {
	Initialized   bool `json:"initialized"`
	Authenticated bool `json:"authenticated"`
}

type adminRecord struct {
	Username string `json:"username"`
	Salt     string `json:"salt"`
	Hash     string `json:"hash"`
}

type FileAdminStore struct {
	dir  string
	path string
	mu   sync.Mutex
}

func NewFileAdminStore(dir string) *FileAdminStore {
	return &FileAdminStore{dir: dir, path: filepath.Join(dir, adminRecordFilename)}
}

func (s *FileAdminStore) Dir() string { return s.dir }

func (s *FileAdminStore) Initialized() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := os.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *FileAdminStore) Set(username, password string) error {
	_, data, err := newAdminRecord(username, password)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.path); err == nil {
		return ErrAdminAlreadyInitialized
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	return writeAtomic(s.path, append(data, '\n'), 0o600)
}

func (s *FileAdminStore) Reset(username, password string) error {
	username, data, err := newAdminRecord(username, password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrAdminNotInitialized
	}
	if err != nil {
		return err
	}
	var record adminRecord
	if err := json.Unmarshal(current, &record); err != nil {
		return fmt.Errorf("decode administrator record: %w", err)
	}
	if strings.TrimSpace(username) != record.Username {
		return ErrInvalidCredentials
	}
	return writeAtomic(s.path, append(data, '\n'), 0o600)
}

func (s *FileAdminStore) Authenticate(username, password string) error {
	s.mu.Lock()
	data, err := os.ReadFile(s.path)
	s.mu.Unlock()
	if errors.Is(err, os.ErrNotExist) {
		return ErrAdminNotInitialized
	}
	if err != nil {
		return err
	}
	var record adminRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return fmt.Errorf("decode administrator record: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(record.Salt)
	if err != nil || len(salt) == 0 {
		return fmt.Errorf("administrator record has invalid salt")
	}
	expected, err := base64.RawStdEncoding.DecodeString(record.Hash)
	if err != nil || len(expected) != adminKeyLength {
		return fmt.Errorf("administrator record has invalid hash")
	}
	actual := derivePassword(password, salt)
	if strings.TrimSpace(username) != record.Username || subtle.ConstantTimeCompare(actual, expected) != 1 {
		return ErrInvalidCredentials
	}
	return nil
}

func derivePassword(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, adminArgonTime, adminArgonMemory, adminArgonThreads, adminKeyLength)
}

func newAdminRecord(username, password string) (string, []byte, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return "", nil, fmt.Errorf("username is required")
	}
	if utf8.RuneCountInString(password) < 12 {
		return "", nil, fmt.Errorf("password must contain at least 12 UTF-8 characters")
	}
	salt := make([]byte, adminSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", nil, fmt.Errorf("generate password salt: %w", err)
	}
	record := adminRecord{
		Username: username,
		Salt:     base64.RawStdEncoding.EncodeToString(salt),
		Hash:     base64.RawStdEncoding.EncodeToString(derivePassword(password, salt)),
	}
	data, err := json.Marshal(record)
	if err != nil {
		return "", nil, err
	}
	return username, data, nil
}
