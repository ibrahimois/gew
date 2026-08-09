import { EventEmitter } from 'node:events';
import * as path from 'node:path';

import mock from 'mock-require';

class Uri {
  public readonly scheme = 'file';
  public readonly fsPath: string;

  private constructor(fsPath: string) {
    this.fsPath = path.resolve(fsPath);
  }

  public static file(fsPath: string): Uri {
    return new Uri(fsPath);
  }
}

class CancellationTokenSource {
  private readonly emitter = new EventEmitter();
  public readonly token: {
    isCancellationRequested: boolean;
    onCancellationRequested(listener: () => void): { dispose(): void };
  };

  public constructor() {
    this.token = {
      isCancellationRequested: false,
      onCancellationRequested: (listener) => {
        this.emitter.on('cancel', listener);
        return {
          dispose: (): void => {
            this.emitter.off('cancel', listener);
          },
        };
      },
    };
  }

  public cancel(): void {
    if (this.token.isCancellationRequested) {
      return;
    }
    this.token.isCancellationRequested = true;
    this.emitter.emit('cancel');
  }

  public dispose(): void {
    this.emitter.removeAllListeners();
  }
}

mock('vscode', {
  Uri,
  CancellationTokenSource,
  ProgressLocation: { Notification: 15 },
  workspace: {
    getConfiguration: () => ({ get: (_key: string, fallback: unknown) => fallback }),
    workspaceFolders: [],
    isTrusted: true,
  },
  window: {
    showInformationMessage: async () => undefined,
    showErrorMessage: async () => undefined,
    showQuickPick: async () => undefined,
    withProgress: async (_options: unknown, task: () => unknown) => task(),
  },
  commands: {
    executeCommand: async () => undefined,
  },
});
