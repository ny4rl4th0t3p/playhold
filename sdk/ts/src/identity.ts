/** A guest's identity, issued by the server on the first visit. */
export interface Identity {
  playerId: string;
  secret: string;
}

/** The part of the browser's Storage this SDK uses. */
export type IdentityStorage = Pick<Storage, "getItem" | "setItem" | "removeItem">;

/**
 * Keeps the guest's identity across reloads. It also keeps a copy in memory, because storage can be blocked,
 * for example inside an iframe, and a guest should still survive a dropped connection there.
 */
export class IdentityStore {
  private memory: Identity | undefined;

  constructor(
    private readonly storage: IdentityStorage | undefined,
    private readonly key: string,
  ) {}

  /** Storage comes first: another tab may have received a newer identity. */
  load(): Identity | undefined {
    try {
      const raw = this.storage?.getItem(this.key);
      const stored = raw ? parseIdentity(raw) : undefined;
      if (stored) {
        return stored;
      }
    } catch {
      // storage is blocked, so memory is all there is
    }
    return this.memory;
  }

  save(identity: Identity): void {
    this.memory = identity;
    try {
      this.storage?.setItem(this.key, JSON.stringify(identity));
    } catch {
      // storage is blocked, so memory is all there is
    }
  }

  clear(): void {
    this.memory = undefined;
    try {
      this.storage?.removeItem(this.key);
    } catch {
      // storage is blocked, so there is nothing to remove
    }
  }
}

function parseIdentity(raw: string): Identity | undefined {
  const value: unknown = JSON.parse(raw);
  if (typeof value !== "object" || value === null) {
    return undefined;
  }
  const { playerId, secret } = value as Partial<Identity>;
  return typeof playerId === "string" && typeof secret === "string" ? { playerId, secret } : undefined;
}

/** Returns localStorage if this environment has it. Merely reading it can throw inside a sandboxed iframe. */
export function defaultStorage(): IdentityStorage | undefined {
  try {
    return globalThis.localStorage;
  } catch {
    return undefined;
  }
}