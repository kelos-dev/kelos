package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const maxSubsessions = 8

type notificationWatermark struct {
	ID   string    `json:"id"`
	Time time.Time `json:"time"`
}

// Subsession describes a conversation in a Session's shared workspace.
type Subsession struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	NotificationID   string `json:"notificationId,omitempty"`
	NotificationType string `json:"notificationType,omitempty"`
	Active           bool   `json:"active,omitempty"`
	WaitingForInput  bool   `json:"waitingForInput,omitempty"`
}

type subsessionGroup struct {
	ctx    context.Context
	cancel context.CancelFunc
	root   *Server

	createMu    sync.Mutex
	creating    bool
	mu          sync.Mutex
	children    map[string]*Server
	names       map[string]Subsession
	failure     error
	workers     sync.WaitGroup
	subscribers map[chan []Subsession]struct{}
	newProvider func(context.Context, ProviderConfig) (Provider, error)

	publishMu             sync.Mutex
	publish               SessionStatusPublisher
	statuses              map[string]ObservedSessionStatus
	notificationWatermark notificationWatermark
}

func newSubsessionGroup(ctx context.Context, cancel context.CancelFunc, publisher SessionStatusPublisher) *subsessionGroup {
	return &subsessionGroup{
		ctx: ctx, cancel: cancel, publish: publisher, newProvider: NewProvider,
		children: map[string]*Server{}, names: map[string]Subsession{},
		statuses: map[string]ObservedSessionStatus{}, subscribers: map[chan []Subsession]struct{}{},
	}
}

func (g *subsessionGroup) publisher(id string) SessionStatusPublisher {
	return func(ctx context.Context, status ObservedSessionStatus) error {
		g.publishMu.Lock()
		defer g.publishMu.Unlock()
		previous := g.statuses[id]
		g.statuses[id] = status
		notificationID := func(status ObservedSessionStatus) string {
			if status.LastNotification == nil {
				return ""
			}
			return status.LastNotification.ID
		}
		if g.root != nil && (previous.Active != status.Active || previous.WaitingForInput != status.WaitingForInput || notificationID(previous) != notificationID(status)) {
			g.mu.Lock()
			g.broadcastLocked()
			g.mu.Unlock()
		}
		if g.publish == nil {
			return nil
		}
		root, reported := g.statuses[""]
		if !reported {
			return errors.New("Main conversation status has not been reported yet")
		}
		combined := status
		combined.Model = root.Model
		combined.Active, combined.WaitingForInput = false, false
		combined.LastNotification = nil
		for _, observed := range g.statuses {
			combined.Active = combined.Active || observed.Active
			combined.WaitingForInput = combined.WaitingForInput || observed.WaitingForInput
			if observed.LastNotification != nil && (combined.LastNotification == nil ||
				observed.LastNotification.EventTime.After(combined.LastNotification.EventTime.Time)) {
				combined.LastNotification = observed.LastNotification
			}
		}
		candidate := combined.LastNotification
		if candidate != nil && g.notificationWatermark.ID != "" && candidate.ID != g.notificationWatermark.ID &&
			!candidate.EventTime.After(g.notificationWatermark.Time) {
			combined.LastNotification = nil
		}
		if combined.LastNotification != nil && combined.LastNotification.ID != g.notificationWatermark.ID {
			watermark := notificationWatermark{ID: combined.LastNotification.ID, Time: combined.LastNotification.EventTime.Time}
			if g.root != nil {
				if err := writeSubsessionState(filepath.Join(g.root.config.StateDir, "notification-watermark.json"), watermark); err != nil {
					return fmt.Errorf("recording Session notification watermark: %w", err)
				}
			}
			g.notificationWatermark = watermark
		}
		return g.publish(ctx, combined)
	}
}

func (g *subsessionGroup) directory() string {
	return filepath.Join(g.root.config.StateDir, "subsessions")
}

