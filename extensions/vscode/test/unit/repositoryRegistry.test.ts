import { strict as assert } from 'node:assert';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { afterEach, describe, it } from 'node:test';

import {
  ENABLED_REPOSITORIES_KEY,
  RepositoryRegistry,
  canonicalizePath,
  isHybridRepository,
  type DisposableLike,
  type RepositoryRegistryHost,
  type WorkspaceFolderInfo,
} from '../../src/repositoryRegistry';

const temporaryDirectories: string[] = [];

afterEach(async (): Promise<void> => {
  await Promise.all(
    temporaryDirectories.splice(0).map(async (directory) =>
      fs.promises.rm(directory, { recursive: true, force: true }),
    ),
  );
});

function folder(root: string): WorkspaceFolderInfo {
  return { name: path.basename(root), path: root };
}

function createHost(options: {
  folders: readonly WorkspaceFolderInfo[];
  activeFile?: string;
  pick?: WorkspaceFolderInfo;
  state?: unknown;
}): RepositoryRegistryHost & {
  readonly contexts: boolean[];
  readonly picks: number[];
  getStoredState(): unknown;
} {
  let state = options.state;
  const contexts: boolean[] = [];
  const picks: number[] = [];
  return {
    contexts,
    picks,
    getStoredState: (): unknown => state,
    getWorkspaceFolders: (): readonly WorkspaceFolderInfo[] => options.folders,
    getActiveFilePath: (): string | undefined => options.activeFile,
    pickWorkspaceFolder: async (candidates): Promise<WorkspaceFolderInfo | undefined> => {
      picks.push(candidates.length);
      return options.pick;
    },
    readState: (key): unknown => key === ENABLED_REPOSITORIES_KEY ? state : undefined,
    writeState: async (key, value): Promise<void> => {
      assert.equal(key, ENABLED_REPOSITORIES_KEY);
      state = [...value];
    },
    setEnabledContext: async (enabled): Promise<void> => {
      contexts.push(enabled);
    },
    onWorkspaceFoldersChanged: (): DisposableLike => ({ dispose: (): void => undefined }),
  };
}

async function makeRepository(options: { git: boolean; gew: boolean }): Promise<string> {
  const root = await fs.promises.mkdtemp(path.join(os.tmpdir(), 'gew-vscode-registry-'));
  temporaryDirectories.push(root);
  if (options.git) {
    await fs.promises.mkdir(path.join(root, '.git'));
  }
  if (options.gew) {
    await fs.promises.mkdir(path.join(root, '.gew'));
  }
  return root;
}

describe('RepositoryRegistry', () => {
  it('deduplicates canonical paths and rewrites malformed state on enable', async () => {
    const root = await makeRepository({ git: true, gew: true });
    const registryHost = createHost({ folders: [folder(path.join(root, '.'))], state: { malformed: true } });
    const registry = await RepositoryRegistry.create(registryHost);

    const result = await registry.enable();

    assert.equal(result.status, 'enabled');
    assert.deepEqual(registryHost.getStoredState(), [await canonicalizePath(root)]);
    assert.deepEqual(registry.getEnabledPaths(), [await canonicalizePath(root)]);
    assert.equal(registryHost.contexts.at(-1), true);
  });

  it('deduplicates canonical persisted aliases during initialization', async () => {
    const root = await makeRepository({ git: true, gew: true });
    const registryHost = createHost({
      folders: [folder(root)],
      state: [root, path.join(root, '.')],
    });
    const registry = await RepositoryRegistry.create(registryHost);

    assert.deepEqual(registry.getEnabledPaths(), [await canonicalizePath(root)]);
  });

  it('treats invalid persisted path strings as malformed state', async () => {
    const root = await makeRepository({ git: true, gew: true });
    const registry = await RepositoryRegistry.create(createHost({
      folders: [folder(root)],
      state: ['', 'relative/repository'],
    }));

    assert.deepEqual(registry.getEnabledPaths(), []);
  });

  it('accepts only roots containing .git and .gew directories', async () => {
    const hybrid = await makeRepository({ git: true, gew: true });
    const gitOnly = await makeRepository({ git: true, gew: false });
    const gewOnly = await makeRepository({ git: false, gew: true });

    assert.equal(await isHybridRepository(hybrid), true);
    assert.equal(await isHybridRepository(gitOnly), false);
    assert.equal(await isHybridRepository(gewOnly), false);
  });

  it('prefers the active enabled folder', async () => {
    const first = await makeRepository({ git: true, gew: true });
    const second = await makeRepository({ git: true, gew: true });
    const registryHost = createHost({
      folders: [folder(first), folder(second)],
      activeFile: path.join(second, 'src', 'active.ts'),
      pick: folder(first),
      state: [first, second],
    });
    const registry = await RepositoryRegistry.create(registryHost);

    assert.equal(await registry.resolveOperationTarget(), await canonicalizePath(second));
    assert.deepEqual(registryHost.picks, []);
  });

  it('returns the sole enabled open folder', async () => {
    const enabled = await makeRepository({ git: true, gew: true });
    const disabled = await makeRepository({ git: true, gew: true });
    const registry = await RepositoryRegistry.create(createHost({
      folders: [folder(enabled), folder(disabled)],
      state: [enabled],
    }));

    assert.equal(await registry.resolveOperationTarget(), await canonicalizePath(enabled));
  });

  it('uses a Quick Pick for multiple enabled candidates without an active editor', async () => {
    const first = await makeRepository({ git: true, gew: true });
    const second = await makeRepository({ git: true, gew: true });
    const selected = folder(second);
    const registryHost = createHost({
      folders: [folder(first), selected],
      pick: selected,
      state: [first, second],
    });
    const registry = await RepositoryRegistry.create(registryHost);

    assert.equal(await registry.resolveOperationTarget(), await canonicalizePath(second));
    assert.deepEqual(registryHost.picks, [2]);
  });
});
