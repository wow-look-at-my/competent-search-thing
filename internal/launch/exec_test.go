package launch

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExpandExec(t *testing.T) {
	file := ClassifyTarget("/tmp/a b.txt", false)
	dir := ClassifyTarget("/tmp/d", true)
	url := ClassifyTarget("https://example.com/x", false)
	none := Target{}

	tests := []struct {
		name string
		exec string
		t    Target
		want []string
	}{
		{name: "percent-f file", exec: "editor %f", t: file, want: []string{"editor", "/tmp/a b.txt"}},
		{name: "percent-F file", exec: "editor %F", t: file, want: []string{"editor", "/tmp/a b.txt"}},
		{name: "percent-u path becomes file uri", exec: "browser %u", t: file, want: []string{"browser", "file:///tmp/a%20b.txt"}},
		{name: "percent-U url stays verbatim", exec: "browser %U", t: url, want: []string{"browser", "https://example.com/x"}},
		{name: "percent-f with a url passes the url (documented divergence)", exec: "editor %f", t: url, want: []string{"editor", "https://example.com/x"}},
		{name: "no field code appends the raw target", exec: "editor --wait", t: file, want: []string{"editor", "--wait", "/tmp/a b.txt"}},
		{name: "no field code appends the url", exec: "browser", t: url, want: []string{"browser", "https://example.com/x"}},
		{name: "dir target", exec: "files %U", t: dir, want: []string{"files", "file:///tmp/d"}},
		{name: "empty target drops a lone field-code arg", exec: "editor %f", t: none, want: []string{"editor"}},
		{name: "empty target drops a lone %U too", exec: "editor %U", t: none, want: []string{"editor"}},
		{name: "empty target appends nothing", exec: "editor --new", t: none, want: []string{"editor", "--new"}},
		{name: "icon and caption codes drop", exec: "app %i %c %k %f", t: file, want: []string{"app", "/tmp/a b.txt"}},
		{name: "deprecated codes drop", exec: "app %d %D %n %N %v %m x", t: none, want: []string{"app", "x"}},
		{name: "double percent literal", exec: "app 100%% %f", t: file, want: []string{"app", "100%", "/tmp/a b.txt"}},
		{name: "unknown code keeps the percent", exec: "app %z", t: none, want: []string{"app", "%z"}},
		{name: "trailing lone percent kept", exec: "app %", t: none, want: []string{"app", "%"}},
		{name: "quoted argument with spaces", exec: `"/opt/my editor/bin" %f`, t: file, want: []string{"/opt/my editor/bin", "/tmp/a b.txt"}},
		{name: "backslash escape inside quotes", exec: `app "a\"b" %f`, t: file, want: []string{"app", `a"b`, "/tmp/a b.txt"}},
		{name: "explicit empty quoted arg survives", exec: `app "" %f`, t: file, want: []string{"app", "", "/tmp/a b.txt"}},
		{name: "field code embedded in an argument", exec: "app --file=%f", t: file, want: []string{"app", "--file=/tmp/a b.txt"}},
		{name: "embedded code with empty target leaves the prefix", exec: "app --file=%f", t: none, want: []string{"app", "--file="}},
		{name: "tabs separate", exec: "app\t%f", t: file, want: []string{"app", "/tmp/a b.txt"}},
		{name: "empty exec with target appends only the target", exec: "", t: file, want: []string{"/tmp/a b.txt"}},
		{name: "empty exec empty target", exec: "", t: none, want: nil},
		{name: "dangling backslash inside quotes", exec: `app "x\`, t: none, want: []string{"app", "x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ExpandExec(tt.exec, tt.t))
		})
	}
}
