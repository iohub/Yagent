import * as vscode from 'vscode'

export interface SelectionCodeLensCommand {
  title: string
  tooltip?: string
  action: string
  messageTemplate: string
}

/**
 * A CodeLens provider that shows action lenses whenever the user has a
 * non-empty text selection. The lenses appear on the first line of the
 * selection and are entirely independent of tree-sitter / symbol parsing.
 */
export class SelectionCodeLensProvider implements vscode.CodeLensProvider {
  private _onDidChangeCodeLenses = new vscode.EventEmitter<void>()
  public readonly onDidChangeCodeLenses = this._onDidChangeCodeLenses.event

  /** The current non-empty selection, if any. Keyed by document URI string. */
  private activeSelection: { uri: string; range: vscode.Range } | null = null

  private readonly defaultCommands: SelectionCodeLensCommand[] = [
    {
      title: '{:} Add context',
      tooltip: 'Add selected code in chat',
      action: 'add-in-chat',
      messageTemplate: '{code}',
    },
    {
      title: 'Fix bug',
      tooltip: 'Ask AI to fix the selected code',
      action: 'fix',
      messageTemplate: 'Fix the following code:\n\n{code}',
    },
    {
      title: 'Document',
      tooltip: 'Ask AI to add documentation to the selected code',
      action: 'document',
      messageTemplate: 'Add documentation comments to the following code:\n\n{code}',
    },
  ]

  constructor(private readonly commands: SelectionCodeLensCommand[] = []) {
    if (this.commands.length === 0) {
      this.commands = this.defaultCommands
    }
  }

  /**
   * Call this whenever the editor selection changes.
   * Returns true if the internal state actually changed.
   */
  public updateSelection(editor: vscode.TextEditor | undefined): void {
    if (!editor || editor.selection.isEmpty) {
      if (this.activeSelection !== null) {
        this.activeSelection = null
        this._onDidChangeCodeLenses.fire()
      }
      return
    }

    const uri = editor.document.uri.toString()
    const sel = editor.selection
    const range = new vscode.Range(sel.start, sel.end)

    const prev = this.activeSelection
    const unchanged =
      prev !== null &&
      prev.uri === uri &&
      prev.range.isEqual(range)

    if (!unchanged) {
      this.activeSelection = { uri, range }
      this._onDidChangeCodeLenses.fire()
    }
  }

  public provideCodeLenses(
    document: vscode.TextDocument,
    _token: vscode.CancellationToken
  ): vscode.CodeLens[] {
    if (!this.activeSelection) return []
    if (this.activeSelection.uri !== document.uri.toString()) return []

    const selRange = this.activeSelection.range
    // Attach all lenses to the very first character of the selection line
    // so they render as a single group above the selected block.
    const lensRange = new vscode.Range(selRange.start.line, 0, selRange.start.line, 0)

    return this.commands.map((cmd) => {
      const command: vscode.Command = {
        title: cmd.title,
        tooltip: cmd.tooltip,
        command: 'codeactor.executeSelectionCodeLensCommand',
        arguments: [
          {
            document,
            range: selRange,
            action: cmd.action,
            messageTemplate: cmd.messageTemplate,
          },
        ],
      }
      return new vscode.CodeLens(lensRange, command)
    })
  }
}
