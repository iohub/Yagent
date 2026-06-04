import * as vscode from 'vscode'
import * as path from 'path'
import { GlobalParser } from '../../services/tree-sitter/GlobalParser'

export interface CodeLensCommand {
  title: string
  tooltip?: string
  action: string
  messageTemplate: string
}

export class CommandCodeLensProvider implements vscode.CodeLensProvider {
  private codeLenses: vscode.CodeLens[] = []
  private _onDidChangeCodeLenses = new vscode.EventEmitter<void>()
  public readonly onDidChangeCodeLenses = this._onDidChangeCodeLenses.event
  private commands: CodeLensCommand[] = []

  constructor(commands: CodeLensCommand[]) {
    this.commands = commands

    vscode.workspace.onDidChangeConfiguration(() => {
      this._onDidChangeCodeLenses.fire()
    })

    vscode.workspace.onDidChangeTextDocument(() => {
      this._onDidChangeCodeLenses.fire()
    })

    vscode.window.onDidChangeActiveTextEditor(() => {
      this._onDidChangeCodeLenses.fire()
    })

    this._onDidChangeCodeLenses.fire()
  }

  public updateCommands(commands: CodeLensCommand[]): void {
    this.commands = commands
    this._onDidChangeCodeLenses.fire()
  }

  public async provideCodeLenses(
    document: vscode.TextDocument,
    token: vscode.CancellationToken
  ): Promise<vscode.CodeLens[]> {
    if (token.isCancellationRequested) {
      return []
    }

    this.codeLenses = []
    const fileExt = path.extname(document.fileName).slice(1)

    try {
      // Lazily add parser for this language if not pre-loaded
      if (!GlobalParser.getInstance().hasParser(fileExt)) {
        try {
          await GlobalParser.getInstance().addLanguage(fileExt)
        } catch {
          // Unsupported language — no lenses
          return []
        }
      }

      const { parser, query } = GlobalParser.getInstance().getParser(fileExt)
      const tree = parser.parse(document.getText())
      if (!tree) { return [] }
      const captures = query.captures(tree.rootNode)

      for (const capture of captures) {
        if (capture.name.startsWith('definition')) {
          const range = new vscode.Range(
            document.positionAt(capture.node.startIndex),
            document.positionAt(capture.node.endIndex)
          )

          // Action lenses
          for (const cmd of this.commands) {
            if (cmd.action === 'edit') {
              continue
            }
            const codeLensCommand: vscode.Command = {
              title: cmd.title,
              tooltip: cmd.tooltip,
              command: 'codeactor.executeCodeLensCommand',
              arguments: [{ document, range, nodeType: capture.name, action: cmd.action, messageTemplate: cmd.messageTemplate }],
            }
            this.codeLenses.push(new vscode.CodeLens(range, codeLensCommand))
          }

          // Settings lens
          const settingsLens: vscode.Command = {
            title: '⚙️ Edit',
            tooltip: 'Edit CodeLens commands in settings',
            command: 'codeactor.editCodeLensConfig',
            arguments: [],
          }
          this.codeLenses.push(new vscode.CodeLens(range, settingsLens))
        }
      }
    } catch (error) {
      console.error('[CodeActor] Error providing code lenses:', error)
    }

    return this.codeLenses
  }
}
