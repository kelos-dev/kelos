package controller

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestUsageFromResults(t *testing.T) {
	int64Ptr := func(v int64) *int64 { return &v }

	tests := []struct {
		name          string
		results       map[string]string
		wantNil       bool
		wantCost      string
		wantInputTok  *int64
		wantOutputTok *int64
	}{
		{
			name:    "nil map returns nil",
			results: nil,
			wantNil: true,
		},
		{
			name:    "empty map returns nil",
			results: map[string]string{},
			wantNil: true,
		},
		{
			name: "all three keys present",
			results: map[string]string{
				"cost-usd":      "1.50",
				"input-tokens":  "1000",
				"output-tokens": "500",
			},
			wantCost:      "1.50",
			wantInputTok:  int64Ptr(1000),
			wantOutputTok: int64Ptr(500),
		},
		{
			name: "only cost present",
			results: map[string]string{
				"cost-usd": "0.25",
			},
			wantCost:      "0.25",
			wantInputTok:  nil,
			wantOutputTok: nil,
		},
		{
			name: "only tokens present",
			results: map[string]string{
				"input-tokens":  "2000",
				"output-tokens": "800",
			},
			wantInputTok:  int64Ptr(2000),
			wantOutputTok: int64Ptr(800),
		},
		{
			name: "invalid cost is skipped",
			results: map[string]string{
				"cost-usd":     "not-a-number",
				"input-tokens": "100",
			},
			wantInputTok:  int64Ptr(100),
			wantOutputTok: nil,
		},
		{
			name: "invalid tokens are skipped",
			results: map[string]string{
				"cost-usd":      "0.10",
				"input-tokens":  "abc",
				"output-tokens": "xyz",
			},
			wantCost:      "0.10",
			wantInputTok:  nil,
			wantOutputTok: nil,
		},
		{
			name: "all invalid values returns nil",
			results: map[string]string{
				"cost-usd":      "bad",
				"input-tokens":  "bad",
				"output-tokens": "bad",
			},
			wantNil: true,
		},
		{
			name: "negative tokens are rejected",
			results: map[string]string{
				"input-tokens":  "-100",
				"output-tokens": "-50",
			},
			wantNil: true,
		},
		{
			name: "negative cost is rejected",
			results: map[string]string{
				"cost-usd": "-1.5",
			},
			wantNil: true,
		},
		{
			name: "zero tokens are rejected",
			results: map[string]string{
				"input-tokens":  "0",
				"output-tokens": "0",
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := usageFromResults(tt.results)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("usageFromResults() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("usageFromResults() = nil, want non-nil")
			}
			if tt.wantCost != "" {
				if got.CostUSD == nil {
					t.Fatalf("CostUSD = nil, want %s", tt.wantCost)
				}
				expected := resource.MustParse(tt.wantCost)
				if got.CostUSD.Cmp(expected) != 0 {
					t.Errorf("CostUSD = %s, want %s", got.CostUSD.String(), tt.wantCost)
				}
			} else if got.CostUSD != nil {
				t.Errorf("CostUSD = %s, want nil", got.CostUSD.String())
			}
			if tt.wantInputTok != nil {
				if got.InputTokens == nil {
					t.Fatalf("InputTokens = nil, want %d", *tt.wantInputTok)
				}
				if *got.InputTokens != *tt.wantInputTok {
					t.Errorf("InputTokens = %d, want %d", *got.InputTokens, *tt.wantInputTok)
				}
			} else if got.InputTokens != nil {
				t.Errorf("InputTokens = %d, want nil", *got.InputTokens)
			}
			if tt.wantOutputTok != nil {
				if got.OutputTokens == nil {
					t.Fatalf("OutputTokens = nil, want %d", *tt.wantOutputTok)
				}
				if *got.OutputTokens != *tt.wantOutputTok {
					t.Errorf("OutputTokens = %d, want %d", *got.OutputTokens, *tt.wantOutputTok)
				}
			} else if got.OutputTokens != nil {
				t.Errorf("OutputTokens = %d, want nil", *got.OutputTokens)
			}
		})
	}
}

func TestComputePeriodBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		period    kelos.BudgetPeriod
		now       time.Time
		wantStart time.Time
		wantEnd   time.Time
		wantErr   bool
	}{
		{
			name: "UTC daily period",
			period: kelos.BudgetPeriod{
				Type:     kelos.BudgetPeriodDaily,
				Timezone: "UTC",
			},
			now:       time.Date(2024, 6, 15, 14, 30, 0, 0, time.UTC),
			wantStart: time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2024, 6, 16, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "empty timezone defaults to UTC",
			period: kelos.BudgetPeriod{
				Type:     kelos.BudgetPeriodDaily,
				Timezone: "",
			},
			now:       time.Date(2024, 6, 15, 23, 59, 59, 0, time.UTC),
			wantStart: time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2024, 6, 16, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "non-UTC timezone America/New_York",
			period: kelos.BudgetPeriod{
				Type:     kelos.BudgetPeriodDaily,
				Timezone: "America/New_York",
			},
			// 2024-06-15 10:00 UTC = 2024-06-15 06:00 EDT
			now: time.Date(2024, 6, 15, 10, 0, 0, 0, time.UTC),
			// Period start/end in New York time
			wantStart: time.Date(2024, 6, 15, 0, 0, 0, 0, mustLoadLocation("America/New_York")),
			wantEnd:   time.Date(2024, 6, 16, 0, 0, 0, 0, mustLoadLocation("America/New_York")),
		},
		{
			name: "invalid timezone returns error",
			period: kelos.BudgetPeriod{
				Type:     kelos.BudgetPeriodDaily,
				Timezone: "Invalid/Timezone",
			},
			now:     time.Date(2024, 6, 15, 14, 30, 0, 0, time.UTC),
			wantErr: true,
		},
		{
			name: "unsupported period type returns error",
			period: kelos.BudgetPeriod{
				Type:     "Weekly",
				Timezone: "UTC",
			},
			now:     time.Date(2024, 6, 15, 14, 30, 0, 0, time.UTC),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, err := computePeriodBoundaries(tt.period, tt.now)
			if tt.wantErr {
				if err == nil {
					t.Fatal("computePeriodBoundaries() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("computePeriodBoundaries() error = %v", err)
			}
			if !start.Equal(tt.wantStart) {
				t.Errorf("start = %v, want %v", start, tt.wantStart)
			}
			if !end.Equal(tt.wantEnd) {
				t.Errorf("end = %v, want %v", end, tt.wantEnd)
			}
		})
	}
}

