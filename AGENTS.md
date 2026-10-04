# Voice

Codex App server voice implementation.

## Sources of truth

- Tools: `.mise.toml`

## Rules

- Keep CLI code responsible for dependency wiring, process lifecycle, and output; application packages return results and errors.
- Define narrow interfaces in the consuming package and inject concrete implementations through constructors.
- Prefer small public APIs with substantial implementations; add abstractions only for demonstrated caller needs.
- Isolate provider-specific in adapters; keep agent and tool behavior independent of provider formats.
- Let the model request actions; application code validates and executes tool calls within explicit permissions.
