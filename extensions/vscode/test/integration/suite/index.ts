import { strict as assert } from 'node:assert';
import * as fs from 'node:fs';
import * as path from 'node:path';
import * as vscode from 'vscode';

import type { GewExtensionApi } from '../../../src/extension';
import { Operations, type OperationHost, type OperationRunner } from '../../../src/operations';
import type { CancellationTokenLike } from '../../../src/gewRunner';

const extensionId = 'ibrahimois.gew-vscode';
const commandIds = [
  'gew.enableRepository',
  'gew.disableRepository',
  'gew.pull',
  'gew.push',
  'gew.sync',
] as const;

export async function run(): Promise<void> {
  const extension = vscode.extensions.getExtension<GewExtensionApi>(extensionId);
  assert.ok(extension, `Expected ${extensionId} to be installed in the extension host`);
  const api = await extension.activate();

  await verifyCommands();
  verifyMixedWorkspaceMenuGuards(extension);
  await verifyIsolatedLaunchArguments();
  await verifyEnableDisable(api);
  await verifyGewOnlyRejection(api);
  await verifyUntrustedExecutionDoesNotSpawn();
  verifyTrustMode();
}

async function verifyCommands(): Promise<void> {
  const commands = await vscode.commands.getCommands(true);
  for (const command of commandIds) {
    assert.ok(commands.includes(command), `Expected command ${command} to be registered`);
  }
}

function verifyMixedWorkspaceMenuGuards(extension: vscode.Extension<GewExtensionApi>): void {
  const manifest = extension.packageJSON as {
    contributes?: {
      menus?: Record<string, Array<{ command?: string; when?: string; group?: string }>>;
    };
  };
  const menus = manifest.contributes?.menus ?? {};
  const sourceControlItems = menus['scm/title'] ?? [];

  const sourceControlSync = sourceControlItems.find((item) => item.command === 'gew.sync');
  assert.ok(sourceControlSync, 'Expected GEW Sync in the Source Control title menu');
  assert.equal(sourceControlSync.when, 'isWorkspaceTrusted');
  assert.equal(sourceControlSync.group, 'navigation');
  assert.ok(!sourceControlSync.when.includes('scmProvider'));

  assert.equal(menus['scm/repositories/title'], undefined,
    'Stable VS Code extensions must not use the proposed multi-repository title menu');

  for (const command of ['gew.pull', 'gew.push', 'gew.disableRepository']) {
    const item = sourceControlItems.find((candidate) => candidate.command === command);
    assert.ok(item, `Expected ${command} in the Source Control title menu`);
    assert.equal(item.when, 'gew.hasEnabledHybridRepository && isWorkspaceTrusted');
    assert.ok(!item.when.includes('scmProvider'), `${command} must work in mixed-provider workspaces`);
  }
}

async function verifyIsolatedLaunchArguments(): Promise<void> {
  const testRoot = requiredEnvironment('GEW_VSCODE_TEST_ROOT');
  const launchArgs = JSON.parse(
    await fs.promises.readFile(path.join(testRoot, 'launch-args.json'), 'utf8'),
  ) as string[];
  const userDataDir = requiredEnvironment('GEW_VSCODE_TEST_USER_DATA');
  const extensionsDir = requiredEnvironment('GEW_VSCODE_TEST_EXTENSIONS');

  assert.ok(launchArgs.includes(`--user-data-dir=${userDataDir}`));
  assert.ok(launchArgs.includes(`--extensions-dir=${extensionsDir}`));
  assert.ok(userDataDir.startsWith(testRoot));
  assert.ok(extensionsDir.startsWith(testRoot));
}

async function verifyEnableDisable(api: GewExtensionApi): Promise<void> {
  const repositoryRoot = await fs.promises.realpath(requiredEnvironment('GEW_VSCODE_TEST_REPOSITORY'));

  const enabled = await api.registry.enable(repositoryRoot);
  assert.equal(enabled.status, 'enabled');
  assert.deepEqual(api.registry.getEnabledPaths(), [repositoryRoot]);
  assert.equal(await api.registry.resolveOperationTarget(repositoryRoot), repositoryRoot);

  const disabled = await api.registry.disable(repositoryRoot);
  assert.equal(disabled.status, 'disabled');
  assert.deepEqual(api.registry.getEnabledPaths(), []);
  assert.equal(await api.registry.resolveOperationTarget(repositoryRoot), undefined);
}

async function verifyGewOnlyRejection(api: GewExtensionApi): Promise<void> {
  const repositoryRoot = requiredEnvironment('GEW_VSCODE_TEST_REPOSITORY');
  await fs.promises.rm(path.join(repositoryRoot, '.git'), { recursive: true, force: true });

  const result = await api.registry.enable(repositoryRoot);
  assert.equal(result.status, 'notHybrid');
  assert.deepEqual(api.registry.getEnabledPaths(), []);
}

async function verifyUntrustedExecutionDoesNotSpawn(): Promise<void> {
  let runCount = 0;
  const runner: OperationRunner = {
    runExclusive: async <T>(_root: string, task: () => Promise<T>) => ({
      status: 'completed' as const,
      value: await task(),
    }),
    run: async () => {
      runCount += 1;
      return { exitCode: 0, signal: null, cancelled: false };
    },
  };
  const host: OperationHost = {
    isTrusted: false,
    resolveTarget: async (): Promise<string> => requiredEnvironment('GEW_VSCODE_TEST_WORKSPACE'),
    getExecutablePath: (): string => 'fixture-gew',
    runWithProgress: async <T>(
      _title: string,
      task: (token: CancellationTokenLike, report: (message: string) => void) => Promise<T>,
    ): Promise<T> => task(
      {
        isCancellationRequested: false,
        onCancellationRequested: () => ({ dispose: (): void => undefined }),
      },
      (): void => undefined,
    ),
    showInformation: (): void => undefined,
    showError: (): void => undefined,
    revealOutput: (): void => undefined,
  };

  await new Operations(host, runner).pull();
  assert.equal(runCount, 0);
}

function verifyTrustMode(): void {
  const expectedTrust = requiredEnvironment('GEW_VSCODE_EXPECT_TRUSTED') === 'true';
  assert.equal(vscode.workspace.isTrusted, expectedTrust);
}

function requiredEnvironment(key: string): string {
  const value = process.env[key];
  assert.ok(value, `Expected ${key} in extension test environment`);
  return value;
}
