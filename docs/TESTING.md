# Testing Methodology

> Multi-layered testing approach for agent behavior changes (especially Meta-Agent / ConductorAgent logic).

## Layer 1: Unit Tests (`go test`)

Test individual functions with mock LLM and temp directories. Covers pure logic without real LLM calls:

- **Parsing functions**: `parseMetaAgentOutput`, `fallbackParseMetaAgentOutput`, `extractAgentName`, `extractToolsUsed`, `toSnakeCase`
- **Tool lookup**: `getToolFunc` — verify all 14 known tools return non-nil, unknown returns nil
- **Registration logic**: `registerCustomAgent` — verify adapter is added, duplicate is skipped, unknown tools are handled
- **Custom agent execution**: `executeCustomAgent` — verify mock LLM response is returned, `finish` terminates loop
- **System prompt construction**: verify `<custom_agents>` block appears/disappears correctly

Create a `mockLLM` struct implementing the `llms.Model` interface (`GenerateContent` + `Call`) to control LLM output deterministically.

## Layer 2: Delegate-Meta Integration Tests

Test the `delegate_meta` tool handler end-to-end with a mock MetaAgent:

1. Create `MetaAgent` with a `mockLLM` that returns pre-defined output (structured or malformed)
2. Create `ConductorAgent` with the MetaAgent
3. Find the `delegate_meta` adapter from `conductor.Adapters`
4. Call `delegateMeta.Call(ctx, input)` directly
5. Verify: custom agent registered / not registered, result message format

Key scenarios:
- **Structured output** → agent registered, `[New Agent Registered]` in result
- **Malformed output** → heuristic extraction attempted, `[Meta-Agent Raw Output]` returned
- **Empty agent name** → no registration
- **No agent_design block** → no registration
- **Duplicate delegation** → no duplicate registration

## Layer 3: Server-Level Functional Tests

```bash
# 1. Build and start server in background
go build -o codeactor .
kill $(lsof -ti :9080) 2>/dev/null
nohup ./codeactor http > /tmp/codeactor-server.log 2>&1 &
sleep 3

# 2. Run tasks via CLI client
node clients/nodejs-cli/index.js run . "task description"

# 3. Monitor server logs
grep "Conductor delegating to Meta-Agent\|Custom agent registered\|parse failed\|Heuristic" /tmp/codeactor-server.log

# 4. Inspect task memory for tool call traces
node clients/nodejs-cli/index.js memory <task-id> | grep "tool_call\|delegate_"

# 5. Check task files on disk
python3 -c "
import json
with open('$HOME/.codeactor/tasks/<task-id>.json') as f:
    d = json.load(f)
for m in d.get('messages', []):
    print(f'[{m[\"type\"]}] {str(m.get(\"content\",\"\"))[:200]}')
"
```

## Layer 4: Log Level Configuration

Default `slog` handler in `main.go` `init()` is `LevelError`. HTTP mode override in `main()` uses `LevelInfo`.

| Level | Calls |
|-------|-------|
| `Info` | `Conductor delegating to Meta-Agent`, `Custom agent registered`, `Custom agent registered via heuristic fallback` |
| `Warn` | `Strict Meta-Agent parse failed, trying heuristic extraction`, `Heuristic extraction insufficient, returning raw output` |
| `Error` | `Meta-Agent execution failed`, `CustomAgent LLM error` |

When debugging Meta-Agent issues, ensure log level is `Info` or lower.

## Layer 5: LLM Output Diagnosis

```bash
# Search LLM logs for Meta-Agent interactions
grep -c "MetaAgent\|Meta-Agent" ~/.codeactor/logs/llm-$(date +%Y-%m-%d).log

# Check task memory for empty/short delegate_meta tool results
python3 -c "
import json
with open('$HOME/.codeactor/tasks/<task-id>.json') as f:
    d = json.load(f)
msgs = d.get('messages', [])
for i, m in enumerate(msgs):
    if m['type'] == 'tool':
        c = str(m.get('content',''))
        if len(c) < 20:
            print(f'[{i}] EMPTY or short tool result: {repr(c)}')
"
```

## Agent Isolation with --disable-agents

When debugging a specific sub-agent, use `--disable-agents` to exclude others:

```bash
# Only MetaAgent available (disable all built-in delegates)
./codeactor http --disable-agents=repo,coding,chat

# Test with custom MetaAgent-designed agents only
./codeactor tui --disable-agents=repo,coding,chat,meta
```

This forces the Conductor to use only the remaining delegate tools, making it easier to isolate behavior.

## Common Pitfalls

1. **LLM returns empty content**: Local/quantized models may not follow the Meta-Agent prompt format. The heuristic fallback parser mitigates this.
2. **Log level too high**: `LevelError` hides all `slog.Info` messages. Always use `LevelInfo` or lower for functional testing.
3. **Conductor prefers existing agents**: The LLM naturally delegates to Repo/Coding/Chat agents first. Meta-Agent is only triggered when the LLM judges those insufficient. Test with tasks explicitly matching Meta-Agent use cases, or disable other agents with `--disable-agents`.
4. **Adapter JSON encoding**: `adapter.Call()` JSON-encodes return values. When reading tool results from memory, unmarshal with `json.Unmarshal` to get the raw string.
