// Package users manages the local registry of bank-portal users keyed by PAN.
//
// Non-secret fields live in .data/users (JSON, 0600). Passwords are kept in
// the secrets store under "user-password:<PAN>" and are migrated out of the
// JSON file the first time an old-format file is opened.
package users

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"autoipo/internal/envfile"
	"autoipo/internal/secrets"
)

// Record is one stored user. Password is never serialised to the JSON file.
type Record struct {
	Name     string `json:"name"`
	PAN      string `json:"pan"`
	BankName string `json:"bank_name"`
	UserID   string `json:"user_id"`
	Email    string `json:"email,omitempty"`
	Phone    string `json:"phone,omitempty"`
	Password string `json:"-"`
}

// legacyRecord accepts the pre-secrets file format that stored passwords.
type legacyRecord struct {
	Record
	Password string `json:"password,omitempty"`
}

// SupportedBanks lists the bank portals the flows target.
var SupportedBanks = []string{"HDFC", "Axis"}

var panPattern = regexp.MustCompile(`^[A-Z]{5}[0-9]{4}[A-Z]$`)

// NormalizePAN upper-cases and trims a PAN.
func NormalizePAN(pan string) string { return strings.ToUpper(strings.TrimSpace(pan)) }

// ValidPAN reports whether pan has the standard 10-character PAN shape.
func ValidPAN(pan string) bool { return panPattern.MatchString(NormalizePAN(pan)) }

// NormalizeBank maps case-insensitive input onto a supported bank name, or "".
func NormalizeBank(value string) string {
	trimmed := strings.TrimSpace(value)
	for _, bank := range SupportedBanks {
		if strings.EqualFold(trimmed, bank) {
			return bank
		}
	}
	return ""
}

// Validate checks required fields.
func (r Record) Validate() error {
	var missing []string
	if strings.TrimSpace(r.Name) == "" {
		missing = append(missing, "name")
	}
	if NormalizePAN(r.PAN) == "" {
		missing = append(missing, "PAN")
	}
	if NormalizeBank(r.BankName) == "" {
		missing = append(missing, "bank")
	}
	if strings.TrimSpace(r.UserID) == "" {
		missing = append(missing, "user ID")
	}
	if r.Password == "" {
		missing = append(missing, "password")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s required", strings.Join(missing, ", "))
	}
	if !ValidPAN(r.PAN) {
		return fmt.Errorf("PAN %q must look like ABCDE1234F", NormalizePAN(r.PAN))
	}
	return nil
}

// Placeholders exposes the record as flow placeholder values.
func (r Record) Placeholders() map[string]string {
	return map[string]string{
		"name":     r.Name,
		"pan":      r.PAN,
		"bank":     r.BankName,
		"user_id":  r.UserID,
		"password": r.Password,
		"email":    r.Email,
		"phone":    r.Phone,
	}
}

// PlaceholderNames lists the placeholder keys a user provides, in order.
var PlaceholderNames = []string{"name", "pan", "bank", "user_id", "password", "email", "phone"}

// MaskedPassword renders a fixed-width mask ("Not set" when empty).
func MaskedPassword(password string) string {
	if password == "" {
		return "Not set"
	}
	return "••••••••"
}

// ErrCorrupt is returned (wrapped) when the users file cannot be parsed. The
// store refuses to overwrite such a file.
var ErrCorrupt = errors.New("users file is corrupt")

// Store is the in-memory registry backed by a JSON file and a secrets store.
type Store struct {
	path    string
	secrets secrets.Store
	mu      sync.RWMutex
	records map[string]Record
}

// Open loads the store at path. Passwords found in the JSON (legacy format)
// are moved into the secrets store and the file is rewritten without them.
func Open(path string, sec secrets.Store) (*Store, error) {
	store := &Store{path: path, secrets: sec, records: map[string]Record{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, err
	}
	records, migrated, err := decode(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	var migrationErr error
	for pan, record := range records {
		if password := migrated[pan]; password != "" {
			if err := sec.Set(secrets.UserPasswordKey+pan, password); err != nil {
				migrationErr = errors.Join(migrationErr, fmt.Errorf("migrate password for %s: %w", pan, err))
				record.Password = password
			}
		}
		store.records[pan] = record
	}
	if migrationErr != nil {
		return store, migrationErr
	}
	if len(migrated) > 0 {
		if err := store.saveLocked(); err != nil {
			return store, fmt.Errorf("rewrite users file without passwords: %w", err)
		}
	}
	return store, nil
}

// decode parses the users JSON; it returns the records and any legacy
// passwords found in the file keyed by PAN.
func decode(data []byte) (map[string]Record, map[string]string, error) {
	records := map[string]Record{}
	passwords := map[string]string{}
	if len(bytes.TrimSpace(data)) == 0 {
		return records, passwords, nil
	}
	var raw map[string]legacyRecord
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, err
	}
	for key, legacy := range raw {
		pan := NormalizePAN(key)
		if pan == "" {
			continue
		}
		record := legacy.Record
		record.PAN = NormalizePAN(record.PAN)
		if record.PAN == "" {
			record.PAN = pan
		}
		if legacy.Password != "" {
			passwords[record.PAN] = legacy.Password
		}
		records[record.PAN] = record
	}
	return records, passwords, nil
}

// Path returns the backing file.
func (s *Store) Path() string { return s.path }

// List returns all records sorted by name (then PAN) with passwords loaded.
func (s *Store) List() []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	records := make([]Record, 0, len(s.records))
	for _, record := range s.records {
		records = append(records, s.withPassword(record))
	}
	sortRecords(records)
	return records
}

