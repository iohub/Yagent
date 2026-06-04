import * as vscode from 'vscode';
import * as path from 'path';
import * as fs from 'fs';
import { CommandCodeLensProvider, CodeLensCommand } from './integrations/codelens/CommandCodeLens';
import { SelectionCodeLensProvider } from './integrations/codelens/SelectionCodeLens';
import { GlobalParser } from './services/tree-sitter/GlobalParser';

const SUPPORTED_LANGUAGES = [
  'javascript', 'javascriptreact', 'typescript', 'typescriptreact',
  'python', 'go', 'rust', 'java', 'cpp', 'c', 'csharp',
  'ruby', 'php', 'swift',
];

function getCodeLensCommands(): CodeLensCommand[] {
  const config = vscode.workspace.getConfiguration('codeactor');
  return config.get<CodeLensCommand[]>('codeLensCommands', [
    { title: '⚡ Explain', tooltip: 'Explain this code', action: 'explain', messageTemplate: 'Explain the following code:\n\n{code}' },
    { title: '🔍 Review', tooltip: 'Review for bugs and improvements', action: 'review', messageTemplate: 'Review the following code for bugs and improvements:\n\n{code}' },
  ]);
}

export function activate(context: vscode.ExtensionContext) {
  const provider = new CodeActorViewProvider(context);

  // Register the webview view provider
  vscode.window.registerWebviewViewProvider(
    CodeActorViewProvider.viewType,
    provider,
    { webviewOptions: { retainContextWhenHidden: true } }
  );

  // Initialize GlobalParser (best-effort; CodeLens will lazily load missing languages)
  GlobalParser.getInstance().initialize().catch((err) => {
    console.error('[CodeActor] Failed to initialize tree-sitter parsers:', err);
  });

  // Register CodeLens provider
  const codeLensProvider = new CommandCodeLensProvider(getCodeLensCommands());
  const codeLensDisposable = vscode.languages.registerCodeLensProvider(
    SUPPORTED_LANGUAGES.map((lang) => ({ language: lang })),
    codeLensProvider
  );
  context.subscriptions.push(codeLensDisposable);

  // Update lenses when configuration changes
  context.subscriptions.push(
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration('codeactor.codeLensCommands') || e.affectsConfiguration('codeactor.enableCodeLens')) {
        codeLensProvider.updateCommands(getCodeLensCommands());
      }
    })
  );

  // Command: execute a CodeLens action (symbol / tree-sitter based)
  context.subscriptions.push(
    vscode.commands.registerCommand('codeactor.executeCodeLensCommand', async (args: {
      document: vscode.TextDocument;
      range: vscode.Range;
      nodeType: string;
      action: string;
      messageTemplate: string;
    }) => {
      const code = args.document.getText(args.range);
      const message = args.messageTemplate.replace('{code}', code);
      provider.sendMessage({ type: 'codeLensAction', action: args.action, message });
      await vscode.commands.executeCommand('codeactorView.focus');
    })
  );

  // Command: open settings to edit CodeLens commands
  context.subscriptions.push(
    vscode.commands.registerCommand('codeactor.editCodeLensConfig', () => {
      vscode.commands.executeCommand('workbench.action.openSettings', 'codeactor.codeLensCommands');
    })
  );

  // ── Selection-based CodeLens ────────────────────────────────────────────────
  const selectionLensProvider = new SelectionCodeLensProvider();
  context.subscriptions.push(
    vscode.languages.registerCodeLensProvider(
      SUPPORTED_LANGUAGES.map((lang) => ({ language: lang })),
      selectionLensProvider
    )
  );

  // Keep the selection lens in sync with the active editor selection
  context.subscriptions.push(
    vscode.window.onDidChangeTextEditorSelection((e) => {
      selectionLensProvider.updateSelection(e.textEditor);
    })
  );
  context.subscriptions.push(
    vscode.window.onDidChangeActiveTextEditor((editor) => {
      selectionLensProvider.updateSelection(editor);
    })
  );

  // Command: execute a selection-based CodeLens action
  context.subscriptions.push(
    vscode.commands.registerCommand('codeactor.executeSelectionCodeLensCommand', async (args: {
      document: vscode.TextDocument;
      range: vscode.Range;
      action: string;
      messageTemplate: string;
    }) => {
      const code = args.document.getText(args.range);
      const fileName = path.basename(args.document.fileName);
      const relativePath = vscode.workspace.asRelativePath(args.document.uri);
      const language = args.document.languageId;
      const startLine = args.range.start.line + 1; // VSCode lines are 0-indexed
      const endLine = args.range.end.line + 1;

      // Build a rich context object
      const contextData = {
        type: 'selectionCodeLensAction',
        action: args.action,
        code,
        fileName,
        relativePath,
        language,
        startLine,
        endLine,
        // Pre-format the message if it's 'add-in-chat' action
        chatMessage: args.action === 'add-in-chat'
          ? args.messageTemplate.replace('{code}', code)
          : undefined,
      };

      provider.sendMessage(contextData);
      await vscode.commands.executeCommand('codeactorView.focus');
    })
  );
}

class CodeActorViewProvider implements vscode.WebviewViewProvider {
  public static readonly viewType = 'codeactorView';

