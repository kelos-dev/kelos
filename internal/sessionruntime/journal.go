package sessionruntime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
)

const (
	maxRetainedEvents  = 4096
	maxJournalLineSize = 4 * 1024 * 1024
	journalIDSuffix    = ".id"
)

// Journal keeps the replayable event stream for the live provider process.
type Journal struct {
	mu          sync.Mutex
	maxEvents   int
	events      []Event
	firstEvent  int
	nextID      int64
	subscribers map[int]*journalSubscriber
	nextSubID   int
	path        string
	file        *os.File
	journalID   string
	failure     chan struct{}
	failureErr  error
	failureOnce sync.Once
	closed      bool
}

type journalSubscriber struct {
	events   chan Event
	overflow chan struct{}
}

// HistoryBounds describes the retained range at subscription time.
type HistoryBounds struct {
	FirstEventID int64
	LastEventID  int64
	JournalID    string
	Reset        bool
}

// NewJournal creates an in-memory journal for tests and embedded uses.
func NewJournal() *Journal {
	return newJournal(maxRetainedEvents)
}

func newJournal(maxEvents int) *Journal {
	return &Journal{
		maxEvents:   maxEvents,
		nextID:      1,
		journalID:   uuid.New().String(),
		subscribers: map[int]*journalSubscriber{},
		failure:     make(chan struct{}),
	}
}

// OpenJournal opens or creates a durable event journal.
func OpenJournal(path string) (*Journal, error) {
	journal := newJournal(maxRetainedEvents)
	journal.path = path
	_, statErr := os.Stat(path)
	journalExisted := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("checking Session event journal: %w", statErr)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening Session event journal: %w", err)
	}
	journal.file = file
	embeddedJournalID, rewriteNeeded, err := journal.load()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	journalHasEvents := journal.nextID > 1
	journalID := embeddedJournalID
	if journalID == "" && journalHasEvents {
		journalID = uuid.New().String()
	}
	if journalID == "" {
		journalID, err = loadJournalID(path+journalIDSuffix, journalExisted)
	} else {
		err = writeJournalID(path+journalIDSuffix, journalID)
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	journal.journalID = journalID
	if rewriteNeeded || (journalHasEvents && embeddedJournalID == "") {
		if err := journal.rewrite(); err != nil {
			_ = journal.file.Close()
			return nil, err
		}
	}
	if _, err := journal.file.Seek(0, io.SeekEnd); err != nil {
		_ = journal.file.Close()
		return nil, fmt.Errorf("seeking Session event journal: %w", err)
	}
	return journal, nil
}

func loadJournalID(path string, journalExisted bool) (string, error) {
	if journalExisted {
		data, err := os.ReadFile(path)
		if err == nil {
			journalID := strings.TrimSpace(string(data))
			if _, err := uuid.Parse(journalID); err != nil {
				return "", fmt.Errorf("parsing Session event journal identity: %w", err)
			}
			return journalID, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("reading Session event journal identity: %w", err)
		}
	}

	journalID := uuid.New().String()
	if err := writeJournalID(path, journalID); err != nil {
		return "", err
	}
	return journalID, nil
}

func writeJournalID(path, journalID string) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".session-events-id-*")
	if err != nil {
		return fmt.Errorf("creating Session event journal identity: %w", err)
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return fmt.Errorf("securing Session event journal identity: %w", err)
	}
	if _, err := temporary.WriteString(journalID + "\n"); err != nil {
		return fmt.Errorf("writing Session event journal identity: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("syncing Session event journal identity: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing Session event journal identity: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replacing Session event journal identity: %w", err)
	}
	removeTemporary = false
	return nil
}

func (j *Journal) load() (string, bool, error) {
	reader := bufio.NewReaderSize(j.file, 64*1024)
	var offset int64
	total := 0
	journalID := ""
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) && len(line) > 0 {
			if truncateErr := j.file.Truncate(offset); truncateErr != nil {
				return "", false, fmt.Errorf("discarding incomplete Session event journal record: %w", truncateErr)
			}
			break
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return "", false, fmt.Errorf("reading Session event journal: %w", err)
		}
		if len(line) == 0 {
			break
		}
		if len(line) > maxJournalLineSize {
			return "", false, fmt.Errorf("Session event journal record exceeds %d bytes", maxJournalLineSize)
		}
		var event Event
		if decodeErr := json.Unmarshal(bytes.TrimSuffix(line, []byte{'\n'}), &event); decodeErr != nil {
			return "", false, fmt.Errorf("decoding Session event journal record: %w", decodeErr)
		}
		if event.ID <= 0 || event.ID < j.nextID {
			return "", false, fmt.Errorf("Session event journal contains invalid event ID %d", event.ID)
		}
		if event.JournalID != "" {
			if _, parseErr := uuid.Parse(event.JournalID); parseErr != nil {
				return "", false, fmt.Errorf("parsing Session event journal identity: %w", parseErr)
			}
			if journalID != "" && event.JournalID != journalID {
				return "", false, errors.New("Session event journal contains inconsistent identities")
			}
			journalID = event.JournalID
			event.JournalID = ""
		}
		j.nextID = event.ID + 1
		j.appendRetained(event)
		offset += int64(len(line))
		total++
		if errors.Is(err, io.EOF) {
			break
		}
	}
	return journalID, total > j.maxEvents, nil
}

