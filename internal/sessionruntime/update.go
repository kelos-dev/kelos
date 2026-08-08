package sessionruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionupdate"
)

const sessionUpdateRetryInterval = 2 * time.Second

func (s *Server) initializeSessionUpdate(ctx context.Context) error {
	if s.config.SessionClient == nil {
		return nil
	}
	session, err := s.config.SessionClient.Get(ctx, s.config.SessionName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting Session %q update request: %w", s.config.SessionName, err)
	}
	if err := s.observeSessionUpdate(session); err != nil {
		return err
	}
	if err := s.reportSessionUpdate(ctx); err != nil {
		return fmt.Errorf("reporting Session %q runtime update state: %w", s.config.SessionName, err)
	}
	return nil
}

func (s *Server) runSessionUpdateWatch(ctx context.Context) {
	for ctx.Err() == nil {
		if err := s.watchSessionUpdates(ctx); err != nil && ctx.Err() == nil {
			log.Printf("Watching Session runtime update request failed error=%v", err)
		}
		if !waitForSessionUpdateRetry(ctx) {
			return
		}
	}
}

func (s *Server) watchSessionUpdates(ctx context.Context) error {
	session, err := s.config.SessionClient.Get(ctx, s.config.SessionName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting Session %q: %w", s.config.SessionName, err)
	}
	if err := s.observeSessionUpdate(session); err != nil {
		return err
	}
	watcher, err := s.config.SessionClient.Watch(ctx, metav1.ListOptions{
		FieldSelector:   fields.OneTermEqualSelector("metadata.name", s.config.SessionName).String(),
		ResourceVersion: session.ResourceVersion,
	})
	if err != nil {
		return fmt.Errorf("watching Session %q: %w", s.config.SessionName, err)
	}
	defer watcher.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return nil
			}
			if event.Type == watch.Error {
				return fmt.Errorf("watching Session %q returned an error event", s.config.SessionName)
			}
			updated, ok := event.Object.(*kelos.Session)
			if !ok {
				continue
			}
			if err := s.observeSessionUpdate(updated); err != nil {
				return err
			}
		}
	}
}

func (s *Server) observeSessionUpdate(session *kelos.Session) error {
	request, err := s.requestForPod(session, sessionupdate.RequestAnnotation)
	if err != nil {
		return fmt.Errorf("reading Session %q runtime update request: %w", session.Name, err)
	}
	idleDrain, err := s.requestForPod(session, sessionupdate.IdleDrainRequestAnnotation)
	if err != nil {
		return fmt.Errorf("reading Session %q idle drain request: %w", session.Name, err)
	}

	s.submitMu.Lock()
	changed := !reflect.DeepEqual(s.updateRequest, request) || !reflect.DeepEqual(s.idleDrainRequest, idleDrain)
	if changed {
		s.updateRequest = request
		s.idleDrainRequest = idleDrain
	}
	s.submitMu.Unlock()
	if changed {
		s.signalSessionUpdateReport()
	}
	return nil
}

// requestForPod decodes the drain request stored in annotation, returning it
// only when it targets this Pod.
func (s *Server) requestForPod(session *kelos.Session, annotation string) (*sessionupdate.Request, error) {
	value := session.Annotations[annotation]
	if value == "" {
		return nil, nil
	}
	parsed, err := sessionupdate.Decode(value)
	if err != nil {
		return nil, err
	}
	if parsed.PodUID != s.config.PodUID {
		return nil, nil
	}
	return &parsed, nil
}

func (s *Server) finishTurn() {
	s.submitMu.Lock()
	if s.outstanding > 0 {
		s.outstanding--
	}
	shouldReport := (s.updateRequest != nil || s.idleDrainRequest != nil) && s.outstanding == 0
	s.submitMu.Unlock()
	if shouldReport {
		s.signalSessionUpdateReport()
	}
}

func (s *Server) signalSessionUpdateReport() {
	select {
	case s.updateReport <- struct{}{}:
	default:
	}
}

func (s *Server) runSessionUpdateReporter(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.updateReport:
		}
		for {
			if err := s.reportSessionUpdate(ctx); err == nil {
				break
			} else if ctx.Err() == nil {
				log.Printf("Reporting Session runtime update state failed error=%v", err)
			}
			if !waitForSessionUpdateRetry(ctx) {
				return
			}
		}
	}
}

func (s *Server) reportSessionUpdate(ctx context.Context) error {
	updateReport, idleDrainReport := s.sessionDrainReports()
	updateValue, err := encodeDrainReport(updateReport)
	if err != nil {
		return err
	}
	idleDrainValue, err := encodeDrainReport(idleDrainReport)
	if err != nil {
		return err
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{
				sessionupdate.ReportAnnotation:          updateValue,
				sessionupdate.IdleDrainReportAnnotation: idleDrainValue,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("encoding runtime drain report patch: %w", err)
	}
	_, err = s.config.SessionClient.Patch(ctx, s.config.SessionName, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

func encodeDrainReport(report *sessionupdate.Report) (any, error) {
	if report == nil {
		return nil, nil
	}
	encoded, err := sessionupdate.EncodeReport(*report)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// sessionDrainReports returns the acknowledgement for each pending drain request
// (runtime update and idle drain), or nil for a request that is not pending.
//
// A runtime-update report is Drained once no accepted turn remains: the Pod is
// replaced and recovers from the journal, so an unpublished activity transition
// is not lost.
//
// An idle-drain report additionally requires every queued activity status update
// to be durably published, because a Drained idle report leads to deletion.
// Gating idle drain on publication closes the window where a turn accepted just
// before the drain finishes and reports Drained while its Active=True/Active=False
// status updates are still retrying, which would let the controller delete the
// Session against a stale Active=False state instead of resetting its idle period.
func (s *Server) sessionDrainReports() (updateReport, idleDrainReport *sessionupdate.Report) {
	s.submitMu.Lock()
	defer s.submitMu.Unlock()
	idle := s.outstanding == 0
	return s.drainReportLocked(s.updateRequest, idle),
		s.drainReportLocked(s.idleDrainRequest, idle && !s.hasPendingStatusPublishes())
}

// hasPendingStatusPublishes reports whether any observed activity status update
// is still queued for publication. It returns false when no status publisher is
// configured, so runtimes without a Session client (for example, in tests)
// drain on the outstanding-turn count alone.
func (s *Server) hasPendingStatusPublishes() bool {
	if s.publishSessionStatus == nil {
		return false
	}
	s.sessionStatusMu.Lock()
	defer s.sessionStatusMu.Unlock()
	return len(s.sessionStatusPublishQueue) > 0
}

// drainReportPending reports whether a drain request (runtime update or idle
// drain) is awaiting acknowledgement.
func (s *Server) drainReportPending() bool {
	s.submitMu.Lock()
	defer s.submitMu.Unlock()
	return s.updateRequest != nil || s.idleDrainRequest != nil
}

func (s *Server) drainReportLocked(request *sessionupdate.Request, drained bool) *sessionupdate.Report {
	if request == nil {
		return nil
	}
	phase := sessionupdate.PhaseDraining
	if drained {
		phase = sessionupdate.PhaseDrained
	}
	return &sessionupdate.Report{RequestID: request.ID, PodUID: s.config.PodUID, Phase: phase}
}

func waitForSessionUpdateRetry(ctx context.Context) bool {
	timer := time.NewTimer(sessionUpdateRetryInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