func sortRecords(records []Record) {
	sort.Slice(records, func(i, j int) bool {
		if !strings.EqualFold(records[i].Name, records[j].Name) {
			return strings.ToLower(records[i].Name) < strings.ToLower(records[j].Name)
		}
		return records[i].PAN < records[j].PAN
	})
}

// ListSummaries returns all records sorted like List but WITHOUT loading
// passwords from the secrets store (cheap; suitable for list views). Use Get
// to obtain a single record with its password.
func (s *Store) ListSummaries() []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	records := make([]Record, 0, len(s.records))
	for _, record := range s.records {
		record.Password = ""
		records = append(records, record)
	}
	sortRecords(records)
	return records
}

// Len returns the number of stored users.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}

// Get returns the record for pan.
func (s *Store) Get(pan string) (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.records[NormalizePAN(pan)]
	if !ok {
		return Record{}, false
	}
	return s.withPassword(record), true
}

// Find matches a PAN exactly or a name case-insensitively (unique prefix
// matches are accepted when unambiguous).
func (s *Store) Find(query string) (Record, bool) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Record{}, false
	}
	if record, ok := s.Get(query); ok {
		return record, true
	}
	var matches []Record
	for _, record := range s.List() {
		if strings.EqualFold(record.Name, query) {
			return record, true
		}
		if strings.HasPrefix(strings.ToLower(record.Name), strings.ToLower(query)) {
			matches = append(matches, record)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return Record{}, false
}

func (s *Store) withPassword(record Record) Record {
	if record.Password != "" || s.secrets == nil {
		return record
	}
	if password, err := s.secrets.Get(secrets.UserPasswordKey + record.PAN); err == nil {
		record.Password = password
	}
	return record
}

// Put inserts or updates a record. previousPAN identifies the record being
// edited (empty for a new user) so that PAN changes are handled.
func (s *Store) Put(record Record, previousPAN string) error {
	record.Name = strings.TrimSpace(record.Name)
	record.PAN = NormalizePAN(record.PAN)
	record.BankName = NormalizeBank(record.BankName)
	record.UserID = strings.TrimSpace(record.UserID)
	record.Email = strings.TrimSpace(record.Email)
	record.Phone = strings.TrimSpace(record.Phone)
	if err := record.Validate(); err != nil {
		return err
	}
	previousPAN = NormalizePAN(previousPAN)

	s.mu.Lock()
	defer s.mu.Unlock()
	if record.PAN != previousPAN {
		if _, exists := s.records[record.PAN]; exists {
			return fmt.Errorf("PAN %s already exists", record.PAN)
		}
	}
	if err := s.secrets.Set(secrets.UserPasswordKey+record.PAN, record.Password); err != nil {
		return fmt.Errorf("store password: %w", err)
	}
	backup := s.snapshotLocked()
	if previousPAN != "" && previousPAN != record.PAN {
		delete(s.records, previousPAN)
	}
	stored := record
	stored.Password = ""
	s.records[record.PAN] = stored
	if err := s.saveLocked(); err != nil {
		s.records = backup
		return err
	}
	if previousPAN != "" && previousPAN != record.PAN {
		_ = s.secrets.Delete(secrets.UserPasswordKey + previousPAN)
	}
	return nil
}

// Delete removes a user and its password.
func (s *Store) Delete(pan string) error {
	pan = NormalizePAN(pan)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[pan]; !ok {
		return nil
	}
	backup := s.snapshotLocked()
	delete(s.records, pan)
	if err := s.saveLocked(); err != nil {
		s.records = backup
		return err
	}
	_ = s.secrets.Delete(secrets.UserPasswordKey + pan)
	return nil
}

func (s *Store) snapshotLocked() map[string]Record {
	snapshot := make(map[string]Record, len(s.records))
	for pan, record := range s.records {
		snapshot[pan] = record
	}
	return snapshot
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create users directory: %w", err)
	}
	stripped := make(map[string]Record, len(s.records))
	for pan, record := range s.records {
		record.Password = ""
		stripped[pan] = record
	}
	data, err := json.MarshalIndent(stripped, "", "  ")
	if err != nil {
		return err
	}
	if existing, err := os.ReadFile(s.path); err == nil && len(bytes.TrimSpace(existing)) > 0 {
		_ = os.WriteFile(s.path+".bak", existing, 0o600)
	}
	return envfile.WriteFileAtomic(s.path, append(data, '\n'), 0o600)
}
