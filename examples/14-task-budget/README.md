# TaskBudget Example

This example defines a `TaskBudget` that blocks new matching Tasks once
observed usage for the current accounting period reaches the configured
limit.

TaskBudgets are namespace-scoped. A Task matches this example when it has
the label `team: platform` in the same namespace as the budget.

## Resource

- **TaskBudget** (`taskbudget.yaml`) - sets a daily USD cost limit for Tasks
  selected by `spec.taskSelector`

## Usage

Apply the budget:

```bash
kubectl apply -f taskbudget.yaml
```

Create or update Tasks that should be governed by this budget with matching
labels:

```yaml
metadata:
  labels:
    team: platform
```

Watch budget status:

```bash
kubectl get taskbudgets
kubectl describe taskbudget daily-cost-cap
```

When completed matching Tasks report usage, Kelos records the usage in
`TaskRecord` resources and sums it for the current daily period. If the
recorded cost meets or exceeds `maxCostUSD`, new matching Tasks stay in the
`Waiting` phase with a `BudgetBlocked` condition until the period resets.

TaskBudget admission does not stop a Task that is already running. It gates
new matching Tasks before they start.

## Configuration Notes

- `spec.period.type` currently supports `Daily`.
- `spec.period.timezone` defaults to `UTC` and must be a valid IANA timezone.
- At least one limit is required: `maxCostUSD`, `maxInputTokens`, or
  `maxOutputTokens`.
- An empty `taskSelector` (`{}`) selects all Tasks in the namespace.

## Cleanup

```bash
kubectl delete -f taskbudget.yaml
```
