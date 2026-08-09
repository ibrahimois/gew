import { strict as assert } from 'node:assert';
import { EventEmitter } from 'node:events';
import { describe, it } from 'node:test';

import {
  type CancellationTokenLike,
  type ProcessResult,
} from '../../src/gewRunner';
import { Operations, type OperationHost } from '../../src/operations';

const success: ProcessResult = { exitCode: 0, signal: null, cancelled: false };
const failure: ProcessResult = { exitCode: 2, signal: null, cancelled: false };
const cancelled: ProcessResult = { exitCode: null, signal: 'SIGTERM', cancelled: true };

class TestToken implements CancellationTokenLike {
  public readonly isCancellationRequested = false;
  private readonly emitter = new EventEmitter();

  public onCancellationRequested(listener: () => void): { dispose(): void } {
    this.emitter.on('cancel', listener);
    return {
      dispose: (): void => {
        this.emitter.off('cancel', listener);
      },
    };
  }
}

function createHost(trusted = true): OperationHost & {
  readonly errors: string[];
  readonly information: string[];
} {
  const errors: string[] = [];
  const information: string[] = [];
  return {
    errors,
    information,
    isTrusted: trusted,
    resolveTarget: async (): Promise<string> => '/tmp/hybrid-repository',
    getExecutablePath: (): string => 'gew',
    runWithProgress: async (_title, task) => task(new TestToken(), (): void => undefined),
    showInformation: async (message): Promise<void> => {
      information.push(message);
    },
    showError: async (message): Promise<void> => {
      errors.push(message);
    },
    revealOutput: (): void => undefined,
  };
}

function operationsFor(
  results: readonly ProcessResult[],
  trusted = true,
): {
  operations: Operations;
  phases: string[];
  operationHost: ReturnType<typeof createHost>;
} {
  const phases: string[] = [];
  const operationHost = createHost(trusted);
  const runner = {
    async runExclusive<T>(_root: string, task: () => Promise<T>): Promise<{ status: 'completed'; value: T }> {
      return { status: 'completed', value: await task() };
    },
    async run(request: { readonly phase: string; readonly args: readonly string[] }): Promise<ProcessResult> {
      phases.push(`${request.phase}:${request.args.join(' ')}`);
      return results[phases.length - 1] ?? success;
    },
  };
  const operations = new Operations(operationHost, runner);
  return { operations, phases, operationHost };
}

describe('Operations', () => {
  it('orders Pull before Push during Sync', async () => {
    const { operations, phases } = operationsFor([success, success]);
    await operations.sync();
    assert.deepEqual(phases, [
      'pull:pull --progress always',
      'push:push --progress always',
    ]);
  });

  it('holds the repository lock across both Sync phases', async () => {
    const phases: string[] = [];
    let locked = false;
    const operationHost = createHost();
    const runner = {
      async runExclusive<T>(_root: string, task: () => Promise<T>): Promise<{ status: 'completed'; value: T }> {
        assert.equal(locked, false);
        locked = true;
        try {
          return { status: 'completed', value: await task() };
        } finally {
          locked = false;
        }
      },
      async run(request: { readonly phase: string }): Promise<ProcessResult> {
        assert.equal(locked, true);
        phases.push(request.phase);
        return success;
      },
    };

    await new Operations(operationHost, runner).sync();
    assert.deepEqual(phases, ['pull', 'push']);
    assert.equal(locked, false);
  });

  it('skips Push after failed Pull', async () => {
    const { operations, phases } = operationsFor([failure]);
    await operations.sync();
    assert.deepEqual(phases, ['pull:pull --progress always']);
  });

  it('skips Push after cancelled Pull', async () => {
    const { operations, phases } = operationsFor([cancelled]);
    await operations.sync();
    assert.deepEqual(phases, ['pull:pull --progress always']);
  });

  it('does not run a process in an untrusted workspace', async () => {
    const { operations, phases, operationHost } = operationsFor([success], false);
    await operations.pull();
    assert.deepEqual(phases, []);
    assert.match(operationHost.information[0], /trusted/u);
  });
});
