# Native Platforms: Linux (AT-SPI2), macOS (AXUIElement), Windows (UI Automation)

The public MCP surface is always `AX` and `GUI`. The binary selects its
backend at compile time; an unsupported OS fails explicitly. No platform
selection parameter is exposed to the model.

| OS | Native implementation | Runtime requirements |
| --- | --- | --- |
| Linux | Embedded Python, AT-SPI2, X11 | Signed-in X11 session and the packages in [Computer Use MCP](MCP.md#desktop-runtime); the provider switches accessibility on, and applications started before it may need a restart ([details](MCP.md#accessibility-on-linux)) |
| macOS | Swift, AXUIElement, CoreGraphics, ScreenCaptureKit | macOS 14+, graphical login, Accessibility and Screen Recording consent |
| Windows | C#/.NET 8, FlaUI UIA3 5.0, Win32 | Unlocked interactive session, compatible target integrity level |

The Linux provider is evaluated on OSWorld 1.0, OSWorld 2.0 and WeaveBench,
the macOS provider on MacAgentBench, and the Windows provider on Windows
Agent Arena. The OSWorld harness in this repository uses Linux guests; the
native macOS and Windows providers control a local desktop.

On Windows, the MCP starts its native provider and process-status command with
`CREATE_NO_WINDOW`. Each background launch sets this flag because the launcher's
console settings do not apply automatically to descendants. The MCP client is
responsible for starting the MCP itself without an unwanted console window.

| OS | Checked in CI | Not checked in CI |
| --- | --- | --- |
| Linux | Go tests, race tests of both native transports, provider tests with PyGObject | Desktop input; OSWorld guest runs cover it |
| macOS | Swift unit tests, `GOOS=darwin go vet`, package build, MCP smoke test | Permissions and desktop input |
| Windows | .NET protocol and policy tests, `GOOS=windows go vet`, package build, MCP smoke test | Session, integrity level and desktop input |

## Agent cursor on Linux

Set `COMPUTER_USE_CURSOR=1` when starting `computer-use mcp` or the shared
daemon to show an Agent arrow, animated moves, click pulses and AX target
outlines on the desktop. Remote desktop viewers and display recordings see
the same overlay. This is an optional Linux X11 feature; it requires a
24-bit desktop, an ARGB32 visual, Shape input passthrough and an active
compositing manager.
Unsupported sessions leave it disabled and write an operator diagnostic.

The renderer runs in the Go process. The existing Linux provider sends
geometry-only cues through a private inherited socket; typed text and key
contents are not sent. The overlay does not take focus or intercept input.
It illustrates HCU's existing actions and does not provide background input
or separate control of the real pointer.

Movement is illustrative and never delays input. A click pulse indicates
emitted pointer input, an AX outline indicates the operation's target, and a
red arrow indicates an AX operation that raised an error. These cues do not
prove the application applied the action. Idle cursors disappear automatically.

With the standard X11 capture path, HCU takes a desktop-wide
capture lease and temporarily clips all HCU overlays to empty shapes. The
windows stay mapped, so clipping does not start a window close animation.
Clients with the cursor disabled also participate. A one-pixel witness
confirms that the compositor has processed the clips and then removes itself
the same way. This requires ordered compositor handling of shape changes.
Recordings can show brief gaps and the witness during these captures. If a
reader disconnects mid-capture, surviving renderers repaint their cues once
the lease can be reclaimed.

If exclusion cannot be confirmed, HCU refuses the capture. The failed call
requests a provider reset; the next call starts a fresh provider without
restarting the MCP server or daemon. Desktop actions are not automatically
replayed, and input already emitted remains reported. Recovery requires a
responsive desktop and compositor. Normal renderer shutdown clips the cues
and lets the compositor retire them before destroying the windows. Native
connection operations have bounded deadlines, and shutdown can close the
underlying X11 transport even when X11 is stalled. On desktops that animate
destroyed windows, exclude HCU overlays from close animations to cover a
forced renderer kill, when HCU cannot clip the windows before they disappear.

### Clean capture with the Picom companion

A managed X11 desktop can run the optional Picom companion with its legacy
24-bit XRender backend. Build it with
`computer-use/native/linux/picom/build.sh <output-directory>` on Linux with the
Picom build dependencies, Meson, Ninja, curl, patch and sha256sum installed.
The build script verifies its pinned source archive.

Pushes to the `clawsphere` branch build Debian 12 packages for Linux amd64
and arm64 in the **ClawSphere Linux packages** workflow. Download the
`hcu-<version>-linux-<arch>.tar.gz` for your architecture from the run's
artifacts. Each archive contains `computer-use` and its matching
`picom-hcu`. Both use the desktop's installed runtime libraries and tools.
With Go and the build dependencies installed, run
`scripts/package-linux.sh artifacts` on either architecture to build locally.

Set `HCU_CLEAN_CAPTURE_SOCKET` to an absolute socket path inside a directory
owned by the desktop user with mode 0700. Start `picom-hcu --backend xrender`
in place of the desktop's regular compositor, with that variable in its
environment. Start HCU for the same display and user with the same socket
path. Keep a separate endpoint for each display. HCU checks the compositor's
session identifier against its own X11 root, including after a restart.
Update the companion and HCU together when deploying this protocol.

The companion uses the same XRender painting code and skips the marked HCU
layers in a temporary offscreen render. GUI screenshots and AX pixel evidence
use that output. The desktop, VNC and recordings continue to see the visible
scene, including the arrow, throughout capture. A failed configured endpoint
fails the capture. Unset the variable to use standard X11 capture exclusion.
The companion supports desktops up to 16 Mi pixels and allocates its capture
pixmap only while serving requests. Requests arriving together share one
immutable frame, with each client reading its own crop. Capture batches are
bounded and separated by a cooldown to leave time for visible redraws.
Expired requests are discarded. Local X11 connections use MIT-SHM readback
when available. The compositor waits for replies asynchronously and yields
between bands of a large shared-memory readback.

To exercise the companion with an isolated Xvfb session, run
`computer-use/native/linux/picom/verify.sh <path-to-picom-hcu>` with Xvfb
and Python Xlib installed.

The native tests deliberately draw on a disposable desktop:

```bash
cd computer-use
go build -o /tmp/hcu-cursor-test ./cmd/computer-use
COMPUTER_USE_CURSOR_DESKTOP_TEST=1 \
COMPUTER_USE_CURSOR_TEST_BINARY=/tmp/hcu-cursor-test \
go test ./internal/provider -run TestCursorDesktop -count=1
```

Set `COMPUTER_USE_CURSOR_FAULT_TEST=1` only on a disposable desktop to also
suspend and resume its compositor and X server. The fading outline test
requires a separate desktop with picom configured for instant fade-in and
slow fade-out (for example, `-f -I 1 -O 0.01 -D 10`); enable it with
`COMPUTER_USE_CURSOR_FADE_TEST=1` and run only
`TestCursorDesktopExcludesFadingOutline`.

## AX selection and editable controls

The shared AX interface accepts `select`; native capabilities determine which
ranges can be selected and verified.

| Provider | Implemented selection | Readback |
| --- | --- | --- |
| macOS | All text or one unique text match within one element with a writable AX selection range | Exact UTF-16 range, selected text, and unchanged source text |
| Windows | All text or one unique match in a UIA TextPattern control; one SelectionItem | Complete selection, text range endpoints and text, or the selected item |

Both providers reject cross-element ranges before input. Duplicate or missing
text also fails before selection. After a selection write, failed readback
reports that input may have changed the selection; do not retry blindly.

Windows selection readback proves the UIA selection state. It does not prove
that an application received a selection-change callback or updated its own
cached selection. Verify the resulting application state before relying on
that callback. A native WinForms ListBox test exposed this distinction.

The Linux composite-control `editTarget` identity has no native mapping on
these providers. They validate its wire format and reject its use before input.
Use a current ref for the native editable field. Ordinary native field editing
continues through each provider's existing text and value interfaces.

Every provider scales GUI screenshots to fit within 1366x768 and maps
coordinates back to the native desktop per axis. Each provider checks the
layout before coordinate input. macOS ordered keyboard input accepts up to 64
steps, matching the Go core.

## macOS

Install Go 1.22+, Swift 6.2+ and the macOS SDK, then from the repository root:

```bash
make build-macos
artifacts/macos/computer-use doctor
python3 scripts/smoke-mcp.py artifacts/macos/computer-use
```

Keep `ComputerUseProvider.app` beside `computer-use`. The build script uses
the repository `VERSION` and signs the provider bundle for local execution.
Local signing is not notarization or a distribution signing identity. Replacing
or moving the bundle may require renewing its permissions in System Settings.
Grant Accessibility and Screen Recording to the native provider. Launch from
the signed-in user's session, not a headless service account.
The provider's bundle identifier is `org.computer-use.provider.v2`. Upgrading
from a bundle with a different identifier changes its macOS permission identity;
grant permissions to the new bundle rather than assuming an old grant applies.

Permission diagnostics use the current provider process's non-prompting AX and
CoreGraphics checks. An old TCC database grant cannot override a runtime denial.
After replacing a signed bundle, grant access to the current provider and restart
it before checking permissions again.

The provider uses a private local socket and retains native object identity
within its process generation. A reset invalidates that continuity. Do not
reuse refs from an earlier MCP session or retry an uncertain mutation blindly.

App discovery includes window titles and identities for AX window waits.
Incomplete enumeration or failed title reads return an error, so missing
evidence cannot prove that a window closed. Windows without a unique native
handle remain visible by title but do not receive actionable window refs.

Closing the last document can remove the window before final capture. If the
provider cannot verify the result, it reports `effect_unknown` and stops the
batch. This does not establish that the close failed; inspect the current
desktop before deciding what to do next.

Some window and input operations use Apple's private SkyLight SPI from the
installed macOS system framework. No Apple framework is bundled. These
interfaces are not a public compatibility contract and can change between
macOS releases.

## Windows

Install Go 1.22+, the .NET 8 SDK and PowerShell 7+. Windows PowerShell 5.1
is not supported; installing the .NET SDK does not upgrade PowerShell.
From the repository root (`make build-windows` runs the same script):

```powershell
pwsh -NoProfile -File ./computer-use/native/windows/build.ps1 -OutputDirectory "$PWD/artifacts/windows"
./artifacts/windows/computer-use.exe doctor
python scripts/smoke-mcp.py artifacts/windows/computer-use.exe
```

Keep the self-contained `computer-use-windows-provider.exe` beside
`computer-use.exe`; a separately installed .NET runtime is not required for
that package. The provider runs under the MCP user's account and integrity
level. It does not bypass UAC, secure desktops, or session isolation. UIA
visibility reported by the application can differ from visible pixels;
reobserve instead of forcing a stale or offscreen semantic target.

Add `sdk/python` to `PYTHONPATH` separately; the native package provides only
the CLI and provider. Root `pip install .` neither builds them nor installs
`hcu_sdk`. The CLI/SDK daemon requires Windows AF_UNIX support, available in
Windows 10 1803+ and Windows 11. See [Code entry points](CODE.md#python-sdk)
for endpoints and startup. Sync and async clients can start the daemon or
connect to a manually started `computer-use serve`. Windows ACL checks
protect its private directory; POSIX permission bits do not replace them.

Text input sends synchronous Unicode window messages without using the
clipboard. Standard Unicode Edit and RichEdit controls, including their
Windows Forms wrappers, receive selection-replacement messages. Other
Unicode windows receive character messages. Receivers can ignore a message;
a successful return does not verify accepted text. Observe the result.

Tabs remain literal text. CR, LF and CRLF each represent one line break.
Standard read-only controls reject text before input. Standard single-line
controls reject text containing a line break before sending any prefix.
A text operation has a 15-second total budget and a 5-second per-message
limit. A timeout preserves the uncertain UTF-16 span and message counts in
`text_delivery`; do not replay the text automatically.

A Windows GUI batch stops before non-empty `type` after mouse or keyboard
input. An AX batch also stops before `type` or document-input `set_value` after
`click`, `scroll` or `keypress`. Semantic Value/RangeValue writes remain
allowed. Clearing a document with `set_value` sends Delete, so subsequent
text or document replacement in the same batch also stops. Earlier input remains reported as emitted, with an unknown
effect. A wait or screenshot does not confirm queue processing. Confirm
focus and selection through a fresh observation before sending text in a
separate call; splitting calls alone does not prove completion. Empty text
and text before queued input remain allowed. `keypress` still sends real
key-down and key-up events.

Document `set_value` selects the full range through UIA TextPattern before
replacing it. This selection is part of the requested replacement, not a
substitute for a caller's keypress. If later text fails, the selected range
can already have changed. Observe before recovery.

GUI scroll deltas send one wheel notch per 100 units, and at least one notch
for any non-zero delta, as on Linux. A single-character key must be an ASCII
letter, digit or US-layout punctuation key. Other characters, such as `!` or
`é`, are rejected before any input in the batch; enter them with `type`.

An observation reads the window tree for at most 15 seconds. A larger tree,
such as a LibreOffice Calc window, returns the part read so far with partial
coverage instead of an error. Controls in the unread part are not
addressable; use `find`, a subtree root, or GUI for them.

LibreOffice draws its controls with its own toolkit, VCL, and loads the
Windows accessibility bridge only when its process asks for it. Some
releases, including 24.8, need `SAL_FORCE_IACCESSIBLE2=1` in the environment
of the LibreOffice process; without it a window exposes only its title bar.
LibreOffice 25.2 exposes its controls without the variable. The provider
cannot change the environment of a running application, so
`AX {"mode":"apps"}` and observe name such a window and the restart that fixes
it. To enable the bridge:

1. Set the variable for your account: `setx SAL_FORCE_IACCESSIBLE2 1`.
2. Quit every LibreOffice window, including the Quickstarter in the
   notification area, because a running instance keeps its environment.
3. Start LibreOffice again from the Start menu or from a new shell.

In LibreOffice Calc, a sheet lists its cells over the whole sheet row by
row (24.8) or not at all (25.2), so the provider reads the cells in view by
hit test, one per cell. Cells that hold a value and the cell under the
cursor get refs; the other cells in view are summarized as `/empty-cells`.
After a click or value on a cell, or text, an edit key or a cursor key in
the sheet, the provider reads only the window root, the formula bar, the
sheet, and the cells that hold a value or the cursor. The result notes that
the other controls and the empty cells are as last observed; `observe`
reads them all again.

An uncertain focus or pointer preparation failure is different from a stale
target. The requested input may be known not to have been emitted while a
queued focus request can still change the desktop later. Honor an explicit
stop response: do not replay, automatically observe, or continue the batch.
A fresh screenshot or restarting the MCP process does not prove that a pending
operating-system request has settled. Stop desktop automation and report the
uncertainty for operator recovery.

## Start a session

Run `computer-use doctor` as the signed-in desktop user, then configure the
client as described in [Computer Use MCP](MCP.md). Start with an AX application
observation or a GUI screenshot. Use only refs from the current session and
coordinates from the latest GUI screenshot.

AX readback describes provider-visible state. Verify saved files through the
application or filesystem when persistence is required.