func TestCheckLimitsExceeded(t *testing.T) {
	int64Ptr := func(v int64) *int64 { return &v }
	quantityPtr := func(s string) *resource.Quantity {
		q := resource.MustParse(s)
		return &q
	}

	tests := []struct {
		name         string
		budget       *kelos.TaskBudget
		used         *kelos.TaskUsage
		wantExceeded bool
		wantReason   string
	}{
		{
			name: "under all limits",
			budget: &kelos.TaskBudget{
				Spec: kelos.TaskBudgetSpec{
					MaxCostUSD:      quantityPtr("10"),
					MaxInputTokens:  int64Ptr(100000),
					MaxOutputTokens: int64Ptr(50000),
				},
			},
			used: &kelos.TaskUsage{
				CostUSD:      quantityPtr("5"),
				InputTokens:  int64Ptr(50000),
				OutputTokens: int64Ptr(25000),
			},
			wantExceeded: false,
		},
		{
			name: "cost over limit",
			budget: &kelos.TaskBudget{
				Spec: kelos.TaskBudgetSpec{
					MaxCostUSD: quantityPtr("10"),
				},
			},
			used: &kelos.TaskUsage{
				CostUSD: quantityPtr("10"),
			},
			wantExceeded: true,
			wantReason:   "cost 10 >= limit 10",
		},
		{
			name: "input tokens over limit",
			budget: &kelos.TaskBudget{
				Spec: kelos.TaskBudgetSpec{
					MaxInputTokens: int64Ptr(1000),
				},
			},
			used: &kelos.TaskUsage{
				InputTokens: int64Ptr(1500),
			},
			wantExceeded: true,
			wantReason:   "input tokens 1500 >= limit 1000",
		},
		{
			name: "output tokens over limit",
			budget: &kelos.TaskBudget{
				Spec: kelos.TaskBudgetSpec{
					MaxOutputTokens: int64Ptr(500),
				},
			},
			used: &kelos.TaskUsage{
				OutputTokens: int64Ptr(500),
			},
			wantExceeded: true,
			wantReason:   "output tokens 500 >= limit 500",
		},
		{
			name: "nil limits not exceeded",
			budget: &kelos.TaskBudget{
				Spec: kelos.TaskBudgetSpec{},
			},
			used: &kelos.TaskUsage{
				CostUSD:      quantityPtr("100"),
				InputTokens:  int64Ptr(999999),
				OutputTokens: int64Ptr(999999),
			},
			wantExceeded: false,
		},
		{
			name: "nil usage not exceeded with non-zero limits",
			budget: &kelos.TaskBudget{
				Spec: kelos.TaskBudgetSpec{
					MaxCostUSD:      quantityPtr("10"),
					MaxInputTokens:  int64Ptr(1000),
					MaxOutputTokens: int64Ptr(500),
				},
			},
			used:         &kelos.TaskUsage{},
			wantExceeded: false,
		},
		{
			name: "zero cost limit exceeded with nil usage",
			budget: &kelos.TaskBudget{
				Spec: kelos.TaskBudgetSpec{
					MaxCostUSD: quantityPtr("0"),
				},
			},
			used:         &kelos.TaskUsage{},
			wantExceeded: true,
			wantReason:   "cost 0 >= limit 0",
		},
		{
			name: "zero input tokens limit exceeded with nil usage",
			budget: &kelos.TaskBudget{
				Spec: kelos.TaskBudgetSpec{
					MaxInputTokens: int64Ptr(0),
				},
			},
			used:         &kelos.TaskUsage{},
			wantExceeded: true,
			wantReason:   "input tokens 0 >= limit 0",
		},
		{
			name: "zero output tokens limit exceeded with nil usage",
			budget: &kelos.TaskBudget{
				Spec: kelos.TaskBudgetSpec{
					MaxOutputTokens: int64Ptr(0),
				},
			},
			used:         &kelos.TaskUsage{},
			wantExceeded: true,
			wantReason:   "output tokens 0 >= limit 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exceeded, reason := checkLimitsExceeded(tt.budget, tt.used)
			if exceeded != tt.wantExceeded {
				t.Errorf("checkLimitsExceeded() exceeded = %v, want %v", exceeded, tt.wantExceeded)
			}
			if tt.wantReason != "" && reason != tt.wantReason {
				t.Errorf("checkLimitsExceeded() reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}

func TestCheckBudgetAdmission_InvalidStoredSelectorFailsClosed(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	limit := int64(1)
	budget := &kelos.TaskBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-selector", Namespace: "default"},
		Spec: kelos.TaskBudgetSpec{
			TaskSelector:   metav1.LabelSelector{MatchLabels: map[string]string{"bad key": "platform"}},
			Period:         kelos.BudgetPeriod{Type: kelos.BudgetPeriodDaily},
			MaxInputTokens: &limit,
		},
	}
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "default"},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.TaskBudget{}).
		WithObjects(budget, task).
		Build()

	enforcer := &budgetEnforcer{
		Client: cl,
		now:    func() time.Time { return time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC) },
	}
	admitted, _, err := enforcer.checkBudgetAdmission(context.Background(), task)
	if err == nil {
		t.Fatal("checkBudgetAdmission() error = nil, want invalid selector error")
	}
	if admitted {
		t.Fatal("checkBudgetAdmission() admitted = true, want false")
	}
}

func TestCheckBudgetAdmission_InvalidStoredPeriodFailsClosed(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	limit := int64(1)
	budget := &kelos.TaskBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-period", Namespace: "default"},
		Spec: kelos.TaskBudgetSpec{
			Period:         kelos.BudgetPeriod{Type: kelos.BudgetPeriodDaily, Timezone: "Invalid/Timezone"},
			MaxInputTokens: &limit,
		},
	}
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "default"},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.TaskBudget{}).
		WithObjects(budget, task).
		Build()

	enforcer := &budgetEnforcer{
		Client: cl,
		now:    func() time.Time { return time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC) },
	}
	admitted, _, err := enforcer.checkBudgetAdmission(context.Background(), task)
	if err == nil {
		t.Fatal("checkBudgetAdmission() error = nil, want invalid period error")
	}
	if admitted {
		t.Fatal("checkBudgetAdmission() admitted = true, want false")
	}

	var got kelos.TaskBudget
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "bad-period"}, &got); err != nil {
		t.Fatalf("getting TaskBudget: %v", err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, "Degraded")
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != "InvalidPeriod" {
		t.Fatalf("Degraded condition = %+v, want true InvalidPeriod", cond)
	}
}

