#!/bin/sh
# Fake aharness arm for driver smoke tests. It accepts the harness flags,
# writes one plausible session log, and leaves the workspace untouched.
set -eu
state=""
while [ "$#" -gt 0 ]; do
	case "$1" in
		-workspace) shift 2 ;;
		-state-dir) state=$2; shift 2 ;;
		-task-file) shift 2 ;;
		-model) shift 2 ;;
		-thinking-effort) shift 2 ;;
		*) shift ;;
	esac
done
if [ -z "$state" ]; then
	echo "fake-harness: -state-dir is required" >&2
	exit 2
fi
mkdir -p "$state/sessions"
cat > "$state/sessions/0199d0e0-0000-7000-8000-0000000000aa.jsonl" <<'TASK_EOF'
{"v":2,"seq":1,"ts":"2026-11-11T10:00:00Z","session":"0199d0e0-0000-7000-8000-0000000000aa","type":"session_start","data":{"model":"deepseek-v4.1-flash","toolset_version":"toolset-v1"}}
{"v":2,"seq":2,"ts":"2026-11-11T10:00:00Z","session":"0199d0e0-0000-7000-8000-0000000000aa","type":"run_start","data":{"run_id":"fake-run","model":"deepseek-v4.1-flash","require_usage":true}}
{"v":2,"seq":3,"ts":"2026-11-11T10:00:00Z","session":"0199d0e0-0000-7000-8000-0000000000aa","turn":1,"type":"model_request","data":{"model":"deepseek-v4.1-flash","estimated_input_tokens":120,"message_count":3,"truncated":false}}
{"v":2,"seq":4,"ts":"2026-11-11T10:00:01Z","session":"0199d0e0-0000-7000-8000-0000000000aa","turn":1,"type":"model_response","data":{"model":"deepseek-v4.1-flash","text":"","tool_calls":[{"id":"call-1","name":"read_file","arguments":{"path":"task.md"}}],"finish_reason":"tool_calls","usage":{"input_tokens":128,"output_tokens":12,"cached_input_tokens":64,"cache_write_input_tokens":0,"reasoning_output_tokens":4,"reported":true},"latency_ms":50,"attempt":1,"streamed":false}}
{"v":2,"seq":5,"ts":"2026-11-11T10:00:01Z","session":"0199d0e0-0000-7000-8000-0000000000aa","turn":1,"type":"tool_result","data":{"call_id":"call-1","name":"read_file","ok":true,"code":"ok","output":"lines 1-2 of 2\ncontinue with offset=3","error":"","exit_code":0,"duration_ms":1,"truncated":false,"timed_out":false,"details":{"path":"task.md"}}}
{"v":2,"seq":6,"ts":"2026-11-11T10:00:02Z","session":"0199d0e0-0000-7000-8000-0000000000aa","turn":2,"type":"model_request","data":{"model":"deepseek-v4.1-flash","estimated_input_tokens":140,"message_count":5,"truncated":false}}
{"v":2,"seq":7,"ts":"2026-11-11T10:00:03Z","session":"0199d0e0-0000-7000-8000-0000000000aa","turn":2,"type":"model_response","data":{"model":"deepseek-v4.1-flash","text":"done","tool_calls":[],"finish_reason":"stop","usage":{"input_tokens":150,"output_tokens":8,"cached_input_tokens":96,"cache_write_input_tokens":0,"reasoning_output_tokens":2,"reported":true},"latency_ms":40,"attempt":1,"streamed":false}}
{"v":2,"seq":8,"ts":"2026-11-11T10:00:03Z","session":"0199d0e0-0000-7000-8000-0000000000aa","type":"run_end","data":{"status":"ok","reason":"","run_turns":2,"duration_ms":3000}}
TASK_EOF
exit 0