func (g *subsessionGroup) restore() error {
	data, err := os.ReadFile(filepath.Join(g.root.config.StateDir, "notification-watermark.json"))
	if err == nil {
		if err := json.Unmarshal(data, &g.notificationWatermark); err != nil || g.notificationWatermark.ID == "" || g.notificationWatermark.Time.IsZero() {
			return errors.New("Invalid Session notification watermark")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading Session notification watermark: %w", err)
	}
	entries, err := os.ReadDir(g.directory())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading subsessions: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(g.directory(), entry.Name(), "subsession.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("reading subsession %q: %w", entry.Name(), err)
		}
		var child Subsession
		if err := json.Unmarshal(data, &child); err != nil || child.ID != entry.Name() || child.Name == "" {
			return fmt.Errorf("invalid subsession metadata in %q", entry.Name())
		}
		if _, err := uuid.Parse(child.ID); err != nil {
			return fmt.Errorf("invalid subsession ID %q: %w", child.ID, err)
		}
		if err := g.start(child, false); err != nil {
			return err
		}
	}
	return nil
}

func (g *subsessionGroup) create(name, requestID string) (Subsession, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 64 {
		return Subsession{}, errors.New("Subsession name must contain between 1 and 64 characters")
	}
	g.createMu.Lock()
	defer g.createMu.Unlock()
	id := uuid.NewString()
	if requestID != "" {
		id = uuid.NewSHA1(uuid.NameSpaceOID, []byte(requestID)).String()
		g.mu.Lock()
		existing, found := g.names[id]
		g.mu.Unlock()
		if found {
			if existing.Name != name {
				return Subsession{}, errors.New("Subsession request ID was already used with a different name")
			}
			return existing, nil
		}
	}
	g.root.submitMu.Lock()
	if err := g.root.checkDrainLocked(); err != nil {
		g.root.submitMu.Unlock()
		return Subsession{}, err
	}
	if err := g.ctx.Err(); err != nil {
		g.root.submitMu.Unlock()
		return Subsession{}, err
	}
	g.mu.Lock()
	full := len(g.names) >= maxSubsessions
	g.mu.Unlock()
	if full {
		g.root.submitMu.Unlock()
		return Subsession{}, fmt.Errorf("Session %q supports at most %d subsessions", g.root.config.SessionName, maxSubsessions)
	}
	g.creating = true
	g.root.submitMu.Unlock()
	defer func() {
		g.root.submitMu.Lock()
		g.creating = false
		g.root.submitMu.Unlock()
		if g.root.drainReportPending() {
			g.root.signalSessionUpdateReport()
		}
	}()
	child := Subsession{ID: id, Name: name}
	if err := g.start(child, true); err != nil {
		return Subsession{}, err
	}
	return child, nil
}

func (g *subsessionGroup) start(child Subsession, persist bool) error {
	config := g.root.config
	config.StateDir = filepath.Join(g.directory(), child.ID)
	config.SocketPath = filepath.Join(filepath.Dir(config.SocketPath), child.ID+".sock")
	config.InitialPrompt = ""
	config.SessionClient = nil
	config.PublishSessionStatus = g.publisher(child.ID)
	factory := g.newProvider
	if parent, ok := g.root.provider.(*OpenCodeProvider); ok {
		factory = func(ctx context.Context, config ProviderConfig) (Provider, error) {
			return newOpenCodeProviderWithClient(ctx, config, parent.client)
		}
	}
	server, err := openSessionServer(g.ctx, config, factory, g)
	if err != nil {
		return fmt.Errorf("opening subsession %q: %w", child.Name, err)
	}
	if err := g.register(child, server, persist); err != nil {
		_ = server.closeProvider()
		server.events.close()
		server.journal.Close()
		return err
	}
	go func() {
		defer g.workers.Done()
		if err := server.Serve(g.ctx); err != nil && g.ctx.Err() == nil {
			g.mu.Lock()
			g.failure = fmt.Errorf("subsession %q stopped: %w", child.Name, err)
			g.mu.Unlock()
			g.cancel()
		}
	}()
	return nil
}

