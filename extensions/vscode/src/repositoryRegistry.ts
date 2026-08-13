import * as fs from 'node:fs';
import * as path from 'node:path';

export const ENABLED_REPOSITORIES_KEY = 'gew.enabledRepositoryPaths.v1';

export interface WorkspaceFolderInfo {
  readonly name: string;
  readonly path: string;
}

export interface DisposableLike {
  dispose(): void;
}

export interface RepositoryRegistryHost {
  getWorkspaceFolders(): readonly WorkspaceFolderInfo[];
  getActiveFilePath(): string | undefined;
  pickWorkspaceFolder(
    folders: readonly WorkspaceFolderInfo[],
    placeHolder: string,
  ): Promise<WorkspaceFolderInfo | undefined>;
  readState(key: string): unknown;
  writeState(key: string, value: readonly string[]): Promise<void>;
  setEnabledContext(enabled: boolean): Promise<void>;
  onWorkspaceFoldersChanged(listener: () => void): DisposableLike;
}

export type EnableResult =
  | { readonly status: 'enabled'; readonly root: string }
  | { readonly status: 'alreadyEnabled'; readonly root: string }
  | { readonly status: 'notHybrid'; readonly root: string }
  | { readonly status: 'noSelection' };

export type DisableResult =
  | { readonly status: 'disabled'; readonly root: string }
  | { readonly status: 'noSelection' };

export async function canonicalizePath(inputPath: string): Promise<string> {
  const resolved = path.resolve(inputPath);
  try {
    return await fs.promises.realpath(resolved);
  } catch (error: unknown) {
    if (isMissingPathError(error)) {
      const parent = path.dirname(resolved);
      if (parent === resolved) {
        return resolved;
      }
      const canonicalParent = await canonicalizePath(parent);
      return path.join(canonicalParent, path.basename(resolved));
    }
    throw error;
  }
}

export async function isHybridRepository(root: string): Promise<boolean> {
  const [gitDirectory, gewDirectory] = await Promise.all([
    isDirectory(path.join(root, '.git')),
    isDirectory(path.join(root, '.gew')),
  ]);
  return gitDirectory && gewDirectory;
}

export class RepositoryRegistry {
  private enabledPaths = new Set<string>();

  public constructor(private readonly host: RepositoryRegistryHost) {}

  public static async create(host: RepositoryRegistryHost): Promise<RepositoryRegistry> {
    const registry = new RepositoryRegistry(host);
    await registry.initialize();
    return registry;
  }

  public async initialize(): Promise<void> {
    const stored = this.host.readState(ENABLED_REPOSITORIES_KEY);
    if (!isValidStoredPaths(stored)) {
      this.enabledPaths = new Set<string>();
    } else {
      try {
        const canonicalPaths = await Promise.all(stored.map(async (value) => canonicalizePath(value)));
        this.enabledPaths = new Set(canonicalPaths);
      } catch {
        this.enabledPaths = new Set<string>();
      }
    }
    await this.refreshContext();
  }

  public listen(): DisposableLike {
    return this.host.onWorkspaceFoldersChanged(() => {
      void this.refreshContext();
    });
  }

  public getEnabledPaths(): readonly string[] {
    return [...this.enabledPaths].sort();
  }

  public async enable(requestedPath?: string): Promise<EnableResult> {
    const folder = await this.selectWorkspaceFolder(
      await this.getEnableCandidates(requestedPath),
      'Select a hybrid repository to enable for GEW',
      requestedPath,
    );
    if (folder === undefined) {
      return { status: 'noSelection' };
    }

    const root = await canonicalizePath(folder.path);
    if (!(await isHybridRepository(root))) {
      return { status: 'notHybrid', root };
    }
    if (this.enabledPaths.has(root)) {
      await this.persistAndRefresh();
      return { status: 'alreadyEnabled', root };
    }

    this.enabledPaths.add(root);
    await this.persistAndRefresh();
    return { status: 'enabled', root };
  }

  public async disable(requestedPath?: string): Promise<DisableResult> {
    const candidates = await this.getEnabledOpenFolders(false);
    const folder = await this.selectWorkspaceFolder(
      candidates,
      'Select a repository to disable for GEW',
      requestedPath,
    );
    if (folder === undefined) {
      return { status: 'noSelection' };
    }

    const root = await canonicalizePath(folder.path);
    this.enabledPaths.delete(root);
    await this.persistAndRefresh();
    return { status: 'disabled', root };
  }

  public async resolveOperationTarget(requestedPath?: string): Promise<string | undefined> {
    const candidates = await this.getEnabledOpenFolders(true);
    const folder = await this.selectWorkspaceFolder(
      candidates,
      'Select an enabled hybrid repository for this GEW operation',
      requestedPath,
    );
    return folder === undefined ? undefined : canonicalizePath(folder.path);
  }

  public async refreshContext(): Promise<void> {
    const enabledFolders = await this.getEnabledOpenFolders(true);
    await this.host.setEnabledContext(enabledFolders.length > 0);
  }

  private async persistAndRefresh(): Promise<void> {
    const paths = this.getEnabledPaths();
    await this.host.writeState(ENABLED_REPOSITORIES_KEY, paths);
    await this.refreshContext();
  }

