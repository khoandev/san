package perm

import "testing"

func TestCallFilePath(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		args     map[string]any
		want     string
	}{
		{
			name:     "write names its file in the argument",
			toolName: "Write",
			args:     map[string]any{"file_path": "/repo/main.go", "content": "x"},
			want:     "/repo/main.go",
		},
		{
			name:     "read names its file in the argument",
			toolName: "Read",
			args:     map[string]any{"file_path": "/repo/main.go"},
			want:     "/repo/main.go",
		},
		{
			name:     "edit names its file in the payload header",
			toolName: "Edit",
			args:     map[string]any{"payload": "*** SM:EDIT /repo/main.go\n*** SM:FIND\nold\n*** SM:PUT\nnew\n"},
			want:     "/repo/main.go",
		},
		{
			// The toolbar preview and the payload header can disagree; the tool
			// refuses that call, and the header is the file it would have hit.
			name:     "the payload header outranks a disagreeing argument",
			toolName: "Edit",
			args:     map[string]any{"file_path": "/repo/other.go", "payload": "*** SM:EDIT /repo/main.go\n*** SM:FIND\nx\n*** SM:PUT\ny\n"},
			want:     "/repo/main.go",
		},
		{
			name:     "edit accepts the reference engine's aliases",
			toolName: "Edit",
			args:     map[string]any{"patch": "*** SM:EDIT /repo/main.go\n*** SM:FIND\nx\n*** SM:PUT\ny\n"},
			want:     "/repo/main.go",
		},
		{
			// A payload that does not parse names no file, so the call keeps the
			// path it did state — never a guess.
			name:     "a malformed payload falls back to the argument",
			toolName: "Edit",
			args:     map[string]any{"file_path": "/repo/main.go", "payload": "*** SM:EDIT\n*** SM:FIND\nx\n*** SM:PUT\ny\n"},
			want:     "/repo/main.go",
		},
		{
			name:     "a payload with no header names nothing",
			toolName: "Edit",
			args:     map[string]any{"payload": "*** SM:FIND\nx\n*** SM:PUT\ny\n"},
			want:     "",
		},
		{name: "no arguments", toolName: "Edit", args: map[string]any{}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CallFile(tt.toolName, tt.args); got != tt.want {
				t.Fatalf("CallFile(%q, %v) = %q, want %q", tt.toolName, tt.args, got, tt.want)
			}
		})
	}
}

func TestPayloadTextPrefersTheDocumentedName(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"payload", map[string]any{"payload": "p"}, "p"},
		{"patch alias", map[string]any{"patch": "p"}, "p"},
		{"input alias", map[string]any{"input": "p"}, "p"},
		// An empty alias must not shadow a real payload, in either order.
		{"empty patch beside payload", map[string]any{"payload": "p", "patch": "  "}, "p"},
		{"payload beside empty input", map[string]any{"input": "", "payload": "p"}, "p"},
		{"none", map[string]any{"file_path": "x"}, ""},
		{"whitespace only", map[string]any{"payload": "\n\t "}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PayloadText(tt.args); got != tt.want {
				t.Fatalf("PayloadText(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}
