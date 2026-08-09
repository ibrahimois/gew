import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process';
import * as path from 'node:path';

export interface CancellationTokenLike {
  readonly isCancellationRequested: boolean;
  onCancellationRequested(listener: () => void): { dispose(): void };
}

export interface OutputChannelLike {
  append(value: string): void;
  appendLine(value: string): void;
  show(preserveFocus?: boolean): void;
}

export interface ProcessResult {
  readonly exitCode: number | null;
  readonly signal: NodeJS.Signals | null;
  readonly spawnError?: Error;
  readonly cancelled: boolean;
}

export interface RunRequest {
  readonly executable: string;
  readonly args: readonly string[];
  readonly cwd: string;
  readonly phase: string;
  readonly token: CancellationTokenLike;
}

export type StartResult =
  | { readonly status: 'started'; readonly result: Promise<ProcessResult> }
  | { readonly status: 'busy' };

export type ExclusiveRunResult<T> =
  | { readonly status: 'completed'; readonly value: T }
  | { readonly status: 'busy' };

export type ProcessSpawner = (
  executable: string,
  args: readonly string[],
  options: {
    readonly cwd: string;
    readonly shell: false;
    readonly env: NodeJS.ProcessEnv;
  },
) => ChildProcessWithoutNullStreams;

const TERMINATION_ESCALATION_MS = 1500;

export class GewRunner {
  private readonly inFlight = new Set<string>();

  public constructor(
    private readonly output: OutputChannelLike,
    private readonly spawner: ProcessSpawner = spawn,
  ) {}

  public start(request: RunRequest): StartResult {
    if (this.inFlight.has(request.cwd)) {
      return { status: 'busy' };
    }
    this.inFlight.add(request.cwd);
    const result = this.runProcess(request);
    void result.finally(() => {
      this.inFlight.delete(request.cwd);
    });
    return { status: 'started', result };
  }

  public async runExclusive<T>(
    canonicalRoot: string,
    task: () => Promise<T>,
  ): Promise<ExclusiveRunResult<T>> {
    if (this.inFlight.has(canonicalRoot)) {
      return { status: 'busy' };
    }
    this.inFlight.add(canonicalRoot);
    try {
      return { status: 'completed', value: await task() };
    } finally {
      this.inFlight.delete(canonicalRoot);
    }
  }

  public async run(request: RunRequest): Promise<ProcessResult> {
    return this.runProcess(request);
  }

  public isRunning(canonicalRoot: string): boolean {
    return this.inFlight.has(canonicalRoot);
  }

  private runProcess(request: RunRequest): Promise<ProcessResult> {
    if (request.executable.trim().length === 0) {
      return Promise.resolve({
        exitCode: null,
        signal: null,
        spawnError: new Error('The gew.executablePath setting must not be empty.'),
        cancelled: false,
      });
    }

    const label = `${path.basename(request.cwd)} ${request.phase}`;
    this.output.appendLine(`[${label}] starting GEW`);

    return new Promise<ProcessResult>((resolve) => {
      let child: ChildProcessWithoutNullStreams;
      try {
        child = this.spawner(request.executable, request.args, {
          cwd: request.cwd,
          shell: false,
          env: process.env,
        });
      } catch (error: unknown) {
        const spawnError = normalizeError(error);
        this.output.appendLine(`[${label}] failed to start: ${spawnError.message}`);
        resolve({ exitCode: null, signal: null, spawnError, cancelled: false });
        return;
      }

      let settled = false;
      let cancelled = request.token.isCancellationRequested;
      let spawnError: Error | undefined;
      let escalationTimer: NodeJS.Timeout | undefined;

      const stdoutListener = (chunk: Buffer | string): void => {
        appendPrefixed(this.output, label, chunk.toString());
      };
      const stderrListener = (chunk: Buffer | string): void => {
        appendPrefixed(this.output, label, chunk.toString());
      };
      const cancellationDisposable = request.token.onCancellationRequested(() => {
        cancelled = true;
        terminateChild();
      });

      const cleanup = (): void => {
        cancellationDisposable.dispose();
        child.stdout.off('data', stdoutListener);
        child.stderr.off('data', stderrListener);
        child.off('error', errorListener);
        child.off('close', closeListener);
        if (escalationTimer !== undefined) {
          clearTimeout(escalationTimer);
        }
      };

      const finish = (result: ProcessResult): void => {
        if (settled) {
          return;
        }
        settled = true;
        cleanup();
        resolve(result);
      };

      const terminateChild = (): void => {
        if (child.exitCode !== null || child.signalCode !== null) {
          return;
        }
        child.kill('SIGTERM');
        escalationTimer = setTimeout(() => {
          if (child.exitCode === null && child.signalCode === null) {
            child.kill('SIGKILL');
          }
        }, TERMINATION_ESCALATION_MS);
        escalationTimer.unref();
      };

      const errorListener = (error: Error): void => {
        spawnError = error;
        this.output.appendLine(`[${label}] process error: ${error.message}`);
      };
      const closeListener = (exitCode: number | null, signal: NodeJS.Signals | null): void => {
        this.output.appendLine(
          `[${label}] finished with ${signal === null ? `exit ${String(exitCode)}` : `signal ${signal}`}`,
        );
        finish({ exitCode, signal, spawnError, cancelled });
      };

      child.stdout.on('data', stdoutListener);
      child.stderr.on('data', stderrListener);
      child.once('error', errorListener);
      child.once('close', closeListener);

      if (cancelled) {
        terminateChild();
      }
    });
  }
}

function appendPrefixed(output: OutputChannelLike, label: string, text: string): void {
  const lines = text.split(/(?<=\n)/u);
  for (const line of lines) {
    if (line.length === 0) {
      continue;
    }
    output.append(`[${label}] ${line}`);
    if (!line.endsWith('\n')) {
      output.append('\n');
    }
  }
}

function normalizeError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error));
}
