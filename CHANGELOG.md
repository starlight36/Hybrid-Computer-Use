# Changelog

## Unreleased

## 1.0.1

- AX reports when a window snapshot returns only its top-level root.
  The diagnostic asks for a screenshot and verification of prior action outcomes
  before retrying. It does not infer a renderer crash, reload the application,
  or replay an action. Reads with coverage gaps say that omitted content may
  still exist; scoped reads do not trigger the window-level diagnostic.

- AX `activate` with a window ref or title can switch the target within an
  action batch. Later refs and ref-less steps use the selected window;
  refs from other windows require another explicit activation. Each step
  reports its window, and the final observation uses the last executed window.
- Linux AX `click` and `scroll` accept `allow_global_input`, defaulting to
  `true`. Setting it to `false` forbids driver focus/raise and synthesized
  pointer/keyboard input, including editor focus preparation and Qt state
  synchronization. Semantic actions can still change application focus.
  Unsupported uses reject the whole batch with `invalid_argument` before
  input. A required global route returns `global_input_forbidden` without
  input from that action. Under this policy, an AT-SPI action returning
  false after invocation reports `effect_unknown` without a global retry.
- Linux AX tab clicks prefer the parent container's accessibility selection
  operation. Supported list and table items without their own action also
  use parent selection. Failed selection verification returns
  `effect_unknown` without global input fallback.
- Linux AX scroll can use accessible scrollbar ranges with readback.
  A page means five minimum range increments, not pixels; pages remain
  approximate, and a clamped boundary no-op is verified. AX results retain
  synthesis route notes and warnings without claiming successful effects.
- Linux AX `set_value` reads back the requested control after keyboard fallback.
  Unconfirmed values return `effect_unknown` and stop the batch, preserving input
  evidence and warning against retyping text that may remain in an editor.

- Linux AX `observe` accepts `read_text` for an existing element `root`.
  It pages AT-SPI Text values and complete accessible names or descriptions
  in at most 4000 Unicode code points per call. Results include raw text,
  offsets, total length, and a next-page call; structured text pages retain
  the raw text too. Invalid ranges, owner mismatches, and unavailable Text
  fail explicitly. Lower inline budgets reduce page size without raising
  tree caps. Each page reads live content; detected length changes fail that
  call, but cross-page and same-length edits have no snapshot guarantee.
- Linux AX trees mark clipped fields with their full lengths and read calls.
  Non-interactive Text content gets an 80-code-point preview and a bindable
  ref. Existing Go cell, find excerpt, name, and description rules remain.
- Text paging and the revised hybrid guide change the model-visible contract
  and require a new evaluation attempt. Keep instruction revisions separate;
  the interface changes do not establish evaluation performance.
- AX batch errors expose the failed action's reason and a failure-specific
  recovery hint while retaining batch progress and input evidence. No-input
  argument and unsupported-operation failures use `invalid_argument`;
  uncertain input remains `effect_unknown`.
- Invalid AX activation, settle, and double-click calls explain the supported
  call shapes without accepting aliases.
- Missing, ambiguous, or moved provider-bound AX observation roots report `stale_ref` and
  request a new application observation without the invalid root.

- Linux AX `set_value` and `select` choose Chromium and Electron HTML select
  options through accessibility without opening the popup or activating the
  window. Both actions read back the selection and fail explicitly when it
  cannot be confirmed. HTML list box mutations are refused because their
  accessibility actions do not emit both `input` and `change` events.
- Linux AX and GUI accept case-insensitive keypad and X11 key names.
  Named keys use their requested symbol, including keypad digits with NumLock
  off. Unknown keys report `invalid_argument` with accepted spelling examples
  before the key step takes focus or emits input.
- Linux AX preserves references to controls hidden by an in-window modal dialog.
  After the dialog closes, each action still checks the live target; a provider
  rebuild requires a unique semantic match in the original runtime path and window.
- AX reports text-value evidence for `type` steps without a ref, including
  unchanged or unreadable fields. A focus change alone does not confirm typing.
- Linux AX checks focused-field readback after keyboard typing and reports
  unconfirmed text explicitly. If window activation changes the resolved text
  field, typing stops before keyboard input.
- Linux lists managed application windows that omit `_NET_WM_PID` when
  X-Resource can identify their owner. Windows without an accessibility tree
  support AX activation without pointer movement. Unverified owners produce
  an explicit inventory issue instead of an actionable reference.
- Linux AX `set_value` and `select` choose native GTK combo box options through
  accessibility without opening the popup or taking keyboard focus. Both actions
  verify the active item and fail explicitly if selection cannot be confirmed.
  Combo boxes with an entry use the same option selection; their entry refs
  remain available for free-form text editing.

## 1.0.0

First open-source release of Hybrid Computer Use.

### Desktop MCP Server, CLI, and SDK

- `computer-use` exposes two public MCP tools over stdio: `AX` (`apps`,
  `observe`, `find`, `act`) and `GUI` (`screenshot`, `zoom`, and batched
  pointer/keyboard input).
- `AX` binds short session references (`aN -> wN -> rN`), projects compact
  working trees while keeping all canonical elements searchable through `find`
  (with `next_cursor` paging), executes up to 64 ordered steps with in-call
  `wait` (including `settle: true`) and `select` steps, and returns
  per-target `effect:` lines, `windows:` lines, and compact AX diffs.
- `GUI` scales desktop screenshots to fit `1366x768`, supports `zoom` crops on
  the current frame, executes up to 64 ordered input actions, and checks live
  window layout at target points before each coordinate action
  (`gui_frame_stale`).
- Every `AX` and `GUI` response begins with a single-line JSON status record
  carrying one of 12 public error codes, `input_emitted`, `completed`, and a
  corrected-call `hint`.
- A per-desktop daemon (`computer-use serve`) shares live desktop state and
  reference continuity across the CLI, the Python SDK (`hcu_sdk`), attached
  MCP sessions (`--attach`), and the programmatic tool calling (PTC) adapter.

### Native Platform Backends

- **Linux** (primary): embedded Python provider using AT-SPI2, X11, GdkPixbuf,
  XTest, and optional IBus Simple composition; automatically enables session
  accessibility on first use.
- **macOS 14+**: native Swift sidecar app bundle using `AXUIElement`,
  CoreGraphics, and ScreenCaptureKit.
- **Windows 10/11**: native C# / .NET 8 sidecar using FlaUI UIA3 and Win32,
  with AF_UNIX socket support for the CLI and Python SDK.

### Instructions and OSWorld Evaluation Harness

- Plain-text model instructions in `instructions/` separate common computer-use
  policy (`base.md`) from channel interaction guides (`hybrid.md`,
  `ax-only.md`, `gui-only.md`, and `hybrid-code.md`).
- `hcu.osworld` runs OSWorld 1.0 and 2.0 tasks in isolated Docker/QEMU guests
  with Codex, Claude Code, Gemini CLI, or Grok Build, paired with task-scoped
  OpenAI Responses, Anthropic, and Gemini gateways (`hcu.gateway`) for
  credential isolation, rate-limit handling, and response-budget accounting.
