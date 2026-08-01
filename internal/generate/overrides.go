package generate

// UnionOverride describes the accessor field order for one embedding union.
// Clang's normalized fields remain authoritative for all field types and layout data.
type UnionOverride struct {
	CName  string
	Fields []string
}

var embeddingUnionOverrides = []UnionOverride{
	{CName: "ghostty_input_trigger_key_u", Fields: []string{"physical", "unicode"}},
	{CName: "ghostty_platform_u", Fields: []string{"macos", "ios"}},
	{CName: "ghostty_quick_terminal_size_value_u", Fields: []string{"percentage", "pixels"}},
	{CName: "ghostty_target_u", Fields: []string{"surface"}},
	{CName: "ghostty_action_key_table_u", Fields: []string{"activate"}},
	{CName: "ghostty_action_u", Fields: []string{
		"new_split", "toggle_fullscreen", "move_tab", "goto_tab", "goto_split", "goto_window",
		"resize_split", "size_limit", "initial_size", "cell_size", "scrollbar", "inspector",
		"desktop_notification", "set_title", "set_tab_title", "prompt_title", "pwd", "mouse_shape",
		"mouse_visibility", "mouse_over_link", "renderer_health", "quit_timer", "float_window",
		"secure_input", "key_sequence", "key_table", "color_change", "reload_config", "config_change",
		"open_url", "close_tab_mode", "child_exited", "progress_report", "command_finished",
		"start_search", "search_total", "search_selected", "readonly",
	}},
	{CName: "ghostty_ipc_target_u", Fields: []string{"klass"}},
	{CName: "ghostty_ipc_action_u", Fields: []string{"new_window"}},
}

func unionOverride(name string) (UnionOverride, bool) {
	for _, override := range embeddingUnionOverrides {
		if override.CName == name {
			return override, true
		}
	}
	return UnionOverride{}, false
}