func TestCheckBudgetAdmission_SnapshotsLabelsForMatchingBudget(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	limit := int64(1)
	budget := &kelos.TaskBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "budget", Namespace: "default"},
		Spec: kelos.TaskBudgetSpec{
			TaskSelector:   metav1.LabelSelector{MatchLabels: map[string]string{"team": "platform"}},
			Period:         kelos.BudgetPeriod{Type: kelos.BudgetPeriodDaily},
			MaxInputTokens: &limit,
		},
	}
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-1",
			Namespace: "default",
			Labels: map[string]string{
				"team": "platform",
				"env":  "prod",
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.TaskBudget{}).
		WithObjects(budget, task).
		Build()

	enforcer := &budgetEnforcer{
		Client: cl,
		now:    func() time.Time { return time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC) },
	}
	admitted, _, err := enforcer.checkBudgetAdmission(context.Background(), task)
	if err != nil {
		t.Fatalf("checkBudgetAdmission() error = %v", err)
	}
	if !admitted {
		t.Fatal("checkBudgetAdmission() admitted = false, want true")
	}

	var got kelos.Task
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "task-1"}, &got); err != nil {
		t.Fatalf("getting Task: %v", err)
	}
	if got.Annotations[taskBudgetLabelSnapshotAnnotation] == "" {
		t.Fatalf("missing %s annotation", taskBudgetLabelSnapshotAnnotation)
	}
	labels, err := taskRecordLabels(&got)
	if err != nil {
		t.Fatalf("taskRecordLabels() error = %v", err)
	}
	if labels["team"] != "platform" || labels["env"] != "prod" {
		t.Fatalf("snapshot labels = %#v, want team=platform env=prod", labels)
	}
}

func TestCreateTaskRecordUsesBudgetLabelSnapshot(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	snapshot, err := encodeTaskBudgetLabelSnapshot(map[string]string{"team": "platform", "env": "prod"})
	if err != nil {
		t.Fatalf("encoding snapshot: %v", err)
	}
	costUSD := resource.MustParse("1")
	completionTime := metav1.Now()
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-1",
			Namespace: "default",
			UID:       "task-uid",
			Labels:    map[string]string{"team": "changed"},
			Annotations: map[string]string{
				taskBudgetLabelSnapshotAnnotation: snapshot,
			},
		},
		Spec: kelos.TaskSpec{Type: "codex"},
		Status: kelos.TaskStatus{
			Phase:          kelos.TaskPhaseSucceeded,
			CompletionTime: &completionTime,
			Usage:          &kelos.TaskUsage{CostUSD: &costUSD},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(task).
		Build()

	enforcer := &budgetEnforcer{Client: cl}
	if err := enforcer.createTaskRecord(context.Background(), task); err != nil {
		t.Fatalf("createTaskRecord() error = %v", err)
	}

	var record kelos.TaskRecord
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "task-uid"}, &record); err != nil {
		t.Fatalf("getting TaskRecord: %v", err)
	}
	if record.Labels["team"] != "platform" || record.Labels["env"] != "prod" {
		t.Fatalf("record labels = %#v, want snapshot labels", record.Labels)
	}
}

