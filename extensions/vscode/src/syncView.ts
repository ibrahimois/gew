import * as vscode from 'vscode';

export interface GewSyncViewActions {
  sync(): Promise<void>;
  pull(): Promise<void>;
  push(): Promise<void>;
}

export class GewSyncViewProvider implements vscode.WebviewViewProvider {
  public static readonly viewType = 'gew.syncView';

  public constructor(private readonly actions: GewSyncViewActions) {}

  public resolveWebviewView(webviewView: vscode.WebviewView): void {
    webviewView.webview.options = { enableScripts: true };
    webviewView.webview.html = renderView(webviewView.webview);
    webviewView.webview.onDidReceiveMessage(async (message: unknown) => {
      if (!isViewMessage(message)) {
        return;
      }
      switch (message.command) {
        case 'sync':
          await this.actions.sync();
          break;
        case 'pull':
          await this.actions.pull();
          break;
        case 'push':
          await this.actions.push();
          break;
      }
    });
  }
}

function renderView(webview: vscode.Webview): string {
  const nonce = createNonce();
  return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${webview.cspSource} 'nonce-${nonce}'; script-src 'nonce-${nonce}';">
  <style nonce="${nonce}">
    body {
      box-sizing: border-box;
      margin: 0;
      padding: 8px 12px 12px;
      color: var(--vscode-foreground);
      font-family: var(--vscode-font-family);
      font-size: var(--vscode-font-size);
    }
    .sync-button {
      width: 100%;
      min-height: 30px;
      border: 1px solid var(--vscode-button-border, transparent);
      border-radius: 2px;
      color: var(--vscode-button-foreground);
      background: var(--vscode-button-background);
      font: inherit;
      cursor: pointer;
    }
    .sync-button:hover {
      background: var(--vscode-button-hoverBackground);
    }
    .secondary-actions {
      display: grid;
      grid-template-columns: 1fr 1fr;
      gap: 6px;
      margin-top: 6px;
    }
    .secondary-button {
      min-height: 26px;
      border: 1px solid var(--vscode-button-secondaryBackground);
      border-radius: 2px;
      color: var(--vscode-button-secondaryForeground);
      background: var(--vscode-button-secondaryBackground);
      font: inherit;
      cursor: pointer;
    }
    .secondary-button:hover {
      background: var(--vscode-button-secondaryHoverBackground);
    }
  </style>
</head>
<body>
  <button class="sync-button" data-command="sync" title="Pull via GEW REST, then Push after a successful Pull">Sync via GEW REST</button>
  <div class="secondary-actions">
    <button class="secondary-button" data-command="pull">Pull</button>
    <button class="secondary-button" data-command="push">Push</button>
  </div>
  <script nonce="${nonce}">
    const vscode = acquireVsCodeApi();
    document.addEventListener('click', (event) => {
      const target = event.target;
      if (target instanceof HTMLButtonElement) {
        vscode.postMessage({ command: target.dataset.command });
      }
    });
  </script>
</body>
</html>`;
}

function isViewMessage(value: unknown): value is { command: 'sync' | 'pull' | 'push' } {
  if (typeof value !== 'object' || value === null || !('command' in value)) {
    return false;
  }
  const command = (value as { command?: unknown }).command;
  return command === 'sync' || command === 'pull' || command === 'push';
}

function createNonce(): string {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
  let nonce = '';
  for (let index = 0; index < 32; index += 1) {
    nonce += alphabet.charAt(Math.floor(Math.random() * alphabet.length));
  }
  return nonce;
}
