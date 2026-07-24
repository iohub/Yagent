package llm

import (
	"log/slog"
)

// NormalizeMessages is the top-level entry point for message normalization.
// It is idempotent and applies four phases sequentially:
// 1. dropEmptyMessages — remove empty messages
// 2. repairToolCallPairs — fix tool_call pairings
// 3. MergeConsecutiveAssistants — merge consecutive assistant messages
// 4. ensureValidStart — ensure valid message sequence start
func NormalizeMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return messages
	}

	beforeLen := len(messages)

	// Phase 1: drop empty messages
	messages = dropEmptyMessages(messages)

	// Phase 2: repair tool_call pairs
	messages = repairToolCallPairs(messages)

	// Phase 3: merge consecutive assistants
	messages = MergeConsecutiveAssistants(messages)

	// Phase 4: ensure valid start
	messages = ensureValidStart(messages)

	afterLen := len(messages)
	if beforeLen != afterLen {
		slog.Warn("normalized messages", "before", beforeLen, "after", afterLen)
	}

	return messages
}

// MergeConsecutiveAssistants merges adjacent assistant messages.
// - Two text-only assistants (no ToolCalls): merge Content (with \n\n separator) and Reasoning, skip current.
// - Previous has ToolCalls, current is text-only: append current Content to previous Content, skip current.
// - Current has ToolCalls: skip current (keep previous as-is).
func MergeConsecutiveAssistants(messages []Message) []Message {
	if len(messages) == 0 {
		return messages
	}

	result := make([]Message, 0, len(messages))

	for i := range messages {
		msg := messages[i]
		if msg.Role != RoleAssistant {
			result = append(result, msg)
			continue
		}

		// Current is assistant
		if len(result) == 0 {
			result = append(result, msg)
			continue
		}

		last := &result[len(result)-1]
		if last.Role != RoleAssistant {
			result = append(result, msg)
			continue
		}

		// Both are assistant — decide how to merge
		hasPrevToolCalls := len(last.ToolCalls) > 0
		hasCurrToolCalls := len(msg.ToolCalls) > 0

		if !hasPrevToolCalls && !hasCurrToolCalls {
			// Both text-only: merge Content and Reasoning
			if last.Content != "" && msg.Content != "" {
				last.Content += "\n\n" + msg.Content
			} else if msg.Content != "" {
				last.Content = msg.Content
			}
			if msg.Reasoning != "" {
				if last.Reasoning != "" {
					last.Reasoning += "\n\n" + msg.Reasoning
				} else {
					last.Reasoning = msg.Reasoning
				}
			}
			slog.Warn("merged consecutive text-only assistants", "count", len(messages))
			// skip current
		} else if hasPrevToolCalls && !hasCurrToolCalls {
			// Previous has tool calls, current is text-only: append Content
			if msg.Content != "" {
				last.Content += "\n\n" + msg.Content
			}
			slog.Warn("merged text-only assistant after tool-calling assistant", "count", len(messages))
			// skip current
		} else {
			// Current has tool calls: skip current (keep previous)
			slog.Warn("dropped consecutive assistant with tool calls (keeping previous)", "count", len(messages))
			// skip current
		}
	}

	return result
}

// repairToolCallPairs fixes broken tool_call pairings in the message list.
// It scans for assistant messages with ToolCalls and ensures matching tool responses exist.
func repairToolCallPairs(messages []Message) []Message {
	if len(messages) == 0 {
		return messages
	}

	result := make([]Message, 0, len(messages))

	for i := 0; i < len(messages); i++ {
		msg := messages[i]

		// Case 1: Assistant with tool calls
		if msg.Role == RoleAssistant && len(msg.ToolCalls) > 0 {
			expectedIDs := make(map[string]bool, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				expectedIDs[tc.ID] = true
			}

			matchedResponses := make(map[string]Message)
			j := i + 1
			for j < len(messages) {
				next := messages[j]
				if next.Role == RoleTool && next.ToolCallID != "" {
					if expectedIDs[next.ToolCallID] {
						matchedResponses[next.ToolCallID] = next
					}
					j++
				} else if next.Role == RoleAssistant || next.Role == RoleUser || next.Role == RoleSystem {
					break // stop scanning
				} else {
					// non-tool message that's not assistant/user: skip (keep scanning)
					j++
				}
			}

			allResponsesPresent := len(matchedResponses) == len(msg.ToolCalls)

			if allResponsesPresent {
				// Complete pairing: keep assistant + matched tool responses
				result = append(result, msg)
				for _, tc := range msg.ToolCalls {
					if resp, ok := matchedResponses[tc.ID]; ok {
						result = append(result, resp)
					}
				}
			} else {
				// Incomplete pairing: strip ToolCalls, keep Content if non-empty
				slog.Warn("incomplete tool_call pair, stripping tool_calls from assistant",
					"tool_calls", len(msg.ToolCalls),
					"responses", len(matchedResponses))
				if msg.Content != "" {
					preserved := msg
					preserved.ToolCalls = nil
					result = append(result, preserved)
				}
				// skip the unmatched tool responses (they become orphans, handled in Case 2)
			}
			i = j - 1
			continue
		}

		// Case 2: Orphan tool message — check if it matches the last assistant with tool calls
		if msg.Role == RoleTool && msg.ToolCallID != "" {
			// Find the last assistant in result that has tool calls
			hasMatchingAssistant := false
			for k := len(result) - 1; k >= 0; k-- {
				if result[k].Role == RoleAssistant && len(result[k].ToolCalls) > 0 {
					for _, tc := range result[k].ToolCalls {
						if tc.ID == msg.ToolCallID {
							hasMatchingAssistant = true
							break
						}
					}
					break // only check the most recent assistant with tool calls
				}
				// Stop at user/system messages
				if result[k].Role == RoleUser || result[k].Role == RoleSystem {
					break
				}
			}
			if !hasMatchingAssistant {
				slog.Warn("dropping orphan tool message", "toolCallID", msg.ToolCallID)
				continue // discard orphan
			}
		}

		// Case 3: Other messages (system, user, text-only assistant, matched tool)
		result = append(result, msg)
	}

	return result
}

// dropEmptyMessages removes messages that are effectively empty.
// Removes:
// - Messages with empty Role
// - Non-system messages with empty Content, empty ToolCalls, and empty ToolCallID
func dropEmptyMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return messages
	}

	result := make([]Message, 0, len(messages))

	for _, msg := range messages {
		// Skip messages with empty role
		if msg.Role == "" {
			continue
		}

		// System messages are always kept (even if empty content)
		if msg.Role == RoleSystem {
			result = append(result, msg)
			continue
		}

		// Non-system: skip if content, tool calls, and tool call ID are all empty
		if msg.Content == "" && len(msg.ToolCalls) == 0 && msg.ToolCallID == "" {
			continue
		}

		result = append(result, msg)
	}

	return result
}

// ensureValidStart ensures the message list starts with a valid message type.
// Skips leading system messages (keeps the first one), removes leading tool messages.
func ensureValidStart(messages []Message) []Message {
	if len(messages) == 0 {
		return messages
	}

	startIdx := 0
	for startIdx < len(messages) {
		msg := messages[startIdx]
		switch msg.Role {
		case RoleSystem:
			// Keep the first system message, stop here
			break
		case RoleTool:
			// Remove leading tool messages
			startIdx++
			continue
		default:
			// Valid start (user, assistant, etc.)
			break
		}
		break
	}

	if startIdx == 0 {
		return messages
	}

	slog.Warn("trimmed leading messages", "removed", startIdx)
	return messages[startIdx:]
}
