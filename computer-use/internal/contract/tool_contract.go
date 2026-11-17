package contract

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

const (
	PublicToolAX  = "AX"
	PublicToolGUI = "GUI"
)

const keyChordDescription = "One chord, e.g. [\"CTRL\",\"S\"]. " +
	"PAGEUP/PAGE_UP/PAGEDOWN/PAGE_DOWN. Linux named keys are case-insensitive, " +
	"including KP_ADD, KP_SUBTRACT, KP_MULTIPLY, KP_DIVIDE, KP_DECIMAL, KP_ENTER, " +
	"KP_0 through KP_9 and mapped X11 names. Use PLUS for '+'."

const (
	GUIMaxActions     = 64
	GUIMaxScrollDelta = 2000
	GUIMaxDragPoints  = 256
	GUIMaxKeys        = 8
	GUIMaxKeyLength   = 64
	GUIMaxTextLength  = 10000
	GUIMaxTotalWait   = 20
	// GUIZoomMinSide is the smallest zoom region side in frame pixels. A
	// narrower crop magnifies almost nothing, and its few output pixels
	// round to a scale that no longer maps back within two frame pixels.
	GUIZoomMinSide     = 8
	GUIMaxSettleMS     = 3000
	AXBatchMaxActions  = 64
	AXBatchMaxText     = 20000
	AXMaxReadText      = 4000
	MaxClickCount      = 10
	MaxScrollPages     = 100
	MaxVerifyTimeout   = 60000
	MaxActionText      = 10000
	MaxMCPMessageBytes = 1 << 20
	MaxDeliveredTrees  = 512
)

// ServerInstructions is the MCP server description. It says how the two
// tools divide the work; call shapes live in the tool descriptions.
const ServerInstructions = "" +
	"Two tools for the running desktop. AX reads and operates application windows " +
	"through accessibility: it lists windows, returns controls as rN refs, acts on " +
	"them, and waits with wait steps. GUI takes full-desktop screenshots and sends pixel " +
	"input. Use AX for named controls, values, menus, dialogs, windows, and keys. Use " +
	"GUI for pixels, canvases, dragging, and controls that AX does not expose; after " +
	"one targeted AX find misses a visible control, use GUI for it. Send all desktop " +
	"input (window activation, keys, text, mouse) through these tools, not shell input " +
	"utilities or scripted accessibility calls: the tools check the target window " +
	"before input and report what changed, and outside input makes their refs and " +
	"diffs stale. After GUI input, read AX again before reusing refs."

// PublishedServerInstructions is ServerInstructions without the sentences
// about ablated mechanisms.
func PublishedServerInstructions() string {
	text := ServerInstructions
	if Ablated(AblateWait) {
		text = strings.Replace(text, "returns controls as rN refs, acts on "+
			"them, and waits with wait steps.", "returns controls as rN refs, and acts on them.", 1)
	}
	if Ablated(AblateDiff) {
		text = strings.Replace(text, "check the target window before input and report what changed, "+
			"and outside input makes their refs and diffs stale.",
			"check the target window before input, and outside input makes their refs stale.", 1)
	}
	return text
}

