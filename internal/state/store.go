package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jtsang4/larky/internal/contract"
)

const databaseVersion = 1

type ProcessedEvent struct {
	RequestID string    `json:"request_id,omitempty"`
	SeenAt    time.Time `json:"seen_at"`
	Source    string    `json:"source,omitempty"`
	Synthetic bool      `json:"synthetic,omitempty"`
}

type InboxItem struct {
	Reply contract.RoutedReply `json:"reply"`
}

type Database struct {
	Version      int                                     `json:"version"`
	Requests     map[string]*contract.InteractionRequest `json:"requests"`
	Deliveries   map[string]contract.Delivery            `json:"deliveries"`
	Idempotency  map[string]string                       `json:"idempotency"`
	Events       map[string]ProcessedEvent               `json:"events"`
	Inbox        map[string][]*InboxItem                 `json:"inbox"`
	Unrouted     []contract.IncomingEvent                `json:"unrouted,omitempty"`
	Verification []contract.IncomingEvent                `json:"verification_events,omitempty"`
}

type Store struct {
	path     string
	lockPath string
}

func New(path string) *Store {
	return &Store{path: path, lockPath: path + ".lock"}
}

func (s *Store) View(fn func(*Database) error) error {
	return s.withLock(false, func() error {
		db, err := s.read()
		if err != nil {
			return err
		}
		return fn(db)
	})
}

func (s *Store) Update(fn func(*Database) error) error {
	return s.withLock(true, func() error {
		db, err := s.read()
		if err != nil {
			return err
		}
		if err := fn(db); err != nil {
			return err
		}
		return s.write(db)
	})
}

func (s *Store) withLock(exclusive bool, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	lock, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open state lock: %w", err)
	}
	defer lock.Close()
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(lock.Fd()), mode); err != nil {
		return fmt.Errorf("lock state: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

func (s *Store) read() (*Database, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return newDatabase(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	db := newDatabase()
	if err := json.Unmarshal(data, db); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	normalize(db)
	if db.Version != databaseVersion {
		return nil, fmt.Errorf("unsupported state version %d", db.Version)
	}
	return db, nil
}

func (s *Store) write(db *Database) error {
	normalize(db)
	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}

func newDatabase() *Database {
	return &Database{
		Version:      databaseVersion,
		Requests:     make(map[string]*contract.InteractionRequest),
		Deliveries:   make(map[string]contract.Delivery),
		Idempotency:  make(map[string]string),
		Events:       make(map[string]ProcessedEvent),
		Inbox:        make(map[string][]*InboxItem),
		Unrouted:     make([]contract.IncomingEvent, 0),
		Verification: make([]contract.IncomingEvent, 0),
	}
}

func normalize(db *Database) {
	if db.Version == 0 {
		db.Version = databaseVersion
	}
	if db.Requests == nil {
		db.Requests = make(map[string]*contract.InteractionRequest)
	}
	if db.Deliveries == nil {
		db.Deliveries = make(map[string]contract.Delivery)
	}
	if db.Idempotency == nil {
		db.Idempotency = make(map[string]string)
	}
	if db.Events == nil {
		db.Events = make(map[string]ProcessedEvent)
	}
	if db.Inbox == nil {
		db.Inbox = make(map[string][]*InboxItem)
	}
}

func InboxKey(platform contract.Platform, sessionID string) string {
	return string(platform) + ":" + sessionID
}
