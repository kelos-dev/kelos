package controller

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	taskPipelineLabel      = "kelos.dev/taskpipeline"
	pipelineStageLabel     = "kelos.dev/pipeline-stage"
	pipelineTaskIndexLabel = "kelos.dev/pipeline-index"
	maxPipelineStageTasks  = 256
	pipelineCollisionWait  = 2 * time.Second
)

// TaskPipelineReconciler creates and observes the Tasks in a TaskPipeline.
type TaskPipelineReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kelos.dev,resources=taskpipelines,verbs=get;list;watch
// +kubebuilder:rbac:groups=kelos.dev,resources=taskpipelines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kelos.dev,resources=tasks,verbs=create;get;list;watch

// Reconcile creates the next pipeline stage and aggregates child Task status.
func (r *TaskPipelineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var pipeline kelos.TaskPipeline
	if err := r.Get(ctx, req.NamespacedName, &pipeline); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := validateTaskPipeline(&pipeline); err != nil {
		return ctrl.Result{}, r.failInvalidPipeline(ctx, &pipeline, err)
	}

	tasksByStage, err := r.listPipelineTasks(ctx, &pipeline)
	if err != nil {
		return ctrl.Result{}, err
	}
	if isTerminalTaskPipelinePhase(pipeline.Status.Phase) {
		return ctrl.Result{}, nil
	}

	stageStatuses := summarizePipelineStages(&pipeline, tasksByStage, nil)
	hasFailure := firstPipelineFailure(stageStatuses, tasksByStage, nil) != ""
	stageErrors := make(map[string]string)
	var requeueAfter time.Duration

	if !hasFailure {
		for i := range pipeline.Spec.Stages {
			stage := &pipeline.Spec.Stages[i]
			if i > 0 && stageStatuses[i-1].Phase != kelos.TaskPhaseSucceeded {
				break
			}

			matrixValues, _ := expandPipelineMatrix(stage.Matrix)
			existing := pipelineTasksByIndex(tasksByStage[stage.Name])
			for index, matrix := range matrixValues {
				if _, ok := existing[index]; ok {
					continue
				}

				task, buildErr := buildPipelineTask(&pipeline, stage, index, len(matrixValues), matrix, tasksByStage)
				if buildErr != nil {
					if wait := pipelineResultRetryAfter(&pipeline, i, tasksByStage, time.Now()); wait > 0 {
						requeueAfter = minimumPositiveDuration(requeueAfter, wait)
						break
					}
					stageErrors[stage.Name] = buildErr.Error()
					break
				}
				if err := controllerutil.SetControllerReference(&pipeline, task, r.Scheme); err != nil {
					return ctrl.Result{}, fmt.Errorf("setting TaskPipeline %q owner on Task %q: %w", pipeline.Name, task.Name, err)
				}

				if err := r.Create(ctx, task); err != nil {
					if apierrors.IsAlreadyExists(err) {
						var existingTask kelos.Task
						getErr := r.Get(ctx, client.ObjectKeyFromObject(task), &existingTask)
						switch {
						case apierrors.IsNotFound(getErr):
							requeueAfter = minimumPositiveDuration(requeueAfter, pipelineCollisionWait)
						case getErr != nil:
							return ctrl.Result{}, fmt.Errorf("getting existing Task %q for TaskPipeline %q: %w", task.Name, pipeline.Name, getErr)
						case metav1.IsControlledBy(&existingTask, &pipeline):
							tasksByStage[stage.Name] = append(tasksByStage[stage.Name], &existingTask)
							existing[index] = &existingTask
						case taskOwnedByEarlierPipeline(&existingTask, &pipeline):
							requeueAfter = minimumPositiveDuration(requeueAfter, pipelineCollisionWait)
						default:
							stageErrors[stage.Name] = fmt.Sprintf("Task name %q is already in use", task.Name)
						}
						if _, ok := existing[index]; !ok {
							break
						}
						continue
					}
					return ctrl.Result{}, fmt.Errorf("creating Task %q for TaskPipeline %q: %w", task.Name, pipeline.Name, err)
				}

				logger.Info("Created pipeline Task", "taskPipeline", pipeline.Name, "stage", stage.Name, "task", task.Name)
				tasksByStage[stage.Name] = append(tasksByStage[stage.Name], task)
			}
			if len(stageErrors) > 0 {
				break
			}
		}
	}

	stageStatuses = summarizePipelineStages(&pipeline, tasksByStage, stageErrors)
	failure := firstPipelineFailure(stageStatuses, tasksByStage, stageErrors)
	if err := r.updateTaskPipelineStatus(ctx, &pipeline, stageStatuses, failure); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

func (r *TaskPipelineReconciler) failInvalidPipeline(ctx context.Context, pipeline *kelos.TaskPipeline, validationErr error) error {
	original := pipeline.DeepCopy()
	pipeline.Status.ObservedGeneration = pipeline.Generation
	pipeline.Status.Phase = kelos.TaskPipelinePhaseFailed
	setTaskPipelineReadyCondition(pipeline, metav1.ConditionFalse, "InvalidPipeline", validationErr.Error())
	if reflect.DeepEqual(original.Status, pipeline.Status) {
		return nil
	}
	if err := r.Status().Patch(ctx, pipeline, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("updating invalid TaskPipeline %q status: %w", pipeline.Name, err)
	}
	return nil
}

func (r *TaskPipelineReconciler) listPipelineTasks(ctx context.Context, pipeline *kelos.TaskPipeline) (map[string][]*kelos.Task, error) {
	var taskList kelos.TaskList
	if err := r.List(ctx, &taskList,
		client.InNamespace(pipeline.Namespace),
		client.MatchingLabels{taskPipelineLabel: pipelineLabelValue(pipeline.Name)},
	); err != nil {
		return nil, fmt.Errorf("listing Tasks for TaskPipeline %q: %w", pipeline.Name, err)
	}

	tasksByStage := make(map[string][]*kelos.Task, len(pipeline.Spec.Stages))
	for i := range taskList.Items {
		task := &taskList.Items[i]
		if !metav1.IsControlledBy(task, pipeline) {
			continue
		}
		stageName := task.Labels[pipelineStageLabel]
		if stageName == "" {
			return nil, fmt.Errorf("Task %q owned by TaskPipeline %q has no %s label", task.Name, pipeline.Name, pipelineStageLabel)
		}
		tasksByStage[stageName] = append(tasksByStage[stageName], task)
	}
	for stageName := range tasksByStage {
		sort.Slice(tasksByStage[stageName], func(i, j int) bool {
			return pipelineTaskIndex(tasksByStage[stageName][i]) < pipelineTaskIndex(tasksByStage[stageName][j])
		})
	}
	return tasksByStage, nil
}

func validateTaskPipeline(pipeline *kelos.TaskPipeline) error {
	if len(pipeline.Spec.Stages) == 0 {
		return errors.New("pipeline must contain at least one stage")
	}

	stageNames := make(map[string]struct{}, len(pipeline.Spec.Stages))
	for i := range pipeline.Spec.Stages {
		stage := &pipeline.Spec.Stages[i]
		if errs := validation.IsDNS1123Label(stage.Name); len(errs) > 0 {
			return fmt.Errorf("stage name %q is invalid: %s", stage.Name, strings.Join(errs, "; "))
		}
		if _, exists := stageNames[stage.Name]; exists {
			return fmt.Errorf("stage name %q is duplicated", stage.Name)
		}
		stageNames[stage.Name] = struct{}{}

		matrixValues, err := expandPipelineMatrix(stage.Matrix)
		if err != nil {
			return fmt.Errorf("stage %q: %w", stage.Name, err)
		}
		if len(matrixValues) > maxPipelineStageTasks {
			return fmt.Errorf("stage %q matrix expands to %d Tasks; maximum is %d", stage.Name, len(matrixValues), maxPipelineStageTasks)
		}
		if err := parsePipelineTemplate(stage.Name+" prompt", stage.TaskTemplate.Prompt); err != nil {
			return fmt.Errorf("stage %q prompt: %w", stage.Name, err)
		}
		if stage.TaskTemplate.Branch != "" {
			if err := parsePipelineTemplate(stage.Name+" branch", stage.TaskTemplate.Branch); err != nil {
				return fmt.Errorf("stage %q branch: %w", stage.Name, err)
			}
		}
	}
	return nil
}

func expandPipelineMatrix(matrix *kelos.PipelineMatrix) ([]map[string]string, error) {
	if matrix == nil {
		return []map[string]string{{}}, nil
	}
	if len(matrix.Parameters) == 0 {
		return nil, errors.New("matrix must contain at least one parameter")
	}

	parameters := append([]kelos.PipelineMatrixParameter(nil), matrix.Parameters...)
	sort.Slice(parameters, func(i, j int) bool {
		return parameters[i].Name < parameters[j].Name
	})
	for i := range parameters {
		if parameters[i].Name == "" {
			return nil, errors.New("matrix parameter name must not be empty")
		}
		if i > 0 && parameters[i-1].Name == parameters[i].Name {
			return nil, fmt.Errorf("matrix parameter %q is duplicated", parameters[i].Name)
		}
		if len(parameters[i].Values) == 0 {
			return nil, fmt.Errorf("matrix parameter %q must contain at least one value", parameters[i].Name)
		}
		parameters[i].Values = append([]string(nil), parameters[i].Values...)
		sort.Strings(parameters[i].Values)
	}

	combinations := []map[string]string{{}}
	for _, parameter := range parameters {
		next := make([]map[string]string, 0, len(combinations)*len(parameter.Values))
		for _, combination := range combinations {
			for _, value := range parameter.Values {
				expanded := make(map[string]string, len(combination)+1)
				for existingKey, existingValue := range combination {
					expanded[existingKey] = existingValue
				}
				expanded[parameter.Name] = value
				next = append(next, expanded)
			}
		}
		combinations = next
		if len(combinations) > maxPipelineStageTasks {
			return combinations, nil
		}
	}
	return combinations, nil
}

func parsePipelineTemplate(name, source string) error {
	_, err := template.New(name).
		Funcs(template.FuncMap{"index": strictTemplateIndex}).
		Option("missingkey=error").
		Parse(source)
	return err
}

func renderPipelineTemplate(name, source string, data map[string]interface{}) (string, error) {
	tmpl, err := template.New(name).
		Funcs(template.FuncMap{"index": strictTemplateIndex}).
		Option("missingkey=error").
		Parse(source)
	if err != nil {
		return "", err
	}
	var rendered strings.Builder
	if err := tmpl.Execute(&rendered, data); err != nil {
		return "", err
	}
	return rendered.String(), nil
}

func strictTemplateIndex(item interface{}, indices ...interface{}) (interface{}, error) {
	value := reflect.ValueOf(item)
	for _, index := range indices {
		for value.IsValid() && (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) {
			if value.IsNil() {
				return nil, errors.New("cannot index a nil value")
			}
			value = value.Elem()
		}
		if !value.IsValid() {
			return nil, errors.New("cannot index an invalid value")
		}

		switch value.Kind() {
		case reflect.Array, reflect.Slice, reflect.String:
			position, ok := templateIndexInteger(index)
			if !ok {
				return nil, fmt.Errorf("index %v is not an integer", index)
			}
			if position < 0 || position >= value.Len() {
				return nil, fmt.Errorf("index %d is out of range", position)
			}
			value = value.Index(position)
		case reflect.Map:
			key := reflect.ValueOf(index)
			if !key.IsValid() {
				return nil, errors.New("map index is nil")
			}
			if !key.Type().AssignableTo(value.Type().Key()) {
				if !key.Type().ConvertibleTo(value.Type().Key()) {
					return nil, fmt.Errorf("index %v has type %s, want %s", index, key.Type(), value.Type().Key())
				}
				key = key.Convert(value.Type().Key())
			}
			value = value.MapIndex(key)
			if !value.IsValid() {
				return nil, fmt.Errorf("key %q is not present", index)
			}
		default:
			return nil, fmt.Errorf("value of type %s cannot be indexed", value.Type())
		}
	}
	for value.IsValid() && (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) {
		if value.IsNil() {
			return nil, nil
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return nil, nil
	}
	return value.Interface(), nil
}

func templateIndexInteger(value interface{}) (int, bool) {
	integer := reflect.ValueOf(value)
	if !integer.IsValid() {
		return 0, false
	}
	switch integer.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(integer.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return int(integer.Uint()), true
	default:
		return 0, false
	}
}

func buildPipelineTask(
	pipeline *kelos.TaskPipeline,
	stage *kelos.PipelineStage,
	index, total int,
	matrix map[string]string,
	tasksByStage map[string][]*kelos.Task,
) (*kelos.Task, error) {
	templateData := map[string]interface{}{
		"Matrix": matrix,
		"Stages": pipelineStageTemplateData(pipeline, stage, tasksByStage),
	}
	prompt, err := renderPipelineTemplate(stage.Name+" prompt", stage.TaskTemplate.Prompt, templateData)
	if err != nil {
		return nil, fmt.Errorf("rendering prompt: %w", err)
	}
	branch := stage.TaskTemplate.Branch
	if branch != "" {
		branch, err = renderPipelineTemplate(stage.Name+" branch", branch, templateData)
		if err != nil {
			return nil, fmt.Errorf("rendering branch: %w", err)
		}
	}

	name := pipelineChildTaskName(pipeline.Name, stage.Name, index, total)
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: pipeline.Namespace,
			Labels: map[string]string{
				taskPipelineLabel:      pipelineLabelValue(pipeline.Name),
				pipelineStageLabel:     stage.Name,
				pipelineTaskIndexLabel: strconv.Itoa(index),
			},
		},
		Spec: kelos.TaskSpec{
			Prompt: prompt,
			Branch: branch,
		},
	}
	if stage.TaskTemplate.Worker != nil {
		task.Spec.Worker = stage.TaskTemplate.Worker.DeepCopy()
	}
	if stage.TaskTemplate.WorkerPoolRef != nil {
		task.Spec.WorkerPoolRef = stage.TaskTemplate.WorkerPoolRef.DeepCopy()
	}
	return task, nil
}

