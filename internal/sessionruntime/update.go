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
	var request *sessionupdate.Request
	if value := session.Annotations[sessionupdate.RequestAnnotation]; value != "" {
		parsed, err := sessionupdate.Decode(value)
		if err != nil {
			return fmt.Errorf("reading Session %q runtime update request: %w", session.Name, err)
		}
		if parsed.PodUID == s.config.PodUID {
			request = &parsed
		}
	}

	s.submitMu.Lock()
	changed := !reflect.DeepEqual(s.updateRequest, request)
	if changed {
		s.updateRequest = request
	}
	s.submitMu.Unlock()
	if changed {
		s.signalSessionUpdateReport()
	}
	return nil
}

func (s *Server) finishTurn() {
	s.submitMu.Lock()
	if s.outstanding > 0 {
		s.outstanding--
	}
	shouldReport := s.updateRequest != nil && s.outstanding == 0
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
	var value any
	if report := s.sessionRuntimeUpdateReport(); report != nil {
		encoded, err := sessionupdate.EncodeReport(*report)
		if err != nil {
			return err
		}
		value = encoded
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{sessionupdate.ReportAnnotation: value},
		},
	})
	if err != nil {
		return fmt.Errorf("encoding runtime update report patch: %w", err)
	}
	_, err = s.config.SessionClient.Patch(ctx, s.config.SessionName, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

func (s *Server) sessionRuntimeUpdateReport() *sessionupdate.Report {
	s.submitMu.Lock()
	defer s.submitMu.Unlock()
	var report *sessionupdate.Report
	if s.updateRequest != nil {
		phase := sessionupdate.PhaseDraining
		if s.outstanding == 0 {
			phase = sessionupdate.PhaseDrained
		}
		report = &sessionupdate.Report{
			RequestID: s.updateRequest.ID,
			PodUID:    s.config.PodUID,
			Phase:     phase,
		}
	}
	return report
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