func TestCreateTaskRecordUsesWorkerPoolWorkerMetadata(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	costUSD := resource.MustParse("1")
	completionTime := metav1.Now()
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-1",
			Namespace: "default",
			UID:       "task-uid",
		},
		Spec: kelos.TaskSpec{
			WorkerPoolRef: &kelos.WorkerPoolReference{Name: "pool-1"},
		},
		Status: kelos.TaskStatus{
			Phase:          kelos.TaskPhaseSucceeded,
			CompletionTime: &completionTime,
			Usage:          &kelos.TaskUsage{CostUSD: &costUSD},
		},
	}
	pool := &kelos.WorkerPool{
		ObjectMeta: metav1.ObjectMeta{Name: "pool-1", Namespace: "default"},
		Spec: kelos.WorkerPoolSpec{
			Worker: kelos.WorkerSpec{
				Type:  "claude-code",
				Model: "opus",
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(task, pool).
		Build()

	enforcer := &budgetEnforcer{Client: cl}
	if err := enforcer.createTaskRecord(context.Background(), task); err != nil {
		t.Fatalf("createTaskRecord() error = %v", err)
	}

	var record kelos.TaskRecord
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "task-uid"}, &record); err != nil {
		t.Fatalf("getting TaskRecord: %v", err)
	}
	if record.Spec.Type != "claude-code" {
		t.Fatalf("record type = %q, want claude-code", record.Spec.Type)
	}
	if record.Spec.Model != "opus" {
		t.Fatalf("record model = %q, want opus", record.Spec.Model)
	}
}

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func newTestReconciler(objs ...runtime.Object) *TaskReconciler {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		Build()

	return &TaskReconciler{
		Client: cl,
		Scheme: scheme,
	}
}

