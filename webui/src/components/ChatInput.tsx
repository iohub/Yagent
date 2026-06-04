import React, { useState, KeyboardEvent, ChangeEvent, useEffect, useRef, useCallback } from 'react';
import { MessageSquare, AtSign, Folder, Plus, Square, Paperclip, Sparkles, ChevronLeft, File, X, TextCursor, Pin, ArrowUp } from 'lucide-react';
import { AddModelModal, ModelConfig } from './AddModelModal';
import { configManager } from '../utils/configManager';

interface ChatInputProps {
  onSendMessage: (message: string) => void;
  onClearChat: () => void;
  onSettings: () => void;
  currentMode: 'chat' | 'agent';
  onModeChange: (mode: 'chat' | 'agent') => void;
  isProcessing: boolean;
  isTaskRunning?: boolean;
  onAbort?: () => void;
  onQuickAction?: (action: string) => void;
  onSubmitTask?: (taskDesc: string) => void;
}

// Context item (chips at the top)
interface ContextItem {
  id: string;
  type: 'file' | 'folder' | 'selection';
  name: string;
  path?: string;
  code?: string;
  language?: string;
  startLine?: number;
  endLine?: number;
}

// File suggestion for @ mention picker
interface FileSuggestion {
  name: string;
  path: string;
  fullPath: string;
  type: 'file' | 'folder';
}

const MOCK_FILES: FileSuggestion[] = [
  { name: 'CommandCodeLens.js',   path: '...ode/out/integrations/codelens',     fullPath: 'out/integrations/codelens/CommandCodeLens.js',        type: 'file' },
  { name: 'SelectionCodeLens.ts', path: '...code/src/integrations/codelens',    fullPath: 'src/integrations/codelens/SelectionCodeLens.ts',      type: 'file' },
  { name: 'codelens_commands.json', path: '...t-VScode-Extension/config',       fullPath: 'config/codelens_commands.json',                       type: 'file' },
  { name: 'CommandProvider.ts',   path: '...ension/src/integrations/codelens',  fullPath: 'src/integrations/codelens/CommandProvider.ts',         type: 'file' },
  { name: 'App.tsx',              path: '...webui/src',                          fullPath: 'webui/src/App.tsx',                                   type: 'file' },
  { name: 'ChatInput.tsx',        path: '...webui/src/components',              fullPath: 'webui/src/components/ChatInput.tsx',                  type: 'file' },
  { name: 'ChatMessages.tsx',     path: '...webui/src/components',              fullPath: 'webui/src/components/ChatMessages.tsx',               type: 'file' },
];

// Project name derived from URL / window title, fallback value
function getProjectName(): string {
  try {
    const parts = window.location.pathname.split('/').filter(Boolean);
    return parts[parts.length - 1] || 'project';
  } catch {
    return 'project';
  }
}

