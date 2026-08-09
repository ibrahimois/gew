import { strict as assert } from 'node:assert';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { afterEach, describe, it } from 'node:test';

import {
  GewRunner,
  type CancellationTokenLike,
  type OutputChannelLike,
  type ProcessResult,
} from '../../src/gewRunner';

const temporaryDirectories: string[] = [];

afterEach(async (): Promise<void> => {
  await Promise.all(
    temporaryDirectories.splice(0).map(async (directory) =>
      fs.promises.rm(directory, { recursive: true, force: true }),
    ),
  );
});

class TestOutput implements OutputChannelLike {
  public readonly values: string[] = [];

  public append(value: string): void {
    this.values.push(value);
  }

  public appendLine(value: string): void {
    this.values.push(`${value}\n`);
  }

  public show(): void {}
}

class TestCancellationToken implements CancellationTokenLike {
  public isCancellationRequested = false;
  private readonly listeners = new Set<() => void>();

  public onCancellationRequested(listener: () => void): { dispose(): void } {
    this.listeners.add(listener);
    return {
      dispose: (): void => {
        this.listeners.delete(listener);
      },
    };
  }

  public cancel(): void {
    this.isCancellationRequested = true;
    for (const listener of this.listeners) {
      listener();
    }
  }
}

async function createFixture(): Promise<{ executable: string; root: string }> {
  const root = await fs.promises.mkdtemp(path.join(os.tmpdir(), 'gew-vscode runner ; '));
  temporaryDirectories.push(root);
  const executable = path.join(root, 'fixture executable ;.js');
  await fs.promises.writeFile(
    executable,
    `#!/usr/bin/env node
const fs = require('node:fs');
const path = require('node:path');
const [mode, ...values] = process.argv.slice(2);
if (mode === 'record') {
  fs.writeFileSync(path.join(process.cwd(), 'record.json'), JSON.stringify({ argv: values, cwd: process.cwd() }));
  process.stdout.write('stdout line\\n');
  process.stderr.write('stderr line\\n');
  process.exit(0);
}
if (mode === 'fail') process.exit(Number(values[0]));
if (mode === 'wait') {
  fs.writeFileSync(path.join(process.cwd(), 'started'), 'yes');
  setInterval(() => {}, 1000);
}
`,
    { mode: 0o755 },
  );
  return { executable, root };
}

async function waitForFile(file: string): Promise<void> {
  const deadline = Date.now() + 5_000;
  while (Date.now() < deadline) {
    try {
      await fs.promises.access(file);
      return;
    } catch {
      await new Promise<void>((resolve) => setTimeout(resolve, 20));
    }
  }
  throw new Error(`Timed out waiting for ${file}`);
}

async function resultOf(start: ReturnType<GewRunner['start']>): Promise<ProcessResult> {
  assert.equal(start.status, 'started');
  return start.result;
}

describe('GewRunner', () => {
  it('passes executable, cwd, and metacharacter arguments without shell interpolation', async () => {
    const fixture = await createFixture();
    const output = new TestOutput();
    const runner = new GewRunner(output);
    const args = ['record', 'value with spaces', '; touch injected', '$(whoami)'];

    const result = await resultOf(runner.start({
      executable: fixture.executable,
      args,
      cwd: fixture.root,
      phase: 'pull',
      token: new TestCancellationToken(),
    }));

    assert.equal(result.exitCode, 0);
    assert.equal(result.spawnError, undefined);
    const record = JSON.parse(
      await fs.promises.readFile(path.join(fixture.root, 'record.json'), 'utf8'),
    ) as { argv: string[]; cwd: string };
    assert.deepEqual(record.argv, args.slice(1));
    assert.equal(record.cwd, await fs.promises.realpath(fixture.root));
    assert.equal(fs.existsSync(path.join(fixture.root, 'injected')), false);
    assert.match(output.values.join(''), /stdout line/u);
    assert.match(output.values.join(''), /stderr line/u);
  });

  it('returns nonzero exit status and spawn errors', async () => {
    const fixture = await createFixture();
    const runner = new GewRunner(new TestOutput());
    const failure = await resultOf(runner.start({
      executable: fixture.executable,
      args: ['fail', '7'],
      cwd: fixture.root,
      phase: 'push',
      token: new TestCancellationToken(),
    }));
    assert.equal(failure.exitCode, 7);

    const spawnFailure = await resultOf(runner.start({
      executable: path.join(fixture.root, 'missing executable'),
      args: ['pull'],
      cwd: fixture.root,
      phase: 'pull',
      token: new TestCancellationToken(),
    }));
    assert.ok(spawnFailure.spawnError instanceof Error);
    assert.ok(spawnFailure.exitCode === null || spawnFailure.exitCode === -2);
  });

  it('terminates a cancelled child', async () => {
    const fixture = await createFixture();
    const token = new TestCancellationToken();
    const runner = new GewRunner(new TestOutput());
    const running = resultOf(runner.start({
      executable: fixture.executable,
      args: ['wait'],
      cwd: fixture.root,
      phase: 'pull',
      token,
    }));

    await waitForFile(path.join(fixture.root, 'started'));
    token.cancel();
    const result = await running;

    assert.equal(result.cancelled, true);
    assert.notEqual(result.signal, null);
  });

  it('rejects same-root overlap while allowing different roots', async () => {
    const first = await createFixture();
    const second = await createFixture();
    const firstToken = new TestCancellationToken();
    const runner = new GewRunner(new TestOutput());

    const firstRun = resultOf(runner.start({
      executable: first.executable,
      args: ['wait'],
      cwd: first.root,
      phase: 'pull',
      token: firstToken,
    }));
    await waitForFile(path.join(first.root, 'started'));

    const overlap = runner.start({
      executable: first.executable,
      args: ['record'],
      cwd: first.root,
      phase: 'push',
      token: new TestCancellationToken(),
    });
    assert.equal(overlap.status, 'busy');

    const secondRun = await resultOf(runner.start({
      executable: second.executable,
      args: ['record'],
      cwd: second.root,
      phase: 'pull',
      token: new TestCancellationToken(),
    }));
    assert.equal(secondRun.exitCode, 0);

    firstToken.cancel();
    await firstRun;
  });
});
