# CI Pipeline Web Dashboard

A real-time, visually-rich web-based UI for rendering events, results, and summaries of CI engine executions.

## Features

- **Real-time Pipeline Visualization**: See targets organized by execution stages in a visual DAG representation
- **Live Event Log**: Watch events stream in real-time as targets start, complete, or fail
- **Plan Composer**: Build custom pipelines by selecting targets with automatic dependency resolution
- **Saved Plans**: Save, load, and manage reusable pipeline configurations
- **Run Modes**: Execute all targets, specific targets, or individual stages
- **Failure Simulation**: Test failure handling by selecting targets to fail
- **Interactive Controls**: Start, cancel, and clear pipeline runs with dedicated buttons
- **Comprehensive Summary**: View detailed run statistics and per-target results after completion
- **Modern Dark Theme**: GitHub-inspired dark UI with smooth animations
- **Server-Sent Events (SSE)**: Efficient real-time communication without WebSocket complexity

## Project Structure

```text
wip/examples/clients/web/
├── main.go              # Entry point - embeds frontend, runs backend
├── README.md            # This file
├── backend/
│   └── server.go        # HTTP server, API handlers, SSE broadcasting
├── frontend/
│   ├── index.html       # Main HTML entry point
│   ├── ARCHITECTURE.md  # Frontend architecture documentation
│   ├── css/             # Modular CSS stylesheets
│   ├── html/            # HTML component templates (reference)
│   └── js/              # ES6 JavaScript modules
└── todos/               # Tracked issues and improvements
    ├── 1-CRITICAL.md
    ├── 2-HIGH.md
    ├── 3-MEDIUM.md
    └── 4-LOW.md
```

## Architecture

```text
┌─────────────────────────────────────────────────────────┐
│                    Web Browser                          │
│ ┌─────────────────────────────────────────────────────┐ │
│ │              Frontend (ES6 Modules)                 │ │
│ │  • CIDashboard  - SSE connection, event handling    │ │
│ │  • PlanComposer - Target selection, plan preview    │ │
│ │  • PlanRunner   - Saved plans, run mode selection   │ │
│ └─────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────┘
                             │
                             │ SSE (Server-Sent Events)
                             │ REST API
                             ▼
┌─────────────────────────────────────────────────────────┐
│               Go Server (backend/server.go)             │
│ ┌─────────────────────────────────────────────────────┐ │
│ │              sseEventSink                           │ │
│ │  • Implements engine.EventHandler                   │ │
│ │  • Broadcasts events to all SSE clients             │ │
│ │  • Manages client connections                       │ │
│ └─────────────────────────────────────────────────────┘ │
│ ┌─────────────────────────────────────────────────────┐ │
│ │              HTTP Handlers                          │ │
│ │  • /api/events      - SSE endpoint                  │ │
│ │  • /api/run         - Start demo pipeline           │ │
│ │  • /api/run/custom  - Start custom pipeline         │ │
│ │  • /api/run/plan    - Run from saved plan           │ │
│ │  • /api/cancel      - Cancel running pipeline       │ │
│ │  • /api/status      - Check current run status      │ │
│ │  • /api/targets     - List available targets        │ │
│ │  • /api/preview     - Preview plan stages           │ │
│ │  • /api/plans/*     - Saved plans CRUD              │ │
│ └─────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────┘
                             │
                             │ engine.EventHandler
                             ▼
┌─────────────────────────────────────────────────────────┐
│                    CI Engine                            │
│ ┌─────────────────────────────────────────────────────┐ │
│ │              engine.Executor                        │ │
│ │  • Executes Plan with worker pool                   │ │
│ │  • Emits events via EventSink                       │ │
│ │  • Returns RunSummary                               │ │
│ └─────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────┘
```

## Running the Server

```bash
# From the web directory
cd wip/examples/clients/web
go run .

# Or build and run
go build -o ci-dashboard
./ci-dashboard
```

The server starts on `http://localhost:8080` by default.

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/` | GET | Serves the main dashboard HTML |
| `/frontend/*` | GET | Serves static assets (CSS, JS) |
| `/api/events` | GET | SSE endpoint for real-time events |
| `/api/run` | POST | Start a demo pipeline run |
| `/api/run?fail=true` | POST | Start a demo run with simulated failure |
| `/api/run/custom` | POST | Start a custom pipeline with selected targets |
| `/api/run/plan` | POST | Run from a saved plan |
| `/api/cancel` | POST | Cancel the currently running pipeline |
| `/api/status` | GET | Check if a pipeline is running |
| `/api/targets` | GET | List available targets for plan composition |
| `/api/preview` | POST | Preview plan stages without executing |
| `/api/plans` | GET | List all saved plans |
| `/api/plans/save` | POST | Save a new plan |
| `/api/plans/delete` | DELETE | Delete a saved plan |
| `/api/plans/details` | GET | Get detailed plan information |

## Event Types

The SSE stream sends JSON events:

### Plan Event

Sent at the start of a run with pipeline metadata:

```json
{
  "type": "plan",
  "targets": [{"name": "build", "desc": "...", "deps": ["lint"]}],
  "stages": [{"index": 0, "targets": ["checkout"]}]
}
```

### Target Events

```json
{"type": "started", "time": "...", "targetName": "build"}
{"type": "completed", "time": "...", "targetName": "build", "result": {...}}
{"type": "skipped", "time": "...", "targetName": "deploy"}
```

### Summary Event

Sent when the run completes:

```json
{
  "type": "summary",
  "failed": false,
  "results": {"build": {"durationMs": 450, ...}}
}
```

## Demo Pipeline

The included demo pipeline simulates a typical CI/CD workflow:

```text
Stage 0: checkout
Stage 1: install-deps
Stage 2: lint, format-check, unit-tests
Stage 3: build, integration-tests
Stage 4: security-scan, package
Stage 5: docker-build
Stage 6: push-registry
Stage 7: deploy-staging
Stage 8: smoke-tests
Stage 9: all
```

## Screenshot

The dashboard features:

- Dark theme with GitHub-inspired styling
- Real-time connection status indicator
- Pipeline graph with stages and targets
- Animated status indicators for running targets
- Scrollable event log with timestamps
- Comprehensive run summary with statistics

## Dependencies

- Go 1.21+ (for `embed` directive and generics)
- No external Go dependencies (stdlib only)
- No build tools required for frontend (vanilla JS/CSS)
