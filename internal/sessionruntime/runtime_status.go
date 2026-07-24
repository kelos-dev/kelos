package sessionruntime

import (
	"net/url"
	"strconv"
	"strings"
)

type runtimeStatusProvider interface {
	runtimeStatusSnapshot() RuntimeStatus
}

func newRuntimeStatus(config Config) RuntimeStatus {
	status := RuntimeStatus{
		SessionName: config.SessionName,
		AgentType:   config.AgentType,
		Model:       config.Model,
		Effort:      config.Effort,
		WorkingDir:  config.WorkingDir,
		HomeDir:     environmentValue(config.Environment, "HOME"),
	}
	return status
}

func (s RuntimeStatus) empty() bool {
	return s.SessionName == "" &&
		s.AgentType == "" &&
		s.Model == "" &&
		s.Effort == "" &&
		s.WorkingDir == "" &&
		s.HomeDir == "" &&
		s.Branch == "" &&
		s.PullRequestNumber == 0 &&
		s.Usage == nil &&
		s.WeeklyLimit == nil
}

func cloneRuntimeStatus(status RuntimeStatus) RuntimeStatus {
	if status.Usage != nil {
		usage := *status.Usage
		status.Usage = &usage
	}
	if status.WeeklyLimit != nil {
		limit := *status.WeeklyLimit
		status.WeeklyLimit = &limit
	}
	return status
}

func (s *Server) runtimeStatusSnapshot() RuntimeStatus {
	s.runtimeStatusMu.RLock()
	defer s.runtimeStatusMu.RUnlock()
	return cloneRuntimeStatus(s.runtimeStatus)
}

func (s *Server) subscribeRuntimeStatus() (<-chan RuntimeStatus, func()) {
	s.runtimeStatusMu.Lock()
	defer s.runtimeStatusMu.Unlock()
	if s.runtimeStatusSubscribers == nil {
		s.runtimeStatusSubscribers = map[int]chan RuntimeStatus{}
	}
	s.nextRuntimeStatusSubscriber++
	id := s.nextRuntimeStatusSubscriber
	updates := make(chan RuntimeStatus, 1)
	s.runtimeStatusSubscribers[id] = updates
	return updates, func() {
		s.runtimeStatusMu.Lock()
		if existing, ok := s.runtimeStatusSubscribers[id]; ok {
			delete(s.runtimeStatusSubscribers, id)
			close(existing)
		}
		s.runtimeStatusMu.Unlock()
	}
}

func (s *Server) updateWorkspaceRuntimeStatus(status WorkspaceStatus) {
	s.runtimeStatusMu.Lock()
	s.runtimeStatus.Branch = status.Branch
	s.runtimeStatus.PullRequestNumber = pullRequestNumber(status)
	s.broadcastRuntimeStatusLocked()
	s.runtimeStatusMu.Unlock()
}

func (s *Server) updateProviderRuntimeStatus(update RuntimeStatus) {
	s.runtimeStatusMu.Lock()
	if update.Model != "" {
		s.runtimeStatus.Model = update.Model
	}
	if update.Effort != "" {
		s.runtimeStatus.Effort = update.Effort
	}
	if update.Usage != nil {
		usage := *update.Usage
		s.runtimeStatus.Usage = &usage
	}
	if update.WeeklyLimit != nil {
		limit := *update.WeeklyLimit
		s.runtimeStatus.WeeklyLimit = &limit
	}
	s.broadcastRuntimeStatusLocked()
	s.runtimeStatusMu.Unlock()
}

func (s *Server) broadcastRuntimeStatusLocked() {
	status := cloneRuntimeStatus(s.runtimeStatus)
	for _, updates := range s.runtimeStatusSubscribers {
		select {
		case updates <- status:
		default:
			select {
			case <-updates:
			default:
			}
			select {
			case updates <- status:
			default:
			}
		}
	}
}

func pullRequestNumber(status WorkspaceStatus) int {
	if status.PullRequest == nil {
		return 0
	}
	parsed, err := url.Parse(status.PullRequest.URL)
	if err != nil {
		return 0
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 || parts[len(parts)-2] != "pull" {
		return 0
	}
	number, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || number < 1 {
		return 0
	}
	return number
}
