package snippet

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestExtractBuildsJevRouterState(t *testing.T) {
	anthropic := []byte(`{"system":"do not send","messages":[` +
		`{"role":"user","content":"zeroth ask"},` +
		`{"role":"user","content":"first ask"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"private tool output"},{"type":"text","text":"not human"}]},` +
		`{"role":"user","content":"second ask"},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"hidden"},{"type":"text","text":"I propose to migrate the table."},{"type":"tool_use","name":"Edit"},{"type":"tool_use","name":"Bash"}]},` +
		`{"role":"user","content":[{"type":"text","text":"<system-reminder>ignore</system-reminder><memory-context>recalled memory</memory-context>yes go ahead, key sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAA\n` + "```sh\\nrm -rf x\\nls\\n```" + `"}]}]}`)
	state, _ := Extract(http.Header{"User-Agent": {"claude-cli/2.1"}}, anthropic, 4000)
	want := State{
		Request:              "yes go ahead, key [REDACTED anthropic-key]\n[code block (sh), 2 lines]",
		RecentUserTurns:      []string{"first ask", "second ask"},
		LastAssistantMessage: "I propose to migrate the table.",
		Session:              Session{Harness: "Claude Code", Depth: "early (under 20k tokens)", RecentTools: "Bash 2 times, Edit 1 time"},
	}
	if !reflect.DeepEqual(state, want) {
		t.Fatalf("anthropic state =\n%#v\nwant\n%#v", state, want)
	}

	long := strings.TrimSpace(strings.Repeat("word ", 40))
	chat := []byte(`{"messages":[` +
		`{"role":"assistant","content":"earlier prose","tool_calls":[{"id":"c1","function":{"name":"read_file"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"file contents"},` +
		`{"role":"user","content":"` + long + `"}]}`)
	state, _ = Extract(nil, chat, 40)
	want = State{
		Request: long[:10] + " … [159 characters omitted] … " + long[len(long)-30:],
		Session: Session{Harness: "unknown", Depth: "new session", RecentTools: "read_file 1 time"},
	}
	if !reflect.DeepEqual(state, want) {
		t.Fatalf("chat state =\n%#v\nwant\n%#v", state, want)
	}

	responses := []byte(`{"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"fix it"}]},` +
		`{"type":"function_call","call_id":"c1","name":"shell"},` +
		`{"type":"function_call_output","call_id":"c1","output":"secret output"},` +
		`{"type":"custom_tool_call","call_id":"c2","name":"apply_patch"},` +
		`{"type":"message","role":"user","content":[{"type":"input_image","image_url":"x"}]}]}`)
	state, _ = Extract(http.Header{"Originator": {"codex_cli_rs"}}, responses, 4000)
	want = State{
		Request:         "(the user sent 1 image(s) and no text)",
		RecentUserTurns: []string{"fix it"},
		Session:         Session{Harness: "Codex CLI", Depth: "early (under 20k tokens)", RecentTools: "shell 1 time, apply_patch 1 time"},
	}
	if !reflect.DeepEqual(state, want) {
		t.Fatalf("responses state =\n%#v\nwant\n%#v", state, want)
	}
}

func TestScrubSecretPatterns(t *testing.T) {
	a := func(n int) string { return strings.Repeat("a", n) }
	for _, tc := range []struct{ in, want string }{
		{"k -----BEGIN RSA " + "PRIVATE KEY-----\nabc\n-----END RSA " + "PRIVATE KEY----- end", "k [REDACTED private-key] end"},
		{"k sk-ant-api03-" + a(24) + " end", "k [REDACTED anthropic-key] end"},
		{"k sk-or-v1-" + a(32) + " end", "k [REDACTED openrouter-key] end"},
		{"k sk-proj-" + a(24) + " end", "k [REDACTED openai-key] end"},
		{"k AKI" + "A" + strings.Repeat("B", 16) + " end", "k [REDACTED aws-access-key] end"},
		{"k aws_secret_access_key=" + strings.Repeat("A", 40) + " end", "k [REDACTED aws-secret-key] end"},
		{"k ghp_" + a(36) + " end", "k [REDACTED github-token] end"},
		{"k glpat-" + a(20) + " end", "k [REDACTED gitlab-token] end"},
		{"k xox" + "b-" + strings.Repeat("1", 10) + " end", "k [REDACTED slack-token] end"},
		{"k sk_live_" + a(16) + " end", "k [REDACTED stripe-key] end"},
		{"k AIza" + a(35) + " end", "k [REDACTED google-api-key] end"},
		{"k pul-" + a(40) + " end", "k [REDACTED pulumi-token] end"},
		{"k eyJ" + a(10) + ".eyJ" + a(10) + "." + a(10) + " end", "k [REDACTED jwt] end"},
		{"k https://user:pass@host end", "k [REDACTED url-credentials]host end"},
		{"k password=hunter2hunter2 end", "k [REDACTED assignment] end"},
		{"k password=$secret=abcdefgh12 end", "k password=$[REDACTED assignment] end"},
		// References are not secrets.
		{"password=process.env.DB_PASSWORD", "password=process.env.DB_PASSWORD"},
		{"api_key = os.environ['X']", "api_key = os.environ['X']"},
		{`pwd=os.getenv("PWD")`, `pwd=os.getenv("PWD")`},
		{"access_token=env.ACCESS_TOKEN", "access_token=env.ACCESS_TOKEN"},
		{"secret: $SECRET_VALUE", "secret: $SECRET_VALUE"},
		{"auth_token=<your-token-here>", "auth_token=<your-token-here>"},
		{"client_secret={{vault_client_secret}}", "client_secret={{vault_client_secret}}"},
		{"password=********", "password=********"},
	} {
		if got := Scrub(tc.in); got != tc.want {
			t.Errorf("Scrub(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
