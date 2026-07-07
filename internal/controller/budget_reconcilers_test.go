package controller

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func mustQuantity(s string) *resource.Quantity {
	q := resource.MustParse(s)
	return &q
}

func nowTime(t time.Time) *metav1.Time {
	mt := metav1.NewTime(t)
	return &mt
}

func TestTaskRecordReconciler_GC(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	now := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	ttl := int32(3600)

	expired := &kelos.TaskRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "expired", Namespace: "default"},
		Spec: kelos.TaskRecordSpec{
			TaskRef:                   kelos.TaskReference{Name: "t1", UID: "u1"},
			Phase:                     kelos.TaskPhaseSucceeded,
			CompletionTime:            nowTime(now.Add(-2 * time.Hour)),
			TTLSecondsAfterCompletion: &ttl,
		},
	}
	future := &kelos.TaskRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "future", Namespace: "default"},
		Spec: kelos.TaskRecordSpec{
			TaskRef:                   kelos.TaskReference{Name: "t2", UID: "u2"},
			Phase:                     kelos.TaskPhaseSucceeded,
			CompletionTime:            nowTime(now.Add(-30 * time.Minute)),
			TTLSecondsAfterCompletion: &ttl,
		},
	}
	noTTL := &kelos.TaskRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "no-ttl", Namespace: "default"},
		Spec: kelos.TaskRecordSpec{
			TaskRef:        kelos.TaskReference{Name: "t3", UID: "u3"},
			Phase:          kelos.TaskPhaseSucceeded,
			CompletionTime: nowTime(now.Add(-10 * time.Hour)),
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(expired, future, noTTL).
		Build()

	r := &TaskRecordReconciler{Client: cl, Scheme: scheme, NowFunc: func() time.Time { return now }}

	// Expired record is deleted.
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "expired", Namespace: "default"}})
	if err != nil {
		t.Fatalf("Reconcile(expired) error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("expired: RequeueAfter = %v, want 0", res.RequeueAfter)
	}
	var got kelos.TaskRecord
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "expired", Namespace: "default"}, &got); !apierrors.IsNotFound(err) {
		t.Errorf("expired record not deleted, err = %v", err)
	}

	// Future record is requeued near its expiry, not deleted.
	res, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "future", Namespace: "default"}})
	if err != nil {
		t.Fatalf("Reconcile(future) error: %v", err)
	}
	if res.RequeueAfter <= 29*time.Minute || res.RequeueAfter > 31*time.Minute {
		t.Errorf("future: RequeueAfter = %v, want ~30m", res.RequeueAfter)
	}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "future", Namespace: "default"}, &got); err != nil {
		t.Errorf("future record should still exist, err = %v", err)
	}

	// No-TTL record is retained with no requeue.
	res, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "no-ttl", Namespace: "default"}})
	if err != nil {
		t.Fatalf("Reconcile(no-ttl) error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("no-ttl: RequeueAfter = %v, want 0", res.RequeueAfter)
	}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "no-ttl", Namespace: "default"}, &got); err != nil {
		t.Errorf("no-ttl record should still exist, err = %v", err)
	}
}

func TestTaskBudgetReconciler_RefreshesUsed(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	now := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

	budget := &kelos.TaskBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "b1", Namespace: "default"},
		Spec: kelos.TaskBudgetSpec{
			TaskSelector: metav1.LabelSelector{MatchLabels: map[string]string{"team": "platform"}},
			Period:       kelos.BudgetPeriod{Type: kelos.BudgetPeriodDaily, Timezone: "UTC"},
			MaxCostUSD:   mustQuantity("100"),
		},
	}
	rec1 := &kelos.TaskRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "default", Labels: map[string]string{"team": "platform"}},
		Spec: kelos.TaskRecordSpec{
			TaskRef:        kelos.TaskReference{Name: "t1", UID: "u1"},
			Phase:          kelos.TaskPhaseSucceeded,
			CompletionTime: nowTime(now.Add(-1 * time.Hour)),
			Usage:          &kelos.TaskUsage{CostUSD: mustQuantity("30")},
		},
	}
	rec2 := &kelos.TaskRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "r2", Namespace: "default", Labels: map[string]string{"team": "platform"}},
		Spec: kelos.TaskRecordSpec{
			TaskRef:        kelos.TaskReference{Name: "t2", UID: "u2"},
			Phase:          kelos.TaskPhaseSucceeded,
			CompletionTime: nowTime(now.Add(-2 * time.Hour)),
			Usage:          &kelos.TaskUsage{CostUSD: mustQuantity("20")},
		},
	}
	// Different team — must not be counted.
	recOther := &kelos.TaskRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "r3", Namespace: "default", Labels: map[string]string{"team": "other"}},
		Spec: kelos.TaskRecordSpec{
			TaskRef:        kelos.TaskReference{Name: "t3", UID: "u3"},
			Phase:          kelos.TaskPhaseSucceeded,
			CompletionTime: nowTime(now.Add(-1 * time.Hour)),
			Usage:          &kelos.TaskUsage{CostUSD: mustQuantity("999")},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.TaskBudget{}).
		WithObjects(budget, rec1, rec2, recOther).
		Build()

	r := &TaskBudgetReconciler{Client: cl, Scheme: scheme, NowFunc: func() time.Time { return now }}

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "b1", Namespace: "default"}})
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Errorf("RequeueAfter = %v, want > 0 (period end)", res.RequeueAfter)
	}

	var got kelos.TaskBudget
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "b1", Namespace: "default"}, &got); err != nil {
		t.Fatalf("Get budget: %v", err)
	}
	if got.Status.Used == nil || got.Status.Used.CostUSD == nil {
		t.Fatalf("status.used.costUSD not set: %+v", got.Status.Used)
	}
	if got.Status.Used.CostUSD.Cmp(resource.MustParse("50")) != 0 {
		t.Errorf("status.used.costUSD = %s, want 50", got.Status.Used.CostUSD.String())
	}
}