  private webview?: vscode.WebviewView;

  constructor(private readonly context: vscode.ExtensionContext) {}

  public sendMessage(message: Record<string, unknown>): void {
    this.webview?.webview.postMessage(message);
  }

  resolveWebviewView(webviewView: vscode.WebviewView): void {
    this.webview = webviewView;

    webviewView.webview.options = {
      enableScripts: true,
      localResourceRoots: [this.getWebviewRoot()]
    };

    const html = this.getWebviewHtml();
    webviewView.webview.html = html;

    // Handle messages from the webview
    webviewView.webview.onDidReceiveMessage(this.handleWebviewMessage.bind(this));

    // Push initial theme and subscribe to theme changes
    this.postTheme();
    const themeSub = vscode.window.onDidChangeActiveColorTheme(() => this.postTheme());
    webviewView.onDidDispose(() => themeSub.dispose());
  }

  private themeKindToName(kind: vscode.ColorThemeKind): 'light' | 'dark' | 'high-contrast' | 'high-contrast-light' {
    switch (kind) {
      case vscode.ColorThemeKind.Light:
        return 'light';
      case vscode.ColorThemeKind.HighContrast:
        return 'high-contrast';
      case vscode.ColorThemeKind.HighContrastLight:
        return 'high-contrast-light';
      case vscode.ColorThemeKind.Dark:
      default:
        return 'dark';
    }
  }

  private postTheme(): void {
    if (!this.webview) return;
    const theme = this.themeKindToName(vscode.window.activeColorTheme.kind);
    this.webview.webview.postMessage({ type: 'themeChange', theme });
  }

  private getWebviewRoot(): vscode.Uri {
    const possiblePaths = [
      path.join(this.context.extensionPath, 'media', 'webui'),
      path.join(this.context.extensionPath, 'dist'),
      path.join(this.context.extensionPath, '..', 'webui', 'build'),
    ];

    for (const p of possiblePaths) {
      if (fs.existsSync(path.join(p, 'index.html'))) {
        return vscode.Uri.file(p);
      }
    }

    return vscode.Uri.file(possiblePaths[0]);
  }

  private getWebviewHtml(): string {
    const webuiPath = this.getWebviewRoot().fsPath;
    const indexPath = path.join(webuiPath, 'index.html');

    if (!fs.existsSync(indexPath)) {
      return this.getErrorHtml('WebUI build not found. Please run "npm run build:webui" first.');
    }

    let html = fs.readFileSync(indexPath, 'utf-8');
    return this.updateHtmlForWebview(html);
  }

  private updateHtmlForWebview(html: string): string {
    const webuiPath = this.getWebviewRoot().fsPath;
    const webview = this.webview!;
    const serverUrl = process.env.SERVER_URL || 'ws://localhost:3000';

    // Update script and link src to use webview protocol
    html = html.replace(/src="(\/static\/[^"]+)"/g, (_, src) => {
      const filePath = path.join(webuiPath, src);
      const uri = webview.webview.asWebviewUri(vscode.Uri.file(filePath));
      return `src="${uri}"`;
    });

    html = html.replace(/href="(\/static\/[^"]+)"/g, (_, href) => {
      const filePath = path.join(webuiPath, href);
      const uri = webview.webview.asWebviewUri(vscode.Uri.file(filePath));
      return `href="${uri}"`;
    });

    // Inject server URL and VS Code API
    // Use server mode (WebSocket) for connection
    const injectionScript = `
      <script>
        (function() {
          // Set server URL for WebSocket connection
          window.SERVER_URL = '${serverUrl}';
          // Provide VSCode API for other features
          window.vscode = acquireVsCodeApi();
          window.addEventListener('message', function(event) {
            window.vscode.postMessage(event.data);
          });
        })();
      </script>
    `;

    html = html.replace('<head>', `<head>${injectionScript}`);

    return html;
  }

  private handleWebviewMessage(message: any) {
    switch (message.type) {
      case 'ready':
        console.log('CodeActor webview ready');
        break;
      case 'error':
        vscode.window.showErrorMessage(message.message);
        break;
      case 'configRequest':
        // Respond with default config for server mode
        this.webview?.webview.postMessage({
          type: 'configResponse',
          id: message.id,
          config: {
            selectedModel: 'Claude 3.5 Sonnet',
            selectedRole: 'Developer',
            theme: 'dark',
            language: 'en',
            enableAutoSave: true,
            enableNotifications: true,
            customModels: []
          }
        });
        break;
      default:
        console.log('Unknown message:', message.type);
    }
  }

  private getErrorHtml(message: string): string {
    return `
      <!DOCTYPE html>
      <html>
        <head>
          <style>
            body {
              display: flex;
              align-items: center;
              justify-content: center;
              height: 100vh;
              margin: 0;
              font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
              color: #ccc;
              background: #1e1e1e;
            }
            .error { text-align: center; padding: 20px; }
            h2 { color: #f48771; }
          </style>
        </head>
        <body>
          <div class="error">
            <h2>CodeActor</h2>
            <p>${message}</p>
            <p style="font-size:12px;color:#666;">Run: npm run build in the vscode directory</p>
          </div>
        </body>
      </html>
    `;
  }
}

export function deactivate() {}
