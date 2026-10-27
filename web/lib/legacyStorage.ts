// Browser storage keys moved from keel.* to aven.*. Reads accept the old key
// and copy it forward. Each old key warns once per page load.

const warned = new Set<string>();

export function readStorage(key: string, legacy?: string): string | null {
  if (typeof window === "undefined") return null;
  try {
    const cur = window.localStorage.getItem(key);
    if (cur != null) return cur;
    if (!legacy) return null;
    const old = window.localStorage.getItem(legacy);
    if (old == null) return null;
    if (!warned.has(legacy)) {
      warned.add(legacy);
      console.warn(`${legacy} is set; ${key} is the new name (the old name still works)`);
    }
    try {
      window.localStorage.setItem(key, old);
    } catch {
      /* ignore quota / private mode */
    }
    return old;
  } catch {
    return null;
  }
}

export function writeStorage(key: string, value: string) {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(key, value);
  } catch {
    /* ignore */
  }
}

export function removeStorage(key: string, legacy?: string) {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.removeItem(key);
    if (legacy) window.localStorage.removeItem(legacy);
  } catch {
    /* ignore */
  }
}

function sessionGet(key: string): string | null {
  try {
    return window.sessionStorage.getItem(key);
  } catch {
    return null;
  }
}

export function readSession(key: string, legacy?: string): string | null {
  if (typeof window === "undefined") return null;
  const cur = sessionGet(key);
  if (cur != null) return cur;
  if (!legacy) return null;
  const old = sessionGet(legacy);
  if (old == null) return null;
  if (!warned.has(legacy)) {
    warned.add(legacy);
    console.warn(`${legacy} is set; ${key} is the new name (the old name still works)`);
  }
  try {
    window.sessionStorage.setItem(key, old);
  } catch {
    /* ignore */
  }
  return old;
}

export function writeSession(key: string, value: string) {
  if (typeof window === "undefined") return;
  try {
    window.sessionStorage.setItem(key, value);
  } catch {
    /* ignore */
  }
}