func TestTaskBudgetReconciler_EnqueueBudgetsForRecord(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	// Empty selector matches all records in the namespace.
	bAll := &kelos.TaskBudget{ObjectMeta: metav1.ObjectMeta{Name: "b-all", Namespace: "ns1"}}
	// Selector matches the record's labels.
	bMatch := &kelos.TaskBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "b-match", Namespace: "ns1"},
		Spec:       kelos.TaskBudgetSpec{TaskSelector: metav1.LabelSelector{MatchLabels: map[string]string{"team": "platform"}}},
	}
	// Selector does not match the record's labels → must be excluded.
	bNoMatch := &kelos.TaskBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "b-nomatch", Namespace: "ns1"},
		Spec:       kelos.TaskBudgetSpec{TaskSelector: metav1.LabelSelector{MatchLabels: map[string]string{"team": "other"}}},
	}
	// Different namespace → must be excluded.
	bOther := &kelos.TaskBudget{ObjectMeta: metav1.ObjectMeta{Name: "b-other", Namespace: "ns2"}}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bAll, bMatch, bNoMatch, bOther).Build()
	r := &TaskBudgetReconciler{Client: cl, Scheme: scheme}

	record := &kelos.TaskRecord{ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "ns1", Labels: map[string]string{"team": "platform"}}}
	reqs := r.enqueueBudgetsForRecord(context.Background(), record)
	names := map[string]bool{}
	for _, req := range reqs {
		if req.Namespace != "ns1" {
			t.Errorf("enqueued budget in wrong namespace: %s", req.Namespace)
		}
		names[req.Name] = true
	}
	if len(reqs) != 2 {
		t.Fatalf("got %d requests %v, want 2 (b-all and b-match)", len(reqs), names)
	}
	if !names["b-all"] || !names["b-match"] {
		t.Errorf("missing expected budgets, got %v", names)
	}
	if names["b-nomatch"] {
		t.Errorf("non-matching budget was enqueued: %v", names)
	}
}

func TestWorkerPoolReconciler_BudgetBlocksAssignment(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	now := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "t1", Namespace: "default", UID: "uid-1", Labels: map[string]string{"team": "platform"}},
		Spec: kelos.TaskSpec{
			Type:          "claude-code",
			WorkerPoolRef: &kelos.WorkerPoolReference{Name: "pool"},
		},
	}
	budget := &kelos.TaskBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "b1", Namespace: "default"},
		Spec: kelos.TaskBudgetSpec{
			TaskSelector: metav1.LabelSelector{MatchLabels: map[string]string{"team": "platform"}},
			Period:       kelos.BudgetPeriod{Type: kelos.BudgetPeriodDaily, Timezone: "UTC"},
			MaxCostUSD:   mustQuantity("10"),
		},
	}
	// Existing usage at the limit → block.
	record := &kelos.TaskRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "default", Labels: map[string]string{"team": "platform"}},
		Spec: kelos.TaskRecordSpec{
			TaskRef:        kelos.TaskReference{Name: "t0", UID: "u0"},
			Phase:          kelos.TaskPhaseSucceeded,
			CompletionTime: nowTime(now.Add(-1 * time.Hour)),
			Usage:          &kelos.TaskUsage{CostUSD: mustQuantity("10")},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Task{}, &kelos.TaskBudget{}).
		WithObjects(task, budget, record).
		Build()

	r := newWorkerPoolReconciler(cl, scheme)
	r.NowFunc = func() time.Time { return now }

	res, err := r.reconcileTask(context.Background(), task)
	if err != nil {
		t.Fatalf("reconcileTask error: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Errorf("RequeueAfter = %v, want > 0 (blocked requeue)", res.RequeueAfter)
	}

	var got kelos.Task
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "t1", Namespace: "default"}, &got); err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if got.Status.Phase != kelos.TaskPhaseWaiting {
		t.Errorf("task phase = %q, want Waiting", got.Status.Phase)
	}
	cond := false
	for _, c := range got.Status.Conditions {
		if c.Type == "BudgetBlocked" && c.Status == metav1.ConditionTrue {
			cond = true
		}
	}
	if !cond {
		t.Errorf("expected BudgetBlocked condition, got %+v", got.Status.Conditions)
	}
	if got.Status.PodName != "" {
		t.Errorf("task should not be assigned a pod, got %q", got.Status.PodName)
	}
}