// Append records and broadcasts one event after writing it to durable storage.
func (j *Journal) Append(event Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return errors.New("Session event journal is closed")
	}
	if j.failureErr != nil {
		return j.failureErr
	}

	event.ID = j.nextID
	if j.file != nil {
		persistedEvent := event
		persistedEvent.JournalID = j.journalID
		encoded, err := json.Marshal(persistedEvent)
		if err == nil {
			encoded = append(encoded, '\n')
			if len(encoded) > maxJournalLineSize {
				err = fmt.Errorf("Session event journal record exceeds %d bytes", maxJournalLineSize)
			} else {
				_, err = j.file.Write(encoded)
			}
		}
		if err != nil {
			failure := fmt.Errorf("writing Session event journal: %w", err)
			j.fail(failure)
			return failure
		}
	}
	j.nextID++
	compacted := j.appendRetained(event)
	if compacted && j.file != nil {
		if err := j.rewrite(); err != nil {
			j.fail(err)
			return err
		}
	}
	for id, subscriber := range j.subscribers {
		select {
		case subscriber.events <- event:
		default:
			close(subscriber.events)
			close(subscriber.overflow)
			delete(j.subscribers, id)
		}
	}
	return nil
}

func (j *Journal) appendRetained(event Event) bool {
	j.events = append(j.events, event)
	if len(j.events)-j.firstEvent <= j.maxEvents {
		return false
	}
	j.events[j.firstEvent] = Event{}
	j.firstEvent++
	if j.firstEvent < j.maxEvents {
		return false
	}
	j.events = append([]Event(nil), j.events[j.firstEvent:]...)
	j.firstEvent = 0
	return true
}

func (j *Journal) rewrite() error {
	temporary, err := os.CreateTemp(filepath.Dir(j.path), ".session-events-*")
	if err != nil {
		return fmt.Errorf("compacting Session event journal: %w", err)
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return fmt.Errorf("securing compacted Session event journal: %w", err)
	}
	writer := bufio.NewWriter(temporary)
	for _, event := range j.events[j.firstEvent:] {
		persistedEvent := event
		persistedEvent.JournalID = j.journalID
		encoded, err := json.Marshal(persistedEvent)
		if err != nil {
			return fmt.Errorf("encoding compacted Session event journal: %w", err)
		}
		if _, err := writer.Write(append(encoded, '\n')); err != nil {
			return fmt.Errorf("writing compacted Session event journal: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flushing compacted Session event journal: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("syncing compacted Session event journal: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing compacted Session event journal: %w", err)
	}
	if err := j.file.Close(); err != nil {
		return fmt.Errorf("closing Session event journal for compaction: %w", err)
	}
	if err := os.Rename(temporaryName, j.path); err != nil {
		return fmt.Errorf("replacing compacted Session event journal: %w", err)
	}
	removeTemporary = false
	j.file, err = os.OpenFile(j.path, os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("reopening compacted Session event journal: %w", err)
	}
	return nil
}

func (j *Journal) fail(err error) {
	j.failureErr = err
	j.failureOnce.Do(func() { close(j.failure) })
	for id, subscriber := range j.subscribers {
		close(subscriber.events)
		delete(j.subscribers, id)
	}
}

// Snapshot returns the currently retained events.
func (j *Journal) Snapshot() []Event {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Event(nil), j.events[j.firstEvent:]...)
}

// Failed is closed if durable journal writes fail.
func (j *Journal) Failed() <-chan struct{} {
	return j.failure
}

// Err returns the durable journal failure, if any.
func (j *Journal) Err() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.failureErr
}

// Subscribe returns retained events, a live stream, an overflow signal, and a cancel function.
func (j *Journal) Subscribe(since int64) ([]Event, <-chan Event, <-chan struct{}, func()) {
	_, retained, events, overflow, cancel := j.subscribe(since, "", false)
	return retained, events, overflow, cancel
}

// SubscribeWithBounds returns retained events and reports whether the caller's
// cursor or journal identity was stale and reset.
func (j *Journal) SubscribeWithBounds(since int64, journalID string) (HistoryBounds, []Event, <-chan Event, <-chan struct{}, func()) {
	return j.subscribe(since, journalID, true)
}

func (j *Journal) subscribe(since int64, journalID string, resetStale bool) (HistoryBounds, []Event, <-chan Event, <-chan struct{}, func()) {
	j.mu.Lock()
	defer j.mu.Unlock()
	firstEventID := j.nextID
	lastEventID := j.nextID - 1
	if j.firstEvent < len(j.events) {
		firstEventID = j.events[j.firstEvent].ID
	}
	journalChanged := journalID != "" && journalID != j.journalID
	journalUnknown := since > 0 && journalID == ""
	reset := resetStale && (journalChanged || journalUnknown || since < firstEventID-1 || since > lastEventID)
	if reset {
		since = 0
	}

	retained := make([]Event, 0, len(j.events)-j.firstEvent)
	for _, event := range j.events[j.firstEvent:] {
		if event.ID > since {
			retained = append(retained, event)
		}
	}

	id := j.nextSubID
	j.nextSubID++
	subscriber := &journalSubscriber{events: make(chan Event, 256), overflow: make(chan struct{})}
	if !j.closed && j.failureErr == nil {
		j.subscribers[id] = subscriber
	} else {
		close(subscriber.events)
	}
	cancel := func() {
		j.mu.Lock()
		defer j.mu.Unlock()
		if subscriber, ok := j.subscribers[id]; ok {
			close(subscriber.events)
			delete(j.subscribers, id)
		}
	}
	return HistoryBounds{
		FirstEventID: firstEventID,
		LastEventID:  lastEventID,
		JournalID:    j.journalID,
		Reset:        reset,
	}, retained, subscriber.events, subscriber.overflow, cancel
}

// Close closes all subscriptions and the durable journal file.
func (j *Journal) Close() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return
	}
	j.closed = true
	for id, subscriber := range j.subscribers {
		close(subscriber.events)
		delete(j.subscribers, id)
	}
	if j.file != nil {
		_ = j.file.Close()
	}
}