// pipelineResultRetryAfter keeps a missing template value retryable while a
// completed earlier stage is still within the Task controller's output capture window.
func pipelineResultRetryAfter(
	pipeline *kelos.TaskPipeline,
	stageIndex int,
	tasksByStage map[string][]*kelos.Task,
	now time.Time,
) time.Duration {
	var retryAfter time.Duration
	for i := 0; i < stageIndex; i++ {
		for _, task := range tasksByStage[pipeline.Spec.Stages[i].Name] {
			if task.Status.Phase != kelos.TaskPhaseSucceeded || task.Status.CompletionTime == nil {
				continue
			}
			if len(task.Status.Outputs) > 0 || len(task.Status.Results) > 0 {
				continue
			}
			remaining := task.Status.CompletionTime.Add(outputRetryWindow).Sub(now)
			if remaining > retryAfter {
				retryAfter = remaining
			}
		}
	}
	return retryAfter
}

func minimumPositiveDuration(current, candidate time.Duration) time.Duration {
	if current <= 0 || candidate < current {
		return candidate
	}
	return current
}

func pipelineStageTemplateData(
	pipeline *kelos.TaskPipeline,
	stage *kelos.PipelineStage,
	tasksByStage map[string][]*kelos.Task,
) map[string]interface{} {
	data := make(map[string]interface{})
	for i := range pipeline.Spec.Stages {
		completedStage := &pipeline.Spec.Stages[i]
		if completedStage.Name == stage.Name {
			break
		}
		tasks := append([]*kelos.Task(nil), tasksByStage[completedStage.Name]...)
		combinations, _ := expandPipelineMatrix(completedStage.Matrix)
		sort.Slice(tasks, func(i, j int) bool {
			return pipelineTaskIndex(tasks[i]) < pipelineTaskIndex(tasks[j])
		})
		results := make([]interface{}, 0, len(tasks))
		for _, task := range tasks {
			index := pipelineTaskIndex(task)
			var matrix map[string]string
			if index >= 0 && index < len(combinations) {
				matrix = combinations[index]
			}
			results = append(results, map[string]interface{}{
				"Name":    task.Name,
				"Matrix":  matrix,
				"Outputs": task.Status.Outputs,
				"Results": task.Status.Results,
			})
		}
		data[completedStage.Name] = results
	}
	return data
}