export const ChatInput: React.FC<ChatInputProps> = ({
  onSendMessage,
  onClearChat,
  onSettings,
  currentMode,
  onModeChange,
  isProcessing,
  isTaskRunning,
  onAbort,
  onSubmitTask,
}) => {
  const [message, setMessage] = useState('');
  const [selectedModel, setSelectedModel] = useState(configManager.getSelectedModel());
  const [showModelMenu, setShowModelMenu] = useState(false);
  const [showAddModelModal, setShowAddModelModal] = useState(false);
  const [contextItems, setContextItems] = useState<ContextItem[]>([]);
  const [showFilePicker, setShowFilePicker] = useState(false);
  const [filePickerQuery, setFilePickerQuery] = useState('');
  const [filePickerSelected, setFilePickerSelected] = useState(0);
  const [atMentionStart, setAtMentionStart] = useState<number>(-1);

  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const modelMenuRef = useRef<HTMLDivElement>(null);
  const filePickerRef = useRef<HTMLDivElement>(null);
  const containerRef = useRef<HTMLDivElement>(null);

  // Derived state
  const isAutoMode = currentMode === 'agent';
  const projectName = 'claude-code-best';

  // Filtered file suggestions
  const filteredFiles = filePickerQuery
    ? MOCK_FILES.filter(f => f.name.toLowerCase().includes(filePickerQuery.toLowerCase()))
    : MOCK_FILES;

  // ── send / keyboard ──────────────────────────────────────────────────────
  const handleSend = () => {
    if (message.trim() && !isProcessing && !isTaskRunning) {
      // Build full message with markdown-formatted context
      let fullMessage = message.trim();
      if (contextItems.length > 0) {
        const contextMarkdown = contextItems
          .map(c => {
            if (c.type === 'selection') {
              const location = c.startLine && c.endLine
                ? `${c.path || c.name}:${c.startLine}-${c.endLine}`
                : (c.path || c.name);
              const lang = c.language || '';
              return [
                '### Context: Selection',
                `- **Name**: ${c.name}`,
                `- **Location**: ${location}`,
                '',
                '```' + lang,
                c.code || '',
                '```',
              ].join('\n');
            }

            const location = c.path || c.name;
            return [
              `### Context: ${c.type === 'file' ? 'File' : 'Folder'}`,
              `- **Name**: ${c.name}`,
              `- **Path**: ${location}`,
            ].join('\n');
          })
          .join('\n\n');

        fullMessage = [
          '## Context',
          contextMarkdown,
          '',
          '## User Request',
          message.trim(),
        ].join('\n');
      }

      if (onSubmitTask) {
        onSubmitTask(fullMessage);
      } else {
        onSendMessage(fullMessage);
      }
      setMessage('');
    }
  };

  const handleKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (showFilePicker) {
      if (e.key === 'ArrowDown') { e.preventDefault(); setFilePickerSelected(i => Math.min(i + 1, filteredFiles.length - 1)); return; }
      if (e.key === 'ArrowUp')   { e.preventDefault(); setFilePickerSelected(i => Math.max(i - 1, 0)); return; }
      if (e.key === 'Enter')     { e.preventDefault(); selectFile(filteredFiles[filePickerSelected]); return; }
      if (e.key === 'Escape')    { e.preventDefault(); closeFilePicker(); return; }
    }
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSend();
    }
  };

  // ── @ mention detection ───────────────────────────────────────────────────
  const handleInputChange = (e: ChangeEvent<HTMLTextAreaElement>) => {
    const value = e.target.value;
    const cursor = e.target.selectionStart ?? value.length;
    setMessage(value);

    const textBefore = value.substring(0, cursor);
    const atMatch = textBefore.match(/@([^\s@]*)$/);
    if (atMatch) {
      const query = atMatch[1];
      setFilePickerQuery(query);
      setAtMentionStart(cursor - atMatch[0].length);
      setShowFilePicker(true);
      setFilePickerSelected(0);
    } else {
      setShowFilePicker(false);
      setAtMentionStart(-1);
    }
  };

  // ── file picker actions ───────────────────────────────────────────────────
  const selectFile = useCallback((file: FileSuggestion | undefined) => {
    if (!file) return;
    // Replace the @query in textarea with the file path reference
    if (atMentionStart >= 0 && textareaRef.current) {
      const cursor = textareaRef.current.selectionStart ?? message.length;
      const before = message.substring(0, atMentionStart);
      const after  = message.substring(cursor);
      setMessage(before + `@${file.fullPath} ` + after);
    }
    // Add chip to top toolbar
    setContextItems(prev => {
      if (prev.find(c => c.name === file.name)) return prev;
      return [...prev, { id: file.fullPath, type: file.type, name: file.name, path: file.path }];
    });
    closeFilePicker();
    textareaRef.current?.focus();
  }, [atMentionStart, message]);

  const closeFilePicker = () => {
    setShowFilePicker(false);
    setFilePickerQuery('');
    setAtMentionStart(-1);
  };

  const removeContextItem = (id: string) => {
    setContextItems(prev => prev.filter(c => c.id !== id));
  };

  // Trigger @ picker manually from button
  const triggerAtMention = () => {
    const ta = textareaRef.current;
    if (!ta) return;
    const cursor = ta.selectionStart ?? message.length;
    const newMsg = message.substring(0, cursor) + '@' + message.substring(cursor);
    setMessage(newMsg);
    setAtMentionStart(cursor);
    setFilePickerQuery('');
    setShowFilePicker(true);
    setFilePickerSelected(0);
    setTimeout(() => { ta.focus(); ta.setSelectionRange(cursor + 1, cursor + 1); }, 0);
  };

  // ── click outside ─────────────────────────────────────────────────────────
  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (modelMenuRef.current && !modelMenuRef.current.contains(e.target as Node)) setShowModelMenu(false);
      if (filePickerRef.current && !filePickerRef.current.contains(e.target as Node) &&
          textareaRef.current && !textareaRef.current.contains(e.target as Node)) {
        closeFilePicker();
      }
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, []);

  // Auto-focus on load
  useEffect(() => {
    if (textareaRef.current && !isProcessing) textareaRef.current.focus();
  }, [isProcessing]);

  // Config change listener
  useEffect(() => {
    const unsub = configManager.onConfigChange(cfg => setSelectedModel(cfg.selectedModel));
    return () => { unsub(); };
  }, []);

  // Listen for add-chat-context events from VSCode extension
  useEffect(() => {
    const handler = (e: Event) => {
      const customEvent = e as CustomEvent<ContextItem>;
      const newItem = customEvent.detail;
      setContextItems(prev => {
        // Avoid duplicates based on code content
        if (prev.find(c => c.code === newItem.code)) return prev;
        return [...prev, newItem];
      });
    };
    window.addEventListener('add-chat-context', handler);
    return () => window.removeEventListener('add-chat-context', handler);
  }, []);

  // ── model display name (shorten long names) ───────────────────────────────
  const modelShortName = selectedModel.replace(/claude-/i, '').replace(/\s*(sonnet|opus|haiku)\s*/i, ' $1').trim();

  // ── add model handler ─────────────────────────────────────────────────────
  const handleAddModel = (modelConfig: ModelConfig) => {
    configManager.addCustomModel(modelConfig);
  };

  return (
    <div ref={containerRef} className="relative group bg-card rounded-xl border border-border shadow-lg">

      {/* ── File Picker Dropdown ─────────────────────────────────────────── */}
      {showFilePicker && filteredFiles.length > 0 && (
        <div
          ref={filePickerRef}
          className="absolute bottom-[calc(100%+6px)] left-0 right-0 mx-0 bg-vscode-bg-secondary border border-border rounded-xl shadow-xl z-50 overflow-hidden"
        >
          {/* Header */}
          <div className="flex items-center gap-2 px-3 py-2 border-b border-border/60">
            <button
              type="button"
              onClick={closeFilePicker}
              className="text-muted-foreground hover:text-foreground transition-colors"
            >
              <ChevronLeft className="w-4 h-4" />
            </button>
            <span className="text-sm font-medium text-foreground">Files</span>
          </div>
          {/* File list */}
          <div className="max-h-48 overflow-y-auto">
            {filteredFiles.map((file, idx) => (
              <button
                key={file.fullPath}
                type="button"
                onClick={() => selectFile(file)}
                className={`w-full flex items-center gap-2.5 px-3 py-2 text-left transition-colors ${
                  idx === filePickerSelected ? 'bg-muted' : 'hover:bg-muted/50'
                }`}
              >
                <File className="w-3.5 h-3.5 text-muted-foreground shrink-0" />
                <span className="text-sm font-medium text-foreground truncate">{file.name}</span>
                <span className="text-xs text-muted-foreground truncate ml-auto shrink-0 max-w-[160px]">{file.path}</span>
              </button>
            ))}
          </div>
        </div>
      )}

      {/* ── Top toolbar: icon buttons + context chips ────────────────────── */}
      <div className="flex items-center gap-1 px-3 pt-2.5 pb-1.5 flex-wrap">
        {/* @ mention button */}
        <button
          type="button"
          onClick={triggerAtMention}
          className="p-1 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted/60 transition-colors"
          title="Add file reference (@)"
        >
          <AtSign className="w-4 h-4" />
        </button>
        {/* Text cursor / selection */}

        {/* Project folder chip */}
        <div className="flex items-center gap-1 text-xs bg-muted/50 rounded-md px-2 py-0.5 text-foreground/80">
          <Folder className="w-3 h-3 text-muted-foreground" />
          <span className="font-medium">{projectName}</span>
        </div>

        {/* File context chips */}
        {contextItems.map(item => (
          <div
            key={item.id}
            className="flex items-center gap-1 text-xs bg-muted/50 rounded-md px-2 py-0.5 text-foreground/80"
          >
            <span className="font-medium max-w-[120px] truncate">{item.name}</span>
            {item.type === 'selection' && item.startLine && item.endLine && (
              <span className="text-muted-foreground/70 text-[10px]">:{item.startLine}-{item.endLine}</span>
            )}
            <button
              type="button"
              onClick={() => removeContextItem(item.id)}
              className="ml-0.5 text-muted-foreground hover:text-foreground transition-colors"
            >
              <X className="w-3 h-3" />
            </button>
          </div>
        ))}
      </div>

      {/* ── Textarea ─────────────────────────────────────────────────────── */}
      <div className="px-3 pb-1">
        <textarea
          ref={textareaRef}
          value={message}
          onChange={handleInputChange}
          onKeyDown={handleKeyDown}
          placeholder={isTaskRunning ? "Task is running..." : "Describe your task..."}
          rows={4}
          disabled={isTaskRunning && !isProcessing === false}
          className="w-full bg-transparent text-sm text-foreground placeholder:text-muted-foreground resize-none focus:outline-none min-h-[72px] disabled:opacity-50"
          style={{ fieldSizing: 'content' } as React.CSSProperties}
        />
      </div>

      {/* ── Bottom toolbar ────────────────────────────────────────────────── */}
      <div className="flex items-center justify-between px-3 pb-2.5 pt-1 border-t border-border/40">

        {/* Left: Auto toggle + mode icon + separator + model */}
        <div className="flex items-center gap-2">
          {/* Auto toggle pill */}
          <button
            type="button"
            onClick={() => onModeChange(isAutoMode ? 'chat' : 'agent')}
            className={`flex items-center gap-1.5 rounded-full px-2 py-[3px] border border-transparent transition-all duration-200 focus:outline-none focus-visible:ring-2 focus-visible:ring-green-500/40 ${
              isAutoMode
                ? 'bg-green-500/10 hover:bg-green-500/15'
                : 'bg-muted/70 hover:bg-muted'
            }`}
            title={isAutoMode ? 'Switch to chat mode' : 'Switch to agent (Auto) mode'}
          >
            {isAutoMode ? (
              /* ON: [Auto] [●]  — green tinted pill, text left, filled knob right */
              <>
                <span className="text-xs font-semibold text-green-600 dark:text-green-400 leading-none select-none">Auto</span>
                <span className="w-[18px] h-[18px] rounded-full bg-green-500/15 border border-green-500/30 flex items-center justify-center shrink-0">
                  <span className="w-2.5 h-2.5 rounded-full bg-green-500" />
                </span>
              </>
            ) : (
              /* OFF: [○] [Auto]  — knob left, text right muted */
              <>
                <span className="w-[18px] h-[18px] rounded-full bg-white shadow-sm border border-border/50 shrink-0" />
                <span className="text-xs font-medium text-foreground/75 leading-none select-none">Auto</span>
              </>
            )}
          </button>

          {/* Separator */}
          <span className="text-border/80 text-base select-none">|</span>

          {/* Model selector */}
          <div className="relative" ref={modelMenuRef}>
            <button
              type="button"
              onClick={() => setShowModelMenu(v => !v)}
              className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors px-1 py-0.5 rounded-md hover:bg-muted/60"
              title={`Model: ${selectedModel}`}
            >
              <span className="max-w-[100px] truncate">{modelShortName}</span>
            </button>

            {showModelMenu && (
              <div className="absolute bottom-full left-0 mb-2 w-52 bg-vscode-bg-secondary border border-border rounded-lg shadow-xl z-50 text-sm">
                {['Claude 3.5 Sonnet', 'Claude 3 Opus', 'Claude 3 Haiku', 'GPT-4o', 'GPT-4 Turbo'].map(model => (
                  <button
                    key={model}
                    type="button"
                    onClick={() => { setSelectedModel(model); configManager.updateSelectedModel(model); setShowModelMenu(false); }}
                    className={`w-full flex items-center gap-2 px-2.5 py-1.5 text-foreground hover:bg-muted transition-colors ${selectedModel === model ? 'bg-muted text-primary' : ''}`}
                  >
                    <div className={`w-1.5 h-1.5 rounded-full ${selectedModel === model ? 'bg-primary' : 'bg-muted-foreground'}`} />
                    <span className="font-medium text-xs">{model}</span>
                  </button>
                ))}
                <div className="border-t border-border my-0.5" />
                <button
                  type="button"
                  onClick={() => { setShowModelMenu(false); setShowAddModelModal(true); }}
                  className="w-full flex items-center gap-2 px-2.5 py-1.5 text-foreground hover:bg-muted transition-colors rounded-b-lg"
                >
                  <Plus className="w-3 h-3" />
                  <span className="font-medium text-xs">Add model</span>
                </button>
              </div>
            )}
          </div>
        </div>

        {/* Right: attachment + sparkles + send/abort */}
        <div className="flex items-center gap-1.5">
          {/* Attachment */}
          <button
            type="button"
            className="p-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted/60 transition-colors"
            title="Attach file"
          >
            <Paperclip className="w-4 h-4" />
          </button>
          {/* Sparkles */}
          <button
            type="button"
            className="p-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted/60 transition-colors"
            title="AI features"
          >
            <Sparkles className="w-4 h-4" />
          </button>
          {/* Send / Abort */}
          {(isTaskRunning || isProcessing) ? (
            <button
              type="button"
              onClick={onAbort}
              className="flex items-center justify-center w-8 h-8 rounded-full bg-red-600 text-white hover:bg-red-700 transition-colors"
              title="Abort"
            >
              <Square className="w-3.5 h-3.5 fill-current" />
            </button>
          ) : (
            <button
              type="button"
              onClick={handleSend}
              disabled={!message.trim()}
              className={`flex items-center justify-center w-8 h-8 rounded-full transition-colors ${
                message.trim()
                  ? 'bg-primary text-primary-foreground hover:bg-primary/90'
                  : 'bg-muted text-muted-foreground cursor-not-allowed'
              }`}
              title="Send (Enter)"
            >
              <ArrowUp className="w-4 h-4" />
            </button>
          )}
        </div>
      </div>

      {/* ── Add Model Modal ───────────────────────────────────────────────── */}
      <AddModelModal
        isOpen={showAddModelModal}
        onClose={() => setShowAddModelModal(false)}
        onAddModel={handleAddModel}
      />
    </div>
  );
};