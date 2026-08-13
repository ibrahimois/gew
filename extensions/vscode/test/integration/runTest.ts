import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';

import { runTests } from '@vscode/test-electron';

async function main(): Promise<void> {
  const extensionDevelopmentPath = path.resolve(__dirname, '../../..');
  const extensionTestsPath = path.resolve(__dirname, 'suite/index');
  const fixtureRoot = await fs.promises.mkdtemp(path.join(os.tmpdir(), 'gew-vscode-integration-'));
  const userDataDir = path.join(fixtureRoot, 'user-data');
  const extensionsDir = path.join(fixtureRoot, 'extensions');
  const workspaceRoot = path.join(fixtureRoot, 'parent-workspace');
  const repositoryRoot = path.join(workspaceRoot, 'hybrid-fixture');
  await fs.promises.mkdir(path.join(repositoryRoot, '.git'), { recursive: true });
  await fs.promises.mkdir(path.join(repositoryRoot, '.gew'), { recursive: true });

  const vscodeExecutablePath = process.env.VSCODE_EXECUTABLE_PATH;
  const launchArgs = [
    workspaceRoot,
    '--disable-extensions',
    '--disable-gpu',
    '--disable-workspace-trust',
    `--user-data-dir=${userDataDir}`,
    `--extensions-dir=${extensionsDir}`,
  ];

  try {
    await fs.promises.writeFile(
      path.join(fixtureRoot, 'launch-args.json'),
      JSON.stringify(launchArgs),
    );
    await runTests({
      ...(vscodeExecutablePath === undefined
        ? { version: '1.125.0' }
        : { vscodeExecutablePath }),
      extensionDevelopmentPath,
      extensionTestsPath,
      launchArgs,
      extensionTestsEnv: sanitizeEnvironment({
        GEW_VSCODE_TEST_ROOT: fixtureRoot,
        GEW_VSCODE_TEST_WORKSPACE: workspaceRoot,
        GEW_VSCODE_TEST_REPOSITORY: repositoryRoot,
        GEW_VSCODE_TEST_USER_DATA: userDataDir,
        GEW_VSCODE_TEST_EXTENSIONS: extensionsDir,
        GEW_VSCODE_EXPECT_TRUSTED: 'true',
      }),
    });
  } finally {
    await fs.promises.rm(fixtureRoot, { recursive: true, force: true });
  }
}

function sanitizeEnvironment(additions: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  const environment = { ...process.env, ...additions };
  for (const key of [
    'GEW_TOKEN',
    'GEW_SERVER',
    'GEW_PROVIDER',
    'GEW_AUTH_KIND',
    'GEW_USERNAME',
  ]) {
    delete environment[key];
  }
  return environment;
}

void main().catch((error: unknown) => {
  console.error(error);
  process.exitCode = 1;
});