func pipelineChildTaskName(pipelineName, stageName string, index, total int) string {
	name := pipelineName + "-" + stageName
	if total > 1 {
		name += "-" + strconv.Itoa(index)
	}
	const maxTaskNameLength = 63
	if len(name) <= maxTaskNameLength {
		return name
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))[:8]
	prefixLength := maxTaskNameLength - len(hash) - 1
	prefix := strings.TrimRight(name[:prefixLength], "-.")
	return prefix + "-" + hash
}

func pipelineLabelValue(pipelineName string) string {
	const maxLabelValueLength = 63
	if len(pipelineName) <= maxLabelValueLength {
		return pipelineName
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(pipelineName)))[:8]
	prefixLength := maxLabelValueLength - len(hash) - 1
	prefix := strings.TrimRight(pipelineName[:prefixLength], "-._")
	return prefix + "-" + hash
}

func pipelineTasksByIndex(tasks []*kelos.Task) map[int]*kelos.Task {
	indexed := make(map[int]*kelos.Task, len(tasks))
	for _, task := range tasks {
		indexed[pipelineTaskIndex(task)] = task
	}
	return indexed
}

func taskOwnedByEarlierPipeline(task *kelos.Task, pipeline *kelos.TaskPipeline) bool {
	for _, owner := range task.OwnerReferences {
		if owner.Kind == "TaskPipeline" && owner.Name == pipeline.Name && owner.UID != pipeline.UID {
			return true
		}
	}
	return false
}

