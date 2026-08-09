import { strict as assert } from 'node:assert';
import { describe, it } from 'node:test';

import { GewSyncViewProvider, type GewSyncViewActions } from '../../src/syncView';

interface MessageWebview {
  options: { enableScripts?: boolean };
  html: string;
  readonly cspSource: string;
  onDidReceiveMessage(listener: (message: unknown) => void | Promise<void>): { dispose(): void };
}

function createView(): {
  view: { webview: MessageWebview };
  send(message: unknown): Promise<void>;
} {
  let listener: ((message: unknown) => void | Promise<void>) | undefined;
  const webview: MessageWebview = {
    options: {},
    html: '',
    cspSource: 'test-csp',
    onDidReceiveMessage: (nextListener) => {
      listener = nextListener;
      return { dispose: (): void => undefined };
    },
  };
  return {
    view: { webview },
    send: async (message: unknown): Promise<void> => {
      await listener?.(message);
    },
  };
}

describe('GewSyncViewProvider', () => {
  it('renders a prominent Sync button with Pull and Push controls', () => {
    const provider = new GewSyncViewProvider({
      sync: async (): Promise<void> => undefined,
      pull: async (): Promise<void> => undefined,
      push: async (): Promise<void> => undefined,
    });
    const { view } = createView();

    provider.resolveWebviewView(view as never);

    assert.equal(view.webview.options.enableScripts, true);
    assert.match(view.webview.html, />Sync via GEW REST</u);
    assert.match(view.webview.html, /data-command="pull"/u);
    assert.match(view.webview.html, /data-command="push"/u);
    assert.match(view.webview.html, /Content-Security-Policy/u);
  });

  it('dispatches only supported view messages', async () => {
    const calls: string[] = [];
    const actions: GewSyncViewActions = {
      sync: async (): Promise<void> => { calls.push('sync'); },
      pull: async (): Promise<void> => { calls.push('pull'); },
      push: async (): Promise<void> => { calls.push('push'); },
    };
    const provider = new GewSyncViewProvider(actions);
    const fixture = createView();
    provider.resolveWebviewView(fixture.view as never);

    await fixture.send({ command: 'sync' });
    await fixture.send({ command: 'pull' });
    await fixture.send({ command: 'push' });
    await fixture.send({ command: 'unknown' });

    assert.deepEqual(calls, ['sync', 'pull', 'push']);
  });
});
