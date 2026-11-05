# Third-Party Notices

| Component | Files | License text |
|---|---|---|
| macOS provider portions | `computer-use/native/macos/` | `licenses/MIT-open-codex-computer-use.txt` |
| macOS SkyLight bridge | `computer-use/native/macos/Sources/ComputerUseProviderKit/SkyLightSPI.swift`, `SkyClickSimulation.swift` | `licenses/MIT-cua-yabai.txt` |
| Cua Driver default cursor geometry | `computer-use/internal/provider/cursor_artwork_linux.go` | `licenses/MIT-cua-cursor.txt` |
| XGB X11 client | `computer-use/vendor/github.com/jezek/xgb` | `LICENSE` in that directory |
| Optional Picom XRender capture companion | `computer-use/native/linux/picom/clean-capture.patch` and the source built by `build.sh` | Upstream `LICENSE.spdx` and `LICENSES/`, copied by the build script |
| OSWorld-V2 modified files | `osworld-overlay/desktop_env/` (except `cache.py`, `cdp.py`), `hcu/osworld/task_loader.py` | `licenses/Apache-2.0.txt` |
| `StableIndexer` portions | `computer-use/native/linux/provider/ax_records.py` | `licenses/Apache-2.0.txt`, `licenses/Playwright-NOTICE.txt` |
| Go x/text | `computer-use/vendor/golang.org/x/text` | `LICENSE` and `PATENTS` in that directory |
| santhosh-tekuri/jsonschema v6.0.3 | `computer-use/vendor/github.com/santhosh-tekuri/jsonschema/v6` | `LICENSE` in that directory; `licenses/Apache-2.0.txt` |
| Windows NuGet packages (FlaUI, System.Drawing.Common) | restored at build time | packaged by `build.ps1` |
