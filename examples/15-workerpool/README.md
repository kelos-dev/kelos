# WorkerPool Example

This example demonstrates using a WorkerPool for persistent execution.
WorkerPools maintain pre-warmed worker pods backed by StatefulSets,
eliminating cold-start overhead for tasks.

## Resources

- **WorkerPool** (`workerpool.yaml`) — Manages 2 persistent worker pods with 20Gi storage
- **Task** (`task-with-pool.yaml`) — A task that executes on the worker pool
- **TaskSpawner** (`taskspawner-with-pool.yaml`) — Automatically spawns tasks on the pool

## Usage

1. Create the secrets:
   ```bash
   kubectl apply -f credentials-secret.yaml
   kubectl apply -f github-token-secret.yaml
   ```

2. Create the workspace and worker pool:
   ```bash
   kubectl apply -f workspace.yaml
   kubectl apply -f workerpool.yaml
   ```

3. Wait for the pool to become ready:
   ```bash
   kelos get workerpools
   ```

4. Create a task:
   ```bash
   kubectl apply -f task-with-pool.yaml
   ```

The task will be assigned to an available worker pod in the pool.