func pipelineTaskIndex(task *kelos.Task) int {
	index, err := strconv.Atoi(task.Labels[pipelineTaskIndexLabel])
	if err != nil {
		return -1
	}
	return index
}

func summarizePipelineStages(
	pipeline *kelos.TaskPipeline,
	tasksByStage map[string][]*kelos.Task,
	stageErrors map[string]string,
) []kelos.PipelineStageStatus {
	statuses := make([]kelos.PipelineStageStatus, 0, len(pipeline.Spec.Stages))
	for i := range pipeline.Spec.Stages {
		stage := &pipeline.Spec.Stages[i]
		combinations, _ := expandPipelineMatrix(stage.Matrix)
		tasks := append([]*kelos.Task(nil), tasksByStage[stage.Name]...)
		sort.Slice(tasks, func(i, j int) bool {
			return pipelineTaskIndex(tasks[i]) < pipelineTaskIndex(tasks[j])
		})

		status := kelos.PipelineStageStatus{Name: stage.Name, Total: int32(len(combinations))}
		for _, task := range tasks {
			switch task.Status.Phase {
			case kelos.TaskPhaseSucceeded:
				status.Succeeded++
			case kelos.TaskPhaseFailed:
				status.Failed++
			default:
				status.Active++
			}
		}

		switch {
		case stageErrors[stage.Name] != "":
			status.Phase = kelos.TaskPhaseFailed
		case status.Failed > 0:
			status.Phase = kelos.TaskPhaseFailed
		case status.Succeeded == status.Total && int32(len(tasks)) == status.Total:
			status.Phase = kelos.TaskPhaseSucceeded
		case len(tasks) > 0:
			status.Phase = kelos.TaskPhaseRunning
		case i > 0 && statuses[i-1].Phase != kelos.TaskPhaseSucceeded:
			status.Phase = kelos.TaskPhaseWaiting
		default:
			status.Phase = kelos.TaskPhasePending
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func firstPipelineFailure(
	statuses []kelos.PipelineStageStatus,
	tasksByStage map[string][]*kelos.Task,
	stageErrors map[string]string,
) string {
	for _, status := range statuses {
		if status.Phase == kelos.TaskPhaseFailed {
			if stageErrors[status.Name] != "" {
				return fmt.Sprintf("Stage %q failed: %s", status.Name, stageErrors[status.Name])
			}
			for _, task := range tasksByStage[status.Name] {
				if task.Status.Phase != kelos.TaskPhaseFailed {
					continue
				}
				message := fmt.Sprintf("Stage %q failed because Task %q failed", status.Name, task.Name)
				if task.Status.Message != "" {
					message += ": " + task.Status.Message
				}
				return message
			}
			return fmt.Sprintf("Stage %q failed", status.Name)
		}
	}
	return ""
}

func (r *TaskPipelineReconciler) updateTaskPipelineStatus(
	ctx context.Context,
	pipeline *kelos.TaskPipeline,
	stageStatuses []kelos.PipelineStageStatus,
	failure string,
) error {
	original := pipeline.DeepCopy()
	pipeline.Status.ObservedGeneration = pipeline.Generation
	pipeline.Status.StageStatuses = stageStatuses

	var active int32
	var observed int32
	allSucceeded := len(stageStatuses) > 0
	for _, status := range stageStatuses {
		active += status.Active
		observed += status.Active + status.Succeeded + status.Failed
		if status.Phase != kelos.TaskPhaseSucceeded {
			allSucceeded = false
		}
	}
	switch {
	case allSucceeded:
		pipeline.Status.Phase = kelos.TaskPipelinePhaseSucceeded
		setTaskPipelineReadyCondition(pipeline, metav1.ConditionTrue, "Succeeded", "All pipeline stages succeeded")
	case failure != "" && active == 0:
		pipeline.Status.Phase = kelos.TaskPipelinePhaseFailed
		setTaskPipelineReadyCondition(pipeline, metav1.ConditionFalse, "Failed", failure)
	case observed > 0:
		pipeline.Status.Phase = kelos.TaskPipelinePhaseRunning
		message := "Pipeline is running"
		if failure != "" {
			message = failure
		}
		setTaskPipelineReadyCondition(pipeline, metav1.ConditionUnknown, "Running", message)
	default:
		pipeline.Status.Phase = kelos.TaskPipelinePhasePending
		setTaskPipelineReadyCondition(pipeline, metav1.ConditionUnknown, "Pending", "Pipeline has not started")
	}

	if reflect.DeepEqual(original.Status, pipeline.Status) {
		return nil
	}
	if err := r.Status().Patch(ctx, pipeline, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("updating TaskPipeline %q status: %w", pipeline.Name, err)
	}
	return nil
}

func isTerminalTaskPipelinePhase(phase kelos.TaskPipelinePhase) bool {
	return phase == kelos.TaskPipelinePhaseSucceeded || phase == kelos.TaskPipelinePhaseFailed
}

func setTaskPipelineReadyCondition(
	pipeline *kelos.TaskPipeline,
	status metav1.ConditionStatus,
	reason string,
	message string,
) {
	apiMeta.SetStatusCondition(&pipeline.Status.Conditions, metav1.Condition{
		Type:               kelos.TaskPipelineConditionReady,
		Status:             status,
		ObservedGeneration: pipeline.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// SetupWithManager sets up the TaskPipeline controller with the Manager.
func (r *TaskPipelineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kelos.TaskPipeline{}).
		Owns(&kelos.Task{}).
		Complete(r)
}