type ToolDefinition struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	Annotations map[string]any `json:"annotations,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

// enabledChannels reads the channel capability switch. COMPUTER_USE_CHANNELS is a comma-separated list; by default
// all three channels (a11y, gui, keyboard) are enabled.
//
// A disabled channel's tools are absent from tools/list rather than rejected
// at call time: a model tries every tool it can see, and each rejection wastes
// a turn.
//
// The switch serves ablation experiments that need a channel truly disabled,
// not just its screenshots, and deployments that trust only semantic actions
// and want coordinate synthesis disabled entirely.
func enabledChannels() map[string]bool {
	raw := strings.TrimSpace(os.Getenv("COMPUTER_USE_CHANNELS"))
	if raw == "" {
		return map[string]bool{"a11y": true, "gui": true, "keyboard": true}
	}
	enabled := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part != "" {
			enabled[part] = true
		}
	}
	return enabled
}

const (
	ObserveNone       = "none"
	ObserveDiff       = "diff"
	ObserveTree       = "tree"
	ObserveScreenshot = "screenshot"
	ObserveFull       = "full"
)

var ObserveValues = []string{ObserveDiff, ObserveTree, ObserveFull, ObserveScreenshot, ObserveNone}

// AppArgumentHelp describes the app argument. Accessibility app names mix
// display names, binary names and app ids, and no rule derives one from
// another, so the argument accepts any of these or a PID. An aN is also
// accepted but not advertised: apps names windows by wN.
const AppArgumentHelp = "App name, app id, or PID; use once, then window_ref."

// ToolChannel maps each public tool to the channel that enables it. Tools
// outside the map belong to no channel and are always enabled.
var ToolChannel = map[string]string{
	PublicToolAX:  "a11y",
	PublicToolGUI: "gui",
}

func SortedChannels() []string {
	enabled := enabledChannels()
	names := make([]string, 0, len(enabled))
	for name := range enabled {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func ToolIsEnabled(name string) bool {
	channel, known := ToolChannel[name]
	if !known {
		return true
	}
	return enabledChannels()[channel]
}

func ToolDefinitions() []ToolDefinition {
	kept := make([]ToolDefinition, 0, 2)
	for _, tool := range AllToolDefinitions() {
		if tool.Name != PublicToolAX && tool.Name != PublicToolGUI {
			continue
		}
		if ToolIsEnabled(tool.Name) {
			kept = append(kept, tool)
		}
	}
	return kept
}

// PublicToolCallName accepts exactly the advertised tool names. MCP tool
// names are case-sensitive, and a tolerated spelling would be one the
// schema never describes.
func PublicToolCallName(name string) (string, bool) {
	switch name {
	case PublicToolAX, PublicToolGUI:
		return name, true
	default:
		return "", false
	}
}

// nonBlank matches a string with at least one non-space character; the
// runtime rejects blank strings in these fields.
const nonBlank = "\\S"

func nonBlankString(description string) map[string]any {
	property := map[string]any{"type": "string", "pattern": nonBlank}
	if description != "" {
		property["description"] = description
	}
	return property
}

// axInputSchema is the single flat AX schema. It has no oneOf: which
// fields belong to which mode or action type is checked by decodeAXRequest,
// which names the field and shows a corrected call. Keep it under the
// client compaction budget (TestAXSchemaSurvivesClientCompaction).
func axInputSchema() map[string]any {
	query := map[string]any{}
	for _, field := range AXFindFilters {
		query[field] = nonBlankString("")
	}
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"mode"},
		// window_ref already names its app.
		"not": map[string]any{"required": []string{"app", "window_ref"}},
		"$defs": map[string]any{"query": map[string]any{
			"type": "object", "additionalProperties": false, "properties": query,
			"description": "At least one filter; ANDed, case-insensitive.",
		}},
		"properties": map[string]any{
			"mode": map[string]any{
				"type": "string", "enum": []string{AXModeApps, AXModeObserve, AXModeFind, AXModeAct},
				"description": "apps lists confirmed top-level windows (wN, title, app, active) and reports incomplete inventories; observe returns a tree or diff with rN; find returns matching rN; act runs actions.",
			},
			"app": nonBlankString(AppArgumentHelp),
			"window_ref": map[string]any{
				"type": "string", "pattern": "^w[1-9][0-9]*$",
				"description": "observe/find: window to read. act: omit when actions have refs, which name the window; otherwise sets the initial target for ref-less steps instead of the focused window.",
			},
			"scope": map[string]any{"type": "string", "enum": AXScopeValues, "description": "observe/find; default window."},
			"observe": map[string]any{
				"type": "string", "enum": ObserveValues,
				"description": "Returned evidence. act: default diff; explicit tree returns the complete window tree, full adds the full-desktop GUI screenshot. observe: default tree. none only with act.",
			},
			"root": map[string]any{
				"type": "string", "pattern": "^(focused|r[1-9][0-9]*)$",
				"description": "observe: subtree at rN or the focused element.",
			},
			"read_text": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"field"},
				"description": "Linux observe only: read one live root rN without input or a tree. The ref identifies its window; omit app, scope and observe. Offsets count Unicode code points. Follow next_read to continue; pages are live reads, not a frozen document.",
				"properties": map[string]any{
					"field": map[string]any{"type": "string", "enum": []string{"name", "value", "description"}, "description": "value reads the node's AT-SPI Text interface; unavailable text returns an error. name and description read those attributes."},
					"start": map[string]any{"type": "integer", "minimum": 0, "maximum": 2147483647, "description": "Start offset, inclusive; default 0."},
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": AXMaxReadText, "description": "Maximum code points; default 4000."},
				},
			},
			"query": map[string]any{"$ref": "#/$defs/query"},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": MaxAXFindPageSize, "description": "find page size; default 20."},
			"cursor": map[string]any{
				"type": "string", "pattern": "^f[1-9][0-9]*$",
				"description": "find: next_cursor from a truncated result; send with mode find only.",
			},
			"actions": map[string]any{
				"type": "array", "minItems": 1, "maxItems": AXBatchMaxActions,
				"description": "act: ordered steps; a failed step stops the batch. Wait timeouts total at most 60000 ms; an omitted timeout counts as 5000 ms.",
				"items": map[string]any{
					"type": "object", "additionalProperties": false, "required": []string{"type"},
					"properties": map[string]any{
						"type": map[string]any{"type": "string", "enum": AXActionTypes},
						"ref": map[string]any{
							"type": "string", "pattern": "^[rw][1-9][0-9]*$",
							"description": "rN element; wN for activate/close. Omit for keys/text to the batch target (focused window by default).",
						},
						"to_ref": map[string]any{
							"type": "string", "pattern": "^r[1-9][0-9]*$",
							"description": "select: range end; ref..to_ref spans text or cells.",
						},
						"title": nonBlankString("activate/close: unique window-title substring, instead of wN."),
						"text": map[string]any{
							"type": "string", "minLength": 1, "maxLength": MaxActionText,
							"description": "wait: text in the ref element, or anywhere in the window. select: substring of ref to select.",
						},
						"keys": map[string]any{
							"type": "array", "minItems": 1, "maxItems": GUIMaxKeys,
							"items":       map[string]any{"type": "string", "pattern": "^\\S(.*\\S)?$", "maxLength": GUIMaxKeyLength},
							"description": keyChordDescription,
						},
						"value":     map[string]any{"type": "string", "maxLength": MaxActionText},
						"direction": map[string]any{"type": "string", "enum": []string{"up", "down", "left", "right"}},
						"pages":     map[string]any{"type": "number", "exclusiveMinimum": 0, "maximum": MaxScrollPages},
						"allow_global_input": map[string]any{
							"type": "boolean", "default": true,
							"description": "Linux click/scroll only. false forbids driver focus/raise and pointer/keyboard synthesis. Semantic actions may themselves change app focus. Unsupported uses reject the batch before input.",
						},
						"window": nonBlankString("wait: a top-level window whose title contains this, on the whole desktop."),
						"query":  map[string]any{"$ref": "#/$defs/query"},
						"state":  nonBlankString("wait: ref state, e.g. checked."),
						"gone":   map[string]any{"type": "boolean", "description": "wait until it is absent."},
						"settle": map[string]any{"type": "boolean", "description": "wait: true waits until the UI changes after the step before it, then stops changing."},
						"timeout_ms": map[string]any{
							"type": "integer", "minimum": 1, "maximum": MaxVerifyTimeout,
							"description": "wait: default 5000.",
						},
					},
				},
			},
		},
	}
	ablateAXInputSchema(schema)
	return schema
}

// ablateAXInputSchema removes the fields and words of ablated mechanisms
// from the AX schema.
func ablateAXInputSchema(schema map[string]any) {
	properties := schema["properties"].(map[string]any)
	actions := properties["actions"].(map[string]any)
	item := actions["items"].(map[string]any)["properties"].(map[string]any)
	wait, batching := Ablated(AblateWait), Ablated(AblateBatching)
	if wait {
		for _, field := range []string{"window", "query", "state", "gone", "settle", "timeout_ms"} {
			delete(item, field)
		}
		item["type"].(map[string]any)["enum"] = PublishedAXActionTypes()
		item["text"].(map[string]any)["description"] = "select: substring of ref to select."
	}
	actions["maxItems"] = AXActMaxActions()
	switch {
	case batching && wait:
		actions["description"] = "act: one step."
	case batching:
		actions["description"] = "act: one input step, with optional wait steps before or after it; " +
			"a failed step stops the batch. Wait timeouts total at most 60000 ms; an omitted timeout counts as 5000 ms."
	case wait:
		actions["description"] = "act: ordered steps; a failed step stops the batch."
	}
	if Ablated(AblateDiff) {
		properties["mode"].(map[string]any)["description"] = "apps lists confirmed top-level windows " +
			"(wN, title, app, active) and reports incomplete inventories; observe returns a tree with rN; " +
			"find returns matching rN; act runs actions."
		observe := properties["observe"].(map[string]any)
		observe["enum"] = PublishedObserveValues()
		observe["description"] = "observe: returned evidence; default tree, full adds the full-desktop GUI screenshot."
	}
}

func guiActionProperty(includeScreenshot bool) map[string]any {
	point := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"x": numberProperty("X pixel in the exact returned full-desktop image"),
			"y": numberProperty("Y pixel in the exact returned full-desktop image"),
		},
		"required": []string{"x", "y"},
	}
	mouseKeys := map[string]any{
		"type":        "array",
		"description": "Held modifiers.",
		"maxItems":    GUIMaxKeys,
		"items": map[string]any{
			"type": "string",
			"enum": []string{"CTRL", "CONTROL", "SHIFT", "ALT", "SUPER"},
		},
	}
	keyArray := map[string]any{
		"type":        "array",
		"description": keyChordDescription,
		"minItems":    1,
		"maxItems":    GUIMaxKeys,
		"items": map[string]any{
			"type":      "string",
			"pattern":   nonBlankPattern,
			"maxLength": GUIMaxKeyLength,
		},
	}
	branch := func(
		actionType, description string,
		properties map[string]any,
		required []string,
	) map[string]any {
		fields := map[string]any{
			"type": constStringProperty(actionType, description),
		}
		for key, value := range properties {
			fields[key] = value
		}
		return objectSchema(fields, append([]string{"type"}, required...))
	}
	scroll := branch("scroll", "Scroll at a point.", map[string]any{
		"x":        numberProperty("Screenshot X pixel."),
		"y":        numberProperty("Screenshot Y pixel."),
		"scroll_x": map[string]any{"type": "number", "minimum": -GUIMaxScrollDelta, "maximum": GUIMaxScrollDelta},
		"scroll_y": map[string]any{"type": "number", "minimum": -GUIMaxScrollDelta, "maximum": GUIMaxScrollDelta},
		"keys":     mouseKeys,
	}, []string{"x", "y"})
	scroll["anyOf"] = []any{
		map[string]any{
			"required": []string{"scroll_x"},
			"properties": map[string]any{
				"scroll_x": map[string]any{"not": map[string]any{"const": 0}},
			},
		},
		map[string]any{
			"required": []string{"scroll_y"},
			"properties": map[string]any{
				"scroll_y": map[string]any{"not": map[string]any{"const": 0}},
			},
		},
	}
	branches := []any{}
	if includeScreenshot {
		branches = append(branches,
			branch("screenshot", "Capture the final full desktop; this action must be the only action in the call.", nil, nil),
		)
	}
	branches = append(branches,
		branch("click", "Click.", map[string]any{
			"x": numberProperty("Screenshot X pixel."),
			"y": numberProperty("Screenshot Y pixel."),
			"button": enumStringProperty(
				"Mouse button. Defaults to left.",
				[]string{"left", "right", "wheel", "back", "forward"},
			),
			"keys": mouseKeys,
		}, []string{"x", "y"}),
		branch("double_click", "Double click.", map[string]any{
			"x":    numberProperty("Screenshot X pixel."),
			"y":    numberProperty("Screenshot Y pixel."),
			"keys": mouseKeys,
		}, []string{"x", "y"}),
		branch("move", "Move pointer.", map[string]any{
			"x":    numberProperty("Screenshot X pixel."),
			"y":    numberProperty("Screenshot Y pixel."),
			"keys": mouseKeys,
		}, []string{"x", "y"}),
		branch("drag", "Drag path.", map[string]any{
			"path": map[string]any{
				"type": "array", "minItems": 2, "maxItems": GUIMaxDragPoints,
				"items": point,
			},
			"keys": mouseKeys,
		}, []string{"path"}),
		scroll,
		branch("type", "Type literal text.", map[string]any{
			"text": map[string]any{"type": "string", "maxLength": GUIMaxTextLength},
		}, []string{"text"}),
		branch("keypress", "Press one key chord.", map[string]any{
			"keys": keyArray,
		}, []string{"keys"}),
		branch("wait", "Wait exactly one second without an intermediate observation; do not stack waits.", nil, nil),
	)
	return map[string]any{
		"description": "One GUI action.",
		"oneOf":       branches,
	}
}

func GUIInputSchema() map[string]any {
	observe := objectSchema(map[string]any{
		"actions": map[string]any{
			"type":     "array",
			"minItems": 1,
			"maxItems": 1,
			"items": map[string]any{"oneOf": []any{
				objectSchema(map[string]any{
					"type": constStringProperty(
						"screenshot",
						"Capture the final full desktop; this must be the only action.",
					),
				}, []string{"type"}),
				objectSchema(map[string]any{
					"type": constStringProperty("zoom", "Look only: enlarge a frame region; alone."),
					"region": map[string]any{
						"type": "array", "minItems": 4, "maxItems": 4,
						"items":       map[string]any{"type": "number", "minimum": 0},
						"description": fmt.Sprintf("[x0,y0,x1,y1] frame pixels; sides >= %d.", GUIZoomMinSide),
					},
				}, []string{"type", "region"}),
			}},
		},
	}, []string{"actions"})
	act := objectSchema(map[string]any{
		"actions": map[string]any{
			"type":        "array",
			"minItems":    1,
			"maxItems":    GUIMaxActions,
			"items":       guiActionProperty(false),
			"description": "Ordered GUI actions.",
		},
		"settle_ms": integerRangeProperty(
			fmt.Sprintf(
				"Visual stillness: default %dms, max %dms. "+
					"Final screenshot stillness, not semantic success and not extra sleep. "+
					"After wait, usually omit it. At the cap inspect the screenshot"+
					map[bool]string{false: " or use an AX wait step.", true: "."}[Ablated(AblateWait)],
				DefaultSettleMS,
				GUIMaxSettleMS,
			),
			0,
			GUIMaxSettleMS,
		),
	}, []string{"actions"})
	return map[string]any{
		"type":        "object",
		"description": "One screenshot or zoom action observes; otherwise run an input batch.",
		// Some clients close the root object with additionalProperties:false.
		// Declare its field names here; the branches retain every constraint.
		"properties": map[string]any{
			"actions":   map[string]any{},
			"settle_ms": map[string]any{},
		},
		"oneOf": []any{observe, act},
	}
}

// axToolDescription is model-visible on every client, including ones that
// compact the input schema. Clients may truncate descriptions at 1,000 bytes.
const axToolDescription = "Accessibility for application windows. Linux: observe with root rN and read_text reads bounded pages of full name, value (AT-SPI Text), or description; follow next_read. Modes: " +
	"apps lists confirmed windows (wN, title, app, active) and reports incomplete inventories; missing windows then remain unknown. observe returns the window tree with " +
	"rN refs. find returns matching rN. act runs ordered steps, checks them before input, " +
	"stops at the first failed step, and reports each target's change, windows " +
	"opened/closed/focused, and a diff. " + axBatchWindowDescription +
	"Copy rN/wN exactly; stale refs fail. keypress/type " +
	"without ref use the batch target, initially the focused window unless refs or window_ref select it. select marks ref, its text substring, or ref..to_ref " +
	"(paragraphs, cells) and reports the selection read back. activate/close take a title substring or wN. " +
	"A wait step sends no input and fails the batch on timeout: window (title, any app), " +
	"query (as find), ref with value/state/text, or text; gone inverts. settle needs no " +
	"prediction: it waits until the UI changes after the step before it and stops changing; " +
	"it fails only if nothing changed. After a window wait, keys go to that window, after " +
	"settle to the focused one. Skip waits the report answers. Errors return code and " +
	"hint. Examples:\n" +
	`{"mode":"act","actions":[{"type":"click","ref":"r12"},{"type":"wait","window":"Export"},{"type":"type","text":"q3.csv"},{"type":"keypress","keys":["ENTER"]}]}` + "\n" +
	`{"mode":"find","window_ref":"w2","query":{"role":"button","name_contains":"Save"}}`

const (
	axBatchWindowDescription = "activate with wN or title switches the batch target for later refs and ref-less steps. " +
		"Other-window refs fail until another activate selects their window. " +
		"Each step reports its window; final observation uses the last executed window. "
	axActFullSentence = "act runs ordered steps, checks them before input, " +
		"stops at the first failed step, and reports each target's change, windows " +
		"opened/closed/focused, and a diff."
	axActFullExample  = `{"mode":"act","actions":[{"type":"click","ref":"r12"},{"type":"wait","window":"Export"},{"type":"type","text":"q3.csv"},{"type":"keypress","keys":["ENTER"]}]}`
	axWaitDescription = "A wait step sends no input and fails the batch on timeout: window (title, any app), " +
		"query (as find), ref with value/state/text, or text; gone inverts. settle needs no " +
		"prediction: it waits until the UI changes after the step before it and stops changing; " +
		"it fails only if nothing changed. After a window wait, keys go to that window, after " +
		"settle to the focused one. "
)

// publishedAXToolDescription is axToolDescription without the sentences
// and example steps of ablated mechanisms.
func publishedAXToolDescription() string {
	wait, batching, diff := Ablated(AblateWait), Ablated(AblateBatching), Ablated(AblateDiff)
	subject := "act runs ordered steps"
	clauses := []string{"checks them before input", "stops at the first failed step"}
	example := axActFullExample
	switch {
	case batching && wait:
		subject, clauses = "act runs one step", []string{"checks it before input"}
		example = `{"mode":"act","actions":[{"type":"click","ref":"r12"}]}`
	case batching:
		subject = "act runs one input step with optional wait steps before or after it"
		example = `{"mode":"act","actions":[{"type":"click","ref":"r12"},{"type":"wait","window":"Export"}]}`
	case wait:
		example = `{"mode":"act","actions":[{"type":"click","ref":"r12"},{"type":"type","text":"q3.csv"},{"type":"keypress","keys":["ENTER"]}]}`
	}
	if !diff {
		clauses = append(clauses, "reports each target's change, windows opened/closed/focused, and a diff")
	}
	parts := append([]string{subject}, clauses...)
	sentence := parts[0] + " and " + parts[1] + "."
	if len(parts) > 2 {
		last := len(parts) - 1
		sentence = strings.Join(parts[:last], ", ") + ", and " + parts[last] + "."
	}
	if diff {
		// The tools state how the ablated act behaves, as the guide does.
		sentence += " Its result shows the whole current tree of its window after its " +
			"actions instead of a report of what changed."
	}
	text := strings.Replace(axToolDescription, axActFullSentence, sentence, 1)
	text = strings.Replace(text, axActFullExample, example, 1)
	if batching {
		text = strings.Replace(text, axBatchWindowDescription, "", 1)
	}
	if diff {
		text = strings.Replace(text, " and reports the selection read back", "", 1)
	}
	if wait {
		text = strings.Replace(text, axWaitDescription,
			"act cannot pause for a later state; to check one, read again with observe or find. ", 1)
	}
	if wait || diff {
		text = strings.Replace(text, "Skip waits the report answers. ", "", 1)
	}
	return text
}

var guiToolDescription = fmt.Sprintf("Full-desktop screenshot and pixel input. "+
	`{"actions":[{"type":"screenshot"}]} alone observes; {"type":"zoom","region":[x0,y0,x1,y1]} alone `+
	"enlarges a frame region to read small text and icons. Any other batch is checked before "+
	"input and returns one final screenshot. x/y are pixels of the latest full-desktop image (screen "+
	"width x height) from GUI or AX full/screenshot, which return the same frame. type/keypress go to the current focus, so click "+
	"the field first. Batch the actions planned from one screenshot; end "+
	"the batch after an action that may open, close, or move a window, and plan the rest "+
	"from the returned image. GUI cannot address windows; AX activates and closes them by "+
	"title. wait is one second with no observation, at most %d s per batch. Errors return "+
	"code and hint like AX; gui_frame_stale means nothing was sent: replan from the "+
	"returned screenshot. Example: "+
	`{"actions":[{"type":"click","x":640,"y":412},{"type":"type","text":"hello"},{"type":"keypress","keys":["ENTER"]}]}`,
	GUIMaxTotalWait)

func AllToolDefinitions() []ToolDefinition {
	annotations := func(title string) map[string]any {
		return map[string]any{"title": title, "destructiveHint": true, "openWorldHint": false}
	}
	return []ToolDefinition{
		{
			Name:        PublicToolAX,
			Title:       "Accessibility",
			Description: publishedAXToolDescription(),
			Annotations: annotations("Accessibility"),
			InputSchema: axInputSchema(),
		},
		{
			Name:        PublicToolGUI,
			Title:       "Desktop screenshot and input",
			Description: guiToolDescription,
			Annotations: annotations("Desktop screenshot and input"),
			InputSchema: GUIInputSchema(),
		},
	}
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

// nonBlankPattern requires at least one non-whitespace character. The AX
// runtime compares these strings after trimming, so a blank value can match
// nothing.
const nonBlankPattern = `\S`

func enumStringProperty(description string, values []string) map[string]any {
	property := stringProperty(description)
	property["enum"] = values
	return property
}

func numberProperty(description string) map[string]any {
	return map[string]any{"type": "number", "description": description}
}

func integerProperty(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func integerRangeProperty(description string, minimum, maximum int) map[string]any {
	property := integerProperty(description)
	property["minimum"] = minimum
	property["maximum"] = maximum
	return property
}

func constStringProperty(value, description string) map[string]any {
	return map[string]any{
		"type":        "string",
		"const":       value,
		"description": description,
	}
}
