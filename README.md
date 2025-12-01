# neng

The anything engine.

## Failure Propagation

When a target fails, all of its **transitive dependents** are automatically marked as skipped:

```text
A → B → C → D
```

If `A` fails:

- `B`, `C`, and `D` are all marked as skipped
- `EventTargetSkipped` is emitted for each
- `RunSummary.Results` includes all targets with correct status

This ensures `RunSummary` always provides a complete picture of execution, with no targets left in an ambiguous pending state.