func (g *subsessionGroup) register(child Subsession, server *Server, persist bool) error {
	g.root.submitMu.Lock()
	defer g.root.submitMu.Unlock()
	if err := g.ctx.Err(); err != nil {
		return err
	}
	if persist {
		if err := g.root.checkDrainLocked(); err != nil {
			return err
		}
		if err := writeSubsessionState(filepath.Join(server.config.StateDir, "subsession.json"), child); err != nil {
			return fmt.Errorf("saving subsession %q: %w", child.Name, err)
		}
	}
	server.attachmentStore = g.root.attachmentStore
	g.mu.Lock()
	g.children[child.ID], g.names[child.ID] = server, child
	g.broadcastLocked()
	g.workers.Add(1)
	g.mu.Unlock()
	return nil
}

func writeSubsessionState(path string, value any) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".subsession-state-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
	}()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	if err := json.NewEncoder(temporary).Encode(value); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}

func (g *subsessionGroup) updateWorkspaceStatus(status WorkspaceStatus) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.root.updateWorkspaceRuntimeStatus(status)
	for _, child := range g.children {
		child.updateWorkspaceRuntimeStatus(status)
	}
}

func (g *subsessionGroup) get(id string) (*Server, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if child := g.children[id]; child != nil {
		return child, nil
	}
	return nil, fmt.Errorf("Subsession %q does not exist", id)
}

func (g *subsessionGroup) subscribe() (<-chan []Subsession, func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	updates := make(chan []Subsession, 1)
	updates <- g.listLocked()
	g.subscribers[updates] = struct{}{}
	return updates, func() {
		g.mu.Lock()
		delete(g.subscribers, updates)
		g.mu.Unlock()
	}
}

func (g *subsessionGroup) list() []Subsession {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.listLocked()
}

func (g *subsessionGroup) listLocked() []Subsession {
	children := make([]Subsession, 0, len(g.names)+1)
	for id, child := range g.names {
		children = append(children, conversationSummary(child, g.children[id]))
	}
	sort.Slice(children, func(i, j int) bool {
		if children[i].Name == children[j].Name {
			return children[i].ID < children[j].ID
		}
		return children[i].Name < children[j].Name
	})
	return append([]Subsession{conversationSummary(Subsession{Name: "Main conversation"}, g.root)}, children...)
}

func conversationSummary(conversation Subsession, server *Server) Subsession {
	if notification := server.journal.LatestNotification(); notification != nil {
		conversation.NotificationID = notification.ID
		conversation.NotificationType = string(notification.Type)
	}
	server.activeMu.Lock()
	conversation.Active = server.activeTurn != "" || server.backgroundActive.Load()
	server.activeMu.Unlock()
	conversation.WaitingForInput = server.pendingInputCount.Load() > 0
	return conversation
}

func (g *subsessionGroup) broadcastLocked() {
	children := g.listLocked()
	for updates := range g.subscribers {
		select {
		case <-updates:
		default:
		}
		updates <- children
	}
}

func (g *subsessionGroup) err() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.failure
}

func (g *subsessionGroup) close() {
	g.cancel()
	g.createMu.Lock()
	g.createMu.Unlock()
	g.workers.Wait()
}

func (s *Server) checkDrainLocked() error {
	if s.idleDrainRequest != nil {
		return errors.New("Session is idle and is being reclaimed")
	}
	if s.updateRequest != nil {
		return errors.New("Session runtime is draining for an update; retry after it reconnects")
	}
	if s.providerStopping.Load() {
		return errors.New("Session provider is restarting; retry after it reconnects")
	}
	return nil
}

// childrenDrained is called with the root's submission lock held so accepting
// child work cannot race with acknowledging a drain of the shared workspace.
func (g *subsessionGroup) childrenDrained(idle bool) bool {
	if g.creating {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, child := range g.children {
		child.submitMu.Lock()
		busy := child.outstanding > 0
		child.submitMu.Unlock()
		if busy || (idle && (child.backgroundActive.Load() || child.hasPendingStatusPublishes())) {
			return false
		}
	}
	return true
}
