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
	"sync"
)

const (
	maxRetainedEvents  = 4096
	maxJournalLineSize = 4 * 1024 * 1024
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
	failure     chan struct{}
	failureErr  error
	failureOnce sync.Once
	closed      bool
}

type journalSubscriber struct {
	events   chan Event
	overflow chan struct{}
}

// NewJournal creates an in-memory journal for tests and embedded uses.
func NewJournal() *Journal {
	return newJournal(maxRetainedEvents)
}

func newJournal(maxEvents int) *Journal {
	return &Journal{
		maxEvents:   maxEvents,
		nextID:      1,
		subscribers: map[int]*journalSubscriber{},
		failure:     make(chan struct{}),
	}
}

// OpenJournal opens or creates a durable event journal.
func OpenJournal(path string) (*Journal, error) {
	journal := newJournal(maxRetainedEvents)
	journal.path = path
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening Session event journal: %w", err)
	}
	journal.file = file
	if err := journal.load(); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("seeking Session event journal: %w", err)
	}
	return journal, nil
}

func (j *Journal) load() error {
	reader := bufio.NewReaderSize(j.file, 64*1024)
	var offset int64
	total := 0
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) && len(line) > 0 {
			if truncateErr := j.file.Truncate(offset); truncateErr != nil {
				return fmt.Errorf("discarding incomplete Session event journal record: %w", truncateErr)
			}
			break
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("reading Session event journal: %w", err)
		}
		if len(line) == 0 {
			break
		}
		if len(line) > maxJournalLineSize {
			return fmt.Errorf("Session event journal record exceeds %d bytes", maxJournalLineSize)
		}
		var event Event
		if decodeErr := json.Unmarshal(bytes.TrimSuffix(line, []byte{'\n'}), &event); decodeErr != nil {
			return fmt.Errorf("decoding Session event journal record: %w", decodeErr)
		}
		if event.ID <= 0 || event.ID < j.nextID {
			return fmt.Errorf("Session event journal contains invalid event ID %d", event.ID)
		}
		j.nextID = event.ID + 1
		j.appendRetained(event)
		offset += int64(len(line))
		total++
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if total > j.maxEvents {
		if err := j.rewrite(); err != nil {
			return err
		}
	}
	return nil
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
		encoded, err := json.Marshal(event)
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
		encoded, err := json.Marshal(event)
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
	j.mu.Lock()
	defer j.mu.Unlock()

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
	return retained, subscriber.events, subscriber.overflow, cancel
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
