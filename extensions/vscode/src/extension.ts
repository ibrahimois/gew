import * as vscode from 'vscode';

import { GewRunner } from './gewRunner';
import { Operations } from './operations';
import { GewSyncViewProvider } from './syncView';
import {
  ENABLED_REPOSITORIES_KEY,
  RepositoryRegistry,
  type RepositoryRegistryHost,
  type WorkspaceFolderInfo,
} from './repositoryRegistry';

const OUTPUT_CHANNEL_NAME = 'GEW Source Control';

export interface GewExtensionApi {
  readonly registry: RepositoryRegistry;
}

export async function activate(context: vscode.ExtensionContext): Promise<GewExtensionApi> {
  const output = vscode.window.createOutputChannel(OUTPUT_CHANNEL_NAME);
  const registry = new RepositoryRegistry(createRegistryHost(context));

  const runner = new GewRunner(output);
  const operations = new Operations(
    {
      get isTrusted(): boolean {
        return vscode.workspace.isTrusted;
      },
      resolveTarget: async (): Promise<string | undefined> => registry.resolveOperationTarget(),
      getExecutablePath: (): string => vscode.workspace
        .getConfiguration('gew')
        .get<string>('executablePath', 'gew'),
      runWithProgress: async <T>(
        title: string,
        task: (
          token: vscode.CancellationToken,
          report: (message: string) => void,
        ) => Promise<T>,
      ): Promise<T> => vscode.window.withProgress(
        {
          location: vscode.ProgressLocation.Notification,
          title,
          cancellable: true,
        },
        async (progress, token) => task(token, (message) => progress.report({ message })),
      ),
      showInformation: async (message: string): Promise<void> => {
        await vscode.window.showInformationMessage(message);
      },
      showError: async (message: string): Promise<void> => {
        await vscode.window.showErrorMessage(message);
      },
      revealOutput: (): void => output.show(true),
    },
    runner,
  );

  const syncView = new GewSyncViewProvider({
    sync: async (): Promise<void> => operations.sync(),
    pull: async (): Promise<void> => operations.pull(),
    push: async (): Promise<void> => operations.push(),
  });

  context.subscriptions.push(
    output,
    registry.listen(),
    vscode.window.registerWebviewViewProvider(GewSyncViewProvider.viewType, syncView),
    vscode.commands.registerCommand('gew.showSyncView', async () => showSyncView()),
    vscode.commands.registerCommand('gew.enableRepository', async (resource?: unknown) => {
      const result = await registry.enable(asResourcePath(resource));
      switch (result.status) {
        case 'enabled':
          await vscode.window.showInformationMessage(`Enabled GEW Source Control for ${result.root}.`);
          await showSyncView();
          break;
        case 'alreadyEnabled':
          await vscode.window.showInformationMessage(`GEW Source Control is already enabled for ${result.root}.`);
          await showSyncView();
          break;
        case 'notHybrid':
          await vscode.window.showErrorMessage(
            'GEW Source Control requires both .git and .gew directories. Clone with "gew clone --backend git" or migrate a clean workspace with "gew migrate --to git".',
          );
          break;
        case 'noSelection':
          await vscode.window.showInformationMessage('Open or select a workspace folder to enable GEW Source Control.');
          break;
      }
    }),
    vscode.commands.registerCommand('gew.disableRepository', async (resource?: unknown) => {
      const result = await registry.disable(asResourcePath(resource));
      if (result.status === 'disabled') {
        await vscode.window.showInformationMessage(`Disabled GEW Source Control for ${result.root}.`);
      }
    }),
    vscode.commands.registerCommand('gew.pull', async () => operations.pull()),
    vscode.commands.registerCommand('gew.push', async () => operations.push()),
    vscode.commands.registerCommand('gew.sync', async () => operations.sync()),
  );

  // Register the webview provider before restoring the context key that makes
  // the view visible. Otherwise VS Code can reveal the contributed view while
  // no provider is available and leave it unresolved for the session.
  await registry.initialize();

  return { registry };
}

export function deactivate(): void {}

async function showSyncView(): Promise<void> {
  await vscode.commands.executeCommand('workbench.view.scm');
  await vscode.commands.executeCommand(`${GewSyncViewProvider.viewType}.focus`);
  // Focusing the GEW sibling view can cause VS Code to leave its built-in
  // Changes view hidden. Focus Changes last so both share the same Source
  // Control container and the commit/change workflow remains primary.
  await vscode.commands.executeCommand('workbench.scm.focus');
}

function createRegistryHost(context: vscode.ExtensionContext): RepositoryRegistryHost {
  return {
    getWorkspaceFolders: (): readonly WorkspaceFolderInfo[] =>
      (vscode.workspace.workspaceFolders ?? [])
        .filter((folder) => folder.uri.scheme === 'file')
        .map((folder) => ({ name: folder.name, path: folder.uri.fsPath })),
    getActiveFilePath: (): string | undefined => {
      const uri = vscode.window.activeTextEditor?.document.uri;
      return uri?.scheme === 'file' ? uri.fsPath : undefined;
    },
    pickWorkspaceFolder: async (
      folders: readonly WorkspaceFolderInfo[],
      placeHolder: string,
    ): Promise<WorkspaceFolderInfo | undefined> => {
      const selected = await vscode.window.showQuickPick(
        folders.map((folder) => ({ label: folder.name, description: folder.path, folder })),
        { placeHolder },
      );
      return selected?.folder;
    },
    readState: (key: string): unknown => context.globalState.get(key),
    writeState: async (key: string, value: readonly string[]): Promise<void> => {
      await context.globalState.update(key, value);
    },
    setEnabledContext: async (enabled: boolean): Promise<void> => {
      await vscode.commands.executeCommand('setContext', 'gew.hasEnabledHybridRepository', enabled);
    },
    onWorkspaceFoldersChanged: (listener: () => void): vscode.Disposable =>
      vscode.workspace.onDidChangeWorkspaceFolders(listener),
  };
}

function asResourcePath(resource: unknown): string | undefined {
  if (resource instanceof vscode.Uri && resource.scheme === 'file') {
    return resource.fsPath;
  }
  if (typeof resource === 'object' && resource !== null && 'rootUri' in resource) {
    const rootUri = (resource as { rootUri?: unknown }).rootUri;
    if (rootUri instanceof vscode.Uri && rootUri.scheme === 'file') {
      return rootUri.fsPath;
    }
  }
  return undefined;
}

export const testExports = {
  enabledRepositoriesKey: ENABLED_REPOSITORIES_KEY,
};
