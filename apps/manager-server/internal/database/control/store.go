package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	dbmysql "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

const CurrentVersion = 1

var (
	ErrNotFound           = errors.New("database control state not found")
	ErrGenerationConflict = errors.New("database control generation conflict")
)

type MigrationRef struct {
	ID              string `json:"id,omitempty"`
	Phase           string `json:"phase,omitempty"`
	Status          string `json:"status,omitempty"`
	ValidationToken string `json:"validationToken,omitempty"`
}

type CachePolicy struct {
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retentionDays"`
}

type FailoverState struct {
	Status        string               `json:"status,omitempty"`
	From          database.BackendKind `json:"from,omitempty"`
	To            database.BackendKind `json:"to,omitempty"`
	Epoch         uint64               `json:"epoch,omitempty"`
	RequestedAtMS int64                `json:"requestedAtMs,omitempty"`
	CompletedAtMS int64                `json:"completedAtMs,omitempty"`
	Reason        string               `json:"reason,omitempty"`
}

// State is only an in-process/control-file representation. Controllers must
// return MySQL.Redacted(), never State or MySQL directly.
type State struct {
	Version             int                  `json:"version"`
	Generation          uint64               `json:"generation"`
	RoutingEpoch        uint64               `json:"routingEpoch"`
	MySQL               dbmysql.Config       `json:"mysql"`
	AdminAuthCopy       string               `json:"-"`
	WritePrimary        database.BackendKind `json:"writePrimary"`
	BusinessReadPrimary database.BackendKind `json:"businessReadPrimary"`
	SystemReadPrimary   database.BackendKind `json:"systemReadPrimary"`
	ReplicationEnabled  bool                 `json:"replicationEnabled"`
	Migration           MigrationRef         `json:"migration"`
	CachePolicy         CachePolicy          `json:"cachePolicy"`
	Failover            FailoverState        `json:"failover"`
	UpdatedAtMS         int64                `json:"updatedAtMs"`
}

func DefaultState() State {
	return State{
		Version: CurrentVersion, WritePrimary: database.BackendSQLite,
		BusinessReadPrimary: database.BackendSQLite, SystemReadPrimary: database.BackendSQLite,
		// Scheduled SQLite cache cleanup is opt-in.  An administrator must
		// explicitly enable it after reviewing the retention policy.
		CachePolicy: CachePolicy{Enabled: false, RetentionDays: 15},
	}
}

func (s State) Validate() error {
	if s.Version != 0 && s.Version != CurrentVersion {
		return fmt.Errorf("unsupported database control version %d", s.Version)
	}
	for name, backend := range map[string]database.BackendKind{
		"write primary": s.WritePrimary, "business read primary": s.BusinessReadPrimary,
		"system read primary": s.SystemReadPrimary,
	} {
		if backend != database.BackendSQLite && backend != database.BackendMySQL {
			return fmt.Errorf("invalid %s %q", name, backend)
		}
	}
	if s.CachePolicy.RetentionDays < 1 || s.CachePolicy.RetentionDays > 3650 {
		return errors.New("SQLite cache retention days must be between 1 and 3650")
	}
	if s.MySQL.Host != "" {
		if err := s.MySQL.Validate(); err != nil {
			return err
		}
	}
	if (s.WritePrimary == database.BackendMySQL || s.BusinessReadPrimary == database.BackendMySQL ||
		s.SystemReadPrimary == database.BackendMySQL || s.ReplicationEnabled) && s.MySQL.Host == "" {
		return errors.New("mysql must be configured before it can be routed or replicated")
	}
	return nil
}

type Store struct {
	path      string
	protector *security.Protector
	mu        sync.Mutex
	cached    atomic.Pointer[State]
}

func NewStore(path string, protector *security.Protector) (*Store, error) {
	if path == "" {
		return nil, errors.New("database control path is required")
	}
	if protector == nil {
		return nil, errors.New("database control protector is required")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database control path: %w", err)
	}
	return &Store{path: absPath, protector: protector}, nil
}

func (s *Store) Path() string { return s.path }

// LoadCached returns the last state validated by Load or Save. It is intended
// for the per-request routing hot path; administrative operations continue to
// use Load so on-disk corruption and backup recovery remain observable.
func (s *Store) LoadCached() (State, error) {
	if cached := s.cached.Load(); cached != nil {
		return *cached, nil
	}
	return s.Load()
}

func (s *Store) Load() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := s.acquireFileLock()
	if err != nil {
		return State{}, err
	}
	defer releaseFileLock(lock)
	state, _, err := s.loadLocked()
	if err == nil {
		s.cached.Store(&state)
	}
	return state, err
}