func TestSumPeriodUsage(t *testing.T) {
	quantityPtr := func(s string) *resource.Quantity {
		q := resource.MustParse(s)
		return &q
	}
	int64Ptr := func(v int64) *int64 { return &v }
	timePtr := func(t time.Time) *metav1.Time {
		mt := metav1.NewTime(t)
		return &mt
	}

	periodStart := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2024, 6, 16, 0, 0, 0, 0, time.UTC)

	inPeriod := &kelos.TaskRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "in-period",
			Namespace: "default",
			Labels:    map[string]string{"team": "alpha"},
		},
		Spec: kelos.TaskRecordSpec{
			TaskRef:        kelos.TaskReference{Name: "task-1", UID: "uid-1"},
			Phase:          kelos.TaskPhaseSucceeded,
			CompletionTime: timePtr(periodStart.Add(6 * time.Hour)),
			Usage: &kelos.TaskUsage{
				CostUSD:      quantityPtr("2.5"),
				InputTokens:  int64Ptr(1000),
				OutputTokens: int64Ptr(500),
			},
		},
	}

	alsoInPeriod := &kelos.TaskRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "also-in-period",
			Namespace: "default",
			Labels:    map[string]string{"team": "alpha"},
		},
		Spec: kelos.TaskRecordSpec{
			TaskRef:        kelos.TaskReference{Name: "task-2", UID: "uid-2"},
			Phase:          kelos.TaskPhaseSucceeded,
			CompletionTime: timePtr(periodStart.Add(12 * time.Hour)),
			Usage: &kelos.TaskUsage{
				CostUSD:      quantityPtr("1.5"),
				InputTokens:  int64Ptr(2000),
				OutputTokens: int64Ptr(800),
			},
		},
	}

	beforePeriod := &kelos.TaskRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "before-period",
			Namespace: "default",
			Labels:    map[string]string{"team": "alpha"},
		},
		Spec: kelos.TaskRecordSpec{
			TaskRef:        kelos.TaskReference{Name: "task-3", UID: "uid-3"},
			Phase:          kelos.TaskPhaseSucceeded,
			CompletionTime: timePtr(periodStart.Add(-1 * time.Hour)),
			Usage: &kelos.TaskUsage{
				CostUSD:     quantityPtr("10"),
				InputTokens: int64Ptr(99999),
			},
		},
	}

	afterPeriod := &kelos.TaskRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "after-period",
			Namespace: "default",
			Labels:    map[string]string{"team": "alpha"},
		},
		Spec: kelos.TaskRecordSpec{
			TaskRef:        kelos.TaskReference{Name: "task-4", UID: "uid-4"},
			Phase:          kelos.TaskPhaseSucceeded,
			CompletionTime: timePtr(periodEnd.Add(1 * time.Hour)),
			Usage: &kelos.TaskUsage{
				CostUSD: quantityPtr("50"),
			},
		},
	}

	differentLabel := &kelos.TaskRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "different-label",
			Namespace: "default",
			Labels:    map[string]string{"team": "beta"},
		},
		Spec: kelos.TaskRecordSpec{
			TaskRef:        kelos.TaskReference{Name: "task-5", UID: "uid-5"},
			Phase:          kelos.TaskPhaseSucceeded,
			CompletionTime: timePtr(periodStart.Add(3 * time.Hour)),
			Usage: &kelos.TaskUsage{
				CostUSD: quantityPtr("100"),
			},
		},
	}

	r := newTestReconciler(inPeriod, alsoInPeriod, beforePeriod, afterPeriod, differentLabel)

	selector := labels.SelectorFromSet(labels.Set{"team": "alpha"})
	used, err := r.sumPeriodUsage(context.Background(), "default", selector, periodStart, periodEnd)
	if err != nil {
		t.Fatalf("sumPeriodUsage() error: %v", err)
	}

	// Should sum only inPeriod + alsoInPeriod: cost 2.5+1.5=4, input 1000+2000=3000, output 500+800=1300
	wantCost := resource.MustParse("4")
	if used.CostUSD == nil || used.CostUSD.Cmp(wantCost) != 0 {
		t.Errorf("CostUSD = %v, want %s", used.CostUSD, wantCost.String())
	}
	if used.InputTokens == nil || *used.InputTokens != 3000 {
		t.Errorf("InputTokens = %v, want 3000", used.InputTokens)
	}
	if used.OutputTokens == nil || *used.OutputTokens != 1300 {
		t.Errorf("OutputTokens = %v, want 1300", used.OutputTokens)
	}
}

func TestSumPeriodUsageEmptyResult(t *testing.T) {
	periodStart := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2024, 6, 16, 0, 0, 0, 0, time.UTC)

	r := newTestReconciler()

	selector := labels.SelectorFromSet(labels.Set{"team": "alpha"})
	used, err := r.sumPeriodUsage(context.Background(), "default", selector, periodStart, periodEnd)
	if err != nil {
		t.Fatalf("sumPeriodUsage() error: %v", err)
	}

	if used.CostUSD != nil {
		t.Errorf("CostUSD = %v, want nil", used.CostUSD)
	}
	if used.InputTokens != nil {
		t.Errorf("InputTokens = %v, want nil", used.InputTokens)
	}
	if used.OutputTokens != nil {
		t.Errorf("OutputTokens = %v, want nil", used.OutputTokens)
	}
}
