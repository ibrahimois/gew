import * as path from 'node:path';

import { type CancellationTokenLike, type ProcessResult } from './gewRunner';

export type OperationName = 'Pull' | 'Push' | 'Sync';
export type OperationPhase = 'Pull' | 'Push';

export interface OperationHost {
  readonly isTrusted: boolean;
  resolveTarget(): Promise<string | undefined>;
  getExecutablePath(): string;
  runWithProgress<T>(
    title: string,
    task: (token: CancellationTokenLike, report: (message: string) => void) => Promise<T>,
  ): Promise<T>;
  showInformation(message: string): Promise<void>;
  showError(message: string): Promise<void>;
  revealOutput(): void;
}

export interface OperationRunner {
  runExclusive<T>(canonicalRoot: string, task: () => Promise<T>): Promise<ExclusiveRunResult<T>>;
  run(request: {
    readonly executable: string;
    readonly args: readonly string[];
    readonly cwd: string;
    readonly phase: string;
    readonly token: CancellationTokenLike;
  }): Promise<ProcessResult>;
}

export type ExclusiveRunResult<T> =
  | { readonly status: 'completed'; readonly value: T }
  | { readonly status: 'busy' };

export class Operations {
  public constructor(
    private readonly host: OperationHost,
    private readonly runner: OperationRunner,
  ) {}

  public async pull(): Promise<void> {
    await this.execute('Pull', ['Pull']);
  }

  public async push(): Promise<void> {
    await this.execute('Push', ['Push']);
  }

  public async sync(): Promise<void> {
    await this.execute('Sync', ['Pull', 'Push']);
  }

  private async execute(operation: OperationName, phases: readonly OperationPhase[]): Promise<void> {
    if (!this.host.isTrusted) {
      await this.host.showInformation(`GEW ${operation} is unavailable until this workspace is trusted.`);
      return;
    }

    const root = await this.host.resolveTarget();
    if (root === undefined) {
      await this.host.showInformation('Enable an open hybrid repository before running GEW Source Control actions.');
      return;
    }

    const repositoryName = path.basename(root);
    let executable: string;
    try {
      executable = this.host.getExecutablePath();
      if (executable.trim().length === 0) {
        throw new Error('The gew.executablePath setting must not be empty.');
      }
    } catch (error: unknown) {
      await this.fail(operation, error instanceof Error ? error.message : String(error));
      return;
    }

    await this.host.runWithProgress(
      `GEW: ${operation} via REST — ${repositoryName}`,
      async (token, report) => {
        const exclusive = await this.runner.runExclusive(root, async (): Promise<void> => {
          for (const phase of phases) {
            report(`Running ${phase} for ${repositoryName}`);
            const args = phase === 'Pull'
              ? ['pull', '--progress', 'always']
              : ['push', '--progress', 'always'];
            let result: ProcessResult;
            try {
              result = await this.runner.run({
                executable,
                args,
                cwd: root,
                phase: phase.toLowerCase(),
                token,
              });
            } catch (error: unknown) {
              await this.fail(operation, error instanceof Error ? error.message : String(error));
              return;
            }

            if (result.cancelled) {
              await this.host.showInformation(`GEW ${operation} cancelled for ${repositoryName}.`);
              return;
            }
            if (!isSuccess(result)) {
              await this.fail(operation, describeFailure(result));
              return;
            }
          }
          await this.host.showInformation(`GEW ${operation} completed for ${repositoryName}.`);
        });
        if (exclusive.status === 'busy') {
          await this.host.showInformation(`A GEW operation is already running for ${repositoryName}.`);
        }
      },
    );
  }

  private async fail(operation: OperationName, detail: string): Promise<void> {
    this.host.revealOutput();
    await this.host.showError(`GEW ${operation} failed: ${detail}`);
  }
}

function isSuccess(result: ProcessResult): boolean {
  return result.spawnError === undefined && result.exitCode === 0 && result.signal === null;
}

function describeFailure(result: ProcessResult): string {
  if (result.spawnError !== undefined) {
    return result.spawnError.message;
  }
  if (result.signal !== null) {
    return `terminated by signal ${result.signal}`;
  }
  return `exit status ${String(result.exitCode)}`;
}