func (s *Store) Save(expectedGeneration uint64, next State) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return State{}, fmt.Errorf("create database control directory: %w", err)
	}
	lock, err := s.acquireFileLock()
	if err != nil {
		return State{}, err
	}
	defer releaseFileLock(lock)
	current, recovered, err := s.loadLocked()
	if err != nil && !errors.Is(err, ErrNotFound) {
		return State{}, err
	}
	if errors.Is(err, ErrNotFound) {
		current = State{}
	}
	if current.Generation != expectedGeneration {
		return State{}, fmt.Errorf("%w: expected %d, current %d", ErrGenerationConflict,
			expectedGeneration, current.Generation)
	}
	if current.Generation == math.MaxUint64 {
		return State{}, errors.New("database control generation exhausted")
	}
	next.Version = CurrentVersion
	next.Generation = current.Generation + 1
	next.UpdatedAtMS = time.Now().UnixMilli()
	if err := next.Validate(); err != nil {
		return State{}, err
	}
	plaintext, err := marshalPersistedState(next)
	if err != nil {
		return State{}, fmt.Errorf("marshal database control state: %w", err)
	}
	ciphertext, err := s.protector.ProtectString(string(plaintext))
	if err != nil {
		return State{}, fmt.Errorf("encrypt database control state: %w", err)
	}
	if err := s.replaceLocked([]byte(ciphertext), recovered); err != nil {
		return State{}, err
	}
	s.cached.Store(&next)
	return next, nil
}

func (s *Store) Update(expectedGeneration uint64, update func(*State) error) (State, error) {
	if update == nil {
		return State{}, errors.New("database control update callback is required")
	}
	current, err := s.Load()
	if errors.Is(err, ErrNotFound) {
		current = DefaultState()
	} else if err != nil {
		return State{}, err
	}
	if current.Generation != expectedGeneration {
		return State{}, fmt.Errorf("%w: expected %d, current %d", ErrGenerationConflict,
			expectedGeneration, current.Generation)
	}
	if err := update(&current); err != nil {
		return State{}, err
	}
	return s.Save(expectedGeneration, current)
}

func (s *Store) loadLocked() (State, bool, error) {
	state, err := s.readFile(s.path)
	if err == nil {
		return state, false, nil
	}
	mainErr := err
	backup, backupErr := s.readFile(s.path + ".bak")
	if backupErr == nil {
		return backup, true, nil
	}
	if errors.Is(mainErr, os.ErrNotExist) && errors.Is(backupErr, os.ErrNotExist) {
		return State{}, false, ErrNotFound
	}
	return State{}, false, fmt.Errorf("load database control state (main: %v; backup: %v)", mainErr, backupErr)
}

func (s *Store) readFile(path string) (State, error) {
	ciphertext, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	plaintext, err := s.protector.UnprotectString(string(ciphertext))
	if err != nil {
		return State{}, fmt.Errorf("decrypt %s: %w", filepath.Base(path), err)
	}
	state, err := unmarshalPersistedState([]byte(plaintext))
	if err != nil {
		return State{}, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	if err := state.Validate(); err != nil {
		return State{}, fmt.Errorf("validate %s: %w", filepath.Base(path), err)
	}
	return state, nil
}

type persistedStateAlias State
type persistedConfigAlias dbmysql.Config

type persistedMySQLConfig struct {
	persistedConfigAlias
	Password string `json:"password,omitempty"`
}

type persistedState struct {
	persistedStateAlias
	MySQL         persistedMySQLConfig `json:"mysql"`
	AdminAuthCopy string               `json:"adminAuthCopy,omitempty"`
}

func marshalPersistedState(state State) ([]byte, error) {
	return json.Marshal(persistedState{
		persistedStateAlias: persistedStateAlias(state),
		MySQL:               persistedMySQLConfig{persistedConfigAlias: persistedConfigAlias(state.MySQL), Password: state.MySQL.Password},
		AdminAuthCopy:       state.AdminAuthCopy,
	})
}

func unmarshalPersistedState(encoded []byte) (State, error) {
	var persisted persistedState
	if err := json.Unmarshal(encoded, &persisted); err != nil {
		return State{}, err
	}
	state := State(persisted.persistedStateAlias)
	state.MySQL = dbmysql.Config(persisted.MySQL.persistedConfigAlias)
	state.MySQL.Password = persisted.MySQL.Password
	state.AdminAuthCopy = persisted.AdminAuthCopy
	return state, nil
}

func (s *Store) replaceLocked(ciphertext []byte, recovered bool) error {
	dir := filepath.Dir(s.path)
	temp, err := os.CreateTemp(dir, ".database-control-*.tmp")
	if err != nil {
		return fmt.Errorf("create database control temporary file: %w", err)
	}
	tempName := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if err := hardenFilePermissions(tempName); err != nil {
		_ = temp.Close()
		return fmt.Errorf("harden database control temporary file: %w", err)
	}
	if _, err := temp.Write(ciphertext); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write database control temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync database control temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close database control temporary file: %w", err)
	}
	backupPath := s.path + ".bak"
	if recovered {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if _, err := os.Stat(s.path); err == nil {
		if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(s.path, backupPath); err != nil {
			return fmt.Errorf("rotate database control backup: %w", err)
		}
		if err := hardenFilePermissions(backupPath); err != nil {
			return fmt.Errorf("harden database control backup: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tempName, s.path); err != nil {
		if _, backupErr := os.Stat(backupPath); backupErr == nil {
			_ = os.Rename(backupPath, s.path)
		}
		return fmt.Errorf("install database control state: %w", err)
	}
	cleanup = false
	if err := hardenFilePermissions(s.path); err != nil {
		return err
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("sync database control directory: %w", err)
	}
	return nil
}

func (s *Store) acquireFileLock() (*os.File, error) {
	lockPath := s.path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open database control lock: %w", err)
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock database control file: %w", err)
	}
	return file, nil
}

func releaseFileLock(file *os.File) {
	if file == nil {
		return
	}
	_ = unlockFile(file)
	_ = file.Close()
}