  private async getEnableCandidates(requestedPath?: string): Promise<WorkspaceFolderInfo[]> {
    const workspaceFolders = await canonicalizeFolders(this.host.getWorkspaceFolders());
    let nestedRoot: string | undefined;
    if (requestedPath !== undefined) {
      nestedRoot = await canonicalizePath(requestedPath);
    } else {
      const activeFilePath = this.host.getActiveFilePath();
      if (activeFilePath !== undefined) {
        nestedRoot = await findHybridRepositoryRoot(activeFilePath, workspaceFolders);
      }
    }

    if (
      nestedRoot !== undefined
      && workspaceFolders.some((folder) => isWithin(nestedRoot, folder.path))
      && !workspaceFolders.some((folder) => folder.path === nestedRoot)
    ) {
      workspaceFolders.push({ name: path.basename(nestedRoot), path: nestedRoot });
    }
    return workspaceFolders;
  }

  private async getEnabledOpenFolders(requireHybrid: boolean): Promise<WorkspaceFolderInfo[]> {
    const workspaceFolders = await canonicalizeFolders(this.host.getWorkspaceFolders());
    const folders: WorkspaceFolderInfo[] = [];
    for (const enabledPath of [...this.enabledPaths].sort()) {
      const canonicalPath = await canonicalizePath(enabledPath);
      const exactWorkspaceFolder = workspaceFolders.find((folder) => folder.path === canonicalPath);
      if (!workspaceFolders.some((folder) => isWithin(canonicalPath, folder.path))) {
        continue;
      }
      if (requireHybrid && !(await isHybridRepository(canonicalPath))) {
        continue;
      }
      folders.push({
        name: exactWorkspaceFolder?.name ?? path.basename(canonicalPath),
        path: canonicalPath,
      });
    }
    return folders;
  }

  private async selectWorkspaceFolder(
    folders: readonly WorkspaceFolderInfo[],
    placeHolder: string,
    requestedPath?: string,
  ): Promise<WorkspaceFolderInfo | undefined> {
    if (folders.length === 0) {
      return undefined;
    }

    if (requestedPath !== undefined) {
      const requestedRoot = await canonicalizePath(requestedPath);
      return findCanonicalFolder(folders, requestedRoot);
    }

    const activeFilePath = this.host.getActiveFilePath();
    if (activeFilePath !== undefined) {
      const activeCanonicalPath = await canonicalizePath(activeFilePath);
      const activeFolder = await findContainingCanonicalFolder(folders, activeCanonicalPath);
      if (activeFolder !== undefined) {
        return activeFolder;
      }
    }

    if (folders.length === 1) {
      return folders[0];
    }
    return this.host.pickWorkspaceFolder(folders, placeHolder);
  }
}

async function canonicalizeFolders(
  folders: readonly WorkspaceFolderInfo[],
): Promise<WorkspaceFolderInfo[]> {
  return Promise.all(folders.map(async (folder) => ({
    ...folder,
    path: await canonicalizePath(folder.path),
  })));
}

async function findCanonicalFolder(
  folders: readonly WorkspaceFolderInfo[],
  requestedRoot: string,
): Promise<WorkspaceFolderInfo | undefined> {
  for (const folder of folders) {
    if ((await canonicalizePath(folder.path)) === requestedRoot) {
      return folder;
    }
  }
  return undefined;
}

async function findContainingCanonicalFolder(
  folders: readonly WorkspaceFolderInfo[],
  filePath: string,
): Promise<WorkspaceFolderInfo | undefined> {
  const matches: WorkspaceFolderInfo[] = [];
  for (const folder of folders) {
    const canonicalRoot = await canonicalizePath(folder.path);
    if (isWithin(filePath, canonicalRoot) || isWithin(filePath, path.resolve(folder.path))) {
      matches.push(folder);
    }
  }
  return matches.sort((left, right) => right.path.length - left.path.length)[0];
}

async function findHybridRepositoryRoot(
  activeFilePath: string,
  workspaceFolders: readonly WorkspaceFolderInfo[],
): Promise<string | undefined> {
  const activePath = await canonicalizePath(activeFilePath);
  const containingFolders = workspaceFolders
    .filter((folder) => isWithin(activePath, folder.path))
    .sort((left, right) => right.path.length - left.path.length);

  for (const workspaceFolder of containingFolders) {
    let candidate = path.dirname(activePath);
    while (isWithin(candidate, workspaceFolder.path)) {
      if (await isHybridRepository(candidate)) {
        return candidate;
      }
      if (candidate === workspaceFolder.path) {
        break;
      }
      candidate = path.dirname(candidate);
    }
  }
  return undefined;
}

function isWithin(candidate: string, root: string): boolean {
  const relative = path.relative(root, candidate);
  return relative === '' || (!relative.startsWith(`..${path.sep}`) && relative !== '..' && !path.isAbsolute(relative));
}

async function isDirectory(candidate: string): Promise<boolean> {
  try {
    return (await fs.promises.stat(candidate)).isDirectory();
  } catch (error: unknown) {
    if (isMissingPathError(error)) {
      return false;
    }
    throw error;
  }
}

function isValidStoredPaths(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item: unknown) =>
    typeof item === 'string' && item.trim().length > 0 && path.isAbsolute(item) && !item.includes('\0'),
  );
}

function isMissingPathError(error: unknown): boolean {
  return error instanceof Error && 'code' in error && error.code === 'ENOENT';
}