func TestUsageEqual(t *testing.T) {
	i64 := func(v int64) *int64 { return &v }
	tests := []struct {
		name string
		a, b *kelos.TaskUsage
		want bool
	}{
		{"both nil", nil, nil, true},
		{"one nil", &kelos.TaskUsage{}, nil, false},
		{"equal cost different scale", &kelos.TaskUsage{CostUSD: mustQuantity("1")}, &kelos.TaskUsage{CostUSD: mustQuantity("1000m")}, true},
		{"different cost", &kelos.TaskUsage{CostUSD: mustQuantity("1")}, &kelos.TaskUsage{CostUSD: mustQuantity("2")}, false},
		{"equal tokens", &kelos.TaskUsage{InputTokens: i64(5), OutputTokens: i64(6)}, &kelos.TaskUsage{InputTokens: i64(5), OutputTokens: i64(6)}, true},
		{"different tokens", &kelos.TaskUsage{InputTokens: i64(5)}, &kelos.TaskUsage{InputTokens: i64(7)}, false},
		{"cost nil vs set", &kelos.TaskUsage{}, &kelos.TaskUsage{CostUSD: mustQuantity("1")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := usageEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("usageEqual = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBudgetBlockUnchanged(t *testing.T) {
	wantMsg := "Budget \"b\" exceeded: cost 5 >= limit 1"
	wantCond := "Blocked by TaskBudget \"b\": cost 5 >= limit 1"

	blocked := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Generation: 3},
		Status: kelos.TaskStatus{
			Phase:   kelos.TaskPhaseWaiting,
			Message: wantMsg,
			Conditions: []metav1.Condition{{
				Type:               "BudgetBlocked",
				Status:             metav1.ConditionTrue,
				Reason:             "BudgetExceeded",
				Message:            wantCond,
				ObservedGeneration: 3,
			}},
		},
	}
	if !budgetBlockUnchanged(blocked, wantMsg, wantCond) {
		t.Error("expected unchanged=true for identical blocked state")
	}

	// Different message → changed.
	if budgetBlockUnchanged(blocked, "other", wantCond) {
		t.Error("expected unchanged=false when message differs")
	}

	// Wrong phase → changed.
	running := blocked.DeepCopy()
	running.Status.Phase = kelos.TaskPhaseRunning
	if budgetBlockUnchanged(running, wantMsg, wantCond) {
		t.Error("expected unchanged=false when phase differs")
	}

	// Stale observedGeneration → changed.
	stale := blocked.DeepCopy()
	stale.Generation = 4
	if budgetBlockUnchanged(stale, wantMsg, wantCond) {
		t.Error("expected unchanged=false when observedGeneration is stale")
	}
}

func TestBudgetEnforcer_CreateTaskRecordIdempotent(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "t1", Namespace: "default", UID: "uid-1", Labels: map[string]string{"team": "platform"}},
		Spec:       kelos.TaskSpec{Type: "claude-code"},
		Status: kelos.TaskStatus{
			Phase:          kelos.TaskPhaseSucceeded,
			CompletionTime: nowTime(time.Now()),
			Usage:          &kelos.TaskUsage{CostUSD: mustQuantity("5")},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(task).Build()
	e := &budgetEnforcer{Client: cl, now: time.Now}

	if err := e.createTaskRecord(context.Background(), task); err != nil {
		t.Fatalf("first createTaskRecord: %v", err)
	}
	// Second call must be a no-op (AlreadyExists treated as success).
	if err := e.createTaskRecord(context.Background(), task); err != nil {
		t.Fatalf("second createTaskRecord: %v", err)
	}

	var records kelos.TaskRecordList
	if err := cl.List(context.Background(), &records); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records.Items) != 1 {
		t.Fatalf("got %d records, want 1", len(records.Items))
	}
	if records.Items[0].Spec.Type != "claude-code" {
		t.Errorf("record type = %q, want claude-code", records.Items[0].Spec.Type)
	}
}
